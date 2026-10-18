package catalogapi

import (
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
)

func TestMovieSourcesMatchSpecificFilm(t *testing.T) {
	film := db.Film{IMDBID: "ttResidentEvil", TitleRU: "Обитель зла", Title: "Resident Evil", Year: 2026, Kind: "movie"}
	m := newFilmTitleMatcher(film)
	tests := []struct {
		title string
		want  bool
	}{
		{"Обитель зла / Resident Evil (2026) WEB-DL 1080p", true},
		{"Resident.Evil.2026.1080p.WEB-DL", true},
		{"Обитель зла [2026, ужасы, WEB-DL]", true},
		{"Resident Evil WEB-DL 1080p", true},
		{"Обитель зла: Раккун-Сити / Resident Evil: Welcome to Raccoon City (2021) HDRip", false},
		{"Синистер. Обитель зла / Tenement (2024) WEB-DL", false},
		{"Обитель зла: Остров смерти / Resident Evil: Death Island (2023) BDRip", false},
		{"Обитель зла / Resident Evil [S1] (2022) WEBRip", false},
		{"Обитель зла: Мутация / The Cure (2026) WEBRip", false},
		{"Обитель зла: Бесконечная тьма / E01-E04 Resident Evil: Infinite Darkness", false},
		{"Resident Evil (2002) BDRip", false},
		{"Resident Evil: Death Island (2026) BDRip", false},
		{"Обитель зла / Resident Evil: Death Island (2026) BDRip", false},
		{"Resident Evil 2 (2026) BDRip", false},
		{"Обитель страха (2026) WEB-DL", false},
		{"Resident Evilness (2026) WEB-DL", false},
		{"Resident Evil [S1-3] (2026) WEB-DL", false},
		{"Resident Evil / E01-E04 (2026) WEB-DL", false},
		{"Обитель зла / Alternate title (2002) BDRip", false},
	}
	var cached []db.Source
	wantCount := 0
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			if got := m.matches(tt.title); got != tt.want {
				t.Errorf("matches = %v, want %v", got, tt.want)
			}
		})
		cached = append(cached, db.Source{Title: tt.title, Magnet: "magnet:?xt=test"})
		if tt.want {
			wantCount++
		}
	}
	if items := sourceItemsFromDB(cached, film); len(items) != wantCount {
		t.Errorf("cached sources = %d, want %d", len(items), wantCount)
	}
}

func TestMovieTitleFormatting(t *testing.T) {
	for _, tt := range []struct {
		film  db.Film
		title string
	}{
		{db.Film{Title: "1917", Year: 2019}, "1917 (2019) BDRip"},
		{db.Film{Title: "2012", Year: 2009}, "2012.2009.1080p.BluRay"},
		{db.Film{Title: "Blade Runner 2049", Year: 2017}, "Blade.Runner.2049.2017.1080p.BluRay"},
		{db.Film{TitleRU: "Ёлки", Year: 2010}, "Елки (2010) BDRip"},
		{db.Film{Title: "Spider-Man", Year: 2002}, "Spider.Man.2002.1080p.BluRay"},
		{db.Film{Title: "Resident Evil: Death Island", Year: 2023}, "Resident.Evil.Death.Island.2023.BDRip"},
		{db.Film{Title: "Example", Kind: "tvSeries", Year: 2010}, "Example [S12] (2026) WEB-DL"},
	} {
		if !newFilmTitleMatcher(tt.film).matches(tt.title) {
			t.Errorf("valid release rejected: %s", tt.title)
		}
	}
}

