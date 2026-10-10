package iptvapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
)

func TestRedirectFailureDoesNotLeakUpstreamURL(t *testing.T) {
	const marker = "regression-private-marker"
	unavailable := httptest.NewServer(http.NotFoundHandler())
	unavailableURL := unavailable.URL
	unavailable.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, unavailableURL+"/private-path-"+marker+"/index.m3u8?token="+marker, http.StatusFound)
	}))
	defer upstream.Close()
	m := newLiveManager(Config{DataDir: t.TempDir()})
	_, err := m.fetch(context.Background(), upstream.URL+"/index.m3u8", "", "", "")
	if err == nil {
		t.Fatal("redirect to unavailable upstream succeeded")
	}
	var original *url.Error
	if !errors.As(err, &original) {
		t.Fatal("original request failure is unavailable to errors.As")
	}
	if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), unavailableURL) {
		t.Fatal("public fetch error leaked upstream URL")
	}

	var logs bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousLogWriter)
	s := &Server{live: m}
	token := m.addToken(upstream.URL+"/index.m3u8", "", "")
	r := httptest.NewRequest(http.MethodGet, "/api/iptv/seg/"+token, nil)
	r.SetPathValue("token", token)
	w := httptest.NewRecorder()
	s.handleSegment(w, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
	for _, output := range []string{w.Body.String(), logs.String()} {
		if strings.Contains(output, marker) || strings.Contains(output, unavailableURL) || strings.Contains(output, upstream.URL) {
			t.Fatal("failed redirect leaked upstream URL in response or logs")
		}
	}
	if !strings.Contains(logs.String(), "вложенного HLS-плейлиста") {
		t.Fatal("diagnostic does not identify a playlist fetch failure")
	}
}

const redirectTestUA = "IPTV-Redirect-Test"
const redirectTestReferer = "https://provider.example/player"

// Проверяем не только адреса токенов, но и загрузку каждого ресурса через
// настоящий HTTP-прокси, включая заголовки канала и частичный сегмент.
func TestPlaylistFinalURLResources(t *testing.T) {
	for _, mode := range []string{"no redirect", "same origin", "cross origin chain"} {
		t.Run(mode, func(t *testing.T) {
			var resourceURL string
			serveResources := func(w http.ResponseWriter, r *http.Request) {
				assertRedirectHeaders(t, r)
				switch r.URL.Path {
				case "/cdn/final/index.m3u8":
					w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
					fmt.Fprintf(w, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin?k=1\"\n#EXT-X-MAP:URI=\"../init.mp4\"\n#EXTINF:6,\nsegment.ts\n#EXTINF:6,\n%s/absolute.ts?token=private\n#EXT-X-ENDLIST\n", resourceURL)
				case "/cdn/final/key.bin":
					if r.URL.RawQuery != "k=1" {
						t.Errorf("key query = %q", r.URL.RawQuery)
					}
					_, _ = w.Write([]byte("encryption-key"))
				case "/cdn/init.mp4":
					_, _ = w.Write([]byte("initialization"))
				case "/cdn/final/segment.ts":
					if r.Header.Get("Range") != "bytes=2-5" {
						t.Errorf("Range = %q", r.Header.Get("Range"))
					}
					w.Header().Set("Content-Type", "video/mp2t")
					w.Header().Set("Accept-Ranges", "bytes")
					w.Header().Set("Content-Range", "bytes 2-5/10")
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write([]byte("2345"))
				case "/absolute.ts":
					if r.URL.RawQuery != "token=private" {
						t.Errorf("absolute query = %q", r.URL.RawQuery)
					}
					_, _ = w.Write([]byte("absolute-segment"))
				default:
					http.NotFound(w, r)
				}
			}
			cdn := httptest.NewServer(http.HandlerFunc(serveResources))
			defer cdn.Close()
			resourceURL = cdn.URL
			var originURL string
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assertRedirectHeaders(t, r)
				switch r.URL.Path {
				case "/start":
					if mode == "same origin" {
						http.Redirect(w, r, "/cdn/final/index.m3u8", http.StatusFound)
					} else {
						http.Redirect(w, r, "/relay", http.StatusFound)
					}
				case "/relay":
					http.Redirect(w, r, cdn.URL+"/cdn/final/index.m3u8", http.StatusTemporaryRedirect)
				default:
					serveResources(w, r)
				}
			}))
			defer origin.Close()
			originURL = origin.URL
			startURL, finalURL := originURL+"/start", originURL+"/cdn/final/index.m3u8"
			if mode == "no redirect" {
				startURL = finalURL
			}
			if mode == "cross origin chain" {
				finalURL = cdn.URL + "/cdn/final/index.m3u8"
			}
			base := strings.TrimSuffix(finalURL, "index.m3u8")
			m := newLiveManager(Config{DataDir: t.TempDir()})
			body, err := m.playlist(context.Background(), db.IPTVChannel{ID: 42, IsHLS: true, StreamURL: startURL}, redirectTestUA, redirectTestReferer, db.IPTVPlaylist{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "private") || strings.Contains(string(body), cdn.URL) {
				t.Fatalf("upstream URL leaked: %s", body)
			}
			checkProxyResources(t, m, string(body), []proxyResourceWant{
				{base + "key.bin?k=1", "encryption-key", 200, ""},
				{strings.TrimSuffix(base, "final/") + "init.mp4", "initialization", 200, ""},
				{base + "segment.ts", "2345", 206, "bytes=2-5"},
				{cdn.URL + "/absolute.ts?token=private", "absolute-segment", 200, ""},
			})
		})
	}
}

