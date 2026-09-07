// Package proxy предоставляет HTTP-прокси из конфигурации (PROXY_STATIC)
// с автоматическим failover на прямое соединение при сбое. Динамические
// публичные списки (PROXY_SOURCES) не используются: прокси задаются
// явно, доверяются и проверяются фактическим использованием (при сбое
// убираются, и запросы идут напрямую).
package proxy

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config — параметры прокси.
type Config struct {
	// Static — жёстко заданные прокси [user:pass@]host:port (PROXY_STATIC).
	Static []string
	// Timeout — таймаут подключения через прокси (dial/TLS).
	Timeout time.Duration
}

// Pool — пул HTTP-прокси (из PROXY_STATIC) с round-robin и failover:
// при сбое соединения прокси помечается нерабочим (MarkBroken) и
// убирается из пула, запросы идут через следующий или напрямую.
type Pool struct {
	mu         sync.Mutex
	staticList []string                   // рабочие статические прокси
	index      int                        // round-robin индекс
	transports map[string]*http.Transport // прокси -> транспорт (переиспользование соединений)
	direct     *http.Transport            // прямое соединение (fallback)
	timeout    time.Duration
}

// NewPool создаёт пул из статических прокси.
func NewPool(cfg Config) *Pool {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	p := &Pool{
		transports: make(map[string]*http.Transport),
		direct:     defaultTransport(),
		timeout:    cfg.Timeout,
	}
	for _, s := range cfg.Static {
		if s = normalize(s); s != "" {
			p.staticList = append(p.staticList, s)
		}
	}
	return p
}

// defaultTransport — транспорт для прямых соединений (fallback).
func defaultTransport() *http.Transport {
	return &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
}

// normalize приводит прокси к виду [user:pass@]host:port, убирая схему
// и лишние пробелы.
func normalize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.Trim(s, "/")
	return s
}

// proxyURL строит *url.URL из строки прокси вида [user:pass@]host:port.
// Учётные данные попадают в url.User — Go сам отправит их как
// Proxy-Authorization: Basic.
func proxyURL(addr string) *url.URL {
	u := &url.URL{Scheme: "http"}
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		u.Host = addr[i+1:]
		userinfo := addr[:i]
		if j := strings.IndexByte(userinfo, ':'); j >= 0 {
			u.User = url.UserPassword(userinfo[:j], userinfo[j+1:])
		} else {
			u.User = url.User(userinfo)
		}
	} else {
		u.Host = addr
	}
	return u
}

// redact убирает учётные данные из адреса для логов (пароль не должен
// попадать в логи).
func redact(addr string) string {
	if i := strings.IndexByte(addr, '@'); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// Next возвращает следующий прокси (round-robin). Пустая строка, если
// пул пуст (тогда запросы идут напрямую).
func (p *Pool) Next() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.staticList) == 0 {
		return ""
	}
	addr := p.staticList[p.index%len(p.staticList)]
	p.index++
	return addr
}

// MarkBroken удаляет прокси из пула (сбой соединения, 407 и т.п.).
func (p *Pool) MarkBroken(addr string) {
	p.mu.Lock()
	if removeAddr(&p.staticList, addr) {
		p.index = 0
	}
	tr, ok := p.transports[addr]
	if ok {
		tr.CloseIdleConnections()
		delete(p.transports, addr)
	}
	n := len(p.staticList)
	p.mu.Unlock()
	log.Printf("proxy: removed broken %s (working: %d)", redact(addr), n)
}

// removeAddr удаляет addr из списка. Возвращает true, если элемент удалён.
func removeAddr(list *[]string, addr string) bool {
	for i, a := range *list {
		if a == addr {
			*list = append((*list)[:i], (*list)[i+1:]...)
			return true
		}
	}
	return false
}

// All возвращает копию текущих прокси.
func (p *Pool) All() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.staticList...)
}

// Size возвращает число рабочих прокси.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.staticList)
}

// ProxyURL возвращает функцию для http.Transport.Proxy или
// torrent.ClientConfig.HTTPProxy: берёт следующий рабочий прокси
// (round-robin) или nil (прямое соединение), если прокси нет.
func (p *Pool) ProxyURL() func(*http.Request) (*url.URL, error) {
	return func(*http.Request) (*url.URL, error) {
		addr := p.Next()
		if addr == "" {
			return nil, nil
		}
		return proxyURL(addr), nil
	}
}

// transportFor возвращает транспорт для конкретного прокси, создавая
// его лениво (соединения переиспользуются).
func (p *Pool) transportFor(addr string) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if tr, ok := p.transports[addr]; ok {
		return tr
	}
	tr := &http.Transport{
		Proxy:               http.ProxyURL(proxyURL(addr)),
		DialContext:         (&net.Dialer{Timeout: p.timeout, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: p.timeout,
	}
	p.transports[addr] = tr
	return tr
}
