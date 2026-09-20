package db

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

func TestEpisodeHistory(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	ctx := context.Background()
	// Only session-local tables; never change the user's real history.
	_, err = conn.ExecContext(ctx, `CREATE TEMP TABLE users (id BIGINT PRIMARY KEY);
		INSERT INTO users VALUES (1),(2);
		CREATE TEMP TABLE films (imdb_id TEXT, title TEXT, title_ru TEXT, kind TEXT, release_date TEXT, poster_url TEXT);
		CREATE TEMP TABLE watch_history (
		id BIGSERIAL PRIMARY KEY, user_id BIGINT, film_id TEXT, magnet TEXT, file INT,
		season INT, episode INT, position_sec DOUBLE PRECISION, duration_sec DOUBLE PRECISION,
		voice TEXT, updated_at TIMESTAMPTZ DEFAULT now(), UNIQUE(user_id,film_id,magnet,file));`)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(conn)
	for _, p := range []WatchProgress{
		{FilmID: "tt1", Magnet: "old", File: 121, Season: 3, Episode: 32, Position: 1479},
		{FilmID: "tt1", Magnet: "old", File: 142, Season: 4, Episode: 21, Position: 136},
		{FilmID: "tt1", Magnet: "new", File: 0, Season: 4, Episode: 21, Position: 200},
		{FilmID: "tt2", Magnet: "other", File: 0, Position: 50},
	} {
		if err := repo.SaveWatchProgress(ctx, 1, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SaveWatchProgress(ctx, 2, WatchProgress{FilmID: "tt1", File: 0, Position: 999}); err != nil {
		t.Fatal(err)
	}
	entries, err := repo.ListEpisodeHistory(ctx, 1, "tt1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Position != 200 || entries[1].Position != 136 || entries[2].Position != 1479 {
		t.Fatalf("lost or mixed episode progress: %+v", entries)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE watch_history SET updated_at=now()+interval '1 day'
		WHERE user_id=1 AND film_id='tt1' AND season=3`); err != nil {
		t.Fatal(err)
	}
	entries, err = repo.ListEpisodeHistory(ctx, 1, "tt1")
	if err != nil || entries[0].Season != 4 {
		t.Fatalf("future episode: %+v %v", entries, err)
	}
	history, err := repo.ListWatchHistory(ctx, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range history {
		if e.FilmID == "tt1" && (e.Season != 4 || e.Episode != 21) {
			t.Fatalf("future timestamp hides actual progress: %+v", e)
		}
	}
	if err := repo.DeleteWatchHistory(ctx, 1, "tt1", "", -1); err != nil {
		t.Fatal(err)
	}
	entries, err = repo.ListEpisodeHistory(ctx, 1, "tt1")
	if err != nil || len(entries) != 0 {
		t.Fatalf("delete: %+v %v", entries, err)
	}
	entries, err = repo.ListEpisodeHistory(ctx, 2, "tt1")
	if err != nil || len(entries) != 1 {
		t.Fatalf("user isolation: %+v %v", entries, err)
	}
}
