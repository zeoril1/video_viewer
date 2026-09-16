package catalogapi

import (
	"context"
	"errors"
	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/magnet"
	"testing"
	"time"
)

type failureRepo struct {
	*db.Repo
	saves    int
	preserve bool
}

func (r *failureRepo) GetByIMDBID(context.Context, string) (db.Film, bool, error) {
	return db.Film{IMDBID: "tt123", Title: "Example", Kind: "tvSeries", Seasons: 2}, true, nil
}
func (r *failureRepo) ListSources(context.Context, string) ([]db.Source, error) { return nil, nil }
func (r *failureRepo) SaveSources(_ context.Context, _ string, _ []db.Source, preserve ...bool) (int, int, int, error) {
	r.saves++
	r.preserve = len(preserve) > 0 && preserve[0]
	return 0, 0, 0, nil
}

type partialProvider struct{ calls int }

type mixedProvider struct{}

func (mixedProvider) Name() string { return "mixed" }
func (mixedProvider) Search(context.Context, string, int) ([]magnet.Result, error) {
	return []magnet.Result{found("Example S01 1080p", 10, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}, errors.New("one indexer failed")
}

func TestResultsAccompanyingIndexerErrorAreSaved(t *testing.T) {
	repo := &failureRepo{}
	m := newSourcesManager(Config{})
	m.db, m.magnet = repo, mixedProvider{}
	if err := m.run(context.Background(), "tt123"); err == nil {
		t.Fatal("partial failure must retain retry status")
	}
	if repo.saves != 1 || !repo.preserve {
		t.Fatal("useful partial results were discarded or pruning was enabled")
	}
}

func (p *partialProvider) Name() string { return "test" }
func (p *partialProvider) Search(context.Context, string, int) ([]magnet.Result, error) {
	p.calls++
	if p.calls == 1 {
		return []magnet.Result{found("Example S01 1080p", 10, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}, nil
	}
	return nil, errors.New("tracker unavailable")
}
func TestPartialSearchPreservesSourcesAndRetries(t *testing.T) {
	repo := &failureRepo{}
	provider := &partialProvider{}
	m := newSourcesManager(Config{})
	m.db, m.magnet = repo, provider
	m.refresh("tt123")
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		running := m.running["tt123"]
		m.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("search did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if provider.calls < 2 {
		t.Fatal("partial failure scenario was not exercised")
	}
	if repo.saves != 1 || !repo.preserve {
		t.Fatal("partial search replaced cached sources")
	}
	if _, ok := m.lastDone["tt123"]; ok {
		t.Fatal("failed search marked fresh")
	}
	if !m.retryAfter["tt123"].After(time.Now()) {
		t.Fatal("missing retry backoff")
	}
	if _, ready := m.sourcesForView(context.Background(), "tt123"); ready {
		t.Fatal("failed search reported ready")
	}
	if m.running["tt123"] {
		t.Fatal("retried before backoff elapsed")
	}
	m.retryAfter["tt123"] = time.Now().Add(-time.Second)
	m.magnet = nil
	m.sourcesForView(context.Background(), "tt123")
	// Wait for the retry before returning, including under the race detector.
	for {
		m.mu.Lock()
		running := m.running["tt123"]
		_, done := m.lastDone["tt123"]
		m.mu.Unlock()
		if !running {
			if !done {
				t.Fatal("expired backoff did not retry")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}
