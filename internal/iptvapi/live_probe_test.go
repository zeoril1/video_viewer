package iptvapi

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
)

// epgCandidates — публичные XMLTV-источники телепрограммы. Проверено живьём
// (июль 2026): teleguide.info отдаёт полную программу РФ и сопоставляется с
// каналами iptv-org по названию (id там числовые).
var epgCandidates = []string{
	"https://www.teleguide.info/download/new3/xmltv.xml.gz",
}

// TestLiveProbe — ручная живая проверка: синхронизация реального плейлиста и
// подбор источника программы. Только при IPTV_LIVE=1 и живой БД
// (`IPTV_LIVE_DSN`); созданный плейлист удаляется в конце.
//
// Пример:
//
//	$env:IPTV_LIVE='1'
//	$env:IPTV_LIVE_DSN='postgres://video_viewer:video_viewer@127.0.0.1:5432/video_viewer?sslmode=disable'
//	go test ./internal/iptvapi/ -run TestLiveProbe -v -count=1
func TestLiveProbe(t *testing.T) {
	if os.Getenv("IPTV_LIVE") == "" {
		t.Skip("IPTV_LIVE не задан")
	}
	dsn := os.Getenv("IPTV_LIVE_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	conn, err := db.OpenRetry(ctx, dsn, 3, 2*time.Second)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	repo := db.NewRepo(conn)
	defer repo.Close()
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}
	s := &Server{cfg: Config{DB: repo, DataDir: t.TempDir(), FFmpegPath: "ffmpeg"}}

	pl := db.IPTVPlaylist{
		Name:    "Проверка RU (iptv-org)",
		Kind:    "m3u",
		URL:     "https://iptv-org.github.io/iptv/languages/rus.m3u",
		Enabled: true,
	}
	id, err := repo.SaveIPTVPlaylist(ctx, pl)
	if err != nil {
		t.Fatalf("save playlist: %v", err)
	}
	defer func() { _ = repo.DeleteIPTVPlaylist(context.Background(), id) }()
	t.Logf("playlist id=%d", id)

	n, err := s.SyncPlaylist(ctx, id)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	groups, _ := repo.IPTVGroups(ctx, id)
	chans, _ := repo.ListIPTVChannels(ctx, id, "", "", 0, 0)
	hls := 0
	for _, c := range chans {
		if c.IsHLS {
			hls++
		}
	}
	t.Logf("каналов=%d групп=%d hls=%d (ts=%d)", n, len(groups), hls, len(chans)-hls)

	for _, epgURL := range epgCandidates {
		pl.ID = id
		pl.EPGURL = epgURL
		if _, err := repo.SaveIPTVPlaylist(ctx, pl); err != nil {
			t.Errorf("epg %s: save: %v", epgURL, err)
			continue
		}
		start := time.Now()
		got, err := s.SyncEPG(ctx, pl)
		dur := time.Since(start).Round(time.Millisecond)
		if err != nil {
			t.Logf("EPG %-55s ошибка за %s: %v", epgURL, dur, err)
			continue
		}
		var keyed, prog int
		_ = conn.QueryRowContext(ctx, `SELECT count(*) FROM iptv_channels WHERE playlist_id=$1 AND epg_key<>''`, id).Scan(&keyed)
		_ = conn.QueryRowContext(ctx, `SELECT count(*) FROM iptv_programs`).Scan(&prog)
		t.Logf("EPG %-55s сопоставлено каналов=%3d передач=%5d (всего в БД %d) за %s", epgURL, keyed, got, prog, dur)
		if keyed > 0 {
			rows, err := conn.QueryContext(ctx, `SELECT c.name, c.epg_key FROM iptv_channels c WHERE c.playlist_id=$1 AND c.epg_key<>'' ORDER BY c.num LIMIT 5`, id)
			if err == nil {
				for rows.Next() {
					var nm, key string
					if rows.Scan(&nm, &key) == nil {
						t.Logf("      %-28s -> %s", nm, key)
					}
				}
				rows.Close()
			}
		}
	}
}
