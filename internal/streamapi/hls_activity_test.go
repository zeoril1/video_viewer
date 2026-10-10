package streamapi

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBufferedPlaybackRenewsOnlyItsExactHLSSession(t *testing.T) {
	m := newHLSManager("")
	dir := t.TempDir()
	s := &hlsSession{id: "film", magnet: "magnet", file: 2, track: 1, subs: -1, start: 120, quality: "source", lastUsed: time.Now().Add(-2 * time.Minute), dir: dir}
	m.sessions[hlsSessionKey("film", "session")] = s
	if !m.touchPlayback("film", "session", "magnet", 2, 120, 1, -1, "source", 150) {
		t.Fatal("current buffered playback rejected")
	}
	if s.playhead != 30 || time.Since(s.lastUsed) > time.Second || s.lastPlayback.IsZero() {
		t.Fatal("session was not renewed")
	}
	previous := s.lastUsed
	if m.touchPlayback("film", "session", "magnet", 2, 0, 1, -1, "source", 500) {
		t.Fatal("queued old seek affected successor")
	}
	if m.touchPlayback("film", "session", "magnet", 2, 120, 0, -1, "source", 500) {
		t.Fatal("old audio track affected successor")
	}
	if m.touchPlayback("film", "other", "magnet", 2, 120, 1, -1, "source", 500) {
		t.Fatal("other viewer affected session")
	}
	if m.touchPlayback("film", "session", "magnet", 2, 120, 1, 0, "source", 500) {
		t.Fatal("old subtitle track affected successor")
	}
	if s.playhead != 30 || !s.lastUsed.Equal(previous) {
		t.Fatal("stale state changed retention")
	}
	if !m.touchPlayback("film", "session", "magnet", 2, 120, 1, -1, "720", 150) {
		t.Fatal("legacy quality must not expire current source playback")
	}
}

func TestConsumedHLSSegmentsRetainBackBufferAndUnpublishedData(t *testing.T) {
	m := newHLSManager("")
	dir := t.TempDir()
	playlist := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:60,\nseg_00000.m4s\n#EXTINF:60,\nseg_00001.m4s\n#EXTINF:60,\nseg_00002.m4s\n#EXTINF:60,\nseg_00003.m4s\n"
	for _, name := range []string{"init.mp4", "seg_00000.m4s", "seg_00001.m4s", "seg_00002.m4s", "seg_00003.m4s", "seg_00004.m4s.tmp", "foreign.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("media"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "playlist.m3u8")
	if err := os.WriteFile(path, []byte(playlist), 0600); err != nil {
		t.Fatal(err)
	}
	s := &hlsSession{dir: dir, playlist: path, playhead: 310, lastPlayback: time.Now()}
	m.sessions["film"] = s
	m.pruneConsumedOnce(time.Now())
	for _, name := range []string{"seg_00000.m4s", "seg_00001.m4s"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("consumed segment remains: %s", name)
		}
	}
	for _, name := range []string{"init.mp4", "seg_00002.m4s", "seg_00003.m4s", "seg_00004.m4s.tmp", "foreign.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("buffer/other data removed: %s", name)
		}
	}
	s.playhead = 1000
	s.lastPlayback = time.Now().Add(-viewerTTL - time.Second)
	m.pruneConsumedOnce(time.Now())
	if _, err := os.Stat(filepath.Join(dir, "seg_00003.m4s")); err != nil {
		t.Fatal("abandoned heartbeat removed uncertain data")
	}
}

func TestConsumedSegmentParserFailsSafeForUnknownTimeline(t *testing.T) {
	data := []byte("#EXTM3U\n#EXTINF:10,\nseg_00000.m4s\n#EXTINF:broken,\nseg_00001.m4s\n#EXTINF:10,\nseg_00002.m4s\n")
	if got := consumedSegmentNames(data, 100); !reflect.DeepEqual(got, []string{"seg_00000.m4s"}) {
		t.Fatal(got)
	}
	data = []byte("#EXTM3U\n#EXTINF:10,\n../outside.m4s\n#EXTINF:10,\nseg_00001.m4s.tmp\n#EXTINF:10,\ninit.mp4\n")
	if got := consumedSegmentNames(data, 100); len(got) != 0 {
		t.Fatal("unowned files selected", got)
	}
}
