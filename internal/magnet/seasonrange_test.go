package magnet

// seasonrange_test.go — разбор диапазона сезонов в заголовке раздачи (нужен
// для сборников «S1-14»); диапазоны ЭПИЗОДОВ («S1E1-18») сезонами быть не должны.

import "testing"

func TestSeasonRange(t *testing.T) {
	cases := []struct {
		title    string
		from, to int
	}{
		// реальные заголовки «Реальных пацанов»
		{"Реальные пацаны / S1-14E1-291 of 291 + Фильм о сериале (2010-2023) WEBRip 1080p", 1, 14},
		{"Реальные пацаны / S1-10E1-214 of 214 (2010-2018) WEB-DL", 1, 10},
		{"Реальные пацаны [S01-02x01-41] (2010) DVDRip-FoC", 1, 2},
		{"Реальные пацаны / Блю Маунтин Стэйт [S1-3] (2010-2011) WEB-DL", 1, 3},
		{"Реальные пацаны / Blue Mountain State [S01-03] (2010-11) BDRip 720p", 1, 3},
		{"Сезоны 1-5 (2010) WEB-DL", 0, 0}, // «сезоны N-M» пока не разбираем
		// НЕ диапазон сезонов: диапазон эпизодов одного сезона
		{"Хороший доктор [S2] (2018) WEB-DL 1080p-LostFilm", 0, 0},
		{"Хороший доктор [S01] (2017) S1E1-18 of 18 LostFilm", 0, 0},
		{"Реальные пацаны [S14E1-17 of 17] (2023) WEBRip 720p-Files-x", 0, 0},
		// диапазон сезонов «с только до» без первого номера не считаем
		{"Фильм (2010-2023) WEBRip", 0, 0},
	}
	for _, c := range cases {
		from, to := SeasonRange(c.title)
		if from != c.from || to != c.to {
			t.Errorf("SeasonRange(%q) = (%d,%d), want (%d,%d)", c.title, from, to, c.from, c.to)
		}
	}
}

// TestTitleYearAndSeason — год и одиночный сезон из заголовка: нужны, чтобы
// привести сезоны трекера к сезонам TMDB по году (трекерный «S14 (2023)» — TMDB-сезон 10).
func TestTitleYearAndSeason(t *testing.T) {
	cases := []struct {
		title  string
		year   int
		season int
	}{
		{"Реальные пацаны [S14] (2023) WEBRip 1080p", 2023, 14},
		{"Реальные пацаны [S9E1-20 of 20] (2017) HDTV", 2017, 9},
		{"Реальные пацаны / S1-14E1-291 of 291 + Фильм о сериале (2010-2023) WEBRip", 2010, 0},
		{"Реальные пацаны [S01-02x01-41] (2010) DVDRip-FoC", 2010, 0},
		{"Фильм (2020) BDRip", 2020, 0},
		{"Без года", 0, 0},
	}
	for _, c := range cases {
		if y := TitleYear(c.title); y != c.year {
			t.Errorf("TitleYear(%q) = %d, ожидали %d", c.title, y, c.year)
		}
		if s := TitleSeason(c.title); s != c.season {
			t.Errorf("TitleSeason(%q) = %d, ожидали %d", c.title, s, c.season)
		}
	}
}

// TestParseTitleStillGivesZeroForCollections — сборник остаётся сезоном 0
// (подсказка искомого сезона не должна его переписывать).
func TestParseTitleStillGivesZeroForCollections(t *testing.T) {
	_, _, season := ParseTitle("Реальные пацаны / S1-14E1-291 of 291 (2010-2023) WEBRip 1080p")
	if season != 0 {
		t.Fatalf("сборник должен остаться сезоном 0, получено %d", season)
	}
	if !IsFullCollection("Реальные пацаны / S1-14E1-291 of 291 (2010-2023) WEBRip 1080p") {
		t.Fatal("сборник не распознан как полный")
	}
	if _, _, s := ParseTitle("Реальные пацаны [S9E1-20 of 20] (2017) HDTV"); s != 9 {
		t.Fatalf("сезон 9 не распознан: %d", s)
	}
}
