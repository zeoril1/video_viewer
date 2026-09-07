// Package authapi — HTTP-сервис авторизации и истории просмотра
// (микросервис auth): регистрация/вход/выход, сессии (httpOnly-кука),
// история просмотра с сохранением позиции. Требует БД; без БД все
// эндпоинты отдают 503.
package authapi

import (
	"net/http"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/httpx"
)

// Config — зависимости HTTP-сервиса авторизации.
type Config struct {
	DB *db.Repo // может быть nil (режим без БД) — auth/history отдают 503
	// SecureCookies — ставить Secure-флаг на httpOnly-куку сессии
	// (нужно, когда фронтенд отдаётся за HTTPS-обратным прокси).
	SecureCookies bool
}

// NewServer собирает HTTP-обработчики авторизации и истории в один mux.
func NewServer(cfg Config) http.Handler {
	mux := http.NewServeMux()

	auth := &authHandler{repo: cfg.DB, secureCookies: cfg.SecureCookies, limiter: newLoginLimiter()}
	history := &historyHandler{repo: cfg.DB}

	// POST /api/auth/register — регистрация нового пользователя.
	mux.HandleFunc("POST /api/auth/register", auth.register)
	// POST /api/auth/login — вход по логину/паролю (создаёт сессию).
	mux.HandleFunc("POST /api/auth/login", auth.login)
	// POST /api/auth/logout — выход (удаляет сессию и куку).
	mux.HandleFunc("POST /api/auth/logout", auth.logout)
	// GET /api/auth/me — текущий пользователь (по куке сессии).
	mux.HandleFunc("GET /api/auth/me", auth.me)

	// GET /api/history — история просмотра текущего пользователя.
	mux.HandleFunc("GET /api/history", history.list)
	// POST /api/history/progress — сохранить позицию просмотра.
	mux.HandleFunc("POST /api/history/progress", history.save)
	// DELETE /api/history/{film_id} — удалить запись истории (?magnet=&file=).
	mux.HandleFunc("DELETE /api/history/{film_id}", history.remove)
	// DELETE /api/history — очистить всю историю.
	mux.HandleFunc("DELETE /api/history", history.clear)

	// GET /api/health — проверка живости.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	return httpx.LogMiddleware(mux)
}
