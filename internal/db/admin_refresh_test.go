package db

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAdminMissingIgnoresIMDbAndFutureRatings(t *testing.T) {
	f := Film{Kind: "feature", TitleRU: "Название", PlotRU: "Описание", PosterURL: "poster", TMDBID: "1", Genres: []string{"Drama"}, RatingTMDB: 7, ReleaseDate: "2020-01-01"}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	if fields := MissingAdminFields(f, now); len(fields) != 0 {
		t.Fatal(fields)
	}
	f.RatingTMDB = 0
	for _, date := range []string{"2026-10-05", "2027", "2027-01-01"} {
		f.ReleaseDate = date
		if fields := MissingAdminFields(f, now); len(fields) != 0 {
			t.Fatalf("future %s: %v", date, fields)
		}
	}
	for _, date := range []string{"2026-10-04", "2020-01-01", ""} {
		f.ReleaseDate = date
		if fields := MissingAdminFields(f, now); len(fields) != 1 || fields[0] != "рейтинг TMDB" {
			t.Fatalf("released/unknown %s: %v", date, fields)
		}
	}
}

func TestAdminQueueAllFilmsAndPersistentCooldown(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	ctx := context.Background()
	for _, ddl := range []string{schema, adminRefreshSchema} {
		if _, err = conn.ExecContext(ctx, strings.Replace(ddl, "CREATE TABLE IF NOT EXISTS", "CREATE TEMP TABLE", 1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO films(imdb_id,title,title_ru,kind,plot_ru,genres,poster_url,tmdb_id,rating_tmdb,release_date)
 SELECT 'tt'||n,'Title','Название','feature','Описание','["Drama"]','poster','1',7,'2020-01-01' FROM generate_series(1,605) n;
 UPDATE films SET poster_url='' WHERE imdb_id NOT IN ('tt603','tt604','tt605');
 UPDATE films SET rating_tmdb=0,release_date=to_char(now()+interval '1 year','YYYY-MM-DD') WHERE imdb_id='tt603';
 UPDATE films SET rating_tmdb=0 WHERE imdb_id='tt604';`)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRepo(conn)
	items, err := r.AdminFilmsMissing(ctx)
	if err != nil || len(items) != 603 {
		t.Fatalf("queue=%d err=%v", len(items), err)
	}
	if err = r.SetAdminRefreshResult(ctx, "tt1", "upstream unavailable"); err != nil {
		t.Fatal(err)
	}
	r = NewRepo(conn)
	items, err = r.AdminFilmsMissing(ctx)
	if err != nil || len(items) != 602 {
		t.Fatalf("queue=%d err=%v", len(items), err)
	}
	failed, err := r.AdminFilmsFailed(ctx)
	if err != nil || len(failed) != 1 || failed[0].Reason != "upstream unavailable" || time.Until(failed[0].RetryAt) < 6*24*time.Hour {
		t.Fatalf("failed=%v err=%v", failed, err)
	}
	if _, err = conn.ExecContext(ctx, `UPDATE admin_refresh_failures SET retry_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	items, err = r.AdminFilmsMissing(ctx)
	if err != nil || len(items) != 603 {
		t.Fatalf("expired queue=%d err=%v", len(items), err)
	}
	failed, err = r.AdminFilmsFailed(ctx)
	if err != nil || len(failed) != 0 {
		t.Fatalf("expired failures=%v %v", failed, err)
	}
	if err = r.SetAdminRefreshResult(ctx, "tt1", "again"); err != nil {
		t.Fatal(err)
	}
	if err = r.SetAdminRefreshResult(ctx, "tt1", ""); err != nil {
		t.Fatal(err)
	}
	failed, err = r.AdminFilmsFailed(ctx)
	if err != nil || len(failed) != 0 {
		t.Fatalf("cleared failures=%v %v", failed, err)
	}
}
