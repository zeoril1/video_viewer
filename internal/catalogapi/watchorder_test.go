package catalogapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zeoril1/video_viewer/internal/tmdb"
)

func TestAnimeOrderLiveAttackOnTitan(t *testing.T) {
	if os.Getenv("WATCH_ORDER_LIVE") != "1" {
		t.Skip("WATCH_ORDER_LIVE=1 enables public provider check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	p := &animeProvider{client: &http.Client{Timeout: 15 * time.Second}, endpoint: "https://graphql.anilist.co"}
	data, err := p.order(ctx, tmdb.Film{TMDBID: 1429, Title: "進撃の巨人", Kind: "tvSeries", Year: 2013})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range data.Items {
		if item.Current {
			found = true
		}
	}
	if len(data.Items) < 3 || !found {
		t.Fatalf("unexpected provider result: %+v", data)
	}
	t.Logf("AniList: %d related parts; partial=%v", len(data.Items), data.Partial)
}

func TestWatchOrderCollectionAndCache(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/movie/2":
			w.Write([]byte(`{"id":2,"title":"Second","original_title":"Second","release_date":"2010-01-01","belongs_to_collection":{"id":10}}`))
		case "/movie/2/keywords":
			w.Write([]byte(`{"keywords":[]}`))
		case "/collection/10":
			w.Write([]byte(`{"name":"Saga","parts":[{"id":3,"title":"Unknown"},{"id":2,"title":"Second","release_date":"2010-01-01"},{"id":1,"title":"First","release_date":"2000-01-01"},{"id":1,"title":"Duplicate"}]}`))
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	cfg := Config{TMDB: tmdb.NewClient("test", "", provider.URL)}
	svc := newCatalogService(nil, nil, nil, nil)
	svc.allCache = []catalogEntry{}
	svc.allCachedAt = time.Now()
	mux := http.NewServeMux()
	registerWatchOrder(mux, cfg, svc)
	for i := 0; i < 2; i++ {
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, httptest.NewRequest("GET", "/api/films/tmdb-movie-2/watch-order", nil))
		if res.Code != 200 {
			t.Fatalf("response %d %s", res.Code, res.Body.String())
		}
		var data watchOrder
		if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Items) != 3 || data.Items[0].ID != "tmdb-movie-1" || !data.Items[1].Current || data.Items[2].Date != "" {
			t.Fatalf("bad order %+v", data)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("cache missed: %d requests", calls.Load())
	}
}

func TestWatchOrderMCUIncludesTVWithoutIDCollision(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tv/2":
			w.Write([]byte(`{"id":2,"name":"Series","first_air_date":"2020-01-01"}`))
		case "/tv/2/keywords":
			w.Write([]byte(`{"results":[{"id":99,"name":"marvel cinematic universe (mcu)"}]}`))
		case "/discover/movie":
			if r.URL.Query().Get("with_keywords") != "99" {
				t.Error("missing universe filter")
			}
			w.Write([]byte(`{"total_pages":1,"results":[{"id":2,"title":"Movie","release_date":"2008-01-01"}]}`))
		case "/discover/tv":
			w.Write([]byte(`{"total_pages":1,"results":[{"id":2,"name":"Series","first_air_date":"2020-01-01"}]}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer provider.Close()
	data, err := fetchWatchOrder(context.Background(), Config{TMDB: tmdb.NewClient("test", "", provider.URL)}, nil, "tmdb-tv-2")
	if err != nil || len(data.Items) != 2 {
		t.Fatalf("%+v %v", data, err)
	}
	if data.Items[0].Current || !data.Items[1].Current || data.Items[0].ID == data.Items[1].ID {
		t.Fatalf("movie/tv mixed %+v", data.Items)
	}
}

func TestAnimeOrderCyclesAndWrongAdaptations(t *testing.T) {
	root := animeMedia{ID: 1, Type: "ANIME", Format: "TV"}
	root.Title.English = "Example"
	root.StartDate.Year = 2020
	sequel := animeMedia{ID: 2, Type: "ANIME", Format: "TV"}
	sequel.Title.English = "Example 2"
	sequel.StartDate.Year = 2022
	f := tmdb.Film{Title: "Example", Kind: "tvSeries", Year: 2020, TMDBID: 10}
	if !animeMatches(root, f) {
		t.Fatal("exact match rejected")
	}
	f.Year = 2021
	if animeMatches(root, f) {
		t.Fatal("wrong year accepted")
	}
	f.Year = 2020
	item := orderItem{Title: "Example", Date: "2020"}
	if orderItemMatches(item, CatalogItem{Title: "Example", Year: 2020, Genres: []string{"Drama"}}) {
		t.Fatal("live action accepted")
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "Page(") {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"Page": map[string]any{"media": []animeMedia{root}}}})
			return
		}
		node := root
		relation := "SEQUEL"
		other := sequel
		if strings.Contains(req.Query, "m2:") {
			node = sequel
			relation = "PREQUEL"
			other = root
		}
		raw, _ := json.Marshal(node)
		var m map[string]any
		json.Unmarshal(raw, &m)
		m["relations"] = map[string]any{"edges": []any{map[string]any{"relationType": relation, "node": other}, map[string]any{"relationType": "ADAPTATION", "node": map[string]any{"id": 99, "type": "MANGA"}}}}
		alias := "m1"
		if node.ID == 2 {
			alias = "m2"
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{alias: m}})
	}))
	defer provider.Close()
	p := &animeProvider{client: provider.Client(), endpoint: provider.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, err := p.order(ctx, f)
	if err != nil || len(data.Items) != 2 || !data.Items[0].Current || data.Partial {
		t.Fatalf("%+v %v", data, err)
	}
}

func TestWatchOrderUnavailableAndInvalidID(t *testing.T) {
	mux := http.NewServeMux()
	registerWatchOrder(mux, Config{}, newCatalogService(nil, nil, nil, nil))
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("GET", "/api/films/tt1/watch-order", nil))
	if res.Code != 503 {
		t.Fatalf("want 503 got %d", res.Code)
	}
}
