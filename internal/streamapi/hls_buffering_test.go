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
	m.sessions["idle"] = &hlsSession{id: "idle", dir: idleDir, lastUsed: now.Add(-2 * time.Minute)}
	m.maxDiskBytes = 100
	m.enforceResources(now)
	if m.sessions["active"] != active || active.resourceLimited || m.sessions["idle"] != nil || m.resourcePressure() {
		t.Fatal("idle eviction interrupted the active viewer")
	}
}

func TestCachedSourceHLSRemuxHasBoundedInitialBurst(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "source.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=4", "-t", "120", "-an", "-c:v", "libx264", "-preset", "ultrafast", "-g", "24", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create cached source: %v: %s", err, out)
	}
	args := hlsInputArgs(input, 0, "source")
	args = append(args, "-c:v", "copy", "-an")
	args = append(args, hlsMuxArgs(filepath.Join(dir, "seg_%05d.m4s"))...)
	playlist := filepath.Join(dir, "playlist.m3u8")
	args = append(args, playlist)
	cmd = exec.CommandContext(ctx, ffmpeg, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
		<-done
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(playlist)
		if err == nil && strings.Contains(string(data), "seg_00009.m4s") {
			// An unrestricted streamcopy would finish this tiny 120-second fixture
			// in less than this interval. Pacing must leave it producing the tail.
			time.Sleep(time.Second)
			data, err = os.ReadFile(playlist)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "#EXT-X-PLAYLIST-TYPE:EVENT") || strings.Contains(string(data), "#EXT-X-ENDLIST") || strings.Contains(string(data), "seg_00019.m4s") {
				t.Fatal("cached source was materialized entirely or did not produce a progressive event", string(data))
			}
			return
		}
		select {
		case err := <-done:
			// Keep the deferred wait nonblocking after consuming its result.
			done <- err
			t.Fatalf("remux exited before the buffer check: %v: %s", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("initial HLS buffer was not produced promptly")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
