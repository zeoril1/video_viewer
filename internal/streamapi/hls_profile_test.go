package streamapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHLSForwardWindowUsesAndroidUserAgent(t *testing.T) {
	for _, tc := range []struct {
		name, ua string
		android  bool
	}{
		{"android-webview", "Mozilla/5.0 (Linux; Android 13; TV) AppleWebKit/537.36 VVTV/1.0", true},
		{"android-chrome", "Mozilla/5.0 (Linux; Android 14) Chrome/128.0", true},
		{"legacy-app-marker", "Mozilla/5.0 VVTV/1.0", true},
		{"case-insensitive", "ANDROID vvtv/1.0", true},
		{"desktop", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/128.0", false},
		{"ios", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)", false},
		{"unspecified", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, configured := range []int64{defaultHLSForwardBytes, 0, 32 << 20} {
				m := &hlsManager{forwardBytes: configured}
				r := httptest.NewRequest(http.MethodGet, "/?buffer=android", nil)
				r.Header.Set("User-Agent", tc.ua)
				want := configured
				if want <= 0 {
					want = defaultHLSForwardBytes
				}
				if tc.android && want > androidHLSForwardBytes {
					want = androidHLSForwardBytes
				}
				if got := m.forwardBytesForRequest(r); got != want {
					t.Fatalf("configured=%d: got %d, want %d", configured, got, want)
				}
			}
		})
	}
}

// Exercise the initial manifest handler and real producer, then retain the
// resulting per-viewer limit across UA changes, segment fetches and heartbeats.
func TestHLSManifestCreatesStableAndroidAndDesktopWindows(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := filepath.Join(t.TempDir(), "source.mkv")
	create := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=4", "-t", "18",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "24", fixture)
	if output, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create profile fixture: %v: %s", err, output)
	}
	m := newHLSManager("")
	m.dataDir = t.TempDir()
	m.probeCache[probeKey("tt-profile", "magnet", 0)] = probeResult{Duration: 18, VideoCodec: "h264"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stream/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, fixture)
	})
	mux.HandleFunc("PUT "+hlsOutputPath+"{token}/{name}", m.serveOutput)
	mux.HandleFunc("GET /api/films/{id}/hls.m3u8", m.servePlaylist)
	mux.HandleFunc("GET /api/films/{id}/hls/pl.m3u8", m.serveMediaPlaylist)
	mux.HandleFunc("GET /api/films/{id}/hls/segments/{name}", m.serveSegment)
	server := httptest.NewServer(mux)
	defer server.Close()
	defer m.stopAll()
	m.selfBase = server.URL
	request := func(t *testing.T, path, ua string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		r.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", path, w.Code, w.Body.String())
		}
		return w
	}
	for _, tc := range []struct {
		session, ua string
		limit       int64
	}{
		{"android", "Mozilla/5.0 VVTV/1.0", androidHLSForwardBytes},
		{"desktop", "Mozilla/5.0 (Windows NT 10.0)", defaultHLSForwardBytes},
	} {
		t.Run(tc.session, func(t *testing.T) {
			query := "?magnet=magnet&file=0&track=-1&session=" + tc.session
			request(t, "/api/films/tt-profile/hls.m3u8"+query, tc.ua)
			key := hlsSessionKey("tt-profile", tc.session)
			m.mu.Lock()
			s := m.sessions[key]
			m.mu.Unlock()
			if s == nil || s.output.limit != tc.limit || s.output.fragmentLimit != defaultHLSForwardBytes {
				t.Fatalf("manifest created the wrong output window: %+v", s)
			}
			select {
			case <-s.done:
			case <-ctx.Done():
				t.Fatal("producer did not finish", ctx.Err())
			}
			m.mu.Lock()
			exitErr := s.exitErr
			m.mu.Unlock()
			if exitErr != nil {
				t.Fatal("producer failed", exitErr)
			}
			// A UA change cannot recreate the stream, expand an Android window,
			// or shrink a desktop viewer's independent window.
			otherUA := "Mozilla/5.0 (Linux; Android 14)"
			if tc.session == "android" {
				otherUA = "Mozilla/5.0 (Windows NT 10.0)"
			}
			request(t, "/api/films/tt-profile/hls.m3u8"+query, otherUA)
			generationQuery := query + fmt.Sprintf("&generation=%d", s.generation)
			playlist := request(t, "/api/films/tt-profile/hls/pl.m3u8"+generationQuery, otherUA).Body.String()
			if !strings.Contains(playlist, "&session="+tc.session) || !strings.Contains(playlist, "&generation=") {
				t.Fatal("profile lost playback isolation", playlist)
			}
			for _, name := range []string{"init.mp4", "seg_00000.m4s"} {
				if w := request(t, "/api/films/tt-profile/hls/segments/"+name+generationQuery, otherUA); w.Body.Len() == 0 {
					t.Fatal("empty fragment", name)
				}
			}
			if !m.touchPlayback("tt-profile", tc.session, "magnet", 0, 0, -1, -1, "source", 6) {
				t.Fatal("buffered playback heartbeat was rejected")
			}
			m.mu.Lock()
			current := m.sessions[key]
			m.mu.Unlock()
			if current != s || s.output.limit != tc.limit || s.output.fragmentLimit != defaultHLSForwardBytes || s.playhead != 6 || s.resourceLimited {
				t.Fatal("normal requests changed the session or its profile", s.output.limit)
			}
		})
	}
}
