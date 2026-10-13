package streamapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func outputTestSession(t *testing.T, limit int64) (*hlsManager, *hlsSession, string) {
	t.Helper()
	m := newHLSManager("http://127.0.0.1:8082")
	m.dataDir = t.TempDir()
	token, err := newHLSOutputToken()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(m.dataDir, "session")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s := &hlsSession{id: "tt1", magnet: "magnet", file: 0, track: 0, subs: -1, quality: "source", dir: dir,
		playlist: filepath.Join(dir, "playlist.m3u8"), lastUsed: time.Now(), output: newHLSOutputWindow(limit), outputToken: token}
	m.outputs[token] = s
	m.sessions[hlsSessionKey("tt1", "session")] = s
	t.Cleanup(func() { s.stop() })
	return m, s, token
}

func outputRequest(m *hlsManager, token, name string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, hlsOutputPath+token+"/"+name, body)
	r.RemoteAddr = "127.0.0.1:12345"
	r.SetPathValue("token", token)
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	m.serveOutput(w, r)
	return w
}

func TestHLSForwardWindowWaitsAndResumesWithoutLosingSession(t *testing.T) {
	m, s, token := outputTestSession(t, 64)
	for name, content := range map[string]string{"init.mp4": "init", "seg_00000.m4s": strings.Repeat("a", 40)} {
		if w := outputRequest(m, token, name, strings.NewReader(content)); w.Code != http.StatusCreated {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	playlist := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6,\nseg_00000.m4s\n"
	if w := outputRequest(m, token, "playlist.m3u8", strings.NewReader(playlist)); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		completed <- outputRequest(m, token, "seg_00001.m4s", strings.NewReader(strings.Repeat("b", 40)))
	}()
	select {
	case w := <-completed:
		t.Fatal("full window did not block", w.Code, w.Body.String())
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(s.dir, "seg_00001.m4s")); !os.IsNotExist(err) {
		t.Fatal("incomplete segment was published", err)
	}
	if !m.touchPlayback("tt1", "session", "magnet", 0, 0, 0, -1, "source", 1) {
		t.Fatal("desktop heartbeat rejected")
	}
	select {
	case w := <-completed:
		t.Fatal("desktop hard window credited a still-playing fragment", w.Code)
	case <-time.After(30 * time.Millisecond):
	}
	// No segment or playlist request is needed to retain a buffered player.
	old := time.Now().Add(-2 * time.Minute)
	m.mu.Lock()
	s.lastUsed = old
	m.mu.Unlock()
	if !m.touchPlayback("tt1", "session", "magnet", 0, 0, 0, -1, "source", 6) {
		t.Fatal("heartbeat rejected")
	}
	select {
	case w := <-completed:
		if w.Code != http.StatusCreated {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("producer did not resume at the same session")
	}
	if m.sessions[hlsSessionKey("tt1", "session")] != s || !s.lastUsed.After(old) || s.resourceLimited {
		t.Fatal("forward buffer limit ended or replaced the active session")
	}
	data, err := os.ReadFile(s.playlist)
	if err != nil || strings.Contains(string(data), "#EXT-X-ENDLIST") {
		t.Fatal("limited window became a completed film", string(data), err)
	}
	for _, name := range []string{"init.mp4", "seg_00000.m4s", "seg_00001.m4s"} {
		if _, err := os.Stat(filepath.Join(s.dir, name)); err != nil {
			t.Fatal("retained media disappeared", name, err)
		}
	}
	s.output.mu.Lock()
	remaining := s.output.forwardBytesLocked()
	s.output.mu.Unlock()
	if remaining != 44 {
		t.Fatal("window did not count only init and media ahead", remaining)
	}
}

func TestHLSSoftForwardTargetAllowsOneLargeFragment(t *testing.T) {
	m, s, token := outputTestSession(t, 64)
	s.output = newHLSOutputWindowWithFragmentLimit(64, 160)
	if w := outputRequest(m, token, "init.mp4", strings.NewReader("init")); w.Code != http.StatusCreated {
		t.Fatal(w.Code)
	}
	// Original keyframes can make one valid GOP larger than the Android target.
	// It still needs to finish so the player can consume it and advance playback.
	largeFragment := io.MultiReader(strings.NewReader(strings.Repeat("a", 40)), strings.NewReader(strings.Repeat("a", 60)))
	if w := outputRequest(m, token, "seg_00000.m4s", largeFragment); w.Code != http.StatusCreated {
		t.Fatal("large playable GOP was rejected", w.Code, w.Body.String())
	}
	playlist := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6,\nseg_00000.m4s\n"
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		largeFragment := io.MultiReader(strings.NewReader(strings.Repeat("b", 60)), strings.NewReader(strings.Repeat("b", 40)))
		completed <- outputRequest(m, token, "seg_00001.m4s", largeFragment)
	}()
	select {
	case w := <-completed:
		t.Fatal("prefilled a second GOP beyond the forward target", w.Code, w.Body.String())
	case <-time.After(30 * time.Millisecond):
	}
	// Even before the first playlist establishes its timing, the completed GOP
	// must not be counted as metadata that can expand a second soft window.
	if w := outputRequest(m, token, "playlist.m3u8", strings.NewReader(playlist)); w.Code != http.StatusCreated {
		t.Fatal(w.Code)
	}
	// The first GOP is current even at position zero. Preparing the next one
	// must not wait for a positive timestamp or a ten-second periodic heartbeat.
	select {
	case w := <-completed:
		if w.Code != http.StatusCreated {
			t.Fatal("next large GOP did not resume", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("next GOP remained blocked at the first frame of its predecessor")
	}
	if m.sessions[hlsSessionKey("tt1", "session")] != s || s.resourceLimited {
		t.Fatal("soft target ended or replaced the viewing session")
	}
	s.output.mu.Lock()
	remaining := s.output.forwardBytesLocked()
	s.output.mu.Unlock()
	if remaining != 104 || s.output.limit != 64 || s.output.fragmentLimit != 160 {
		t.Fatal("soft target or hard bound changed", remaining, s.output.limit, s.output.fragmentLimit)
	}
	playlist += "#EXTINF:6,\nseg_00001.m4s\n"
	if w := outputRequest(m, token, "playlist.m3u8", strings.NewReader(playlist)); w.Code != http.StatusCreated {
		t.Fatal(w.Code)
	}
	go func() {
		completed <- outputRequest(m, token, "seg_00002.m4s", strings.NewReader(strings.Repeat("c", 100)))
	}()
	select {
	case w := <-completed:
		t.Fatal("prefilled two future GOPs while the first was playing", w.Code, w.Body.String())
	case <-time.After(30 * time.Millisecond):
	}
	if !m.touchPlayback("tt1", "session", "magnet", 0, 0, 0, -1, "source", 6) {
		t.Fatal("second GOP's playback heartbeat was rejected")
	}
	select {
	case w := <-completed:
		if w.Code != http.StatusCreated {
			t.Fatal("third GOP did not resume at the first frame of the second", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("third GOP waited for the end of the current GOP")
	}
	for _, name := range []string{"seg_00000.m4s", "seg_00001.m4s", "seg_00002.m4s"} {
		if _, err := os.Stat(filepath.Join(s.dir, name)); err != nil {
			t.Fatal("playing/consumed GOP was removed from the back cache", name, err)
		}
	}
	if w := outputRequest(m, token, "seg_00003.m4s", strings.NewReader(strings.Repeat("d", 157))); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("soft target bypassed the existing hard fragment bound", w.Code)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "seg_00003.m4s.tmp")); !os.IsNotExist(err) {
		t.Fatal("hard-limit failure retained partial media", err)
	}
}

func TestHLSOutputCancellationAndOversizedFragment(t *testing.T) {
	for _, action := range []string{"cancel-request", "stop-session", "oversized-with-init", "oversized-with-untimed-subtitles"} {
		t.Run(action, func(t *testing.T) {
			m, s, token := outputTestSession(t, 64)
			if w := outputRequest(m, token, "init.mp4", strings.NewReader("init")); w.Code != http.StatusCreated {
				t.Fatal(w.Code)
			}
			if strings.HasPrefix(action, "oversized-") {
				size := 61
				if action == "oversized-with-untimed-subtitles" {
					if w := outputRequest(m, token, "playlist0.vtt", strings.NewReader("WEBVTT")); w.Code != http.StatusCreated {
						t.Fatal(w.Code)
					}
					size = 55 // Fits alongside init, but cannot fit with untimed VTT.
				}
				w := outputRequest(m, token, "seg_00000.m4s", strings.NewReader(strings.Repeat("a", size)))
				if w.Code != http.StatusRequestEntityTooLarge {
					t.Fatal("individually unplayable fragment waited forever", w.Code)
				}
				if _, err := os.Stat(filepath.Join(s.dir, "seg_00000.m4s.tmp")); !os.IsNotExist(err) {
					t.Fatal("oversized partial upload retained", err)
				}
				return
			}
			if w := outputRequest(m, token, "seg_00000.m4s", strings.NewReader(strings.Repeat("a", 40))); w.Code != http.StatusCreated {
				t.Fatal(w.Code)
			}
			if w := outputRequest(m, token, "playlist.m3u8", strings.NewReader("#EXTM3U\n#EXTINF:6,\nseg_00000.m4s\n")); w.Code != http.StatusCreated {
				t.Fatal(w.Code)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(strings.Repeat("b", 40))).WithContext(ctx)
			r.RemoteAddr = "127.0.0.1:12345"
			r.SetPathValue("token", token)
			r.SetPathValue("name", "seg_00001.m4s")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { m.serveOutput(w, r); close(done) }()
			select {
			case <-done:
				t.Fatal("request did not reach a full window")
			case <-time.After(30 * time.Millisecond):
			}
			if action == "cancel-request" {
				cancel()
			} else {
				m.stopSessions(hlsSessionKey("tt1", "session"))
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("stopped upload kept waiting")
			}
			if w.Code != http.StatusServiceUnavailable {
				t.Fatal("stopped upload was acknowledged as complete", w.Code)
			}
			if _, err := os.Stat(filepath.Join(s.dir, "seg_00001.m4s.tmp")); !os.IsNotExist(err) {
				t.Fatal("stopped upload left partial media", err)
			}
		})
	}
}

func TestHLSOutputPublishesPlaylistsSubtitlesAndRejectsUnownedUploads(t *testing.T) {
	m, s, token := outputTestSession(t, 1024)
	base := m.selfBase + hlsOutputPath + token + "/"
	playlist := "#EXTM3U\n#EXT-X-MAP:URI=\"" + base + "init.mp4\"\n#EXTINF:6,\n" + base + "seg_00000.m4s\n"
	for name, content := range map[string]string{
		"init.mp4": "init", "seg_00000.m4s": "video", "playlist0.vtt": "WEBVTT",
	} {
		if w := outputRequest(m, token, name, strings.NewReader(content)); w.Code != http.StatusCreated {
			t.Fatal(name, w.Code)
		}
	}
	for name, content := range map[string]string{
		"playlist.m3u8":     playlist,
		"playlist_vtt.m3u8": "#EXTM3U\n#EXTINF:6,\n" + base + "playlist0.vtt\n",
		"master.m3u8":       "#EXTM3U\n#EXT-X-MEDIA:TYPE=SUBTITLES,URI=\"" + base + "playlist_vtt.m3u8\"\n" + base + "playlist.m3u8\n",
	} {
		w := outputRequest(m, token, name, strings.NewReader(content))
		if w.Code != http.StatusCreated {
			t.Fatal(name, w.Code, w.Body.String())
		}
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil || bytes.Contains(data, []byte(token)) || bytes.Contains(data, []byte("http://")) {
			t.Fatal("private upload URL leaked into published playlist", name, string(data), err)
		}
	}
	s.output.mu.Lock()
	if s.output.files["seg_00000.m4s"].end != 6 || s.output.files["playlist0.vtt"].end != 6 {
		t.Error("media/subtitles did not acquire a known timeline")
	}
	s.output.mu.Unlock()
	for _, test := range []struct{ name, token, remote string }{
		{"../outside", token, "127.0.0.1:1"},
		{"seg_00001.m4s.tmp", token, "127.0.0.1:1"},
		{"seg_00001.m4s", "wrong-generation", "127.0.0.1:1"},
		{"seg_00001.m4s", token, "192.168.1.2:1"},
	} {
		r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader("bad"))
		r.RemoteAddr = test.remote
		r.SetPathValue("name", test.name)
		r.SetPathValue("token", test.token)
		w := httptest.NewRecorder()
		m.serveOutput(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatal("unowned upload accepted", test, w.Code)
		}
	}
}

func TestHLSChunkedDisconnectNeverPublishesPartialMedia(t *testing.T) {
	m, s, token := outputTestSession(t, 1024)
	broken := io.MultiReader(strings.NewReader("partial"), errorReader{})
	w := outputRequest(m, token, "seg_00000.m4s", broken)
	if w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, suffix := range []string{"", ".tmp"} {
		if _, err := os.Stat(filepath.Join(s.dir, "seg_00000.m4s"+suffix)); !os.IsNotExist(err) {
			t.Fatal("interrupted media survived", suffix, err)
		}
	}
	s.output.mu.Lock()
	if s.output.forwardBytesLocked() != 0 {
		t.Error("failed upload kept its budget")
	}
	s.output.mu.Unlock()
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("disconnected") }

func TestHLSBufferedIdleCleanupAllowsThrottledHeartbeat(t *testing.T) {
	m, s, _ := outputTestSession(t, 1024)
	now := time.Now()
	s.lastUsed = now.Add(-2 * time.Minute)
	m.cleanupIdle(now)
	if m.sessions[hlsSessionKey("tt1", "session")] != s {
		t.Fatal("background tab's minute heartbeat gap removed its HLS buffer")
	}
	if !m.touchPlayback("tt1", "session", "magnet", 0, 0, 0, -1, "source", 10) {
		t.Fatal("buffered playback could not renew its exact session")
	}
	m.cleanupIdle(time.Now().Add(hlsIdleTimeout - time.Second))
	if m.sessions[hlsSessionKey("tt1", "session")] != s {
		t.Fatal("renewed session was discarded")
	}
	m.cleanupIdle(time.Now().Add(hlsIdleTimeout + time.Second))
	if m.sessions[hlsSessionKey("tt1", "session")] != nil {
		t.Fatal("abandoned buffer was never released")
	}
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Fatal("abandoned output remains on disk", err)
	}
}
