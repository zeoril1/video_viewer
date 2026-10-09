package authapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
)

func TestPersonalSkipWritePermissions(t *testing.T) {
	for _, role := range []string{"", db.RoleUser, db.RoleModerator, db.RoleAdmin} {
		for _, key := range []string{"segments.0123456789abcdef0123456789abcdef01234567.0", "segments.anything", "skip_segments", "playback"} {
			want := key == "skip_segments" || key == "playback" || role == db.RoleModerator || role == db.RoleAdmin
			if got := canWritePersonal(db.User{Role: role}, "preferences", key); got != want {
				t.Errorf("role=%q key=%q allowed=%v, want %v", role, key, got, want)
			}
			if !canWritePersonal(db.User{Role: role}, "watchlist", key) {
				t.Error("non-preference item blocked")
			}
		}
	}
}

func TestAdminUsersUnavailableWithoutDatabase(t *testing.T) {
	h := NewServer(Config{})
	for _, request := range []struct{ method, path string }{{"GET", "/api/admin/users"}, {"PUT", "/api/admin/users/1/role"}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(request.method, request.path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: %d, want 503", request.method, request.path, rec.Code)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Error("private role response could be cached")
		}
	}
}
