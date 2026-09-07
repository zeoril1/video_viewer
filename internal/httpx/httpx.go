// Package httpx — общие HTTP-утилиты для микросервисов: middleware
// логирования запросов и хелперы для JSON-ответов.
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

// LogMiddleware логирует каждый HTTP-запрос: метод, путь, статус и время
// обработки. HLS-сегменты (их тысячи) не логируются, чтобы не засорять
// логи. Длинные query-параметры (магнеты) обрезаются.
func LogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		// Каждый HLS-сегмент не логируем: их тысячи (идут каждые ~6с на
		// активный стрим), и они засоряют логи.
		if strings.Contains(r.URL.Path, "/hls/segments/") {
			return
		}
		// Успешные проверки живости (docker healthcheck бьёт в /api/health
		// каждые несколько секунд, а шлюз опрашивает все сервисы) — не
		// логируем: они засоряют лог. Ошибки (status != 200) оставляем.
		if r.Method == http.MethodGet && r.URL.Path == "/api/health" && rec.status == http.StatusOK {
			return
		}
		// Query-параметры в лог — видно, что именно просил фронтенд.
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
