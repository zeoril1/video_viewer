package streamapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHLSGenerationRejectsSegmentsAfterSeek(t *testing.T) {
	m := newFakeHlsSession(t, -1, "")
	s := m.sessions["tt123"]
	s.generation = 2
	if err := os.WriteFile(filepath.Join(s.dir, "seg_00000.m4s"), []byte("new seek bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	q := "?magnet=" + url.QueryEscape(s.magnet) + "&file=2&track=1"
	for _, generation := range []string{"1", "2"} {
		r := httptest.NewRequest(http.MethodGet, "/hls/segments/seg_00000.m4s"+q+"&generation="+generation, nil)
		r.SetPathValue("id", "tt123")
		r.SetPathValue("name", "seg_00000.m4s")
		w := httptest.NewRecorder()
		m.serveSegment(w, r)
		if generation == "1" {
			if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "new seek bytes") {
				t.Fatal("old playlist received the new seek's segment", w.Code, w.Body.String())
			}
		} else if w.Code != http.StatusOK || w.Body.String() != "new seek bytes" {
			t.Fatal("current playlist did not receive its segment", w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/hls.m3u8"+q, nil)
	r.SetPathValue("id", "tt123")
	w := httptest.NewRecorder()
	m.serveMediaPlaylistFrom(w, r, s)
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "&generation=2") {
		t.Fatal("missing generation/cache policy", w.Header(), w.Body.String())
	}
}

func TestHLSDoesNotServeUnpublishedTemporarySegment(t *testing.T) {
	m := newFakeHlsSession(t, -1, "")
	s := m.sessions["tt123"]
	if err := os.WriteFile(filepath.Join(s.dir, "seg_00000.m4s.tmp"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/?magnet="+url.QueryEscape(s.magnet)+"&file=2&track=1", nil)
	r.SetPathValue("id", "tt123")
	r.SetPathValue("name", "seg_00000.m4s.tmp")
	w := httptest.NewRecorder()
	m.serveSegment(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatal("incomplete output segment was served", w.Code)
	}
}

func TestHLSHeadProbesDoNotProtectUnreceivedSegments(t *testing.T) {
	m := newFakeHlsSession(t, -1, "")
	s := m.sessions["tt123"]
	if err := os.WriteFile(filepath.Join(s.dir, "seg_00000.m4s"), []byte("fragment"), 0600); err != nil {
		t.Fatal(err)
	}
	request := func(method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/?magnet="+url.QueryEscape(s.magnet)+"&file=2&track=1", nil)
		r.SetPathValue("id", "tt123")
		r.SetPathValue("name", "seg_00000.m4s")
		w := httptest.NewRecorder()
		m.serveSegment(w, r)
		return w
	}
	if w := request(http.MethodHead); w.Code != http.StatusOK || w.Header().Get("Content-Length") != "8" || w.Body.Len() != 0 {
		t.Fatal("HEAD did not return only the fragment size", w.Code, w.Header(), w.Body.String())
	}
	if s.segments != 0 || len(s.requestedSegments) != 0 {
		t.Fatal("HEAD counted as a downloaded fragment")
	}
	if w := request(http.MethodGet); w.Code != http.StatusOK || w.Body.String() != "fragment" {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.segments != 1 || !s.requestedSegments["seg_00000.m4s"] {
		t.Fatal("GET did not protect a delivered fragment")
	}
}

func TestStaleHLSPlaylistCannotExtendNewSessionLifetime(t *testing.T) {
	for _, endpoint := range []string{"media", "subtitles"} {
		t.Run(endpoint, func(t *testing.T) {
			m := newFakeHlsSession(t, 0, "English")
			s := m.sessions["tt123"]
			s.generation = 2
			lastUsed := time.Now().Add(-2 * time.Minute)
			s.lastUsed = lastUsed
			request := func(generation string) *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/?magnet="+url.QueryEscape(s.magnet)+"&file=2&track=1&generation="+generation, nil)
				r.SetPathValue("id", "tt123")
				return r
			}
			serve := m.serveMediaPlaylist
			if endpoint == "subtitles" {
				serve = m.serveSubPlaylist
			}
			w := httptest.NewRecorder()
			serve(w, request("1"))
			if w.Code != http.StatusNotFound || !s.lastUsed.Equal(lastUsed) {
				t.Fatal("stale playlist renewed the new generation's lifetime", w.Code, s.lastUsed)
			}
			w = httptest.NewRecorder()
			serve(w, request("2"))
			if w.Code != http.StatusOK || !s.lastUsed.After(lastUsed) {
				t.Fatal("current playlist did not renew its session", w.Code, s.lastUsed)
			}
		})
	}
}

func TestHLSResourceSealPreservesRequestedOutputAndOtherViewer(t *testing.T) {
	m := newHLSManager("http://localhost")
	m.dataDir = t.TempDir()
	m.maxDiskBytes = 100
	now := time.Now()
	makeSession := func(id string, count int) *hlsSession {
		dir := filepath.Join(m.dataDir, id)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		var playlist strings.Builder
		playlist.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MAP:URI=\"init.mp4\"\n")
		for i := 0; i < count; i++ {
			name := fmt.Sprintf("seg_%05d.m4s", i)
			playlist.WriteString("#EXTINF:6,\n" + name + "\n")
			if err := os.WriteFile(filepath.Join(dir, name), []byte{1}, 0600); err != nil {
				t.Fatal(err)
			}
		}
		// The quota covers all files, including the playlist itself. A larger
		// budget is set below after constructing both sessions.
		path := filepath.Join(dir, "playlist.m3u8")
		if err := os.WriteFile(path, []byte(playlist.String()), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "init.mp4"), []byte{0}, 0600); err != nil {
			t.Fatal(err)
		}
		s := &hlsSession{id: id, dir: dir, playlist: path, lastUsed: now, subs: -1}
		m.sessions[id] = s
		return s
	}
	large := makeSession("large", 120)
	small := makeSession("other-viewer", 10)
	large.requestedSegments = map[string]bool{"seg_00050.m4s": true, "init.mp4": true}
	if err := os.WriteFile(filepath.Join(large.dir, "seg_00120.m4s.tmp"), []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	// Trimming 69 unrequested segment files must resolve this excess.
	m.maxDiskBytes = hlsDirectoryBytes(m.dataDir) - 40
	m.enforceResources(now)
	if m.sessions["large"] != large || !large.resourceLimited || m.sessions["other-viewer"] != small || small.resourceLimited {
		t.Fatal("pressure removed unrelated viewer or the usable limited session")
	}
	for _, name := range []string{"init.mp4", "seg_00000.m4s", "seg_00050.m4s"} {
		if _, err := os.Stat(filepath.Join(large.dir, name)); err != nil {
			t.Fatal("already requested output deleted", name, err)
		}
	}
	for _, name := range []string{"seg_00051.m4s", "seg_00119.m4s", "seg_00120.m4s.tmp"} {
		if _, err := os.Stat(filepath.Join(large.dir, name)); !os.IsNotExist(err) {
			t.Fatal("unused future output retained", name, err)
		}
	}
	body, err := m.readSessionPlaylist(large, false)
	if err != nil || !strings.HasSuffix(string(body), "#EXT-X-ENDLIST\n") || strings.Contains(string(body), "seg_00051.m4s") {
		t.Fatal("limited playlist advertises removed output", string(body), err)
	}
	if m.resourcePressure() {
		t.Fatal("selective sealing did not release the cache pressure")
	}
	m.enforceResources(now)
	if small.resourceLimited {
		t.Fatal("unrelated viewer sealed after pressure was resolved")
	}
}

func TestHLSResourcePressureEvictsIdleBeforeActive(t *testing.T) {
	m := newHLSManager("http://localhost")
	m.dataDir = t.TempDir()
	activeDir := filepath.Join(m.dataDir, "active")
	idleDir := filepath.Join(m.dataDir, "idle")
	for _, dir := range []string{activeDir, idleDir} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "init.mp4"), make([]byte, 60), 0600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	active := &hlsSession{id: "active", dir: activeDir, lastUsed: now}
	m.sessions["active"] = active
	m.sessions["idle"] = &hlsSession{id: "idle", dir: idleDir, lastUsed: now.Add(-hlsIdleTimeout - time.Second)}
	m.maxDiskBytes = 100
	m.enforceResources(now)
	if m.sessions["active"] != active || active.resourceLimited || m.sessions["idle"] != nil || m.resourcePressure() {
		t.Fatal("idle eviction interrupted the active viewer")
	}
}

func TestCachedSourceHLSHTTPWindow(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "source.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=4", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "120", "-c:v", "libx264", "-preset", "ultrafast", "-g", "24", "-c:a", "pcm_s16le", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create cached source: %v: %s", err, out)
	}
	subtitles := filepath.Join(dir, "source.srt")
	if err := os.WriteFile(subtitles, []byte("1\n00:00:00,000 --> 00:01:59,000\nIntegration subtitle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, seek := range []float64{0, 30} {
		t.Run(fmt.Sprintf("seek-%.0f", seek), func(t *testing.T) {
			m, s, token := outputTestSession(t, 256<<10)
			mux := http.NewServeMux()
			mux.HandleFunc("PUT "+hlsOutputPath+"{token}/{name}", m.serveOutput)
			mux.HandleFunc("POST "+hlsOutputPath+"{token}/{name}", m.serveOutput)
			server := httptest.NewServer(mux)
			defer server.Close()
			m.selfBase = server.URL
			base := server.URL + hlsOutputPath + token + "/"
			args := hlsInputArgs(input, seek, "source")
			args = append(args, "-i", subtitles, "-map", "0:v:0", "-map", "0:a:0", "-map", "1:0", "-c:v", "copy", "-c:a", "aac", "-b:a", "192k", "-ac", "2")
			args = append(args, hlsHTTPMuxArgs(base+"seg_%05d.m4s")...)
			args = append(args, "-c:s", "webvtt", "-var_stream_map", "v:0,a:0,s:0,sgroup:subtitle", "-master_pl_name", "master.m3u8", base+"playlist.m3u8")
			cmd := exec.CommandContext(ctx, ffmpeg, args...)
			cmd.Stderr = &ffmpegLogWriter{}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() {
				s.output.close()
				_ = cmd.Process.Kill()
				<-done
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				s.output.mu.Lock()
				size := s.output.forwardBytesLocked()
				waiting := s.output.waiting
				s.output.mu.Unlock()
				if size > s.output.limit {
					t.Fatal("forward window exceeded its byte quota", size)
				}
				if waiting > 0 {
					break
				}
				select {
				case err := <-done:
					done <- err
					t.Fatal("remux exited before filling the window", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("initial HLS forward buffer was not produced promptly", size, hlsDirectoryBytes(s.dir))
				}
				time.Sleep(20 * time.Millisecond)
			}
			data, err := os.ReadFile(s.playlist)
			if err != nil || !strings.Contains(string(data), "#EXT-X-PLAYLIST-TYPE:EVENT") || strings.Contains(string(data), "#EXT-X-ENDLIST") || strings.Contains(string(data), token) {
				t.Fatal("limited live window is invalid", string(data), err)
			}
			for _, name := range []string{"init.mp4", "master.m3u8", "playlist_vtt.m3u8", "playlist0.vtt"} {
				if _, err := os.Stat(filepath.Join(s.dir, name)); err != nil {
					t.Fatal("missing real FFmpeg video/subtitle output", name, err)
				}
			}
			// One hundred media seconds are bigger than this quota. Advancing the
			// same session must resume uploads and complete the remaining source.
			s.output.advance(200)
			select {
			case err := <-done:
				done <- err
				if err != nil {
					t.Fatal("remux did not resume successfully", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("remux remained blocked after advancing playback")
			}
			data, err = os.ReadFile(s.playlist)
			if err != nil || !strings.Contains(string(data), "#EXT-X-ENDLIST") || strings.Contains(string(data), token) {
				t.Fatal("natural film completion did not publish a safe ENDLIST", string(data), err)
			}
			// Probe the first actual output fragment for both copied video and AAC.
			probe, err := exec.LookPath("ffprobe")
			if err != nil {
				t.Skip("ffprobe is not installed")
			}
			init, _ := os.ReadFile(filepath.Join(s.dir, "init.mp4"))
			segment, _ := os.ReadFile(filepath.Join(s.dir, "seg_00000.m4s"))
			joined := filepath.Join(s.dir, "probe.mp4")
			if err := os.WriteFile(joined, append(init, segment...), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.CommandContext(ctx, probe, "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", joined).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "h264") || !strings.Contains(string(out), "aac") {
				t.Fatal("actual output did not contain playable A/V", string(out), err)
			}
		})
	}
}

func TestHLSBufferedProducerStopSeekAndViewerIsolation(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	input, subtitles := filepath.Join(dir, "source.mkv"), filepath.Join(dir, "source.srt")
	if err := os.WriteFile(subtitles, []byte("1\n00:00:00,000 --> 00:01:59,000\nIntegration subtitle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=4", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-i", subtitles, "-t", "120", "-map", "0:v", "-map", "1:a", "-map", "2:s", "-c:v", "libx264", "-preset", "ultrafast", "-g", "24", "-c:a", "pcm_s16le", "-c:s", "srt", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal("create fixture", err, string(out))
	}
	m := newHLSManager("")
	m.dataDir = t.TempDir()
	m.forwardBytes = 256 << 10
	m.slots = make(chan struct{}, 2)
	m.probeCache[probeKey("tt1", "magnet", 0)] = probeResult{Duration: 120, Tracks: []audioTrack{{Index: 1, Ordinal: 0}}, Subtitles: []subtitleTrack{{Index: 2, Ordinal: 0, Codec: "subrip", Language: "eng"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stream/{id}", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, input) })
	mux.HandleFunc("PUT "+hlsOutputPath+"{token}/{name}", m.serveOutput)
	mux.HandleFunc("POST "+hlsOutputPath+"{token}/{name}", m.serveOutput)
	server := httptest.NewServer(mux)
	defer server.Close()
	defer m.stopAll()
	m.selfBase = server.URL
	waitFull := func(s *hlsSession) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			s.output.mu.Lock()
			waiting, bytes := s.output.waiting, s.output.forwardBytesLocked()
			s.output.mu.Unlock()
			if bytes > m.forwardBytes {
				t.Fatal("producer exceeded byte quota", bytes)
			}
			if waiting > 0 {
				return
			}
			select {
			case <-s.done:
				t.Fatal("producer exited before the test window filled")
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("producer did not reach the forward limit", bytes)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	first, err := m.ensure(ctx, "tt1", "magnet", 0, 0, 0, 0, "source", "first")
	if err != nil {
		t.Fatal(err)
	}
	waitFull(first)
	second, err := m.ensure(ctx, "tt1", "magnet", 0, 0, 0, 30, "source", "second")
	if err != nil {
		t.Fatal(err)
	}
	waitFull(second)
	if first.outputToken == second.outputToken || first.dir == second.dir || len(m.slots) != 2 {
		t.Fatal("viewer output or process slot was shared")
	}
	m.stopSessions(hlsSessionKey("tt1", "first"))
	if len(m.slots) != 1 || m.sessions[hlsSessionKey("tt1", "second")] != second {
		t.Fatal("blocked stop leaked its slot or interrupted the other viewer")
	}
	if _, err := os.Stat(first.dir); !os.IsNotExist(err) {
		t.Fatal("stopped producer's media survived", err)
	}
	current, err := m.ensure(ctx, "tt1", "magnet", 0, 0, 0, 60, "source", "second")
	if err != nil {
		t.Fatal(err)
	}
	if current == second || current.generation <= second.generation || current.outputToken == second.outputToken || len(m.slots) != 1 {
		t.Fatal("seek did not replace only its private generation")
	}
	waitFull(current)
	if w := outputRequest(m, second.outputToken, "seg_00999.m4s", strings.NewReader("late old upload")); w.Code >= 200 && w.Code < 300 {
		t.Fatal("old producer uploaded into a seek successor")
	}
	m.stopSessions(hlsSessionKey("tt1", "second"))
	if len(m.slots) != 0 || len(m.sessions) != 0 {
		t.Fatal("final stop did not release all viewer resources")
	}
}
