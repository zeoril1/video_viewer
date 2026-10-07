package remoteauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireAdminChecksLiveSession(t *testing.T) {
	role := "admin"
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/me" || r.Header.Get("Cookie") != "video_viewer_session=token" {
			t.Errorf("wrong auth lookup: %s, %s", r.URL, r.Header.Get("Cookie"))
		}
		if r.Header.Get("X-User-Role") != "" {
			t.Error("untrusted role was forwarded")
		}
		_, _ = w.Write([]byte(`{"user":{"id":1,"username":"zeoril","role":"` + role + `"}}`))
	}))
	defer auth.Close()
	client := New(auth.URL)
	for _, allowed := range []bool{true, false} {
		if !allowed {
			role = "user"
		}
		r := httptest.NewRequest("GET", "/admin.html", nil)
		r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: "token"})
		r.AddCookie(&http.Cookie{Name: "other", Value: "secret"})
		r.Header.Set("X-User-Role", "admin")
		w := httptest.NewRecorder()
		if got := client.RequireAdmin(w, r); got != allowed {
			t.Fatalf("allowed=%v, got=%v", allowed, got)
		}
		if !allowed && w.Code != http.StatusForbidden {
			t.Fatal(w.Code)
		}
	}
}

func TestRemoteAuthFailsClosed(t *testing.T) {
	for _, upstreamStatus := range []int{401, 500, 302, 200} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "https://example.org/")
			w.WriteHeader(upstreamStatus)
			_, _ = w.Write([]byte(`{"user":{"role":"admin"}}`))
		}))
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: "token"})
		_, status := New(srv.URL).Current(r)
		srv.Close()
		want := http.StatusServiceUnavailable
		if upstreamStatus == 401 {
			want = 401
		}
		if status != want {
			t.Errorf("upstream %d -> %d, want %d", upstreamStatus, status, want)
		}
	}
	_, status := New("").Current(httptest.NewRequest("GET", "/", nil))
	if status != http.StatusUnauthorized {
		t.Fatal(status)
	}
}
