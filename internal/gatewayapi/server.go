// Package gatewayapi — API-шлюз (микросервис gateway): раздаёт статику
// фронтенда (web/) и проксирует /api/* на внутренние микросервисы
// (catalog, stream, auth) по префиксам маршрутов. Также агрегирует
// /api/health по всем сервисам.
package gatewayapi

import (
	"context"
	"encoding/json"
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
	WebDir     string // каталог статики фронтенда (web/)
	CatalogURL string // http://catalog:8081
	StreamURL  string // http://stream:8082
	AuthURL    string // http://auth:8083
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
	// Админ-эндпоинты каталога (список пустых полей, редактирование,
	// обновление из TMDB, лог). Проверка роли — на стороне catalog.
	mux.HandleFunc("/api/admin/", proxyTo(cfg.CatalogURL))

	// ---- Стриминг (stream-сервис) ----
	mux.HandleFunc("GET /api/films/{id}/files", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/tracks", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/hls.m3u8", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/hls/stop", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/hls/pl.m3u8", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/hls/subs/", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/films/{id}/hls/segments/", proxyTo(cfg.StreamURL))
	mux.HandleFunc("GET /api/stream/", proxyTo(cfg.StreamURL))
	// /api/debug/* намеренно НЕ проксируется наружу (диагностика памяти и
	// торрентов доступна только во внутренней сети по портам 8081-8083).

	// ---- Авторизация и история (auth-сервис) ----
	mux.HandleFunc("/api/auth/", proxyTo(cfg.AuthURL))
	mux.HandleFunc("/api/history", proxyTo(cfg.AuthURL))
	mux.HandleFunc("/api/history/", proxyTo(cfg.AuthURL))

	// ---- Здоровье: агрегированный статус всех сервисов ----
	mux.HandleFunc("GET /api/health", healthHandler(cfg))

	// ---- Статические файлы фронтенда ----
	mux.Handle("/", http.FileServer(http.Dir(cfg.WebDir)))

	return httpx.LogMiddleware(mux)
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
