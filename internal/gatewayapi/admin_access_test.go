package gatewayapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminPagesProtectedOnServer(t *testing.T) {
	dir := t.TempDir()
	for _, file := range []string{"admin.html", "storage.html"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("protected contents"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	role := "user"
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"user":{"id":1,"username":"test","role":"` + role + `"}}`))
	}))
	defer auth.Close()
	h := NewServer(Config{WebDir: dir, AuthURL: auth.URL})
	for _, path := range []string{"/admin.html", "/storage.html", "/ADMIN.HTML", "/admin.html.", "/x%5C..%5Cadmin.html", "/admin.html::$DATA"} {
		for _, roleValue := range []string{"user", "moderator", ""} {
			role = roleValue
			r := httptest.NewRequest("GET", path, nil)
			if roleValue != "" {
				r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: "token"})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code == 200 || strings.Contains(w.Body.String(), "protected contents") {
				t.Fatalf("%s role %s bypassed access", path, roleValue)
			}
		}
	}
	role = "admin"
	r := httptest.NewRequest("GET", "/admin.html", nil)
	r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: "token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Cache-Control"))
	}
}

func TestNewAdministrationRoutesReachTheirServices(t *testing.T) {
	upstream := func(label string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(label)) }))
	}
	auth, stream, catalog := upstream("auth"), upstream("stream"), upstream("catalog")
	defer auth.Close()
	defer stream.Close()
	defer catalog.Close()
	h := NewServer(Config{WebDir: t.TempDir(), AuthURL: auth.URL, StreamURL: stream.URL, CatalogURL: catalog.URL})
	for _, tc := range []struct{ method, path, want string }{
		{"GET", "/api/admin/users", "auth"}, {"PUT", "/api/admin/users/1/role", "auth"},
		{"GET", "/api/admin/storage", "stream"}, {"DELETE", "/api/admin/storage/123?file=1", "stream"},
		{"POST", "/api/stream/viewing", "stream"}, {"DELETE", "/api/stream/viewing", "stream"},
		{"GET", "/api/admin/missing", "catalog"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Body.String() != tc.want {
			t.Errorf("%s %s: %s", tc.method, tc.path, w.Body.String())
		}
	}
}
