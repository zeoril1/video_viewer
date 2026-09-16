package gatewayapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientAddressTrustBoundary(t *testing.T) {
	for _, tc := range []struct{ name, peer, trusted, want string }{
		{"direct", "192.0.2.1:5000", "", "192.0.2.1"},
		{"untrusted", "192.0.2.1:5000", "127.0.0.1", "192.0.2.1"},
		{"proxy", "127.0.0.1:5000", "127.0.0.1", "198.51.100.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("X-Video-Viewer-Client-IP"); got != tc.want {
					t.Errorf("got %q, want %q", got, tc.want)
				}
			}))
			defer upstream.Close()
			h := NewServer(Config{AuthURL: upstream.URL, WebDir: t.TempDir(), TrustedProxy: tc.trusted})
			req := httptest.NewRequest("POST", "/api/auth/login", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Video-Viewer-Client-IP", "203.0.113.9")
			req.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.2")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatal(rec.Code)
			}
		})
	}
}
