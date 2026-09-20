package iptvapi

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/zeoril1/video_viewer/internal/db"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGuideArchiveAndProgrammeRetention(t *testing.T) {
	dsn := os.Getenv("FEATURE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("FEATURE_TEST_DATABASE_URL required")
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	schema := fmt.Sprintf("guide_test_%d", time.Now().UnixNano())
	if _, err = conn.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	if _, err = conn.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	repo := db.NewRepo(conn)
	ctx := context.Background()
	if err = repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("duration") != "3600" {
			t.Error("wrong duration")
		}
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXTINF:4,\nsecret-segment.ts?password=hidden\n#EXT-X-ENDLIST\n")
	}))
	defer provider.Close()
	pid, err := repo.SaveIPTVPlaylist(ctx, db.IPTVPlaylist{Name: "Test", Kind: "m3u", URL: provider.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ch := db.IPTVChannel{PlaylistID: pid, ExtID: "test", Name: "Test channel", StreamURL: provider.URL + "/live", IsHLS: true, CatchupMode: "default", CatchupDays: 7, CatchupSource: provider.URL + "/archive?duration={duration}&utc={utc}"}
	if _, _, err = repo.ReplaceIPTVChannels(ctx, pid, []db.IPTVChannel{ch}); err != nil {
		t.Fatal(err)
	}
	chans, err := repo.ListIPTVChannels(ctx, 0, "", "", 20, 0)
	if err != nil || len(chans) != 1 {
		t.Fatalf("channels %v %v", chans, err)
	}
	ch = chans[0]
	if ch.CatchupDays != 7 || ch.CatchupSource == "" {
		t.Fatal("archive metadata lost")
	}
	if err = repo.SetIPTVChannelEPGKeys(ctx, pid, map[string]string{"test": "channel"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	old := db.IPTVProgram{Key: "channel", Start: now.Add(-2 * time.Hour), Stop: now.Add(-time.Hour), Title: "Archive programme"}
	if err = repo.ReplaceIPTVPrograms(ctx, "channel", []db.IPTVProgram{old}); err != nil {
		t.Fatal(err)
	}
	if err = repo.ReplaceIPTVPrograms(ctx, "channel", []db.IPTVProgram{{Key: "channel", Start: now, Stop: now.Add(time.Hour), Title: "Current"}}); err != nil {
		t.Fatal(err)
	}
	ps, err := repo.ListIPTVPrograms(ctx, "channel", old.Start, now.Add(time.Hour), 20)
	if err != nil || len(ps) != 2 {
		t.Fatalf("archive history lost: %v %v", ps, err)
	}
	cfg := Config{DB: repo, DataDir: t.TempDir(), MaxSessions: 2}
	s := &Server{cfg: cfg, live: newLiveManager(cfg)}
	req := httptest.NewRequest("GET", "/api/iptv/guide?from="+url.QueryEscape(old.Start.Format(time.RFC3339)), nil)
	w := httptest.NewRecorder()
	s.handleGuide(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Archive programme") {
		t.Fatalf("guide: %d %s", w.Code, w.Body)
	}
	req = httptest.NewRequest("GET", "/api/iptv/archive/1?start="+url.QueryEscape(old.Start.Format(time.RFC3339))+"&stop="+url.QueryEscape(old.Stop.Format(time.RFC3339)), nil)
	req.SetPathValue("id", fmt.Sprint(ch.ID))
	w = httptest.NewRecorder()
	s.handleArchive(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "/api/iptv/seg/") || strings.Contains(w.Body.String(), "password") {
		t.Fatalf("archive proxy: %d %s", w.Code, w.Body)
	}
}
