package catalogapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/tmdb"
)

func registerDiscover(mux *http.ServeMux, cfg Config) {
	// Bound the cache; failed requests are never cached.
	type entry struct {
		data []byte
		at   time.Time
	}
	var mu sync.Mutex
	cache := map[string]entry{}
	respond := func(w http.ResponseWriter, r *http.Request, fn func() (any, error)) {
		ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
		defer cancel()
		*r = *r.WithContext(ctx)
		if cfg.TMDB == nil {
			http.Error(w, "TMDB is not configured", 503)
			return
		}
		key := r.URL.RequestURI()
		mu.Lock()
		e, ok := cache[key]
		mu.Unlock()
		if !ok || time.Since(e.at) > 15*time.Minute {
			data, err := fn()
			if err != nil {
				http.Error(w, "metadata provider unavailable", 502)
				return
			}
			e.data, _ = json.Marshal(data)
			e.at = time.Now()
			mu.Lock()
			if len(cache) >= 128 {
				cache = map[string]entry{}
			}
			cache[key] = e
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(e.data)
	}
	items := func(ctx context.Context, fs []tmdb.Film) []CatalogItem {
		out := []CatalogItem{}
		for _, f := range fs {
			it := tmdbFilmToEntry(f).item
			if it.ID == "" {
				media := "movie"
				if f.Kind == "tvSeries" {
					media = "tv"
				}
				it.ID = "tmdb-" + media + "-" + it.TMDBID
			}
			out = append(out, it)
			if cfg.DB != nil && f.IMDBID != "" {
				_ = cfg.DB.SaveTMDBFilm(ctx, f)
			}
		}
		return out
	}
	mux.HandleFunc("GET /api/discover/calendar", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		from, err := time.Parse("2006-01", q.Get("month"))
		if err != nil {
			http.Error(w, "month must be YYYY-MM", 400)
			return
		}
		media := q.Get("media")
		if media != "movie" && media != "tv" {
			http.Error(w, "media must be movie or tv", 400)
			return
		}
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 || page > 500 {
			page = 1
		}
		respond(w, r, func() (any, error) {
			v := url.Values{"page": {strconv.Itoa(page)}, "sort_by": {"popularity.desc"}}
			field := "primary_release_date"
			if media == "tv" {
				field = "first_air_date"
				if q.Get("events") == "episodes" {
					field = "air_date"
				}
			}
			v.Set(field+".gte", from.Format("2006-01-02"))
			v.Set(field+".lte", from.AddDate(0, 1, -1).Format("2006-01-02"))
			fs, total, err := cfg.TMDB.Browse(r.Context(), media, v)
			if err != nil {
				return nil, err
			}
			catalogItems := items(r.Context(), fs)
			events := []map[string]any{}
			partial := false
			if media == "tv" && q.Get("events") == "episodes" {
				var wg sync.WaitGroup
				var lock sync.Mutex
				sem := make(chan struct{}, 4)
				for i, film := range fs {
					wg.Add(1)
					go func(i int, film tmdb.Film) {
						defer wg.Done()
						sem <- struct{}{}
						defer func() { <-sem }()
						eps, err := cfg.TMDB.EpisodeCalendar(r.Context(), film.TMDBID, from.Format("2006-01-02"), from.AddDate(0, 1, -1).Format("2006-01-02"))
						lock.Lock()
						defer lock.Unlock()
						if err != nil {
							partial = true
							return
						}
						for _, ep := range eps {
							events = append(events, map[string]any{"date": ep.Date, "item": catalogItems[i], "season": ep.Season, "episode": ep.Episode, "name": ep.Name})
						}
					}(i, film)
				}
				wg.Wait()
			}
			return map[string]any{"items": catalogItems, "events": events, "partial": partial, "page": page, "total_pages": total}, nil
		})
	})
	mux.HandleFunc("GET /api/discover/episodes", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, _ := strconv.ParseInt(q.Get("tmdb_id"), 10, 64)
		from, err := time.Parse("2006-01", q.Get("month"))
		if id <= 0 || err != nil {
			http.Error(w, "invalid calendar query", 400)
			return
		}
		respond(w, r, func() (any, error) {
			eps, err := cfg.TMDB.EpisodeCalendar(r.Context(), id, from.Format("2006-01-02"), from.AddDate(0, 1, -1).Format("2006-01-02"))
			return map[string]any{"episodes": eps}, err
		})
	})
	mux.HandleFunc("GET /api/discover/picks", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		media := q.Get("media")
		if media != "tv" {
			media = "movie"
		}
		respond(w, r, func() (any, error) {
			v := url.Values{"sort_by": {"popularity.desc"}, "vote_count.gte": {"30"}}
			for _, k := range []string{"with_genres", "vote_average.gte", "with_runtime.lte", "with_cast", "with_crew", "page"} {
				if n := q.Get(k); n != "" {
					if _, e := strconv.ParseFloat(n, 64); e == nil {
						v.Set(k, n)
					}
				}
			}
			fs, total, err := cfg.TMDB.Browse(r.Context(), media, v)
			if err != nil {
				return nil, err
			}
			return map[string]any{"items": items(r.Context(), fs), "total_pages": total}, nil
		})
	})
	mux.HandleFunc("GET /api/films/{id}/explore", func(w http.ResponseWriter, r *http.Request) {
		respond(w, r, func() (any, error) {
			id := r.PathValue("id")
			var tmID int64
			media := "movie"
			if strings.HasPrefix(id, "tmdb-") {
				parts := strings.Split(id, "-")
				if len(parts) == 3 {
					media = parts[1]
					tmID, _ = strconv.ParseInt(parts[2], 10, 64)
				}
			} else {
				f, err := cfg.TMDB.FindByIMDB(r.Context(), id)
				if err != nil {
					return nil, err
				}
				tmID = f.TMDBID
				if f.Kind == "tvSeries" {
					media = "tv"
				}
			}
			if tmID <= 0 || (media != "movie" && media != "tv") {
				return map[string]any{"items": []CatalogItem{}}, nil
			}
			ex, err := cfg.TMDB.Explore(r.Context(), tmID, media)
			if err != nil {
				return nil, err
			}
			return map[string]any{"items": items(r.Context(), ex.Similar), "cast": ex.Cast, "directors": ex.Directors, "trailer": ex.Trailer}, nil
		})
	})
}
