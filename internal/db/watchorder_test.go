package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestWatchOrderPersistsAcrossRepositories(t *testing.T) {
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
	if _, err = conn.ExecContext(ctx, strings.Replace(watchOrderSchema, "CREATE TABLE IF NOT EXISTS", "CREATE TEMP TABLE", 1)); err != nil {
		t.Fatal(err)
	}
	r := NewRepo(conn)
	for _, version := range []int{1, 2} {
		raw, _ := json.Marshal(map[string]int{"version": version})
		if err = r.SaveWatchOrder(ctx, "v1:tt1", raw); err != nil {
			t.Fatal(err)
		}
		got, at, err := NewRepo(conn).WatchOrder(ctx, "v1:tt1")
		if err != nil || at.IsZero() {
			t.Fatalf("%s %v", got, err)
		}
		var data map[string]int
		json.Unmarshal(got, &data)
		if data["version"] != version {
			t.Fatal("stale persisted value")
		}
	}
}
