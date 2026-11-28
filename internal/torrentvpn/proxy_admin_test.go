package torrentvpn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/proxycheck"
)

func TestProxyAdminConfigurationAccessAndRefresh(t *testing.T) {
	auth := authServer(t)
	manager := newManager(filepath.Join(t.TempDir(), "wg0.conf"), func(tunnelConfig) (networkTunnel, error) { return &fakeTunnel{}, nil })
	defer manager.Close()
	path := filepath.Join(t.TempDir(), "proxies.json")
	pool, err := proxycheck.NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Replace(proxycheck.Config{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	router := NewFallbackRouter(manager, pool)
	handler := NewControlHandler(manager, auth.URL, ProxyControlOptions{Pool: pool, Router: router})
	request := func(method, path, body, cookie, origin, site string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://kino.test"+path, strings.NewReader(body))
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "video_viewer_session", Value: cookie})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, endpoint := range []struct{ method, path string }{{"GET", "/api/admin/proxies"}, {"PUT", "/api/admin/proxies"}, {"POST", "/api/admin/proxies/refresh"}} {
		for _, cookie := range []string{"", "user"} {
			w := request(endpoint.method, endpoint.path, `{}`, cookie, "http://kino.test", "same-origin")
			want := 401
			if cookie == "user" {
				want = 403
			}
			if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unauthorized %s %s: status=%d", endpoint.method, endpoint.path, w.Code)
			}
		}
	}
	for _, endpoint := range []struct{ method, path string }{{"PUT", "/api/admin/proxies"}, {"POST", "/api/admin/proxies/refresh"}} {
		if w := request(endpoint.method, endpoint.path, `{}`, "admin", "https://other.test", ""); w.Code != 403 {
			t.Fatalf("cross-origin %s accepted: %d", endpoint.method, w.Code)
		}
		if w := request(endpoint.method, endpoint.path, `{}`, "admin", "", "cross-site"); w.Code != 403 {
			t.Fatalf("cross-site %s accepted: %d", endpoint.method, w.Code)
		}
	}
	for _, body := range []string{`{"enabled":true,"unknown":1}`, `{} {}`, `{"enabled":`, strings.Repeat("x", 65<<10), `{"enabled":true,"sources":["http://127.0.0.1/list"]}`} {
		if w := request("PUT", "/api/admin/proxies", body, "admin", "http://kino.test", ""); w.Code != 400 {
			t.Fatalf("invalid proxy settings accepted: %d", w.Code)
		}
	}
	w := request("GET", "/api/admin/proxies", "", "admin", "", "")
	var status proxyAdminStatus
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.ActiveRoute != "unavailable" || status.IntervalSeconds != 300 || status.Config.Enabled {
		t.Fatalf("disabled/unconfigured status=%d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/admin/proxies/refresh", "", "admin", "http://kino.test", ""); w.Code != 409 {
		t.Fatalf("disabled refresh status=%d", w.Code)
	}
	// Empty enabled settings exercise the asynchronous API without any source
	// or proxy connections, and remain valid after restarting the service.
	w = request("PUT", "/api/admin/proxies", `{"enabled":true,"sources":[],"static":[]}`, "admin", "http://kino.test", "")
	if w.Code != 200 {
		t.Fatalf("save empty settings: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/admin/proxies/refresh", "", "admin", "http://kino.test", ""); w.Code != 202 {
		t.Fatalf("manual asynchronous refresh status=%d %s", w.Code, w.Body.String())
	}
	reloaded, err := proxycheck.NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if !reloaded.Status().Config.Enabled || len(reloaded.Status().Config.Sources) != 0 {
		t.Fatal("admin settings were not persisted")
	}
	if _, err := manager.Replace("wg0.conf", testConfig()); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/api/admin/proxies", "", "admin", "", "")
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || status.ActiveRoute != "vpn" || !status.VPNConfigured {
		t.Fatalf("configured VPN is not preferred: %s", w.Body.String())
	}
}
