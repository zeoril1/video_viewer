package db

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/segments"
)

func TestSegmentAnalysisRejectsInvalidBeforeDatabaseAccess(t *testing.T) {
	repo := &Repo{}
	if err := repo.SaveSegmentAnalysis(context.Background(), segments.AnalysisRecord{Status: "analyzing"}); err == nil {
		t.Fatal("incomplete work reached the database")
	}
	if _, _, err := repo.GetSegmentAnalysis(context.Background(), "../invalid"); err == nil {
		t.Fatal("invalid key reached the database")
	}
}

func TestSegmentAnalysisPersistsPerFileAndPreservesSuccessfulResults(t *testing.T) {
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
	if _, err := conn.ExecContext(ctx, strings.ReplaceAll(segmentAnalysisSchema, "CREATE TABLE IF NOT EXISTS", "CREATE TEMP TABLE")); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(conn)
	frames := make([]uint32, 3600)
	frames[0], frames[1] = 123, 456
	record := segments.AnalysisRecord{MediaKey: "segments.0123456789abcdef0123456789abcdef01234567.1", FilmID: "tt1234567", Season: 2, Episode: 3, Duration: 1800,
		Version: 1, Status: segments.StatusReady, Fingerprint: frames, Step: 0.5,
		Segments: []segments.Segment{{Type: segments.Intro, Start: 5, End: 70, Source: "audio_match", AutoSkip: true}}}
	if err := repo.SaveSegmentAnalysis(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, found, err := NewRepo(conn).GetSegmentAnalysis(ctx, record.MediaKey)
	if err != nil || !found || len(got.Fingerprint) != 3600 || got.Segments[0].End != 70 || got.UpdatedAt.IsZero() {
		t.Fatalf("persisted: %+v %v %v", got, found, err)
	}
	if _, found, err := repo.GetSegmentAnalysis(ctx, strings.TrimSuffix(record.MediaKey, ".1")+".2"); err != nil || found {
		t.Fatalf("file identity mixed: %v %v", found, err)
	}
	negative := record
	negative.Status = segments.StatusNotFound
	negative.Segments = nil
	if err := repo.SaveSegmentAnalysis(ctx, negative); err != nil {
		t.Fatal(err)
	}
	got, _, err = repo.GetSegmentAnalysis(ctx, record.MediaKey)
	if err != nil || got.Status != segments.StatusReady {
		t.Fatalf("negative erased ready: %+v %v", got, err)
	}
	broken := record
	broken.Status = "error"
	if err := repo.SaveSegmentAnalysis(ctx, broken); err == nil {
		t.Fatal("incomplete record accepted")
	}
	negative.Version = 2
	if err := repo.SaveSegmentAnalysis(ctx, negative); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveSegmentAnalysis(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, _, err = repo.GetSegmentAnalysis(ctx, record.MediaKey)
	if err != nil || got.Version != 2 || got.Status != segments.StatusNotFound || len(got.Fingerprint) != 3600 {
		t.Fatalf("version went backward or fingerprint lost: %+v %v", got, err)
	}
}
