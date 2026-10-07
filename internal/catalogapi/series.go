package catalogapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

type seriesMetadataRepo interface {
	GetByIMDBID(context.Context, string) (db.Film, bool, error)
	SaveTMDBFilm(context.Context, tmdb.Film) error
	SeriesSeasonCache(context.Context, int64) ([]tmdb.SeasonInfo, time.Time, error)
	SeriesEpisodeCache(context.Context, int64, int) ([]tmdb.EpisodeInfo, time.Time, error)
	SeriesSeasons(context.Context, int64, func(context.Context, int64) ([]tmdb.SeasonInfo, error)) ([]tmdb.SeasonInfo, error)
	SeriesEpisodes(context.Context, int64, int, func(context.Context, int64, int) ([]tmdb.EpisodeInfo, error)) ([]tmdb.EpisodeInfo, error)
}

type seriesJobState struct {
	running bool
	failed  bool
	until   time.Time
}

// Metadata refresh is separate from slow tracker discovery. Only three network
// jobs can run, and repeated requests for one series share the same job.
type seriesMetadataManager struct {
	repo seriesMetadataRepo
	tm   *tmdb.Client
	ctx  context.Context
	mu   sync.Mutex
	jobs map[string]seriesJobState
	sem  chan struct{}
}

func newSeriesMetadataManager(cfg Config) *seriesMetadataManager {
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	var repo seriesMetadataRepo
	if cfg.DB != nil {
		repo = cfg.DB
	}
	return &seriesMetadataManager{repo: repo, tm: cfg.TMDB, ctx: ctx, jobs: map[string]seriesJobState{}, sem: make(chan struct{}, 3)}
}

