package iptvapi

import (
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/iptv"
)

// TestToDBChannelsUniqueExtID — tvg-id в плейлистах не уникален: один канал
// идёт несколькими потоками (SD/HD/«Архив») с общим tvg-id. Ключ базы
// (playlist_id, ext_id) должен остаться уникальным, иначе потоки затирают друг
// друга — каналов в базе меньше, чем в плейлисте, а потоком становится
// последний вариант (обычно «Архив»).
func TestToDBChannelsUniqueExtID(t *testing.T) {
	list := []iptv.Channel{
		{ExtID: "zvezda", Name: "Звезда", EPGID: "zvezda", URL: "http://a/1.m3u8"},
		{ExtID: "zvezda", Name: "Звезда HD", EPGID: "zvezda", URL: "http://a/2.m3u8"},
		{ExtID: "zvezda", Name: "Звезда (Архив)", EPGID: "zvezda", URL: "http://a/3.m3u8"},
		{ExtID: "ntv", Name: "НТВ", EPGID: "ntv", URL: "http://a/4.m3u8"},
		{ExtID: "", Name: "Без tvg-id", URL: "http://a/5.m3u8"},
	}
	out := toDBChannels(list, db.IPTVPlaylist{ID: 11})
	want := []string{"zvezda", "zvezda#2", "zvezda#3", "ntv", ""}
	if len(out) != len(want) {
		t.Fatalf("каналов %d, ждали %d", len(out), len(want))
	}
	for i, w := range want {
		if out[i].ExtID != w {
			t.Errorf("канал %d (%s): ext_id %q, ждали %q", i, out[i].Name, out[i].ExtID, w)
		}
	}
	// tvg-id (epg_id) нумерацией не портим — по нему находится программа.
	for i := 0; i < 3; i++ {
		if out[i].EPGID != "zvezda" {
			t.Errorf("канал %d: epg_id %q, ждали zvezda", i, out[i].EPGID)
		}
	}
	// Повторный синк того же плейлиста даёт те же ключи: каналы обновляются, а
	// не копятся дублями.
	again := toDBChannels(list, db.IPTVPlaylist{ID: 11})
	for i := range out {
		if out[i].ExtID != again[i].ExtID {
			t.Errorf("повторный синк: канал %d ext_id %q вместо %q", i, again[i].ExtID, out[i].ExtID)
		}
	}
}
