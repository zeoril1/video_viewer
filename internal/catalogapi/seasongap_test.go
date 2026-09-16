package catalogapi

// seasongap_test.go — добор сезонов сериала: трекеры держат каждый сезон
// отдельной раздачей, поэтому общий запрос по названию находит лишь часть.
// У «Реальных пацанов» первый проход дал 1, 9, 12 и 14 сезоны из 14, а
// 2–8, 10, 11 и 13 нашлись только по отдельным запросам «N сезон».

import (
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
)

// realPatsany — реальная выдача по «Реальным пацанам» после первого прохода:
// три сезона, найденных общим запросом, и сборник S1-14 (он даёт верхнюю
// границу сезонов).
func realPatsany() []sourceItem {
	return []sourceItem{
		{Season: 1, Title: "Реальные пацаны / Блю Маунтин Стэйт [S01] (2010) WebRip"},
		{Season: 9, Title: "Реальные пацаны [S9E1-20 of 20] (2017) HDTV"},
		{Season: 12, Title: "Реальные пацаны [S12] (2020) HDTV 1080i"},
		{Season: 14, Title: "Реальные пацаны [S14] (2023) WEBRip 720p"},
		{Season: 0, Title: "Реальные пацаны / S1-14E1-291 of 291 + Фильм о сериале (2010-2023) WEBRip 1080p"},
	}
}

func TestMissingSeasonQueries(t *testing.T) {
	film := db.Film{IMDBID: "tt1837341", Title: "Реальные Пацаны", Kind: "tvSeries", Year: 2010}
	// Первый проход: запрос по году (сезон 1) и общий запрос по названию.
	used := []sourceQuery{
		{q: "Реальные Пацаны 2010", seasonHint: 1},
		{q: "Реальные Пацаны"},
	}

	got := missingSeasonQueries(film, realPatsany(), used)
	gotSeasons := map[int]bool{}
	for _, q := range got {
		gotSeasons[q.seasonHint] = true
		if q.q == "" {
			t.Errorf("пустой запрос для сезона %d", q.seasonHint)
		}
	}
	// Сезоны, которых нет в выдаче, должны быть запрошены.
	for _, s := range []int{2, 3, 4, 5, 6, 7, 8, 10, 11, 13} {
		if !gotSeasons[s] {
			t.Errorf("сезон %d не добрался (запросов: %v)", s, gotSeasons)
		}
	}
	// Уже найденные и уже запрошенные сезоны не спрашиваем повторно.
	for _, s := range []int{1, 9, 12, 14} {
		if gotSeasons[s] {
			t.Errorf("сезон %d запрошен повторно", s)
		}
	}
	// Верхняя граница берётся из сборника S1-14 (TMDB знает только 10 сезонов).
	if gotSeasons[13] == false {
		t.Error("сезоны выше числа сезонов TMDB (13) должны добираться по сборнику")
	}
	// Первый сезон ищется по году сериала, остальные — «N сезон».
	for _, q := range got {
		if q.seasonHint == 2 && !strings.HasSuffix(q.q, "2 сезон") {
			t.Errorf("запрос второго сезона: %q", q.q)
		}
	}
}

func TestMissingSeasonQueriesUnknownSeasons(t *testing.T) {
	// Числа сезонов нет ни от Wikidata, ни от TMDB, выдача пустая —
	// проверяем первые seasonProbeLimit сезонов, а не только общий запрос.
	film := db.Film{IMDBID: "tt1", Title: "Сериал", Kind: "tvSeries", Year: 2015}
	got := missingSeasonQueries(film, nil, nil)
	if len(got) != seasonProbeLimit {
		t.Fatalf("запросов %d, ожидали %d", len(got), seasonProbeLimit)
	}
	if got[0].q != "Сериал 2015" || got[0].seasonHint != 1 {
		t.Errorf("первый запрос (сезон 1) должен идти по году: %+v", got[0])
	}
	if got[1].seasonHint != 2 || !strings.HasSuffix(got[1].q, "2 сезон") {
		t.Errorf("второй запрос: %+v", got[1])
	}
}

func TestMissingSeasonQueriesLimit(t *testing.T) {
	// Сборник на 60 сезонов: запросов должно быть не больше maxSeasonQueries.
	film := db.Film{IMDBID: "tt2", Title: "Долгий сериал", Kind: "tvSeries"}
	items := []sourceItem{{Season: 0, Title: "Долгий сериал [S1-60] WEB-DL"}}
	got := missingSeasonQueries(film, items, nil)
	if len(got) != maxSeasonQueries {
		t.Fatalf("запросов %d, ожидали максимум %d", len(got), maxSeasonQueries)
	}
}

func TestMergeSourceItems(t *testing.T) {
	a := []sourceItem{{Magnet: "m1", Title: "S1"}, {Magnet: "m2", Title: "S2"}}
	b := []sourceItem{{Magnet: "m2", Title: "S2 (дубль)"}, {Magnet: "m3", Title: "S3"}}
	got := mergeSourceItems(a, b)
	if len(got) != 3 {
		t.Fatalf("после объединения %d вариантов, ожидали 3: %+v", len(got), got)
	}
	if got[2].Magnet != "m3" {
		t.Errorf("новый вариант потерян: %+v", got)
	}
}
