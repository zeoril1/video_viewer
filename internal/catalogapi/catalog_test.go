package catalogapi

import (
	"testing"
	"time"
)

func TestMatchesQuery(t *testing.T) {
	it := CatalogItem{
		Title:    "Whiplash",
		TitleRU:  "Одержимость",
		Category: "imdb",
		IMDbID:   "tt2582802",
		Year:     2014,
	}
	cases := []struct {
		q    string
		want bool
	}{
		{"одержимость", true},
		{"whiplash", true},
		{"одержимость / whiplash 2014", true},
		{"tt2582802", true},
		{"одержимость / whiplash 2015", false},
		{"Интерстеллар", false},
		{"", true},
	}
	for _, c := range cases {
		if got := matchesQuery(it, c.q); got != c.want {
			t.Errorf("matchesQuery(%q) = %v, want %v", c.q, got, c.want)
		}
	}
}

func TestCleanSearchQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Одержимость / Whiplash 2014", "Одержимость"},
		{"Whiplash 2014", "Whiplash"},
		{"Одержимость / Whiplash", "Одержимость"},
		{"Только вперёд", "Только вперёд"},
		{"Интерстеллар (2014)", "Интерстеллар (2014)"}, // год в скобках не срезается (текущее поведение)
	}
	for _, c := range cases {
		if got := cleanSearchQuery(c.in); got != c.want {
			t.Errorf("cleanSearchQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSectionForItem(t *testing.T) {
	cases := []struct {
		name string
		it   CatalogItem
		want string
	}{
		{"фильм", CatalogItem{Kind: "feature"}, "movie"},
		{"сериал", CatalogItem{Kind: "tvSeries"}, "series"},
		{"мини-сериал", CatalogItem{Kind: "tvMiniSeries"}, "series"},
		{"аниме по жанру", CatalogItem{Kind: "feature", Genres: []string{"Animation", "Anime"}}, "anime"},
		{"мультфильм по жанру", CatalogItem{Kind: "feature", Genres: []string{"Animation"}}, "cartoon"},
		{"мультфильм по типу", CatalogItem{Kind: "animation"}, "cartoon"},
		{"тв-фильм", CatalogItem{Kind: "tvMovie"}, "tv_movie"},
	}
	for _, c := range cases {
		if got := sectionForItem(c.it); got != c.want {
			t.Errorf("%s: sectionForItem = %q, want %q", c.name, got, c.want)
		}
	}
}

// Мультфильмы и аниме должны попадать только в свои разделы — в «Фильмы»,
// «Сериалы» и др. они не должны просачиваться (kind=feature + жанр
// Animation раньше давал мультфильмы в разделе «Фильмы»).
func TestMatchesSectionExcludesAnimation(t *testing.T) {
	cartoon := CatalogItem{Kind: "feature", Genres: []string{"Animation"}}
	cartoonSeries := CatalogItem{Kind: "tvSeries", Genres: []string{"Animation"}}
	anime := CatalogItem{Kind: "feature", Genres: []string{"Animation", "Anime"}}
	feature := CatalogItem{Kind: "feature"}
	series := CatalogItem{Kind: "tvSeries"}

	if matchesSection(cartoon, "movie") {
		t.Error("мультфильм (feature+Animation) не должен попадать в movie")
	}
	if matchesSection(cartoonSeries, "series") {
		t.Error("мультсериал не должен попадать в series")
	}
	if matchesSection(anime, "movie") {
		t.Error("аниме не должно попадать в movie")
	}
	if matchesSection(cartoon, "tv_movie") {
		t.Error("мультфильм не должен попадать в tv_movie")
	}
	if !matchesSection(feature, "movie") {
		t.Error("обычный фильм должен попадать в movie")
	}
	if !matchesSection(series, "series") {
		t.Error("сериал должен попадать в series")
	}
	if !matchesSection(cartoon, "cartoon") {
		t.Error("мультфильм должен попадать в cartoon")
	}
	if !matchesSection(anime, "anime") {
		t.Error("аниме должно попадать в anime")
	}
	// «Все» и «Популярное» не фильтруются по типу.
	if !matchesSection(cartoon, "all") {
		t.Error("все должно включать мультфильмы")
	}
	if !matchesSection(cartoon, "popular") {
		t.Error("популярное может включать мультфильмы")
	}
}

func TestSortCatalogEntries(t *testing.T) {
	entries := []catalogEntry{
		{item: CatalogItem{ID: "a", Title: "Ббб", TitleRU: "Бета", Year: 2001, Rating: 7.0}},
		{item: CatalogItem{ID: "b", Title: "Ааа", TitleRU: "Альфа", Year: 2020, Rating: 8.0}},
		{item: CatalogItem{ID: "c", Title: "Ввв", Year: 2010, Rating: 6.0}},
		{item: CatalogItem{ID: "d", Title: "Ггг", Year: 0, Rating: 9.0}},
		{item: CatalogItem{ID: "e", Title: "Ддд", Year: 2001, ReleaseDate: "2001-06-15", Rating: 7.5}},
	}

	// По умолчанию ("year") — полная дата выпуска: новые сверху; без даты —
	// по году ("YYYY-00-00"); без года — в конец.
	sorted := append([]catalogEntry{}, entries...)
	sortCatalogEntries(sorted, "")
	want := []string{"b", "c", "e", "a", "d"}
	for i, id := range want {
		if sorted[i].item.ID != id {
			t.Errorf("year: позиция %d = %s, want %s", i, sorted[i].item.ID, id)
		}
	}
	// «e» (2001-06-15) опережает «a» (2001, только год) — месяц и день
	// учитываются при сортировке.
	if sorted[2].item.ID != "e" || sorted[3].item.ID != "a" {
		t.Errorf("year: дата с месяцем/днём должна опережать только год: %s, %s", sorted[2].item.ID, sorted[3].item.ID)
	}

	// По рейтингу (лучший из доступных), при равенстве — по дате.
	sorted = append([]catalogEntry{}, entries...)
	sortCatalogEntries(sorted, "rating")
	want = []string{"d", "b", "e", "a", "c"}
	for i, id := range want {
		if sorted[i].item.ID != id {
			t.Errorf("rating: позиция %d = %s, want %s", i, sorted[i].item.ID, id)
		}
	}

	// По названию (русское при наличии, иначе английское).
	sorted = append([]catalogEntry{}, entries...)
	sortCatalogEntries(sorted, "title")
	want = []string{"b", "a", "c", "d", "e"}
	for i, id := range want {
		if sorted[i].item.ID != id {
			t.Errorf("title: позиция %d = %s, want %s", i, sorted[i].item.ID, id)
		}
	}
}

func TestIsReleased(t *testing.T) {
	moscow := time.FixedZone("MSK", 3*60*60)
	west := time.FixedZone("UTC-7", -7*60*60)
	cases := []struct {
		name, release string
		now           time.Time
		want          bool
	}{
		{"future", "2999-01-01", time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), false},
		{"past", "2001-06-15", time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), true},
		{"unknown", "", time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), true},
		{"invalid", "не дата", time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), true},
		{"invalid calendar date", "2026-02-30", time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), true},
		{"UTC today at midnight", "2026-10-10", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), true},
		{"Moscow today at midnight", "2026-10-10", time.Date(2026, 10, 10, 0, 0, 0, 0, moscow), true},
		{"Moscow today before 03:00", "2026-10-10", time.Date(2026, 10, 10, 1, 30, 0, 0, moscow), true},
		{"Moscow tomorrow before midnight", "2026-10-10", time.Date(2026, 10, 9, 23, 59, 59, 0, moscow), false},
		{"west tomorrow while UTC already today", "2026-10-10", time.Date(2026, 10, 9, 23, 59, 59, 0, west), false},
		{"west today at midnight", "2026-10-10", time.Date(2026, 10, 10, 0, 0, 0, 0, west), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isReleasedAt(CatalogItem{ReleaseDate: c.release}, c.now); got != c.want {
				t.Errorf("release %q at %s = %v, want %v", c.release, c.now.Format(time.RFC3339), got, c.want)
			}
		})
	}
}
