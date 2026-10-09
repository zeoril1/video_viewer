package iptvapi

import (
	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/iptv"
	"strings"
	"testing"
	"time"
)

func TestArchiveTemplates(t *testing.T) {
	start := time.Date(2026, 9, 18, 10, 30, 0, 0, time.UTC)
	stop := start.Add(90 * time.Minute)
	ch := db.IPTVChannel{StreamURL: "https://provider.test/live.m3u8", CatchupMode: "default", CatchupSource: "https://provider.test/archive?start={utc}&duration={duration}"}
	raw, err := archiveURL(ch, db.IPTVPlaylist{}, start, stop)
	if err != nil || !strings.Contains(raw, "duration=5400") {
		t.Fatalf("%s %v", raw, err)
	}
	ch.CatchupSource = "file:///etc/passwd"
	if _, err = archiveURL(ch, db.IPTVPlaylist{}, start, stop); err == nil {
		t.Fatal("non-http archive accepted")
	}
	ch.CatchupSource = "https://provider.test/{unsupported}"
	if _, err = archiveURL(ch, db.IPTVPlaylist{}, start, stop); err == nil {
		t.Fatal("unknown template silently accepted")
	}
	ch.CatchupMode = "xtream"
	ch.ExtID = "42"
	raw, err = archiveURL(ch, db.IPTVPlaylist{Kind: "xtream", URL: "https://provider.test/player_api.php", Username: "user", Password: "password"}, start, stop)
	if err != nil || raw != "https://provider.test/timeshift/user/password/90/2026-09-18:10-30/42.m3u8" {
		t.Fatalf("%s %v", raw, err)
	}
}
func TestM3UCatchupMetadata(t *testing.T) {
	channels, err := iptv.ParseM3U(strings.NewReader("#EXTM3U\n#EXTINF:-1 tvg-id=\"test\" catchup=\"default\" catchup-days=\"7\" catchup-source=\"https://example.test/archive?utc={utc}\",Test\nhttps://example.test/live.m3u8\n"))
	if err != nil || len(channels) != 1 || channels[0].CatchupDays != 7 || channels[0].CatchupMode != "default" {
		t.Fatalf("%+v %v", channels, err)
	}
}
