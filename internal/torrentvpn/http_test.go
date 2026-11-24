package torrentvpn

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func authServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("video_viewer_session")
		if err != nil {
			w.WriteHeader(401)
			return
		}
		role := "user"
		if c.Value == "admin" {
			role = "admin"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": 1, "username": "test", "role": role}})
	}))
	t.Cleanup(s.Close)
	return s
}

func uploadRequest(t *testing.T, name string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("config", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	_ = writer.Close()
	r := httptest.NewRequest(http.MethodPut, "http://kino.test/api/admin/vpn", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Origin", "http://kino.test")
	r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: "admin"})
	return r
}

func TestAdminAPIProtectsKeysAndRejectsCrossOriginAndOversizeUploads(t *testing.T) {
	auth := authServer(t)
	m := newManager(filepath.Join(t.TempDir(), "wg0.conf"), func(tunnelConfig) (networkTunnel, error) { return &fakeTunnel{}, nil })
	defer m.Close()
	handler := NewControlHandler(m, auth.URL)
	for _, cookie := range []string{"", "user"} {
		r := httptest.NewRequest("GET", "http://kino.test/api/admin/vpn", nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := 401
		if cookie == "user" {
			want = 403
		}
		if w.Code != want {
			t.Fatalf("cookie=%q status=%d", cookie, w.Code)
		}
	}
	r := uploadRequest(t, "vpn.conf", testConfig())
	r.Header.Set("Origin", "https://other.test")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 || m.Status().Configured {
		t.Fatal("cross origin upload was accepted")
	}
	r = uploadRequest(t, "vpn.conf", []byte(strings.Repeat("x", MaxConfigBytes+1)))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 400 || m.Status().Configured {
		t.Fatal("oversize upload was accepted")
	}
	r = uploadRequest(t, "vpn.ovpn", testConfig())
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("wrong extension accepted")
	}
	r = uploadRequest(t, "vpn.conf", testConfig())
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("valid upload failed: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "PrivateKey") || strings.Contains(w.Body.String(), "YWFh") {
		t.Fatal("upload response disclosed configuration")
	}
	r = httptest.NewRequest("GET", "http://kino.test/api/admin/vpn", nil)
	r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: "admin"})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("admin status is not private/no-store")
	}
	r = httptest.NewRequest("GET", "http://kino.test/api/health", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("health requires a configured VPN")
	}
}

func TestProxyNeverFallsBackToDirectHTTP(t *testing.T) {
	var direct int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct++; _, _ = io.WriteString(w, "leak") }))
	defer target.Close()
	m := newManager(filepath.Join(t.TempDir(), "wg0.conf"), func(tunnelConfig) (networkTunnel, error) { return &fakeTunnel{fail: true}, nil })
	defer m.Close()
	p := NewProxyHandler(m)
	r := httptest.NewRequest("GET", target.URL+"/file.torrent", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 503 || direct != 0 {
		t.Fatal("unconfigured proxy tried direct HTTP")
	}
	if _, err := m.Replace("vpn.conf", testConfig()); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 502 || direct != 0 {
		t.Fatal("failed VPN proxy tried direct HTTP")
	}
	r = httptest.NewRequest(http.MethodConnect, "http://"+strings.TrimPrefix(target.URL, "http://"), nil)
	r.Host = strings.TrimPrefix(target.URL, "http://")
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 502 || direct != 0 {
		t.Fatal("failed VPN proxy tried direct CONNECT")
	}
}

func TestProxyStripsHopHeadersButKeepsDownloadCookies(t *testing.T) {
	h := http.Header{"Connection": {"X-Hop, Cookie-Hop"}, "X-Hop": {"remove"}, "Cookie-Hop": {"remove"}, "Proxy-Authorization": {"secret"}, "Cookie": {"tracker=session"}, "Referer": {"https://tracker.test/topic"}}
	removeHopHeaders(h)
	if h.Get("X-Hop") != "" || h.Get("Cookie-Hop") != "" || h.Get("Proxy-Authorization") != "" || h.Get("Cookie") != "tracker=session" || h.Get("Referer") == "" {
		t.Fatal("hop headers or tracker authentication mishandled")
	}
}
