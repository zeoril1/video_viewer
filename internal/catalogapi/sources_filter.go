// sources_filter.go — отсев раздач других произведений. В заголовке должно быть полное
// название из каталога; одного общего слова франшизы («Аватар») недостаточно.
// Пунктуация, регистр и ё/е не влияют на совпадение. Сезоны могут выходить в разные
// годы, поэтому год сериала, в отличие от фильма, не ограничивает раздачи.
package catalogapi

import (
	"log"
	"regexp"
	"strings"
	"unicode"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/magnet"
)

// nameStopWords — служебные слова сегментов-описаний раздачи, а не названия фильма («Сезон 5. Часть 2»).
var nameStopWords = map[string]bool{
	"сезон": true, "сезоны": true, "сезонов": true, "сезоне": true, "сезона": true,
	"серия": true, "серии": true, "серий": true, "сериям": true, "сериях": true,
	"часть": true, "части": true, "частей": true, "частями": true,
	"фильм": true, "фильма": true, "фильме": true,
	"сериал": true, "сериала": true, "сериале": true,
	"саундтрек": true, "озвучка": true, "озвучкой": true, "перевод": true,
	"субтитры": true, "полный": true, "полная": true, "полное": true,
	"полностью": true, "сборник": true, "обновляемая": true, "надпись": true,
}

// titleMatcher — проверка соответствия заголовка раздачи фильму.
type titleMatcher struct {
	norm        []string // названия целиком, в нижнем регистре
	words       [][]string
	seriesNames []*regexp.Regexp
	movie       bool
	year        int
	names       []*regexp.Regexp
}

// Movies need the complete title: a shared franchise name is not enough.
// Series require the complete title too, while allowing season subtitles and later years.
func newFilmTitleMatcher(film db.Film) *titleMatcher {
	m := newTitleMatcher(trackerTitles(film))
	m.movie = !isSeriesKind(film.Kind)
	m.year = film.Year
	for _, title := range m.norm {
		words := strings.FieldsFunc(normalizeMovieName(title), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if len(words) == 0 {
			continue
		}
		for i := range words {
			words[i] = regexp.QuoteMeta(words[i])
		}
		m.names = append(m.names, regexp.MustCompile(`^`+strings.Join(words, `[^\p{L}\p{N}]+`)+`(?:$|[^\p{L}\p{N}])`))
	}
	return m
}

func normalizeMovieName(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "ё", "е")
}

var movieMetadata = regexp.MustCompile(`(?i)^(?:[\[(]|(?:19|20)\d{2}(?:\D|$)|(?:web[ ._-]?(?:dl|rip)|bd(?:rip|remux)|blu[ ._-]?ray|dvd(?:rip|scr)|hdtv|hdrip|hqrip|hdtc|hdcam|camrip|telesync|remux|uhd|2160p|1080p|720p|480p|4k)\b)`)
var movieEpisode = regexp.MustCompile(`(?i)(?:\bS\d{1,2}(?:\b|E\d|[-x])|\bE\d{1,3}\b|\bseason\s*\d|сезон\s*\d|\d+\s*сезон)`)

func (m *titleMatcher) matchesMovie(title string) bool {
	if movieEpisode.MatchString(title) || magnet.IsFullCollection(title) {
		return false
	}
	matched := false
	for _, segment := range strings.Split(normalizeMovieName(title), "/") {
		segment = strings.TrimSpace(segment)
		prefix, exact := false, false
		for _, name := range m.names {
			loc := name.FindStringIndex(segment)
			if loc == nil {
				continue
			}
			prefix = true
			// The regexp consumes one delimiter; retain brackets for metadata.
			end := loc[1]
			if end > 0 && (segment[end-1] == '(' || segment[end-1] == '[') {
				end--
			}
			rest := strings.TrimLeft(segment[end:], " ._-:–—\t")
			if rest != "" && !movieMetadata.MatchString(rest) {
				continue
			}
			// Read the year after the title, so films named 1917 or 2012 work.
			if year := magnet.TitleYear(rest); m.year > 0 && year > 0 && year != m.year {
				return false
			}
			exact = true
		}
		if prefix && !exact {
			return false // e.g. Resident Evil: Death Island, even with an abbreviated alias
		}
		matched = matched || exact
	}
	// The release year may follow an alternate title rather than the matching one.
	segments := strings.Split(normalizeMovieName(title), "/")
	for i, segment := range segments {
		segments[i] = strings.TrimSpace(segment)
		for _, name := range m.names {
			if loc := name.FindStringIndex(segments[i]); loc != nil {
				segments[i] = segments[i][loc[1]:]
				break
			}
		}
	}
	if year := magnet.TitleYear(strings.Join(segments, "/")); m.year > 0 && year > 0 && year != m.year {
		return false
	}
	return matched
}

