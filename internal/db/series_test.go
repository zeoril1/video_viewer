package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/tmdb"
)

func TestSeriesSeasonsDailyCache(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration test")
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	ctx := context.Background()
	// Session-local table: never modify application metadata.
	if _, err = conn.ExecContext(ctx, strings.Replace(seriesSchema, "CREATE TABLE IF NOT EXISTS", "CREATE TEMP TABLE", 1)); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(conn)
	calls := 0
	fail := false
	fetch := func(context.Context, int64) ([]tmdb.SeasonInfo, error) {
		calls++
		if fail {
			return nil, errors.New("offline")
		}
		return []tmdb.SeasonInfo{{Number: 1, Episodes: 10 + calls}}, nil
	}
	first, err := repo.SeriesSeasons(ctx, 46005, fetch)
	if err != nil || first[0].Episodes != 11 {
		t.Fatalf("initial: %v %v", first, err)
	}
	// New repository instance proves the cache is persisted, not in process memory.
	cached, err := NewRepo(conn).SeriesSeasons(ctx, 46005, fetch)
	if err != nil || calls != 1 || cached[0].Episodes != 11 {
		t.Fatalf("cached: %v %v calls=%d", cached, err, calls)
	}
	if _, err = conn.ExecContext(ctx, `UPDATE series_seasons SET refreshed_at=now()-interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	fail = true
	stale, err := repo.SeriesSeasons(ctx, 46005, fetch)
	if err == nil || len(stale) != 1 || stale[0].Episodes != 11 {
		t.Fatalf("stale fallback: %v %v", stale, err)
	}
	fail = false
	updated, err := repo.SeriesSeasons(ctx, 46005, fetch)
	if err != nil || calls != 3 || updated[0].Episodes != 13 {
		t.Fatalf("refresh: %v %v calls=%d", updated, err, calls)
	}
}
