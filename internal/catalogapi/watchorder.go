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

	"github.com/zeoril1/video_viewer/internal/tmdb"
)

var watchOrderID = regexp.MustCompile(`^(tt[0-9]{1,20}|tmdb-(movie|tv)-[1-9][0-9]{0,17})$`)

type orderItem struct {
	MatchTitles []string `json:"match_titles,omitempty"`
	ID          string   `json:"id,omitempty"`
	Title       string   `json:"title"`
	Date        string   `json:"date,omitempty"`
	ExternalURL string   `json:"external_url,omitempty"`
	Current     bool     `json:"current,omitempty"`
	Extra       bool     `json:"extra,omitempty"`
}
type watchOrder struct {
	Title     string      `json:"title"`
	Source    string      `json:"source"`
	SourceURL string      `json:"source_url"`
	Mode      string      `json:"mode"`
	Note      string      `json:"note,omitempty"`
	Items     []orderItem `json:"items"`
	Partial   bool        `json:"partial,omitempty"`
	Stale     bool        `json:"stale,omitempty"`
	UpdatedAt time.Time   `json:"updated_at"`
}

func filmMedia(f tmdb.Film) string {
	if f.Kind == "tvSeries" || f.Kind == "tvMiniSeries" {
		return "tv"
	}
	return "movie"
}

func fetchWatchOrder(ctx context.Context, cfg Config, anime *animeProvider, id string) (watchOrder, error) {
	out := watchOrder{Items: []orderItem{}, Mode: "release", Source: "TMDB"}
	var film tmdb.Film
	var err error
	if strings.HasPrefix(id, "tmdb-") {
		parts := strings.Split(id, "-")
		if len(parts) != 3 || (parts[1] != "movie" && parts[1] != "tv") {
			return out, fmt.Errorf("invalid id")
		}
		n, e := strconv.ParseInt(parts[2], 10, 64)
		if e != nil || n <= 0 {
			return out, fmt.Errorf("invalid id")
		}
		kind := "feature"
		if parts[1] == "tv" {
			kind = "tvSeries"
		}
		film, err = cfg.TMDB.ByIDKind(ctx, n, kind)
	} else {
		film, err = cfg.TMDB.FindByIMDB(ctx, id)
		if err == nil && film.TMDBID > 0 {
			film, err = cfg.TMDB.ByIDKind(ctx, film.TMDBID, film.Kind)
		}
	}
	if err != nil {
		return out, err
	}
	if film.TMDBID <= 0 {
		return out, nil
	}
	for _, genre := range film.Genres {
		if strings.EqualFold(genre, "Animation") || strings.EqualFold(genre, "Anime") {
			out, err = anime.order(ctx, film)
			if err != nil || len(out.Items) > 1 {
				return out, err
			}
			break
		}
	}
	media := filmMedia(film)
	key, films, partial, err := cfg.TMDB.UniverseOrder(ctx, film.TMDBID, media)
	if err != nil {
		return out, err
	}
	out = watchOrder{Items: []orderItem{}, Mode: "release", Source: "TMDB", Partial: partial}
	if key > 0 {
		out.Title = "Кинематографическая вселенная Marvel"
		out.SourceURL = fmt.Sprintf("https://www.themoviedb.org/keyword/%d", key)
		out.Note = "По дате выхода. Сериалы размещены по премьере первого сезона; это не хронология событий. Состав определяется метками TMDB."
	} else if media == "movie" {
		var collection int64
		out.Title, collection, films, err = cfg.TMDB.CollectionOrder(ctx, film.TMDBID)
		if err != nil {
			return out, err
		}
		if collection > 0 {
			out.SourceURL = fmt.Sprintf("https://www.themoviedb.org/collection/%d", collection)
		}
		out.Note = "Части киноколлекции по дате выхода, а не по времени событий в сюжете."
	}
	for _, f := range films {
		title := f.TitleRU
		if title == "" {
			title = f.Title
		}
		out.Items = append(out.Items, orderItem{ID: fmt.Sprintf("tmdb-%s-%d", filmMedia(f), f.TMDBID), Title: title, Date: f.ReleaseDate, Current: f.TMDBID == film.TMDBID && filmMedia(f) == media})
	}
	return out, nil
}

