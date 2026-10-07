// Package authn — проверка сессии и прав для внутренних сервисов.
//
// Сессии лежат в общей БД (таблица sessions), токен приходит в httpOnly-куке
// video_viewer_session: любой сервис с доступом к БД проверяет пользователя сам.
package authn

import (
	"encoding/json"
	"net/http"

	"github.com/zeoril1/video_viewer/internal/db"
)

// SessionCookieName — имя куки с токеном сессии (то же, что в auth-сервисе).
const SessionCookieName = "video_viewer_session"

// CanEditSegments разрешает менять границы пропусков администраторам и модераторам.
func CanEditSegments(u db.User) bool {
	return u.Role == db.RoleAdmin || u.Role == db.RoleModerator
}

// Current возвращает пользователя по куке сессии (валидность проверяется по БД).
func Current(repo *db.Repo, r *http.Request) (db.User, bool) {
	if repo == nil {
		return db.User{}, false
	}
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return db.User{}, false
	}
	u, ok, err := repo.GetUserBySession(r.Context(), c.Value)
	if err != nil || !ok {
		return db.User{}, false
	}
	return u, true
}

// RequireAdmin проверяет, что запрос идёт от admin: иначе 401/403 и false.
func RequireAdmin(repo *db.Repo, w http.ResponseWriter, r *http.Request) (db.User, bool) {
	u, ok := Current(repo, r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return db.User{}, false
	}
	if u.Role != db.RoleAdmin {
		http.Error(w, "forbidden: admin only", http.StatusForbidden)
		return db.User{}, false
	}
	return u, true
}

// WriteJSON отдаёт JSON-ответ.
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
