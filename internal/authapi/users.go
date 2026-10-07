package authapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/zeoril1/video_viewer/internal/authn"
	"github.com/zeoril1/video_viewer/internal/db"
)

func (h *authHandler) adminUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.repo == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if _, ok := authn.RequireAdmin(h.repo, w, r); !ok {
		return
	}
	items, err := h.repo.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	writePersonalJSON(w, map[string]any{"items": items})
}

func (h *authHandler) adminUserRole(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.repo == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	u, ok := authn.RequireAdmin(h.repo, w, r)
	if !ok || !checkOrigin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if !readSmallJSON(w, r, &body) {
		return
	}
	if !db.ValidRole(body.Role) {
		http.Error(w, "invalid role: user, moderator or admin required", http.StatusBadRequest)
		return
	}
	user, err := h.repo.ChangeUserRole(r.Context(), u.ID, id, body.Role)
	switch {
	case errors.Is(err, db.ErrLastAdmin):
		http.Error(w, "cannot remove the last administrator", http.StatusConflict)
	case errors.Is(err, db.ErrUserNotFound):
		http.Error(w, "user not found", http.StatusNotFound)
	case errors.Is(err, db.ErrAdminRequired):
		http.Error(w, "forbidden: admin only", http.StatusForbidden)
	case err != nil:
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
	default:
		writePersonalJSON(w, map[string]any{"user": user})
	}
}
