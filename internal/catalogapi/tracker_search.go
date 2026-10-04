package catalogapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/zeoril1/video_viewer/internal/magnet"
)

// Retry tomorrow: a release can appear after today's search.
const trackerEmptyTTL = 24 * time.Hour

type indexedProvider interface {
	Indexers() []string
	SearchIndexer(context.Context, string, string, int) ([]magnet.Result, error)
}
type trackerAttempt struct{ skipped, success, failed, matched bool }
type trackerSearch struct {
	m           *sourcesManager
	filmID      string
	matcher     *titleMatcher
	provider    indexedProvider
	states      map[string]*trackerAttempt
	alternative bool
}

func (m *sourcesManager) newTrackerSearch(id string, matcher *titleMatcher) *trackerSearch {
	p, _ := m.magnet.(indexedProvider)
	s := &trackerSearch{m: m, filmID: id, matcher: matcher, provider: p, states: map[string]*trackerAttempt{}}
	if p == nil {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, until := range m.trackerEmptyUntil {
		if !time.Now().Before(until) {
			delete(m.trackerEmptyUntil, key)
		}
	}
	for _, tracker := range p.Indexers() {
		skipped := time.Now().Before(m.trackerEmptyUntil[id+"\x00"+tracker])
		s.states[tracker] = &trackerAttempt{skipped: skipped}
		if skipped {
			log.Printf("sources: %s tracker=%s: пропуск, ранее не найдено подходящих раздач", id, tracker)
		}
	}
	return s
}
func (s *trackerSearch) search(ctx context.Context, q string, limit int) ([]magnet.Result, error) {
	if s.provider == nil {
		return s.m.magnet.Search(ctx, q, limit)
	}
	var out []magnet.Result
	var errs []error
	for _, id := range s.provider.Indexers() {
		state := s.states[id]
		if state.skipped || (s.alternative && state.matched) {
			continue
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		log.Printf("sources: %s tracker=%s: поиск %q", s.filmID, id, q)
		results, err := s.provider.SearchIndexer(ctx, id, q, limit)
		if err != nil {
			state.failed = true
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		} else {
			state.success = true
		}
		matched := 0
		for _, result := range results {
			if !s.matcher.matches(result.Title) {
				continue
			}
			matched++
			result.Provider = "jackett:" + id
			out = append(out, result)
		}
		state.matched = state.matched || matched > 0
		log.Printf("sources: %s tracker=%s: найдено=%d подходит=%d ошибка=%v", s.filmID, id, len(results), matched, err)
	}
	return out, errors.Join(errs...)
}
func (s *trackerSearch) succeeded() bool {
	for _, state := range s.states {
		if state.success || state.skipped {
			return true
		}
	}
	return false
}

func (s *trackerSearch) needsAlternative() bool {
	for _, state := range s.states {
		if !state.skipped && !state.matched {
			return true
		}
	}
	return false
}

// Decide only after all alternative title and season queries have finished.
func (s *trackerSearch) finish() {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if s.m.trackerEmptyUntil == nil {
		s.m.trackerEmptyUntil = map[string]time.Time{}
	}
	for id, state := range s.states {
		if id == "all" || state.skipped || state.failed || !state.success || state.matched {
			continue
		}
		if len(s.m.trackerEmptyUntil) >= 4096 {
			for key := range s.m.trackerEmptyUntil {
				delete(s.m.trackerEmptyUntil, key)
				break
			}
		}
		s.m.trackerEmptyUntil[s.filmID+"\x00"+id] = time.Now().Add(trackerEmptyTTL)
		log.Printf("sources: %s tracker=%s: нет подходящих раздач, поиск отложен на %s", s.filmID, id, trackerEmptyTTL)
	}
}
