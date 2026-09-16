// Package authapi — HTTP-сервис авторизации и истории просмотра (микросервис
// auth): регистрация/вход/выход, сессии (httpOnly-кука), история с позицией.
// Требует БД; без БД все эндпоинты отдают 503.
package authapi

import (
	"net/http"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/httpx"
)

// Config — зависимости HTTP-сервиса авторизации.
type Config struct {
	DB *db.Repo // может быть nil (режим без БД) — auth/history отдают 503
	// SecureCookies — принудительный Secure-флаг на куку сессии
	// (COOKIE_SECURE=1); обычно не нужен — флаг включается сам на HTTPS.
	SecureCookies bool
}

// NewServer собирает HTTP-обработчики авторизации и истории в один mux.
func NewServer(cfg Config) http.Handler {
	mux := http.NewServeMux()

	auth := &authHandler{repo: cfg.DB, secureCookies: cfg.SecureCookies, limiter: newLoginLimiter()}
	history := &historyHandler{repo: cfg.DB}

	mux.HandleFunc("POST /api/auth/register", auth.register)
	mux.HandleFunc("POST /api/auth/login", auth.login)
	mux.HandleFunc("POST /api/auth/logout", auth.logout)
	mux.HandleFunc("GET /api/auth/me", auth.me)

	mux.HandleFunc("GET /api/history", history.list)
	mux.HandleFunc("POST /api/history/progress", history.save)
	// Запись истории удаляется вместе с файлом: ?magnet=&file=.
	mux.HandleFunc("DELETE /api/history/{film_id}", history.remove)
	mux.HandleFunc("DELETE /api/history", history.clear)

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	return httpx.LogMiddleware(mux)
}
