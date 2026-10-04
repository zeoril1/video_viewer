// Package gatewayapi — API-шлюз (микросервис gateway): раздаёт статику
// фронтенда (web/) и проксирует /api/* на внутренние сервисы по префиксам,
// а также агрегирует /api/health.
package gatewayapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/httpx"
)

// Config — адреса внутренних сервисов и каталог статики.
type Config struct {
	TrustedProxy string // доверенный прокси (caddy): хост/IP, иначе пусто
	WebDir       string // каталог статики фронтенда (web/)
	CatalogURL   string // http://catalog:8081
	StreamURL    string // http://stream:8082
	AuthURL      string // http://auth:8083
	IPTVURL      string // http://iptv:8084
}

// NewServer собирает шлюз: reverse-proxy к сервисам + статика + health.
// API-поверхность для фронтенда остаётся прежней (пути не меняются).
func NewServer(cfg Config) http.Handler {
	mux := http.NewServeMux()

	// ---- Каталог (catalog-сервис) ----
	mux.HandleFunc("GET /api/catalog", proxyTo(cfg.CatalogURL))
	mux.HandleFunc("GET /api/catalog/meta", proxyTo(cfg.CatalogURL))
	mux.HandleFunc("GET /api/films/{id}", proxyTo(cfg.CatalogURL))
	mux.HandleFunc("GET /api/films/{id}/sources", proxyTo(cfg.CatalogURL))
	// Админ-эндпоинты каталога: роль проверяет catalog-сервис.
	mux.HandleFunc("/api/admin/", proxyTo(cfg.CatalogURL))

	// ---- Стриминг (stream-сервис) ----
	mux.HandleFunc("GET /api/films/{id}/files", proxyTo(cfg.StreamURL))
	mux.HandleFunc("POST /api/films/{id}/files", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/tracks", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/hls.m3u8", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/hls/stop", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/hls/pl.m3u8", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/hls/subs/", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/hls/segments/", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/stream/", proxyTo(cfg.StreamURL))
	// Тёплый кеш: фронтенд зовёт после просмотра >5% длительности.
	mux.HandleFunc("POST /api/stream/keep", proxyTo(cfg.StreamURL))
	// /api/debug/* намеренно НЕ проксируется наружу (диагностика памяти и
	// торрентов доступна только во внутренней сети).

	// ---- Авторизация и история (auth-сервис) ----
	mux.HandleFunc("/api/auth/", proxyTo(cfg.AuthURL))
	mux.HandleFunc("/api/rooms", proxyTo(cfg.AuthURL))
	mux.HandleFunc("/api/rooms/", proxyTo(cfg.AuthURL))
	mux.HandleFunc("POST /api/stream/prepare", proxyTo(cfg.StreamURL))
	mux.HandleFunc("/api/personal", proxyTo(cfg.AuthURL))
	mux.HandleFunc("/api/personal/", proxyTo(cfg.AuthURL))
	mux.HandleFunc("/api/discover/", proxyTo(cfg.CatalogURL))
	mux.HandleFunc("GET /api/films/{id}/explore", proxyTo(cfg.CatalogURL))
	mux.HandleFunc("GET /api/films/{id}/watch-order", proxyTo(cfg.CatalogURL))
	mux.HandleFunc("/api/history", proxyTo(cfg.AuthURL))
	mux.HandleFunc("/api/history/", proxyTo(cfg.AuthURL))

	// ---- IPTV (iptv-сервис): каналы, EPG, live-HLS ----
	// Включая админские POST/DELETE: права проверяет iptv по общей куке.
	mux.HandleFunc("/api/iptv/", proxyTo(cfg.IPTVURL))

	// ---- Здоровье: агрегированный статус всех сервисов ----
	mux.HandleFunc("GET /api/health", healthHandler(cfg))

	// ---- Статические файлы фронтенда ----
	// no-cache (а не no-store): файл перепроверяется (If-Modified-Since → 304),
	// но после обновления фронтенда клиент сразу получает новую версию.
	// Без этого заголовка браузер/WebView кэширует скрипты фронтенда (web/*.js) эвристически (по
	// Last-Modified) и может долго работать на старой версии.
	mux.Handle("/", noCacheStatic(http.Dir(cfg.WebDir)))

	return httpx.LogMiddleware(clientAddress(mux, cfg.TrustedProxy))
}

// noCacheStatic раздаёт статику с запретом эвристического кэширования
// (клиент перепроверяет файлы и получает 304, пока они не изменены).
func noCacheStatic(fs http.FileSystem) http.Handler {
	files := http.FileServer(fs)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// proxyTo возвращает reverse-proxy к целевому сервису (сохраняет путь и
// query, меняет только хост). FlushInterval=-1 — мгновенный flush, важно
// для потокового видео (Range) и HLS-сегментов.
func proxyTo(target string) http.HandlerFunc {
	if strings.TrimSpace(target) == "" {
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "upstream not configured", http.StatusServiceUnavailable)
		}
	}
	u, err := url.Parse(target)
	if err != nil {
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid upstream URL", http.StatusInternalServerError)
		}
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.FlushInterval = -1
	return rp.ServeHTTP
}

