package catalogapi

import (
	"context"
	"errors"
	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/magnet"
	"testing"
	"time"
)

type indexedFake struct {
	calls    map[string]int
	response func(string, string) ([]magnet.Result, error)
}

func (p *indexedFake) Name() string       { return "jackett" }
func (p *indexedFake) Indexers() []string { return []string{"anime", "movies", "broken"} }
func (p *indexedFake) Search(context.Context, string, int) ([]magnet.Result, error) {
	panic("aggregate search should not be called")
}
func (p *indexedFake) SearchIndexer(_ context.Context, id, q string, _ int) ([]magnet.Result, error) {
	p.calls[id]++
	return p.response(id, q)
}
func TestTrackerEmptyCooldownIsPerFilmAndExpires(t *testing.T) {
	p := &indexedFake{calls: map[string]int{}, response: func(id, q string) ([]magnet.Result, error) {
		switch id {
		case "anime":
			return []magnet.Result{{Title: "Unrelated anime S01"}}, nil
		case "broken":
			return nil, errors.New("not configured")
		default:
			return []magnet.Result{found("Example 2026 1080p", 10, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}, nil
		}
	}}
	m := newSourcesManager(Config{Magnet: p})
	matcher := newFilmTitleMatcher(db.Film{Title: "Example", Kind: "feature", Year: 2026})
	run := func(id string) {
		t.Helper()
		s := m.newTrackerSearch(id, matcher)
		results, err := s.search(context.Background(), "Example 2026", 12)
		if err == nil || !s.succeeded() || len(results) != 1 || results[0].Provider != "jackett:movies" {
			t.Fatalf("unexpected partial result: %v %v", results, err)
		}
		s.finish()
	}
	run("tt1")
	run("tt1")
	if p.calls["anime"] != 1 || p.calls["movies"] != 2 || p.calls["broken"] != 2 {
		t.Fatal(p.calls)
	}
	run("tt2")
	if p.calls["anime"] != 2 {
		t.Fatal("another film was suppressed")
	}
	m.trackerEmptyUntil["tt1\x00anime"] = time.Now().Add(-time.Second)
	run("tt1")
	if p.calls["anime"] != 3 {
		t.Fatal("expired tracker was not retried")
	}
}

func TestTrackerAlternativeTitlePreventsEmptyCooldown(t *testing.T) {
	p := &indexedFake{calls: map[string]int{}, response: func(id, q string) ([]magnet.Result, error) {
		if q == "alternate" {
			return []magnet.Result{{Title: "Example 2026"}}, nil
		}
		return nil, nil
	}}
	m := newSourcesManager(Config{Magnet: p})
	s := m.newTrackerSearch("tt1", newFilmTitleMatcher(db.Film{Title: "Example", Kind: "feature", Year: 2026}))
	s.search(context.Background(), "original", 12)
	s.search(context.Background(), "alternate", 12)
	s.finish()
	if len(m.trackerEmptyUntil) != 0 {
		t.Fatal("tracker disabled before trying alternate title")
	}
	for _, count := range p.calls {
		if count != 2 {
			t.Fatal(p.calls)
		}
	}
}

func TestIndexedPartialSuccessCompletesRefresh(t *testing.T) {
	p := &indexedFake{calls: map[string]int{}, response: func(id, q string) ([]magnet.Result, error) {
		if id == "broken" {
			return nil, errors.New("not configured")
		}
		return []magnet.Result{found("Example S01 1080p", 10, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")}, nil
	}}
	repo := &failureRepo{}
	m := newSourcesManager(Config{Magnet: p})
	m.db = repo
	if err := m.run(context.Background(), "tt123"); err != nil {
		t.Fatal(err)
	}
	if repo.saves != 1 || !repo.preserve {
		t.Fatal("partial results must preserve cached sources")
	}
}
