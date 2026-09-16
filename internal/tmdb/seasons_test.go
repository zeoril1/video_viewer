package tmdb

// seasons_test.go — число сезонов сериала из TMDB (/tv/{id}). Нужно поиску
// источников: без него запрос к трекеру получается один общий, и в выдаче
// оказываются только случайно попавшие туда сезоны.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSeasonsCount(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Query().Get("language") != "ru-RU" {
			t.Errorf("нет language=ru-RU в запросе: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 46005, "name": "Реальные Пацаны",
			"number_of_seasons": 10, "number_of_episodes": 291,
		})
	}))
	defer srv.Close()

	c := NewClient("", "", srv.URL)
	n, err := c.SeasonsCount(context.Background(), 46005)
	if err != nil {
		t.Fatalf("SeasonsCount: %v", err)
	}
	if n != 10 {
		t.Errorf("число сезонов %d, ожидали 10", n)
	}
	if gotPath != "/tv/46005" {
		t.Errorf("запрос ушёл на %q, ожидали /tv/46005", gotPath)
	}
}

func TestSeasonsCountError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient("", "", srv.URL)
	if _, err := c.SeasonsCount(context.Background(), 1); err == nil {
		t.Fatal("ожидали ошибку при 500 от TMDB")
	}
}
