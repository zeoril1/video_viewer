// Package httpx — общие HTTP-утилиты для микросервисов: лог запросов и хелперы JSON-ответов.
package httpx

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// statusRecorder запоминает код ответа для логирования.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// ExternalScheme возвращает схему внешнего запроса: "https" при r.TLS или
// X-Forwarded-Proto от обратного прокси, иначе "http". Нужно, когда сервис
// стоит за прокси (Caddy/nginx): без этого абсолютные ссылки и Secure-кука
// «теряют» https.
func ExternalScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	p := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if p == "" {
		return "http"
	}
	// Прокси может передать список ("https, http") — берём первый элемент.
	if i := strings.IndexByte(p, ','); i >= 0 {
		p = strings.TrimSpace(p[:i])
	}
	return strings.ToLower(p)
}

// IsSecureRequest сообщает, что внешний запрос пришёл по HTTPS.
func IsSecureRequest(r *http.Request) bool {
	return ExternalScheme(r) == "https"
}

// PublicBase возвращает внешний базовый адрес (scheme://host) с учётом
// X-Forwarded-Proto: абсолютные ссылки должны указывать на тот адрес,
// по которому клиент реально обращался.
func PublicBase(r *http.Request) string {
	host := r.Host
	if host == "" {
		host = "localhost"
	}
	return ExternalScheme(r) + "://" + host
}

// LogMiddleware логирует каждый HTTP-запрос: метод, путь, статус и время
// обработки. HLS-сегменты и healthcheck не логируются — их слишком много.
func LogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		// Каждый HLS-сегмент не логируем: их тысячи на активный стрим.
		if strings.Contains(r.URL.Path, "/hls/segments/") {
			return
		}
		// Успешные проверки живости (healthcheck и шлюз) не логируем;
		// ошибки (status != 200) остаются.
		if r.Method == http.MethodGet && r.URL.Path == "/api/health" && rec.status == http.StatusOK {
			return
		}
		// Длинные магнеты обрезаем: хвост с трекерами не информативен,
		// info_hash в начале сохраняется.
		qs := r.URL.RawQuery
		if qs != "" {
			if len(qs) > 300 {
				qs = qs[:300] + "..."
			}
			log.Printf("http %s %s?%s -> %d (%s)", r.Method, r.URL.Path, qs, rec.status, time.Since(start).Round(time.Millisecond))
			return
		}
		log.Printf("http %s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}