func TestNestedPlaylistFinalURLResources(t *testing.T) {
	var cdnURL string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertRedirectHeaders(t, r)
		switch r.URL.Path {
		case "/relocated/media/index.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6,\nsegment.ts\n#EXT-X-ENDLIST\n")
		case "/relocated/media/key.bin":
			fmt.Fprint(w, "nested-key")
		case "/relocated/media/init.mp4":
			fmt.Fprint(w, "nested-init")
		case "/relocated/media/segment.ts":
			fmt.Fprint(w, "nested-segment")
		default:
			http.NotFound(w, r)
		}
	}))
	defer cdn.Close()
	cdnURL = cdn.URL
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertRedirectHeaders(t, r)
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/master/index.m3u8", http.StatusFound)
		case "/master/index.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\nvariant.m3u8\n")
		case "/master/variant.m3u8":
			http.Redirect(w, r, cdnURL+"/relocated/media/index.m3u8", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	m := newLiveManager(Config{DataDir: t.TempDir()})
	body, err := m.playlist(context.Background(), db.IPTVChannel{ID: 7, IsHLS: true, StreamURL: origin.URL + "/start"}, redirectTestUA, redirectTestReferer, db.IPTVPlaylist{})
	if err != nil {
		t.Fatal(err)
	}
	tokens := extractTokens(string(body))
	if len(tokens) != 1 {
		t.Fatalf("master tokens = %v", tokens)
	}
	ref, ok := m.lookupToken(tokens[0])
	if !ok || ref.url != origin.URL+"/master/variant.m3u8" {
		t.Fatalf("wrong variant: %+v", ref)
	}
	proxy := newRedirectTestProxy(m)
	defer proxy.Close()
	resp, err := http.Get(proxy.URL + "/api/iptv/seg/" + tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	nested, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !isHLSPlaylist(nested) {
		t.Fatalf("nested = %d %s", resp.StatusCode, nested)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("nested playlist is cacheable")
	}
	checkProxyResources(t, m, string(nested), []proxyResourceWant{
		{cdn.URL + "/relocated/media/key.bin", "nested-key", 200, ""},
		{cdn.URL + "/relocated/media/init.mp4", "nested-init", 200, ""},
		{cdn.URL + "/relocated/media/segment.ts", "nested-segment", 200, ""},
	})
}

type proxyResourceWant struct {
	url, body string
	status    int
	rangeHdr  string
}

func assertRedirectHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("User-Agent") != redirectTestUA || r.Header.Get("Referer") != redirectTestReferer {
		t.Errorf("headers on %s: UA = %q, Referer = %q", r.URL.Path, r.Header.Get("User-Agent"), r.Header.Get("Referer"))
	}
}

func newRedirectTestProxy(m *liveManager) *httptest.Server {
	s := &Server{live: m}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/iptv/seg/{token}", s.handleSegment)
	return httptest.NewServer(mux)
}

func checkProxyResources(t *testing.T, m *liveManager, body string, wants []proxyResourceWant) {
	t.Helper()
	tokens := extractTokens(body)
	if len(tokens) != len(wants) {
		t.Fatalf("tokens = %v, want %d; playlist = %s", tokens, len(wants), body)
	}
	proxy := newRedirectTestProxy(m)
	defer proxy.Close()
	for i, token := range tokens {
		want := wants[i]
		ref, ok := m.lookupToken(token)
		if !ok || ref.url != want.url {
			t.Fatalf("URI %d = %s, want %s", i, ref.url, want.url)
		}
		req, err := http.NewRequest(http.MethodGet, proxy.URL+"/api/iptv/seg/"+token, nil)
		if err != nil {
			t.Fatal(err)
		}
		if want.rangeHdr != "" {
			req.Header.Set("Range", want.rangeHdr)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want.status || string(payload) != want.body {
			t.Fatalf("resource %d = %d %q, want %d %q", i, resp.StatusCode, payload, want.status, want.body)
		}
		if want.rangeHdr != "" && (resp.Header.Get("Content-Range") != "bytes 2-5/10" || resp.Header.Get("Accept-Ranges") != "bytes") {
			t.Fatalf("range response lost: %v", resp.Header)
		}
	}
}
