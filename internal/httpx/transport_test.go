package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTorrentClientAlwaysUsesProxyIncludingNoProxyHosts(t *testing.T) {
	t.Setenv("NO_PROXY", "tracker.invalid,127.0.0.1")
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Host != "tracker.invalid" || r.URL.Path != "/release.torrent" {
			t.Errorf("proxy received %s", r.URL)
		}
		io.WriteString(w, "torrent metadata")
	}))
	defer proxy.Close()
	client, err := VPNHTTPClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get("http://tracker.invalid/release.torrent")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if body, _ := io.ReadAll(response.Body); string(body) != "torrent metadata" || calls.Load() != 1 {
		t.Fatal(string(body), calls.Load())
	}
}

func TestTorrentClientCannotFallBackToDirect(t *testing.T) {
	var direct atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1) }))
	defer origin.Close()
	deadProxy := httptest.NewServer(http.NotFoundHandler())
	deadURL := deadProxy.URL
	deadProxy.Close()
	for _, proxy := range []string{"", deadURL} {
		client, err := VPNHTTPClient(proxy)
		if err != nil {
			t.Fatal(err)
		}
		if response, err := client.Get(origin.URL + "/test.torrent"); err == nil {
			response.Body.Close()
			t.Fatal("unavailable VPN unexpectedly succeeded")
		}
	}
	if direct.Load() != 0 {
		t.Fatal("torrent download escaped to the direct route")
	}
}

func TestInvalidTorrentProxyDoesNotExposeCredentials(t *testing.T) {
	for _, raw := range []string{"socks5://user:secret@proxy:1080", "http://user:secret@proxy/path", "://secret"} {
		_, err := VPNHTTPClient(raw)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe proxy error", err)
		}
	}
}

func TestDirectTransportDoesNotInheritVPNProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	if DirectTransport().Proxy != nil {
		t.Fatal("direct transport inherited a global VPN proxy")
	}
}
