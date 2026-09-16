package streamapi

// seasons_test.go — раскладка по сезонам TMDB: трекерный S14 (2023) = TMDB-сезон 10,
// а сборник со сквозной нумерацией раньше целиком уезжал в сезон 1.

import (
	"testing"

	"github.com/zeoril1/video_viewer/internal/tmdb"
)

// realPatsanySeasons — структура сезонов TMDB для tt1837341.
func realPatsanySeasons() []tmdb.SeasonInfo {
	return []tmdb.SeasonInfo{
		{Number: 1, Episodes: 50, Year: 2010},
		{Number: 2, Episodes: 40, Year: 2011},
		{Number: 3, Episodes: 32, Year: 2013},
		{Number: 4, Episodes: 40, Year: 2014},
		{Number: 5, Episodes: 36, Year: 2016},
		{Number: 6, Episodes: 16, Year: 2018},
		{Number: 7, Episodes: 20, Year: 2019},
		{Number: 8, Episodes: 20, Year: 2020},
		{Number: 9, Episodes: 20, Year: 2022},
		{Number: 10, Episodes: 17, Year: 2023},
	}
}

func fileWith(name string, season, episode int) torrentFile {
	return torrentFile{Name: name, Season: season, Episode: episode}
}

// TestApplySeasonMappingCollection — сборник со сквозной нумерацией раскладывается по сезонам TMDB, а не в сезон 1.
func TestApplySeasonMappingCollection(t *testing.T) {
	files := []torrentFile{
		fileWith("Realnye.pacany.001.serya.WEB-DL.(1080p).mkv", 0, 0),
		fileWith("Realnye.pacany.002.serya.WEB-DL.(1080p).mkv", 0, 0),
		fileWith("Realnye.pacany.051.serya.WEB-DL.(1080p).mkv", 0, 0),
		fileWith("Realnye.pacany.215.serya.WEB-DL.(1080p).mkv", 0, 0),
		fileWith("Realnye.pacany.291.serya.WEB-DL.(1080p).mkv", 0, 0),
	}
	got := applySeasonMapping(files, "Реальные пацаны / S1-14E1-291 of 291 (2010-2023) WEBRip 1080p", realPatsanySeasons())
	want := []struct{ season, episode int }{{1, 1}, {1, 2}, {2, 1}, {7, 1}, {10, 17}}
	for i, w := range want {
		if got[i].Season != w.season || got[i].Episode != w.episode {
			t.Errorf("файл %d: S%dE%d, ожидали S%dE%d", i, got[i].Season, got[i].Episode, w.season, w.episode)
		}
	}
}

// TestApplySeasonMappingTrackerSeason — сезон берём по году (трекерный «S14 (2023)» → TMDB-сезон 10), серии — из имён.
func TestApplySeasonMappingTrackerSeason(t *testing.T) {
	files := []torrentFile{
		fileWith("Realnye.pacany.S14.E01.2023.WEB-DL.1080p.mkv", 14, 1),
		fileWith("Realnye.pacany.S14.E17.2023.WEB-DL.1080p.mkv", 14, 17),
	}
	got := applySeasonMapping(files, "Реальные пацаны [S14] (2023) WEBRip 1080p", realPatsanySeasons())
	for i, f := range got {
		if f.Season != 10 {
			t.Errorf("файл %d: сезон %d, ожидали 10 (TMDB «Прощальный сезон», 2023)", i, f.Season)
		}
	}
	if got[0].Episode != 1 || got[1].Episode != 17 {
		t.Errorf("серии должны остаться из имён файлов: %+v", got)
	}
}

// TestApplySeasonMappingMultiSeasonPack — пак трекерных сезонов 1-2 — начало сериала, т.е. TMDB-сезон 1.
func TestApplySeasonMappingMultiSeasonPack(t *testing.T) {
	files := make([]torrentFile, 0, 3)
	files = append(files, fileWith("realnye.patsany.s01e01.rus.dvdrip.avi", 1, 1))
	files = append(files, fileWith("realnye.patsany.s01e20.rus.dvdrip.avi", 1, 20))
	files = append(files, fileWith("realnye.patsany.s02e08.rus.dvdrip.avi", 2, 8))
	got := applySeasonMapping(files, "Реальные пацаны [S01-02x01-41] (2010) DVDRip-FoC", realPatsanySeasons())
	for i, f := range got {
		if f.Season != 1 {
			t.Errorf("файл %d: сезон %d, ожидали 1", i, f.Season)
		}
		if f.Episode != i+1 {
			t.Errorf("файл %d: серия %d, ожидали %d (сквозная нумерация пакета)", i, f.Episode, i+1)
		}
	}
}

// TestApplySeasonMappingWithoutStructure — без структуры TMDB поведение прежнее (сезон из имени, иначе 1).
func TestApplySeasonMappingWithoutStructure(t *testing.T) {
	files := []torrentFile{
		fileWith("Show.S02E05.mkv", 2, 5),
		fileWith("Show.S02E06.mkv", 2, 6),
	}
	got := applySeasonMapping(files, "Show [S2] (2020)", nil)
	if got[0].Season != 2 || got[0].Episode != 5 {
		t.Fatalf("сезон/серия из имени файла потеряны: %+v", got[0])
	}
}

// TestEpisodeOfSeparators — разделитель бывает любой («S14.E01», «S02 E10»), иначе файлы остаются без сезона.
func TestEpisodeOfSeparators(t *testing.T) {
	cases := []struct {
		name         string
		season, epis int
	}{
		{"Realnye.pacany.S14.E01.2023.mkv", 14, 1},
		{"Show.S02 E10.avi", 2, 10},
		{"show.s3-e2.mkv", 3, 2},
		{"show.s01e05.mkv", 1, 5},
		{"Show Season 2 Episode 9.mkv", 2, 9},
	}
	for _, c := range cases {
		s, e := episodeOf(c.name)
		if s != c.season || e != c.epis {
			t.Errorf("episodeOf(%q) = (%d,%d), ожидали (%d,%d)", c.name, s, e, c.season, c.epis)
		}
	}
}
