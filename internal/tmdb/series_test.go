package tmdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSeasonEpisodesOnlySelectedSeasonAndCanonicalNumbers(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/tv/42/season/3" || r.URL.Query().Get("language") != "ru-RU" {
			t.Errorf("unexpected request: %s", r.URL.String())
		}
		w.Write([]byte(`{"episodes":[{"episode_number":3,"name":"Третья","air_date":"2026-10-07","runtime":24},{"episode_number":1,"name":"Первая"},{"episode_number":0},{"episode_number":1,"name":"duplicate"}]}`))
	}))
	defer server.Close()
	client := NewClient("test", "", server.URL)
	eps, err := client.SeasonEpisodes(context.Background(), 42, 3)
	if err != nil || requests != 1 || len(eps) != 2 || eps[0].Episode != 1 || eps[1].Episode != 3 || eps[1].Runtime != 24 {
		t.Fatalf("episodes: %+v error=%v requests=%d", eps, err, requests)
	}
}

func TestSeasonStartYearSurvivesPersistentJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"seasons":[{"season_number":1,"episode_count":12,"air_date":"2023-05-06","name":"Первый сезон"}]}`))
	}))
	defer server.Close()
	seasons, err := NewClient("test", "", server.URL).SeasonStructure(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(seasons)
	if err != nil {
		t.Fatal(err)
	}
	var cached []SeasonInfo
	if err := json.Unmarshal(raw, &cached); err != nil {
		t.Fatal(err)
	}
	if len(cached) != 1 || cached[0].Year != 2023 || cached[0].Name != "Первый сезон" || SeasonByYear(cached, 2023) != 1 {
		t.Fatalf("persisted season mapping lost: %+v", cached)
	}
}