// TestTitleMatcherForeignNames — отсев раздач-однофамильцев.
//
// Русское название «Реальные пацаны» — это ещё и перевод Blue Mountain State,
// и трекеры отдают его раздачи по тому же запросу. Сериал чужой, причём с
// сидами, и для фронтенда (сезон 0 = «полный сборник» для любого сезона) он
// попадал бы в источники всех сезонов.
func TestTitleMatcherForeignNames(t *testing.T) {
	m := newTitleMatcher([]string{"Реальные пацаны", "Realnye patsany"})

	own := []string{
		"Реальные пацаны [S11] (2019) WEBRip",
		"Реальные Пацаны / S11E1-20 of 20 (Жанна Кадникова) [2019 г., СитКом, WEB-DL 720p]",
		"Реальные пацаны / S1-14E1-291 of 291 + Фильм о сериале (2010-2023) WEBRip 1080p",
		"Реальные пацаны / (S1-3E1-71) (Жанна Кадникова) [2010-2011 г., Комедия, DVDRip]",
		"Реальные Пацаны / S4E1-20 of 20 (2012 г., СитКом, HDTV)",
		"Реальные пацаны (2022) WEBRip [H.264/1080p] (S14E1-20 of 20 + Фильм о сериале)",
		"Реальные пацаны. Возвращение [S06] (2014) WEB-DL 720p-MediaClub",
		"Realnye.Patsany.001-291.serya.WEBRip.1080p",
		"Реальные пацаны против зомби (2020) WEBRip 1080p",
	}
	for _, title := range own {
		if !m.matches(title) {
			t.Errorf("своя раздача отброшена: %q", title)
		}
	}

	foreign := []string{
		"Реальные пацаны / Блю Маунтин Стэйт / Blue Mountain State [S1-3] (2010-2011) WEB-DL | Arkadiy, SkyeFilmTV, MTV",
		"Блю Маунтин Стэйт (Реальные пацаны) / Blue Mountain State (S2 полный) (2010) WEB-DL [SkyeFilm]",
		"Блю Маунтин Стэйт (Реальные пацаны) / Blue Mountain State  (S1 полный) (2010) HDRip",
	}
	for _, title := range foreign {
		if m.matches(title) {
			t.Errorf("чужая раздача оставлена: %q", title)
		}
	}

	// Общая основа «реал» не подтверждает, что другое название — псевдоним.
	if m.matches("Реальные пацаны / Реальная семья [S7] (2015) HDTV") {
		t.Error("раздача с чужим названием и общей основой слова оставлена")
	}
}

// TestTitleMatcherLatinAliases — латинские названия-псевдонимы и названия на
// другом языке в заголовках не считаются «чужим фильмом»: у зарубежных
// сериалов там бывают варианты названий, которых у нас нет. Отбрасывать
// из-за них раздачу нельзя.
func TestTitleMatcherLatinAliases(t *testing.T) {
	slow := newTitleMatcher([]string{"Медленные лошади", "Slow Horses"})
	for _, title := range []string{
		"Медленные лошади / Slow Horses / Se1 (2022) WEB-DL 1080p",
		"Slow Horses [S01] (2022) WEB-DL",
		"Медленные лошади / Slow Horses / Сезон 1 / S1E1-6 of 6 (2022) HDTV",
	} {
		if !slow.matches(title) {
			t.Errorf("своя раздача отброшена: %q", title)
		}
	}
	// Латиница сама по себе не повод отбросить: наш фильм ищется по переводу,
	// а второй сегмент — неизвестный нам английский вариант.
	m := newTitleMatcher([]string{"Одержимость", "Whiplash"})
	if !m.matches("Одержимость / Whiplash (2014) BDRip 1080p | D, P, A") {
		t.Error("раздача с нашим латинским названием отброшена")
	}
	if !m.matches("Одержимость / Освобождение (2014) BDRip") {
		t.Error("единственный незнакомый сегмент не должен отбрасывать раздачу")
	}
}

// TestTitleMatcherVariants — полный известный псевдоним допускает сокращение
// другого варианта названия в соседнем сегменте.
func TestTitleMatcherVariants(t *testing.T) {
	// «Клинок, рассекающий демонов» — российский вариант названия того же
	// сериала, что и «Истребитель демонов: Бесконечный замок».
	m := newTitleMatcher([]string{"Истребитель демонов: Бесконечный замок", "Клинок, рассекающий демонов: Бесконечный замок"})
	if !m.matches("Истребитель демонов / Клинок, рассекающий демонов: Бесконечный замок (2025) BDRip 1080p") {
		t.Error("раздача с вариантом названия отброшена")
	}
	// Полное известное название само по себе тоже достаточно.
	if !m.matches("Истребитель демонов: Бесконечный замок (2025) BDRip") {
		t.Error("раздача отброшена")
	}
}

