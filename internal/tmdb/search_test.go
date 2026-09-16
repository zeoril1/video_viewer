package tmdb

// search_test.go — поиск должен находить и фильмы, и сериалы: раньше
// запрос уходил только в /search/movie, поэтому сериалы («Реальные пацаны»
// и любые другие) в результатах не появлялись вообще.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// multiStub отдаёт /search/multi с фильмом, сериалом и человеком,
// плюс external_ids для фильма и сериала.
func multiStub(t *testing.T, gotPath *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search/multi":
			if gotPath != nil {
				*gotPath = r.URL.Path
			}
			if q := r.URL.Query().Get("query"); q == "" {
				t.Errorf("запрос без query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"page": 1,
				"results": []map[string]any{
					{
						"id": 46005, "media_type": "tv",
						"name": "Реальные Пацаны", "original_name": "Реальные Пацаны",
						"overview": "Про Колю", "first_air_date": "2010-11-15",
						"genre_ids": []int{35}, "vote_average": 6.4, "vote_count": 100,
						"poster_path": "/poster.jpg",
					},
					{
						"id": 77, "media_type": "movie",
						"title": "Реальные пацаны против зомби", "original_title": "Zombie",
						"overview": "Труп", "release_date": "2020-10-29",
						"genre_ids": []int{35}, "vote_average": 5.5, "vote_count": 20,
					},
					{"id": 999, "media_type": "person", "name": "Актёр"},
				},
			})
		case "/tv/46005/external_ids":
			_ = json.NewEncoder(w).Encode(map[string]string{"imdb_id": "tt1740818"})
		case "/movie/77/external_ids":
			_ = json.NewEncoder(w).Encode(map[string]string{"imdb_id": "tt1111111"})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestSearchFindsMoviesAndTV(t *testing.T) {
	var path string
	srv := multiStub(t, &path)
	defer srv.Close()
	c := NewClient("", "", srv.URL)

	films, err := c.Search(context.Background(), "Реальные пацаны", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if path != "/search/multi" {
		t.Fatalf("поиск идёт по %q, ожидался /search/multi", path)
	}
	if len(films) != 2 {
		t.Fatalf("получено %d записей (человек должен отбрасываться): %+v", len(films), films)
	}

	tv := films[0]
	if tv.Kind != "tvSeries" || tv.TMDBID != 46005 || tv.IMDBID != "tt1740818" {
		t.Errorf("сериал разобран неверно: %+v", tv)
	}
	if tv.Title != "Реальные Пацаны" || tv.TitleRU != "Реальные Пацаны" {
		t.Errorf("названия сериала: %+v", tv)
	}
	if tv.Year != 2010 || tv.ReleaseDate != "2010-11-15" {
		t.Errorf("дата сериала берётся из first_air_date: %+v", tv)
	}
	if len(tv.Genres) != 1 || tv.Genres[0] != "Comedy" {
		t.Errorf("жанры сериала: %+v", tv.Genres)
	}

	mv := films[1]
	if mv.Kind != "feature" || mv.TMDBID != 77 || mv.IMDBID != "tt1111111" || mv.Year != 2020 {
		t.Errorf("фильм разобран неверно: %+v", mv)
	}
}

func TestSearchRespectsLimit(t *testing.T) {
	srv := multiStub(t, nil)
	defer srv.Close()
	c := NewClient("", "", srv.URL)

	films, err := c.Search(context.Background(), "Реальные пацаны", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(films) != 1 || films[0].Kind != "tvSeries" {
		t.Fatalf("лимит не применён: %+v", films)
	}
}
