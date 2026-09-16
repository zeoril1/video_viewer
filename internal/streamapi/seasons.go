package streamapi

// seasons.go — раскладка файлов раздачи по сезонам TMDB. У трекеров своя нарезка
// сезонов (трекерный S14 (2023) = TMDB-сезон 10), а полные сборники нумеруют серии
// СКВОЗНЯКОМ («001 serya»), из-за чего раньше все 293 файла уезжали в сезон 1.
// Правила: сквозной номер в имени → по структуре TMDB; пак с диапазоном сезонов →
// файлы подряд по структуре; обычная раздача сезона → сезон по году, серии из имён.
// Без структуры TMDB всё по-старому: сезон из имени файла, иначе сезон 1.

import (
	"regexp"

	"github.com/zeoril1/video_viewer/internal/magnet"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

// fileAbsRe — сквозной номер серии в имени файла: «001 seriya», «(01 серия)», «12 serya».
var fileAbsRe = regexp.MustCompile(`(?i)(\d{1,4})\s*[.)]?\s*(?:seriy|sery|серия|серии|серий|seriya)`)

// applySeasonMapping раскладывает файлы раздачи по сезонам TMDB (files должны быть
// отсортированы по Index; structure может быть nil — тогда только разбор по именам).
func applySeasonMapping(files []torrentFile, relTitle string, structure []tmdb.SeasonInfo) []torrentFile {
	if len(files) == 0 {
		return files
	}
	// Без структуры TMDB раскладывать не по чему — оставляем разбор по именам.
	if len(structure) == 0 {
		return normalizeEpisodes(files)
	}

	rangeFrom, _ := magnet.SeasonRange(relTitle)
	trackerSeason := magnet.TitleSeason(relTitle)
	multiSeason := filesHaveManySeasons(files)

	// 1. Сквозной номер прямо в имени файла — не зависит от порядка и полноты файлов.
	abs := true
	for i := range files {
		n := fileAbsNumber(files[i].Name)
		if n <= 0 {
			abs = false
			break
		}
		season, episode := tmdb.SeasonAt(structure, n)
		if season <= 0 {
			// Номер за пределами структуры (бонусные фильмы сборника) — оставляем файл
			// без сезона: в сетке серий он не появится, но чужие серии не испортит.
			files[i].Season, files[i].Episode = 0, 0
			continue
		}
		files[i].Season, files[i].Episode = season, episode
	}
	if abs {
		return files
	}

	// 2. Сборник/многозонный пак: файлы идут подряд.
	if rangeFrom > 0 || (trackerSeason == 0 && multiSeason) {
		start := 1
		if rangeFrom > 1 {
			// Пак начинается не с первого сезона — считаем от его начала.
			start = episodesBefore(structure, rangeFrom) + 1
		}
		for i := range files {
			abs := start + i
			season, episode := tmdb.SeasonAt(structure, abs)
			if season <= 0 {
				// Файлов больше, чем серий в структуре — оставляем как есть.
				break
			}
			files[i].Season, files[i].Episode = season, episode
		}
		return files
	}

	// 3. Обычная раздача одного сезона: сезон — из TMDB по году, серии — из имён.
	season := tmdb.SeasonByYear(structure, magnet.TitleYear(relTitle))
	if season <= 0 {
		season = trackerSeason
	}
	if season <= 0 {
		return normalizeEpisodes(files)
	}
	for i := range files {
		files[i].Season = season
	}
	return normalizeEpisodes(files)
}

// filesHaveManySeasons — есть ли в раздаче файлы разных сезонов (многозонный пак).
func filesHaveManySeasons(files []torrentFile) bool {
	seen := 0
	for _, f := range files {
		if f.Season > 0 && f.Season != seen {
			if seen != 0 {
				return true
			}
			seen = f.Season
		}
	}
	return false
}

// episodesBefore — суммарное число серий в сезонах до season (структура TMDB).
func episodesBefore(structure []tmdb.SeasonInfo, season int) int {
	sum := 0
	for _, s := range structure {
		if s.Number < season {
			sum += s.Episodes
		}
	}
	return sum
}

// fileAbsNumber — сквозной номер серии из имени файла (0 — не найден).
func fileAbsNumber(name string) int {
	m := fileAbsRe.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	return atoiOr(m[1], 0)
}

// normalizeEpisodes нумерует серии по порядку внутри сезона там, где номер не
// определён, и проставляет сезон 1 файлам без сезона (прежнее поведение).
func normalizeEpisodes(files []torrentFile) []torrentFile {
	hasExplicitSeason := false
	for i := range files {
		if files[i].Season > 0 {
			hasExplicitSeason = true
			break
		}
	}
	if !hasExplicitSeason {
		for i := range files {
			files[i].Season = 1
		}
	}
	next := map[int]int{}
	for i := range files {
		if files[i].Season <= 0 {
			files[i].Season = 1
		}
		s := files[i].Season
		if next[s] == 0 {
			// Начинаем с максимума уже известных серий этого сезона.
			max := 0
			for _, f := range files {
				if f.Season == s && f.Episode > max {
					max = f.Episode
				}
			}
			next[s] = max
		}
		if files[i].Episode <= 0 {
			next[s]++
			files[i].Episode = next[s]
		}
	}
	return files
}
