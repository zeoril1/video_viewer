package streamapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/remoteauth"
)

func TestStorageAPIAuthorizesBeforeReadingOrDeletingFiles(t *testing.T) {
	role, authStatus := "user", http.StatusOK
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(authStatus)
		_, _ = w.Write([]byte(`{"user":{"id":1,"username":"zeoril","role":"` + role + `"}}`))
	}))
	defer auth.Close()
	h := &storageHandler{access: remoteauth.New(auth.URL)}
	for _, tc := range []struct {
		role             string
		cookie           bool
		authStatus, want int
	}{
		{"user", true, 200, 403}, {"moderator", true, 200, 403},
		{"admin", false, 200, 401}, {"admin", true, 503, 503},
	} {
		role, authStatus = tc.role, tc.authStatus
		for _, method := range []string{"GET", "DELETE"} {
			r := httptest.NewRequest(method, "/api/admin/storage", nil)
			if tc.cookie {
				r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: "token"})
			}
			r.Header.Set("X-User-Role", "admin")
			w := httptest.NewRecorder()
			if method == "GET" {
				h.list(w, r)
			} else {
				h.remove(w, r)
			}
			if w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s role %s auth %d -> %d", method, role, authStatus, w.Code)
			}
		}
	}
	role, authStatus = "admin", 200
	r := httptest.NewRequest("DELETE", "/api/admin/storage/test", nil)
	r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: "token"})
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.remove(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("cross-origin deletion accepted", w.Code)
	}
}

func TestViewerDeletionAndLateHeartbeatAreIsolated(t *testing.T) {
	s := newViewingStore()
	r := httptest.NewRequest("DELETE", "/api/stream/viewing", strings.NewReader(`{"session":"abcdefghijklmnop"}`))
	r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: "token"})
	owner := viewerOwner(r)
	s.update(viewer{owner: owner, Session: "abcdefghijklmnop", Hash: "hash"})
	s.update(viewer{owner: "other", Session: "abcdefghijklmnop", Hash: "hash"})
	w := httptest.NewRecorder()
	s.handle(nil, remoteauth.New(""))(w, r)
	if w.Code != http.StatusNoContent || len(s.snapshot()) != 1 {
		t.Fatal("wrong session removed", w.Code)
	}
	s.update(viewer{owner: owner, Session: "abcdefghijklmnop", Hash: "hash"})
	if len(s.snapshot()) != 1 {
		t.Fatal("late heartbeat restored a closed session")
	}
	r = httptest.NewRequest("DELETE", "/api/stream/viewing", strings.NewReader(`{"session":"abcdefghijklmnop"}`))
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	s.handle(nil, remoteauth.New(""))(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
}
