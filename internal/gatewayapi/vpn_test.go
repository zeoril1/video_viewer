package gatewayapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVPNAdminRoutesAreForwardedToIsolatedService(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("VPN request routed to catalogue or streaming service")
	}))
	defer other.Close()
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			called := false
			vpn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				body, _ := io.ReadAll(r.Body)
				if r.Method != method || r.URL.Path != "/api/admin/vpn" || r.Header.Get("Cookie") != "video_viewer_session=test" || r.Header.Get("Origin") != "https://kino.example" || string(body) != "config upload" {
					t.Error("VPN upload or authorization headers lost")
				}
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusAccepted)
			}))
			defer vpn.Close()
			h := NewServer(Config{WebDir: t.TempDir(), CatalogURL: other.URL, StreamURL: other.URL, TorrentVPNURL: vpn.URL})
			r := httptest.NewRequest(method, "/api/admin/vpn", strings.NewReader("config upload"))
			r.Host = "kino.example"
			r.Header.Set("Origin", "https://kino.example")
			r.Header.Set("Cookie", "video_viewer_session=test")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if !called || w.Code != http.StatusAccepted || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(called, w.Code)
			}
		})
	}
}