// newTitleMatcher готовит сопоставитель по названиям фильма; пустой пропускает всё — фильтровать не по чему.
func newTitleMatcher(titles []string) *titleMatcher {
	m := &titleMatcher{}
	for _, t := range titles {
		t = normalizeMovieName(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		m.norm = append(m.norm, t)
		words := titleWords(t)
		if len(words) == 0 {
			continue
		}
		m.words = append(m.words, words)
		escaped := make([]string, len(words))
		for i, word := range words {
			escaped[i] = regexp.QuoteMeta(word)
		}
		// Keep word boundaries: neither an arbitrary shared stem nor a title
		// embedded in a longer word identifies a series. Do not span slash aliases.
		m.seriesNames = append(m.seriesNames, regexp.MustCompile(`(?:^|[^\p{L}\p{N}])`+strings.Join(escaped, `[^\p{L}\p{N}/]+`)+`(?:$|[^\p{L}\p{N}])`))
	}
	return m
}

// filter выбрасывает раздачи, которые относятся к ДРУГОМУ фильму.
func (m *titleMatcher) filter(filmID string, items []sourceItem) []sourceItem {
	return m.filterWithLog(filmID, items, true)
}

func (m *titleMatcher) filterWithLog(filmID string, items []sourceItem, verbose bool) []sourceItem {
	if m == nil || len(m.norm) == 0 || len(items) == 0 {
		return items
	}
	out := make([]sourceItem, 0, len(items))
	for _, it := range items {
		if m.matches(it.Title) {
			out = append(out, it)
			continue
		}
		if verbose {
			log.Printf("sources: %s: отброшена чужая раздача %q", filmID, it.Title)
		}
	}
	return out
}

// matches — относится ли заголовок раздачи к нашему фильму: наше название упомянуто, чужих названий нет.
func (m *titleMatcher) matches(title string) bool {
	if m == nil || len(m.norm) == 0 {
		return true // фильтровать не по чему
	}
	if m.movie {
		return m.matchesMovie(title)
	}
	low := normalizeMovieName(title)
	if !m.mentionsTitle(low) {
		return false // нашего названия нет вообще — это не наш фильм
	}
	for _, seg := range strings.Split(low, "/") {
		if m.foreignName(seg) {
			return false
		}
	}
	return true
}

// mentionsTitle — упомянуто ли полное название с границами слов.
func (m *titleMatcher) mentionsTitle(low string) bool {
	for _, name := range m.seriesNames {
		if name.MatchString(low) {
			return true
		}
	}
	return false
}

// foreignName — похож ли сегмент заголовка на название ЧУЖОГО фильма.
func (m *titleMatcher) foreignName(seg string) bool {
	// «Whiplash (2014) BDRip» → «Whiplash»: название стоит до скобки.
	if i := strings.IndexAny(seg, "(["); i >= 0 {
		seg = seg[:i]
	}
	seg = strings.TrimSpace(seg)
	if seg == "" || m.mentionsTitle(seg) || m.titlePrefix(seg) {
		return false // полное название или сокращённый вариант перед точным псевдонимом
	}
	words := letterWords(seg, 3)
	cyr := 0
	for _, w := range words {
		if isCyrillic(w) && !nameStopWords[w] {
			cyr++
		}
	}
	// Название — это два и более слова-имени кириллицей; служебные сегменты («Серии: E1-8 of 8») сюда не попадают.
	if cyr < 2 {
		return false
	}
	return true
}

// titlePrefix permits an abbreviated alias only when another segment supplies
// the complete title, e.g. «Истребитель демонов / Клинок ...: Бесконечный замок».
// An overlap such as «Реальная семья» is not an alias of «Реальные пацаны».
func (m *titleMatcher) titlePrefix(s string) bool {
	words := titleWords(s)
	if len(words) == 0 {
		return false
	}
	for _, name := range m.words {
		if len(words) >= len(name) {
			continue
		}
		prefix := true
		for i, word := range words {
			if word != name[i] {
				prefix = false
				break
			}
		}
		if prefix {
			return true
		}
	}
	return false
}

func titleWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// letterWords — слова строки (последовательности букв) длиной от minLength.
func letterWords(s string, minLength int) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) >= minLength {
			out = append(out, string(cur))
		}
		cur = cur[:0]
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) {
			cur = append(cur, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// isCyrillic — слово записано кириллицей (смотрим по первой букве).
func isCyrillic(w string) bool {
	for _, r := range w {
		return unicode.Is(unicode.Cyrillic, r)
	}
	return false
}
