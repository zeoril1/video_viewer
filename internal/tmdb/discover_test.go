package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestCalendarIncludesSeasonStartedBeforeWindow(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/tv/7":
			fmt.Fprint(w, `{"seasons":[{"season_number":0,"air_date":"2024-01-01"},{"season_number":1,"air_date":"2024-01-01"},{"season_number":2,"air_date":"2026-08-01"},{"season_number":3,"air_date":"2027-01-01"}]}`)
		case "/tv/7/season/2":
			fmt.Fprint(w, `{"episodes":[{"name":"Previous","air_date":"2026-08-28","season_number":2,"episode_number":4},{"name":"New","air_date":"2026-09-04","season_number":2,"episode_number":5},{"name":"Unknown","air_date":null,"season_number":2,"episode_number":6}]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := NewClient("", "test", server.URL)
	eps, err := c.EpisodeCalendar(context.Background(), 7, "2026-09-01", "2026-09-30")
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].Episode != 5 {
		t.Fatalf("episodes: %+v", eps)
	}
	if calls["/tv/7/season/1"] != 0 {
		t.Fatal("old season fetched")
	}
}
func TestBrowseTVUsesTVExternalIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover/tv":
			if r.URL.Query().Get("air_date.gte") != "2026-09-01" {
				t.Error("lost date filter")
			}
			fmt.Fprint(w, `{"total_pages":4,"results":[{"id":7,"name":"Сериал","original_name":"Series","first_air_date":"2025-01-01"}]}`)
		case "/tv/7/external_ids":
			fmt.Fprint(w, `{"imdb_id":"tt7"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	fs, pages, err := NewClient("", "token", server.URL).Browse(context.Background(), "tv", url.Values{"air_date.gte": {"2026-09-01"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].IMDBID != "tt7" || fs[0].Kind != "tvSeries" || pages != 4 {
		t.Fatalf("unexpected: %+v pages %d", fs, pages)
	}
}
