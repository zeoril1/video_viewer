package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

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
	if _, err = conn.ExecContext(ctx, strings.ReplaceAll(seriesSchema, "CREATE TABLE IF NOT EXISTS", "CREATE TEMP TABLE")); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(conn)
	missing, err := repo.CachedSeriesSeasons(ctx, 46005)
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing cache: %v %v", missing, err)
	}
	calls := 0
	fail := false
	fetch := func(context.Context, int64) ([]tmdb.SeasonInfo, error) {
		calls++
		// A one-connection pool must still serve cache reads while TMDB is running.
		// This would time out if refresh kept its SQL transaction open.
		readCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if _, err := repo.CachedSeriesSeasons(readCtx, 46005); err != nil {
			t.Fatalf("network refresh holds a database connection: %v", err)
		}
		if fail {
			return nil, errors.New("offline")
		}
		return []tmdb.SeasonInfo{{Number: 1, Episodes: 10 + calls, Year: 2023}}, nil
	}
	first, err := repo.SeriesSeasons(ctx, 46005, fetch)
	if err != nil || first[0].Episodes != 11 {
		t.Fatalf("initial: %v %v", first, err)
	}
	// New repository instance proves the cache is persisted, not in process memory.
	cached, err := NewRepo(conn).SeriesSeasons(ctx, 46005, fetch)
	if err != nil || calls != 1 || cached[0].Episodes != 11 || cached[0].Year != 2023 {
		t.Fatalf("cached: %v %v calls=%d", cached, err, calls)
	}
	if _, err = conn.ExecContext(ctx, `UPDATE series_seasons SET refreshed_at=now()-interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	fail = true
	persisted, err := repo.CachedSeriesSeasons(ctx, 46005)
	if err != nil || len(persisted) != 1 || persisted[0].Episodes != 11 || calls != 1 {
		t.Fatalf("stale cache read: %v %v calls=%d", persisted, err, calls)
	}
	stale, err := repo.SeriesSeasons(ctx, 46005, fetch)
	if err == nil || len(stale) != 1 || stale[0].Episodes != 11 {
		t.Fatalf("stale fallback: %v %v", stale, err)
	}
	fail = false
	// Failed refreshes have a short retry delay; expire it for this explicit retry.
	if _, err = conn.ExecContext(ctx, `UPDATE series_seasons SET refresh_started_at=NULL`); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.SeriesSeasons(ctx, 46005, fetch)
	if err != nil || calls != 3 || updated[0].Episodes != 13 {
		t.Fatalf("refresh: %v %v calls=%d", updated, err, calls)
	}
}

func TestSeriesEpisodeCachePersistsAndPreservesStaleOnFailure(t *testing.T) {
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
	if _, err := conn.ExecContext(ctx, strings.ReplaceAll(seriesSchema, "CREATE TABLE IF NOT EXISTS", "CREATE TEMP TABLE")); err != nil {
		t.Fatal(err)
	}
	r := NewRepo(conn)
	calls := 0
	fail := false
	fetch := func(ctx context.Context, id int64, season int) ([]tmdb.EpisodeInfo, error) {
		calls++
		if id != 42 || season != 3 {
			t.Fatalf("wrong season requested: %d/%d", id, season)
		}
		if fail {
			return nil, errors.New("offline")
		}
		return []tmdb.EpisodeInfo{{Episode: 1, Name: "Первая", AirDate: "2026-10-07"}}, nil
	}
	first, err := r.SeriesEpisodes(ctx, 42, 3, fetch)
	if err != nil || len(first) != 1 {
		t.Fatalf("initial: %v %v", first, err)
	}
	cached, err := NewRepo(conn).SeriesEpisodes(ctx, 42, 3, fetch)
	if err != nil || calls != 1 || len(cached) != 1 || cached[0].Name != "Первая" {
		t.Fatalf("persistent: %v %v calls=%d", cached, err, calls)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE series_episodes SET refreshed_at=now()-interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	fail = true
	stale, err := r.SeriesEpisodes(ctx, 42, 3, fetch)
	if err == nil || len(stale) != 1 || stale[0].Name != "Первая" {
		t.Fatalf("stale fallback: %v %v", stale, err)
	}
	_, at, err := r.SeriesEpisodeCache(ctx, 42, 3)
	if err != nil || time.Since(at) < 24*time.Hour {
		t.Fatalf("failed refresh changed timestamp: %v %v", at, err)
	}
}
