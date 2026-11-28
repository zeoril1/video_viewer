package gatewayapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyAdminRoutesUseVPNControlService(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("proxy administration was forwarded to an unrelated service")
	}))
	defer other.Close()
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/proxies"},
		{http.MethodPut, "/api/admin/proxies"},
		{http.MethodPost, "/api/admin/proxies/refresh"},
	} {
		t.Run(endpoint.method+endpoint.path, func(t *testing.T) {
			calls := 0
			control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if r.Method != endpoint.method || r.URL.Path != endpoint.path || string(body) != "settings" ||
					r.Header.Get("Cookie") != "video_viewer_session=admin" || r.Header.Get("Origin") != "https://kino.example" {
					t.Error("proxy request lost its body, path, method or authentication headers")
				}
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusAccepted)
			}))
			defer control.Close()
			handler := NewServer(Config{WebDir: t.TempDir(), CatalogURL: other.URL, StreamURL: other.URL, AuthURL: other.URL, TorrentVPNURL: control.URL})
			r := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader("settings"))
			r.Host = "kino.example"
			r.Header.Set("Origin", "https://kino.example")
			r.Header.Set("Cookie", "video_viewer_session=admin")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if calls != 1 || w.Code != http.StatusAccepted || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("forwarding calls=%d status=%d headers=%v", calls, w.Code, w.Header())
			}
		})
	}
}
