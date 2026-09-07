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
	}
	for _, c := range cases {
		season, ep := episodeOf(c.name)
		if season != c.season || ep != c.ep {
			t.Errorf("episodeOf(%q) = (%d, %d), want (%d, %d)", c.name, season, ep, c.season, c.ep)
		}
	}
}
