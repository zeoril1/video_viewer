package catalogapi

// Тесты фонового поиска источников (/sources). Логика «искать ли заново»
// переехала в sourcesForView и опирается на окно свежести lastDone (см.
// sources_bg.go), а не на наличие распознанной озвучки — отдельного юнит-
// теста на hasKnownAudio больше нет.

import (
	"context"
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/magnet"
)

// fakeSearch — поисковая функция для тестов: отдаёт заранее заданную выдачу
// по запросу и (если нужно) запоминает переданные лимиты.
func fakeSearch(res map[string][]magnet.Result, limits *[]int) func(context.Context, string, int) ([]magnet.Result, error) {
	return func(_ context.Context, q string, limit int) ([]magnet.Result, error) {
		if limits != nil {
			*limits = append(*limits, limit)
		}
		return res[q], nil
	}
}

// found — результат поиска на трекере (магнет строится из info hash).
func found(title string, seeds int, hash string) magnet.Result {
	return magnet.Result{Title: title, Size: "10.0 GB", Seeds: seeds, Magnet: "magnet:?xt=urn:btih:" + hash}
}

// hasTitle — есть ли среди вариантов раздача с такой частью заголовка.
func hasTitle(items []sourceItem, part string) bool {
	for _, it := range items {
		if strings.Contains(it.Title, part) {
			return true
		}
	}
	return false
}

// TestRunQueriesKeepsZeroSeedVoicesForSeries — у сериала сохраняем раздачу
// без сидов, если в заголовке названа студия озвучки: для 2 сезона
// «Хорошего доктора» все переводы, кроме LostFilm (TVShows, Novamedia,
// Jaskier, Gears Media), были именно с 0 сидов — без них в интерфейсе
// остаётся один перевод. Также сохраняем «мёртвые» раздачи, явно называющие
// сезон («[S2]», «S2E41-50»): старые сезоны длинных сериалов часто лежат
// только без сидов, и без них в списке сезонов появляются дыры (2–6 сезоны
// «Реальных пацанов»). Мусор без студии и без сезона (чужой фильм из
// широкого запроса) по-прежнему отбрасывается.
func TestRunQueriesKeepsZeroSeedVoicesForSeries(t *testing.T) {
	const q = "Сериал / Serial 2 сезон"
	res := map[string][]magnet.Result{q: {
		found("Сериал / Serial / S2E1-8 of 8 (2020) WEB-DL 1080p  TVShows", 0, "1111111111111111111111111111111111111111"),
		found("Сериал / Serial / S2E1-8 of 8 (2020) WEB-DL  LostFilm", 5, "2222222222222222222222222222222222222222"),
		found("Сериал / Serial [S2] (2020) WEB-DL 720p | Gears Media", 0, "4444444444444444444444444444444444444444"),
		found("Сериал / Serial [S02E1-8 of 8] (2020) HDTV", 0, "5555555555555555555555555555555555555555"),
		found("Сериал / Docteur? (2019) BDRip 1080p-селезень-iTunes", 0, "3333333333333333333333333333333333333333"),
	}}

	items := runQueries(context.Background(), fakeSearch(res, nil), "tt1",
		[]sourceQuery{{q: q, seasonHint: 2}}, "test", true)

	if len(items) != 4 {
		t.Fatalf("ожидали 4 варианта (3 «мёртвых» с сезоном/студией + 1 живой), got %d: %+v", len(items), items)
	}
	for _, want := range []string{"TVShows", "LostFilm", "Gears Media", "HDTV"} {
		if !hasTitle(items, want) {
			t.Errorf("потерян вариант %q: %+v", want, items)
		}
	}
	if hasTitle(items, "iTunes") {
		t.Errorf("мусор без студии и без сезона не должен попадать в источники: %+v", items)
	}
	// У «мёртвой» раздачи сезон проставляется подсказкой запроса.
	for _, it := range items {
		if it.Season != 2 {
			t.Errorf("сезон %d у %q, ожидали 2", it.Season, it.Title)
		}
	}
}