func (m *seriesMetadataManager) schedule(key string, work func(context.Context) error) string {
	if m.tm == nil || m.ctx.Err() != nil {
		return "unavailable"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.jobs[key]
	if state.running {
		return "loading"
	}
	if time.Now().Before(state.until) {
		if state.failed {
			return "unavailable"
		}
		return "loading"
	}
	select {
	case m.sem <- struct{}{}:
	default:
		return "loading"
	}
	// Bound bookkeeping as well as network concurrency.
	if len(m.jobs) > 2048 {
		for k, s := range m.jobs {
			if !s.running && time.Now().After(s.until) {
				delete(m.jobs, k)
			}
		}
	}
	m.jobs[key] = seriesJobState{running: true}
	go func() {
		defer func() { <-m.sem }()
		ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
		defer cancel()
		err := work(ctx)
		if err != nil {
			log.Printf("series metadata: %s: %v", key, err)
		}
		m.mu.Lock()
		m.jobs[key] = seriesJobState{failed: err != nil, until: time.Now().Add(30 * time.Second)}
		m.mu.Unlock()
	}()
	return "loading"
}

var seriesIMDBID = regexp.MustCompile(`^tt[0-9]{1,20}$`)

// resolve reads the local film only. Missing TMDB links are filled in the background.
func (m *seriesMetadataManager) resolve(ctx context.Context, id string) (int64, string, error) {
	if strings.HasPrefix(id, "tmdb-tv-") {
		n, err := strconv.ParseInt(strings.TrimPrefix(id, "tmdb-tv-"), 10, 64)
		if err != nil || n <= 0 {
			return 0, "", fmt.Errorf("invalid series id")
		}
		return n, "", nil
	}
	if !seriesIMDBID.MatchString(id) {
		return 0, "", fmt.Errorf("invalid series id")
	}
	film, found, err := m.repo.GetByIMDBID(ctx, id)
	if err != nil {
		return 0, "", err
	}
	if found && !isSeriesKind(film.Kind) {
		return 0, "", fmt.Errorf("not a series")
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(film.TMDBID), 10, 64)
	if found && n > 0 {
		return n, "", nil
	}
	status := m.schedule("resolve:"+id, func(ctx context.Context) error {
		f, err := m.tm.FindByIMDB(ctx, id)
		if err != nil {
			return err
		}
		if !isSeriesKind(f.Kind) || f.TMDBID <= 0 {
			return fmt.Errorf("TMDB series not found")
		}
		f.IMDBID = id
		if err := m.repo.SaveTMDBFilm(ctx, f); err != nil {
			return err
		}
		_, err = m.repo.SeriesSeasons(ctx, f.TMDBID, m.tm.SeasonStructure)
		return err
	})
	return 0, status, nil
}

type seriesSeasonItem struct {
	Season   int    `json:"season"`
	Episodes int    `json:"episodes"`
	Name     string `json:"name,omitempty"`
	AirDate  string `json:"air_date,omitempty"`
}

func (m *seriesMetadataManager) seasons(w http.ResponseWriter, r *http.Request) {
	if m.repo == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	tmdbID, status, err := m.resolve(r.Context(), id)
	if err != nil {
		http.Error(w, "series metadata unavailable", http.StatusNotFound)
		return
	}
	items := []seriesSeasonItem{}
	stale := false
	if tmdbID > 0 {
		seasons, refreshed, err := m.repo.SeriesSeasonCache(r.Context(), tmdbID)
		if err != nil {
			http.Error(w, "series cache unavailable", http.StatusInternalServerError)
			return
		}
		stale = refreshed.IsZero() || time.Since(refreshed) >= db.SeriesMetadataFreshFor
		if stale {
			status = m.schedule(fmt.Sprintf("seasons:%d", tmdbID), func(ctx context.Context) error {
				_, err := m.repo.SeriesSeasons(ctx, tmdbID, m.tm.SeasonStructure)
				return err
			})
		}
		for _, s := range seasons {
			if s.Number > 0 && s.Episodes > 0 {
				items = append(items, seriesSeasonItem{Season: s.Number, Episodes: s.Episodes, Name: s.Name, AirDate: s.AirDate})
			}
		}
		if len(items) > 0 || !stale {
			status = "ready"
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "tmdb_id": tmdbID, "status": status, "seasons": items, "stale": stale})
}

func (m *seriesMetadataManager) episodes(w http.ResponseWriter, r *http.Request) {
	if m.repo == nil {
		http.Error(w, "database not configured", http.StatusServiceUnavailable)
		return
	}
	season, err := strconv.Atoi(r.PathValue("season"))
	if err != nil || season < 0 || season > 1000 {
		http.Error(w, "invalid season", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	tmdbID, status, err := m.resolve(r.Context(), id)
	if err != nil {
		http.Error(w, "series metadata unavailable", http.StatusNotFound)
		return
	}
	episodes := []tmdb.EpisodeInfo{}
	stale := false
	if tmdbID > 0 {
		cached, refreshed, err := m.repo.SeriesEpisodeCache(r.Context(), tmdbID, season)
		if err != nil {
			http.Error(w, "episode cache unavailable", http.StatusInternalServerError)
			return
		}
		if cached != nil {
			episodes = cached
		}
		stale = refreshed.IsZero() || time.Since(refreshed) >= db.SeriesMetadataFreshFor
		if stale {
			status = m.schedule(fmt.Sprintf("episodes:%d:%d", tmdbID, season), func(ctx context.Context) error {
				_, err := m.repo.SeriesEpisodes(ctx, tmdbID, season, m.tm.SeasonEpisodes)
				return err
			})
		}
		if len(episodes) > 0 || !stale {
			status = "ready"
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "tmdb_id": tmdbID, "season": season, "status": status, "episodes": episodes, "stale": stale})
}

func registerSeriesMetadata(mux *http.ServeMux, cfg Config) {
	m := newSeriesMetadataManager(cfg)
	mux.HandleFunc("GET /api/films/{id}/seasons", m.seasons)
	mux.HandleFunc("GET /api/films/{id}/seasons/{season}", m.episodes)
}
