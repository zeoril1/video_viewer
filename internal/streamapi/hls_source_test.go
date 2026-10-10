package streamapi

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyQualityKeepsExistingSourceSession(t *testing.T) {
	m := newHLSManager("")
	s := &hlsSession{id: "tt1", magnet: "magnet", file: 0, track: 0, subs: -1, quality: "source"}
	m.sessions[hlsSessionKey("tt1", "viewer")] = s
	for _, quality := range []string{"", "source", "480", "720", "1080", "2160", "unknown"} {
		got, err := m.ensure(context.Background(), "tt1", "magnet", 0, 0, -1, 0, quality, "viewer")
		if err != nil || got != s {
			t.Fatalf("legacy quality %q restarted source session: %p %v", quality, got, err)
		}
	}
}

func TestPlaylistIgnoresLegacyQualityParameter(t *testing.T) {
	m := newHLSManager("")
	dir := t.TempDir()
	playlist := filepath.Join(dir, "playlist.m3u8")
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nseg_00000.m4s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &hlsSession{id: "tt1", magnet: "magnet", file: 0, track: 0, subs: -1, quality: "source", dir: dir, playlist: playlist}
	m.sessions[hlsSessionKey("tt1", "viewer")] = s
	req := httptest.NewRequest("GET", "/api/films/tt1/hls.m3u8?magnet=magnet&file=0&session=viewer&quality=720", nil)
	req.SetPathValue("id", "tt1")
	rec := httptest.NewRecorder()
	m.servePlaylist(rec, req)
	if rec.Code != 200 || m.sessions[hlsSessionKey("tt1", "viewer")] != s {
		t.Fatalf("legacy query changed original stream: %d %s", rec.Code, rec.Body.String())
	}
}