func TestSeriesSourcesRequireSpecificSeries(t *testing.T) {
	film := db.Film{IMDBID: "tt35854736", TitleRU: "Аватар: Семь убежищ", Title: "Avatar: Seven Havens", Year: 2026, Kind: "tvSeries"}
	m := newFilmTitleMatcher(film)
	tests := []struct {
		title string
		want  bool
	}{
		{"Аватар: Семь убежищ / Avatar: Seven Havens [S01] (2026) WEB-DL 1080p", true},
		{"АВАТАР. СЕМЬ-УБЕЖИЩ [S02] (2027) WEBRip 1080p", true},
		{"Avatar.Seven.Havens.S01E01.1080p.WEB-DL", true},
		{"E01-E12 Avatar - Seven Havens [WEBRip 720p][AVC]", true},
		{"Аватар / Avatar: Seven Havens [S01] WEB-DL 1080p", true},
		{"Аватар короля 2 / E01-E12 Quan Zhi Gao Shou 2 - AniLiberty.TOP [WEBRip 1080p][AVC][1-12]", false},
		{"Аватар короля 3 / E01-E17 Quanzhi Gaoshou 3 - AniLiberty.TOP [WEBRip 1080p][AVC][1-17]", false},
		{"Аватар короля 3 / E01-E17 Quanzhi Gaoshou 3 - AniLiberty.TOP [WEBRip 1080p][HEVC][1-17]", false},
		{"Аватар короля / E01-E12 Quan Zhi Gao Shou - AniLiberty.TOP [WEBRip 720p][AVC][1-12]", false},
		{"Аватар короля: Вершина Славы / Quan Zhi Gao Shou- Zhi Dianfeng Rongyao - AniLiberty.TOP [WEBRip 720p][AVC][Фильм]", false},
		{"Аватар короля OVA / E01-E03 Quan Zhi Gao Shou OVA - AniLiberty.TOP [WEBRip 720p][AVC][1-3]", false},
		{"Аватар: Легенда об Аанге / Avatar: The Last Airbender [S01-03] BDRip", false},
		{"Аватар: Легенда о Корре / The Legend of Korra [S01-04] WEB-DL", false},
		{"Аватар [S01] (2026) WEB-DL", false},
		{"Аватар: Семь королей [S01] WEBRip", false},
		{"Путешествие по семи убежищам [S01] WEBRip", false},
		{"Avatar Seven Havensness [S01] WEB-DL", false},
		{"MyAvatar Seven Havens [S01] WEB-DL", false},
		{"Avatar / Seven Havens [S01] WEB-DL", false},
		{"Аватар: Семь убежищ / Аватар короля [S01] WEB-DL", false},
	}
	var cached []db.Source
	wantCount := 0
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			if got := m.matches(tt.title); got != tt.want {
				t.Errorf("matches = %v, want %v", got, tt.want)
			}
		})
		cached = append(cached, db.Source{Title: tt.title, Magnet: "magnet:?xt=test"})
		if tt.want {
			wantCount++
		}
	}
	if items := sourceItemsFromDB(cached, film); len(items) != wantCount {
		t.Errorf("cached sources = %d, want %d", len(items), wantCount)
	}
}

func TestSeriesTitleWordsAndAliases(t *testing.T) {
	for _, tt := range []struct {
		name  string
		title string
		want  bool
	}{
		{"Чёрное зеркало", "Черное.Зеркало [S07] (2025) WEB-DL", true},
		{"Сверхъестественное", "Сверхъестественное [S01-15] BDRip", true},
		{"Сверхъестественное", "Сверхъестественный отель [S01] WEB-DL", false},
		{"Avatar", "Avatars [S01] WEB-DL", false},
		{"Dark", "Darkness [S01] WEB-DL", false},
		{"Медленные лошади", "Медленная жизнь [S01] WEB-DL", false},
		{"Realnye patsany", "Realnye.Patsany.001-291.serya.WEBRip.1080p", true},
	} {
		t.Run(tt.title, func(t *testing.T) {
			if got := newTitleMatcher([]string{tt.name}).matches(tt.title); got != tt.want {
				t.Errorf("matches = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestTitleMatcherNoTitles — без названий (например, у записи нет ни
// русской, ни оригинальной локализации) фильтр ничего не отбрасывает.
func TestTitleMatcherNoTitles(t *testing.T) {
	m := newTitleMatcher(nil)
	if !m.matches("Что угодно / Anything") {
		t.Error("пустой сопоставитель должен пропускать всё")
	}
}
