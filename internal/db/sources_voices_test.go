package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestMergeSourceVoicesPreservesCanonicalLabelsWithoutAliasing(t *testing.T) {
	first := []string{" TVShows ", "MovieDalen", "", "MovieDalen"}
	second := []string{"LostFilm", "TVShows", "  "}
	got := MergeSourceVoices(first, second)
	if want := []string{"LostFilm", "MovieDalen", "TVShows"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("voices = %v, want %v", got, want)
	}
	got[0] = "changed"
	if first[0] != " TVShows " || second[0] != "LostFilm" {
		t.Fatal("normalization changed provider-owned slices")
	}
	encoded, err := json.Marshal(MergeSourceVoices(nil))
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("empty DB metadata must be a JSON array, got %s: %v", encoded, err)
	}
}

func TestSourceVoicesMigrationPersistenceAndRefresh(t *testing.T) {
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
	// All DDL and rows are session-local, including ensureSourcesSchema calls.
	legacy := strings.Replace(sourcesSchema, "\tvoices JSONB NOT NULL DEFAULT '[]'::jsonb,\n", "", 1)
	legacy = strings.Replace(legacy, "CREATE TABLE IF NOT EXISTS", "CREATE TEMP TABLE", 1)
	if _, err := conn.ExecContext(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "SET search_path TO pg_temp"); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(conn)
	if err := repo.ensureSourcesSchema(ctx); err != nil {
		t.Fatalf("legacy voices migration: %v", err)
	}
	const hash = "0123456789abcdef0123456789abcdef01234567"
	source := Source{Magnet: "magnet:?xt=urn:btih:" + hash, Title: "Example WEB-DL", Seeds: 10,
		Voices: []string{" TVShows ", "MovieDalen", "TVShows"}}
	save := func(srcs []Source, partial ...bool) {
		t.Helper()
		if _, _, _, err := repo.SaveSources(ctx, "tt-voices", srcs, partial...); err != nil {
			t.Fatal(err)
		}
	}
	list := func() []Source {
		t.Helper()
		items, err := NewRepo(conn).ListSources(ctx, "tt-voices")
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	assertVoices := func(source Source, want ...string) {
		t.Helper()
		if !reflect.DeepEqual(MergeSourceVoices(source.Voices), MergeSourceVoices(want)) {
			t.Fatalf("voices = %v, want %v", source.Voices, want)
		}
	}
	save([]Source{source})
	got := list()
	if len(got) != 1 {
		t.Fatalf("initial sources: %v", got)
	}
	assertVoices(got[0], "MovieDalen", "TVShows")

	// A transient metadata failure must not erase known voices while title and
	// seed updates still follow the normal refresh behavior.
	source.Title, source.Seeds, source.Voices = "Example refreshed", 20, nil
	save([]Source{source})
	got = list()
	if got[0].Title != source.Title || got[0].Seeds != 20 {
		t.Fatal("ordinary source metadata did not refresh")
	}
	assertVoices(got[0], "MovieDalen", "TVShows")

	// Two tracker tokens for one content hash contribute their known studios.
	duplicate := source
	source.Voices = []string{"LostFilm"}
	duplicate.Magnet += "&tr=https://tracker.invalid/new-token"
	duplicate.Voices = []string{"Jaskier", "MovieDalen"}
	save([]Source{source, duplicate})
	got = list()
	if len(got) != 1 {
		t.Fatalf("duplicate hash created %d DB rows", len(got))
	}
	assertVoices(got[0], "Jaskier", "LostFilm", "MovieDalen", "TVShows")

	other := Source{Magnet: "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Title: source.Title, Size: source.Size, Seeds: 2, Voices: []string{"NewStudio"}}
	save([]Source{source, other})
	other.Voices = nil
	save([]Source{other}, true)
	got = list()
	if len(got) != 2 {
		t.Fatalf("partial refresh lost cached sources: %v", got)
	}
	for _, item := range got {
		if item.InfoHash == hash {
			assertVoices(item, "Jaskier", "LostFilm", "MovieDalen", "TVShows")
		} else {
			assertVoices(item, "NewStudio")
		}
	}
	save([]Source{other})
	got = list()
	if len(got) != 1 || got[0].InfoHash == hash {
		t.Fatal("complete refresh did not prune an absent source")
	}
	assertVoices(got[0], "NewStudio")
	save(nil)
	if len(list()) != 0 {
		t.Fatal("successful empty refresh must still clear the source cache")
	}
}