func registerWatchOrder(mux *http.ServeMux, cfg Config, svc *catalogService) {
	type cacheEntry struct {
		Order watchOrder
		At    time.Time
	}
	var mu sync.Mutex
	cache := map[string]cacheEntry{}
	// Bound provider work, including requests for different titles.
	slots := make(chan struct{}, 2)
	flights := map[string]chan struct{}{}
	failures := map[string]time.Time{}
	anime := &animeProvider{client: &http.Client{Timeout: 15 * time.Second}, endpoint: "https://graphql.anilist.co"}
	mux.HandleFunc("GET /api/films/{id}/watch-order", func(w http.ResponseWriter, r *http.Request) {
		if cfg.TMDB == nil {
			http.Error(w, "metadata provider not configured", 503)
			return
		}
		id := r.PathValue("id")
		if !watchOrderID.MatchString(id) {
			http.Error(w, "unsupported film id", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
		defer cancel()
		key := "v1:" + id
		var old cacheEntry
		for {
			mu.Lock()
			if until := failures[key]; time.Now().Before(until) {
				mu.Unlock()
				w.Header().Set("Retry-After", "30")
				http.Error(w, "watch order provider unavailable", 502)
				return
			}
			old = cache[key]
			wait := flights[key]
			if !old.At.IsZero() && time.Since(old.At) < 24*time.Hour {
				mu.Unlock()
				break
			}
			if wait != nil {
				mu.Unlock()
				select {
				case <-ctx.Done():
					http.Error(w, "metadata timeout", 504)
					return
				case <-wait:
					continue
				}
			}
			flights[key] = make(chan struct{})
			mu.Unlock()
			defer func() { mu.Lock(); close(flights[key]); delete(flights, key); mu.Unlock() }()
			if cfg.DB != nil {
				raw, at, err := cfg.DB.WatchOrder(ctx, key)
				if err == nil {
					var data watchOrder
					if json.Unmarshal(raw, &data) == nil {
						old = cacheEntry{data, at}
					}
				}
			}
			if old.At.IsZero() || time.Since(old.At) >= 24*time.Hour {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					http.Error(w, "metadata timeout", 504)
					return
				}
				data, err := fetchWatchOrder(ctx, cfg, anime, id)
				<-slots
				if err != nil {
					if old.At.IsZero() {
						mu.Lock()
						if len(failures) >= 256 {
							failures = map[string]time.Time{}
						}
						failures[key] = time.Now().Add(30 * time.Second)
						mu.Unlock()
						w.Header().Set("Retry-After", "30")
						http.Error(w, "watch order provider unavailable", 502)
						return
					}
					old.Order.Stale = true
					old.At = time.Now().Add(-24*time.Hour + time.Minute)
				} else {
					data.UpdatedAt = time.Now()
					old = cacheEntry{data, time.Now()}
					if cfg.DB != nil {
						raw, _ := json.Marshal(data)
						if err := cfg.DB.SaveWatchOrder(ctx, key, raw); err != nil {
							log.Printf("watch order cache save: %v", err)
						}
					}
				}
			}
			mu.Lock()
			if len(cache) >= 256 {
				cache = map[string]cacheEntry{}
			}
			cache[key] = old
			mu.Unlock()
			break
		}
		// Resolve stable TMDB IDs against the current catalogue, so history badges use IMDb IDs.
		data := old.Order
		data.Items = append([]orderItem{}, data.Items...)
		entries := svc.All(ctx)
		for i := range data.Items {
			item := &data.Items[i]
			if item.Current {
				item.ID = id
				continue
			}
			match := ""
			for _, entry := range entries {
				f := entry.item
				if item.ID != "" {
					kind := "movie"
					if f.Kind == "tvSeries" || f.Kind == "tvMiniSeries" {
						kind = "tv"
					}
					if item.ID == "tmdb-"+kind+"-"+f.TMDBID {
						match = f.ID
						break
					}
				} else if orderItemMatches(*item, f) {
					if match != "" && match != f.ID {
						match = ""
						break
					}
					match = f.ID
				}
			}
			if match != "" {
				item.ID = match
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(data)
	})
}

// Missing external IDs require both animation metadata and exact title/year agreement.
func orderItemMatches(item orderItem, f CatalogItem) bool {
	if len(item.Date) < 4 || strconv.Itoa(f.Year) != item.Date[:4] {
		return false
	}
	animation := false
	for _, g := range f.Genres {
		if strings.EqualFold(g, "animation") || strings.EqualFold(g, "anime") {
			animation = true
		}
	}
	if !animation {
		return false
	}
	for _, name := range append([]string{item.Title}, item.MatchTitles...) {
		key := orderTitleKey(name)
		if key != "" && (key == orderTitleKey(f.Title) || key == orderTitleKey(f.TitleRU)) {
			return true
		}
	}
	return false
}
