package streamapi

import "testing"

func TestEpisodeOf(t *testing.T) {
	cases := []struct {
		name       string
		season, ep int
	}{
		{"Avatar S01E01.mkv", 1, 1},
		{"Show.S02E12.1080p.mkv", 2, 12},
		{"Сериал - сезон 3 серия 05.mkv", 3, 5},
		{"Show 2024 - 07.mkv", 0, 7},
		{"file_without_episode.mkv", 0, 0},
		{"Show S1E1 (1).mkv", 1, 1},
		{"Show Season 2 Episode 9.mkv", 2, 9},
		{"Supernatural/Season 01/01. Пилот.mkv", 1, 1},
		{"Supernatural/Сезон 01/13. Шоссе 666.mkv", 1, 13},
		{"Supernatural/02 сезон/21. Врата ада - часть 1.mkv", 2, 21},
		{`Supernatural\S15\05. Притчи 173.mkv`, 15, 5},
		{"Supernatural/Season 01/Show.S02E03.mkv", 2, 3},
		{"01. Пилот.mkv", 0, 1},
	}
	for _, c := range cases {
		season, ep := episodeOf(c.name)
		if season != c.season || ep != c.ep {
			t.Errorf("episodeOf(%q) = (%d, %d), want (%d, %d)", c.name, season, ep, c.season, c.ep)
		}
	}
}
