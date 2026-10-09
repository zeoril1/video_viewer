// credits_test.go — локализация имён из титров TMDB: имена запрашиваются дважды (исходное
// написание и русское), пары сопоставляются по id человека и уходят в таблицу person_names.
package tmdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// creditsStub отдаёт титры фильма: на ru-RU — русские имена, без language — исходные.
func creditsStub(t *testing.T, langs *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/977942/credits" {
			http.NotFound(w, r)
			return
		}
		lang := r.URL.Query().Get("language")
		if langs != nil {
			*langs = append(*langs, lang)
		}
		ru := lang == "ru-RU"
		cast := []map[string]any{}
		names := []struct {
			id     int64
			en, ru string
		}{
			{100, "Andrew Garfield", "Эндрю Гарфилд"},
			{101, "Jamie Bell", "Джейми Белл"},
			{102, "Stephen Dillane", "Stephen Dillane"}, // TMDB перевода не даёт — имя совпадает
			{103, "Tom Hollander", "Том Холландер"},
			{104, "Cosmo Jarvis", "Космо Джарвис"},
			{105, "Thomasin McKenzie", "Томасин МакКензи"},
			{106, "Лишний Актёр", "Лишний Актёр"}, // 7-й в списке — в карточку не попадает
		}
		for _, n := range names {
			name := n.en
			if ru {
				name = n.ru
			}
			cast = append(cast, map[string]any{"id": n.id, "name": name})
		}
		director := "Paul Greengrass"
		if ru {
			director = "Пол Гринграсс"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"crew": []map[string]any{
				{"id": 200, "job": "Producer", "name": "Продюсер"},
				{"id": 201, "job": "Director", "name": director},
			},
			"cast": cast,
		})
	}))
}

func TestCreditsLocalizedPairsNames(t *testing.T) {
	var langs []string
	srv := creditsStub(t, &langs)
	defer srv.Close()
	c := NewClient("key", "", srv.URL)

	cr, err := c.CreditsLocalized(context.Background(), 977942, "feature")
	if err != nil {
		t.Fatalf("CreditsLocalized: %v", err)
	}
	if cr.Director != "Paul Greengrass" || len(cr.Actors) != maxCreditsActors {
		t.Fatalf("титры в исходном написании: director=%q actors=%d", cr.Director, len(cr.Actors))
	}
	if len(langs) != 2 || langs[0] != "" || langs[1] != "ru-RU" {
		t.Fatalf("ожидались запросы без языка и с ru-RU, получили %v", langs)
	}

	byID := map[int64]PersonName{}
	for _, n := range cr.Names {
		byID[n.ID] = n
	}
	if got := byID[201]; got.Name != "Paul Greengrass" || got.NameRU != "Пол Гринграсс" {
		t.Errorf("режиссёр: %+v", got)
	}
	if got := byID[100]; got.NameRU != "Эндрю Гарфилд" {
		t.Errorf("актёр: %+v", got)
	}
	if got := byID[102]; got.NameRU != "" {
		t.Errorf("имя без перевода должно остаться пустым: %+v", got)
	}
	if _, ok := byID[106]; ok {
		t.Error("7-й актёр не должен попадать в пары имён")
	}
}

func TestCreditsReturnsOriginalNames(t *testing.T) {
	srv := creditsStub(t, nil)
	defer srv.Close()
	c := NewClient("key", "", srv.URL)

	director, actors, err := c.Credits(context.Background(), 977942, "feature")
	if err != nil {
		t.Fatalf("Credits: %v", err)
	}
	if director != "Paul Greengrass" {
		t.Errorf("director = %q", director)
	}
	if len(actors) != maxCreditsActors || actors[0] != "Andrew Garfield" {
		t.Errorf("actors = %v", actors)
	}
}

func TestCreditsLocalizedFailsWhenRussianUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("language") == "ru-RU" {
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"crew": []any{}, "cast": []any{}})
	}))
	defer srv.Close()
	c := NewClient("key", "", srv.URL)

	// Без русского варианта пары неполные — ошибка, чтобы задача повторила запрос позже.
	if _, err := c.CreditsLocalized(context.Background(), 977942, "feature"); err == nil {
		t.Fatal("ожидалась ошибка при недоступном ru-RU")
	}
}
