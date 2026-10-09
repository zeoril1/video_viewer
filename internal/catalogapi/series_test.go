package catalogapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

type seriesTestRepo struct {
	mu       sync.Mutex
	seasons  []tmdb.SeasonInfo
	episodes []tmdb.EpisodeInfo
	at       time.Time
	epAt     time.Time
	film     db.Film
}

func (r *seriesTestRepo) GetByIMDBID(context.Context, string) (db.Film, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.film, r.film.IMDBID != "", nil
}
func (r *seriesTestRepo) SaveTMDBFilm(_ context.Context, f tmdb.Film) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.film = db.Film{IMDBID: f.IMDBID, Kind: f.Kind, TMDBID: "42"}
	return nil
}
func (r *seriesTestRepo) SeriesSeasonCache(context.Context, int64) ([]tmdb.SeasonInfo, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seasons, r.at, nil
}
func (r *seriesTestRepo) SeriesEpisodeCache(context.Context, int64, int) ([]tmdb.EpisodeInfo, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.episodes, r.epAt, nil
}
func (r *seriesTestRepo) SeriesSeasons(ctx context.Context, id int64, fetch func(context.Context, int64) ([]tmdb.SeasonInfo, error)) ([]tmdb.SeasonInfo, error) {
	seasons, err := fetch(ctx, id)
	if err == nil {
		r.mu.Lock()
		r.seasons, r.at = seasons, time.Now()
		r.mu.Unlock()
	}
	return seasons, err
}
func (r *seriesTestRepo) SeriesEpisodes(ctx context.Context, id int64, season int, fetch func(context.Context, int64, int) ([]tmdb.EpisodeInfo, error)) ([]tmdb.EpisodeInfo, error) {
	episodes, err := fetch(ctx, id, season)
	if err == nil {
		r.mu.Lock()
		r.episodes, r.epAt = episodes, time.Now()
		r.mu.Unlock()
	}
	return episodes, err
}

func newSeriesTestManager(repo *seriesTestRepo, provider *httptest.Server) *seriesMetadataManager {
	m := newSeriesMetadataManager(Config{TMDB: tmdb.NewClient("test", "", provider.URL)})
	m.repo = repo
	return m
}

func seriesTestRequest(m *seriesMetadataManager, path string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/films/{id}/seasons", m.seasons)
	mux.HandleFunc("GET /api/films/{id}/seasons/{season}", m.episodes)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestSeriesStaleResponseDoesNotWaitForTMDBAndDeduplicatesRefresh(t *testing.T) {
	entered := make(chan struct{}, 10)
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.Write([]byte(`{"seasons":[{"season_number":1,"episode_count":13}]}`))
	}))
	defer provider.Close()
	defer close(release)
	repo := &seriesTestRepo{seasons: []tmdb.SeasonInfo{{Number: 1, Episodes: 12}}, at: time.Now().Add(-25 * time.Hour)}
	m := newSeriesTestManager(repo, provider)
	for i := 0; i < 5; i++ {
		w := seriesTestRequest(m, "/api/films/tmdb-tv-42/seasons")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"episodes":12`) || !strings.Contains(w.Body.String(), `"status":"ready"`) || !strings.Contains(w.Body.String(), `"stale":true`) {
			t.Fatalf("stale response: %d %s", w.Code, w.Body.String())
		}
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh never started")
	}
	select {
	case <-entered:
		t.Fatal("duplicate TMDB refresh")
	default:
	}
}

func TestSeriesFreshEpisodesRemainAvailableWithoutTMDB(t *testing.T) {
	m := newSeriesMetadataManager(Config{})
	m.repo = &seriesTestRepo{episodes: []tmdb.EpisodeInfo{{Episode: 2, Name: "Вторая"}}, epAt: time.Now()}
	w := seriesTestRequest(m, "/api/films/tmdb-tv-42/seasons/1")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"ready"`) || !strings.Contains(w.Body.String(), `"episode":2`) {
		t.Fatalf("cached episodes: %d %s", w.Code, w.Body.String())
	}
	w = seriesTestRequest(m, "/api/films/tmdb-tv-42/seasons/-1")
	if w.Code != 400 {
		t.Fatalf("invalid season accepted: %d", w.Code)
	}
}

func TestSeriesJobsBoundConcurrencyAndRespectCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newSeriesMetadataManager(Config{Context: ctx, TMDB: tmdb.NewClient("test", "", "")})
	entered := make(chan struct{}, 10)
	done := make(chan struct{}, 10)
	work := func(ctx context.Context) error {
		entered <- struct{}{}
		<-ctx.Done()
		done <- struct{}{}
		return ctx.Err()
	}
	for _, key := range []string{"a", "a", "b", "c", "d"} {
		if status := m.schedule(key, work); status != "loading" {
			t.Fatalf("schedule: %s", status)
		}
	}
	for i := 0; i < 3; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("worker not started")
		}
	}
	select {
	case <-entered:
		t.Fatal("more than three jobs started")
	default:
	}
	cancel()
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("job ignored shutdown")
		}
	}
	if status := m.schedule("d", work); status != "unavailable" {
		t.Fatalf("job scheduled after shutdown: %s", status)
	}
}

func TestSeriesRefreshFailureBacksOff(t *testing.T) {
	m := newSeriesMetadataManager(Config{TMDB: tmdb.NewClient("test", "", "")})
	done := make(chan struct{})
	status := m.schedule("failed", func(context.Context) error { close(done); return errors.New("offline") })
	if status != "loading" {
		t.Fatal(status)
	}
	<-done
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := m.jobs["failed"].running
		m.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if status := m.schedule("failed", func(context.Context) error { t.Error("unexpected retry"); return nil }); status != "unavailable" {
		t.Fatalf("no retry backoff: %s", status)
	}
}