// TestRunQueriesDropsZeroSeedForMovie — у фильма переводы определяются по
// звуковым дорожкам файла, поэтому раздачи без сидов не нужны (мёртвую
// раздачу не посмотреть) — отбрасываем их как раньше.
func TestRunQueriesDropsZeroSeedForMovie(t *testing.T) {
	const q = "Фильм / Movie 2020"
	res := map[string][]magnet.Result{q: {
		found("Фильм / Movie (2020) WEB-DL 1080p | D, P, A", 0, "1111111111111111111111111111111111111111"),
		found("Фильм / Movie (2020) WEB-DL 1080p LostFilm", 7, "2222222222222222222222222222222222222222"),
	}}

	items := runQueries(context.Background(), fakeSearch(res, nil), "tt2",
		[]sourceQuery{{q: q}}, "test", false)

	if len(items) != 1 || !hasTitle(items, "LostFilm") {
		t.Fatalf("для фильма должна остаться только раздача с сидами, got %+v", items)
	}
}

// TestRunQueriesLimits — сериал ищется шире фильма: у сезона бывает много
// раздач с разными студиями, а ОБЩИЙ запрос по названию должен вмещать все
// сезоны целиком (RuTracker сортирует выдачу по дате, и с лимитом 30 в неё
// попадали только свежие сезоны — «Реальные пацаны» теряли 1–8).
func TestRunQueriesLimits(t *testing.T) {
	var series []int
	runQueries(context.Background(), fakeSearch(nil, &series), "tt1", []sourceQuery{
		{q: "A 2017", seasonHint: 1},
		{q: "A 2 сезон", seasonHint: 2},
		{q: "A"}, // общий запрос (все сезоны)
	}, "test", true)
	if want := []int{20, 20, seriesAllSeasonsLimit}; !equalInts(series, want) {
		t.Errorf("лимиты для сериала: %v, ожидали %v", series, want)
	}

	var movie []int
	runQueries(context.Background(), fakeSearch(nil, &movie), "tt2",
		[]sourceQuery{{q: "F 2020"}}, "test", false)
	if want := []int{12}; !equalInts(movie, want) {
		t.Errorf("лимиты для фильма: %v, ожидали %v", movie, want)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTrackerTitleSingleName — запрос к трекеру строится из ОДНОГО названия.
// Раньше подставлялись оба («Русское / English»), и трекеры, которые ищут все
// слова запроса (RuTracker, MegaPeer, NoNaMe-club), отдавали ПУСТО: раздачи
// там названы по-русски («Реальные пацаны [S11] (2019) WEBRip»), без
// транслита. В источниках оставались только раздачи rutor (поиск нестрогий) —
// и в интерфейсе были лишь свежие сезоны из его выдачи.
func TestTrackerTitleSingleName(t *testing.T) {
	series := db.Film{
		IMDBID: "tt1837341", Title: "Realnye patsany", TitleRU: "Реальные пацаны",
		Kind: "tvSeries", Year: 2010, Seasons: 10,
	}
	queries := sourceQueries(series)
	if len(queries) != 1 {
		t.Fatalf("для сериала ожидали один общий запрос по названию, got %d: %+v", len(queries), queries)
	}
	if queries[0].q != "Реальные пацаны" {
		t.Errorf("запрос %q, ожидали «Реальные пацаны»", queries[0].q)
	}
	if queries[0].seasonHint != 0 {
		t.Errorf("общий запрос не должен навязывать сезон: %+v", queries[0])
	}
	if alt := trackerTitleAlt(series); alt != "Realnye patsany" {
		t.Errorf("запасное название %q, ожидали «Realnye patsany»", alt)
	}

	// Фильм — «русское название + год» (одно название).
	movie := db.Film{Title: "Whiplash", TitleRU: "Одержимость", Year: 2014}
	mq := sourceQueries(movie)
	if len(mq) != 1 || mq[0].q != "Одержимость 2014" {
		t.Errorf("запрос фильма: %+v", mq)
	}
	// Без русского названия берётся исходное, без «/».
	en := db.Film{Title: "Slow Horses", Year: 2022}
	if eq := sourceQueries(en); len(eq) != 1 || eq[0].q != "Slow Horses 2022" {
		t.Errorf("запрос без перевода: %+v", eq)
	}
}