// healthHandler проверяет живость всех внутренних сервисов.
func healthHandler(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := []struct {
			name string
			url  string
		}{
			{"catalog", cfg.CatalogURL},
			{"stream", cfg.StreamURL},
			{"auth", cfg.AuthURL},
			{"iptv", cfg.IPTVURL},
		}
		client := &http.Client{Timeout: 3 * time.Second}
		statuses := make(map[string]string, len(checks))
		// Сервисы опрашиваются ПАРАЛЛЕЛЬНО: при недоступном сервисе health
		// не должен ждать 3×Timeout последовательно.
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, c := range checks {
			wg.Add(1)
			go func(name, u string) {
				defer wg.Done()
				set := func(s string) {
					mu.Lock()
					statuses[name] = s
					mu.Unlock()
				}
				if strings.TrimSpace(u) == "" {
					set("not_configured")
					return
				}
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"/api/health", nil)
				if err != nil {
					cancel()
					set("error")
					return
				}
				resp, err := client.Do(req)
				cancel()
				if err != nil {
					set("down")
					return
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					set("error")
					return
				}
				set("ok")
			}(c.name, c.url)
		}
		wg.Wait()

		status := "ok"
		code := http.StatusOK
		for _, s := range statuses {
			if s != "ok" {
				status = "degraded"
				code = http.StatusServiceUnavailable
				break
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   status,
			"services": statuses,
		})
	}
}

// clientAddress перезаписывает внутренний заголовок с IP клиента:
// forwarded-адрес принимается только от явно заданного прокси.
func clientAddress(next http.Handler, trustedProxy string) http.Handler {
	tp := &trustedProxyIPs{host: strings.TrimSpace(trustedProxy)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		// Разбираем X-Forwarded-For только там, где он нужен (лимиты входа),
		// и только если он вообще пришёл: без заголовка разбирать нечего.
		if tp.host != "" && (strings.HasPrefix(r.URL.Path, "/api/auth/") || r.URL.Path == "/api/rooms" || strings.HasPrefix(r.URL.Path, "/api/rooms/")) && r.Header.Get("X-Forwarded-For") != "" {
			for _, address := range tp.ips() {
				if address.Equal(net.ParseIP(ip)) {
					chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
					if forwarded := net.ParseIP(strings.TrimSpace(chain[len(chain)-1])); forwarded != nil {
						ip = forwarded.String()
					}
					break
				}
			}
		}
		r.Header.Set("X-Video-Viewer-Client-IP", ip)
		next.ServeHTTP(w, r)
	})
}

// trustedProxyIPs — адреса доверенного прокси. Имя резолвится не чаще раза в минуту
// и с коротким таймаутом: раньше DNS-запрос шёл в каждом запросе /api/auth/* и вход
// ждал ~4 с, пока Docker DNS не ответит про незапущенный caddy (профиль public).
// Пустой ответ кэшируется тот же срок — прокси, поднятый позже, будет подхвачен.
const trustedProxyTTL = time.Minute

type trustedProxyIPs struct {
	host string
	mu   sync.Mutex
	list []net.IP
	at   time.Time
}

func (t *trustedProxyIPs) ips() []net.IP {
	if t.host == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.at.IsZero() && time.Since(t.at) < trustedProxyTTL {
		return t.list
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIP(ctx, "ip", t.host)
	if err != nil {
		addrs = nil // прокси не запущен — forwarded-заголовку просто не доверяем
	}
	t.list, t.at = addrs, time.Now()
	return t.list
}
