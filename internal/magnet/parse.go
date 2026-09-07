package magnet

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Регулярки для извлечения качества, озвучки и сезона из заголовка раздачи.
var (
	srcResRe    = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|480p|4k|uhd|fullhd|fhd|hd|sd)\b`)
	srcDubRe    = regexp.MustCompile(`(?i)(дублирован|дубляж|полн[а-я]+\s+дубляж|\bdub\b)`)
	srcMultiRe  = regexp.MustCompile(`(?i)(профессиональн[а-я]+\s*\(?\s*многоголос|многоголос|multi\s*voice|\bmvo\b|\bmulti\b)`)
	srcTwoRe    = regexp.MustCompile(`(?i)(двухголос|two\s*voice|\b2vo\b|\bdvo\b)`)
	srcSingleRe = regexp.MustCompile(`(?i)(одноголос|авторск|\bavo\b|\bsvo\b|single\s*voice)`)
	srcOrigRe   = regexp.MustCompile(`(?i)(оригинальн|\boriginal\b|\bost\b)`)
	srcSubsRe   = regexp.MustCompile(`(?i)(субтитр|\bsub\b|\bsubs\b)`)
	// Известные студии озвучки в заголовках раздач — чтобы раздачи с именем
	// студии (а не только со словом «многоголосый»/«одноголосый») попадали
	// в правильный перевод. Многоголосая закадровая: Сыендук, Кураж-Бамбей,
	// Jaskier, LostFilm, NewStudio, Novamedia, TVShows, BaibaKo, Амедиа и т.п.
	srcStudioMultiRe = regexp.MustCompile(`(?i)(сыендук|сиендук|syenduk|кураж[ -]бамбей|kurazh|jaskier|lostfilm|newstudio|novamedia|tvshows|baibako|amedia|кипарис)`)
	// Авторский одноголосый: Кубик в Кубе, Гоблин, Доценко, Сербин,
	// Визгунов, Котов (Александр Котов — частый авторский перевод в
	// раздачах MegaPeer/RuTracker: «| А | Котов |», «Продубляж, Котов»).
	// \b не используем: в Go regexp граница слова — ASCII, кириллицу
	// (Котов) она не видит.
	srcStudioSingleRe = regexp.MustCompile(`(?i)(кубик в кубе|кубик в куб|гоблин|доценко|doxa|serbin|визгунов|котов)`)
	// Сезон из заголовка раздачи. Группы: 1 — диапазон сезонов "S1-9"
	// (полный сборник → сезон 0), 2 — одиночный "S8"/"S8E5"/"[S8]" (после
	// номера допустимы e/пробел/.,_,-,\],),/конец строки), 3 — «сезон N»,
	// 4 — "season N".
	srcSeasonRe = regexp.MustCompile(`(?i)(?:[sS](\d{1,2})\s*[-–]\s*[sS]?\d{1,2}|[sS](\d{1,2})(?:[eE]\d+|\s|[._\,\-\)\]]|$)|сезон\s*(\d+)|season\s*(\d+))`)
)

// ParseTitle извлекает (качество, озвучку, сезон) из заголовка раздачи.
// Используется и в on-demand поиске источников, и в фоновом поиске
// магнетов, чтобы метаданные в таблице sources заполнялись одинаково.
func ParseTitle(title string) (quality, audio string, season int) {
	low := strings.ToLower(title)
	if m := srcResRe.FindString(low); m != "" {
		switch {
		case strings.Contains(m, "2160"), strings.Contains(m, "4k"), strings.Contains(m, "uhd"):
			quality = "2160"
		case strings.Contains(m, "1080"), strings.Contains(m, "fullhd"), strings.Contains(m, "fhd"):
			quality = "1080"
		case strings.Contains(m, "720"), strings.Contains(m, "hd"):
			quality = "720"
		case strings.Contains(m, "480"), strings.Contains(m, "sd"):
			quality = "480"
		}
	}
	if m := srcSeasonRe.FindStringSubmatch(low); m != nil {
		switch {
		case m[1] != "":
			// Диапазон "S1-9" — раздача со ВСЕМИ сезонами (полный сборник).
			// Такие раздачи не относятся к конкретному сезону — 0.
			season = 0
		case m[2] != "":
			season = atoiOr(m[2], 0)
		case m[3] != "":
			season = atoiOr(m[3], 0)
		case m[4] != "":
			season = atoiOr(m[4], 0)
		}
	}
	switch {
	case srcDubRe.MatchString(low):
		audio = "dub"
	case srcMultiRe.MatchString(low), srcStudioMultiRe.MatchString(low):
		audio = "multi"
	case srcTwoRe.MatchString(low):
		audio = "two"
	case srcSingleRe.MatchString(low), srcStudioSingleRe.MatchString(low):
		audio = "single"
	case srcOrigRe.MatchString(low):
		audio = "original"
	case srcSubsRe.MatchString(low):
		audio = "subs"
	}
	// Словами перевод не распознан — пробуем буквенные коды в теговых полях
	// («| D, P, A |», «| D |»): на многих трекерах озвучка указана только так.
	if audio == "" {
		audio = parseAudioCodes(title)
	}
	return quality, audio, season
}

// Коды озвучек в теговых полях раздач MegaPeer/RuTracker/NoNaMe: после «|»
// идут одиночные буквы D/P/A (иногда кириллические А/Р) — дубляж /
// профессиональная многоголосная / авторская одноголосная, разделённые
// запятыми или пробелами («| D, P, A |», «| D |», «| D, Р |», «| А |»).
// D/L — вторичны: D=дубляж, L=любительский (одноголосый).
// Разбираем ТОЛЬКО кодовые поля (поле целиком — набор одиночных кодовых
// букв): иначе одиночная «A» в английском названии («A Quiet Place») была
// бы принята за авторскую озвучку, а «Dolby Vision» — за «D».
const (
	audioCodeDub    = 1 << iota // D/Д — дубляж
	audioCodeMulti              // P/Р — профессиональный многоголосый
	audioCodeSingle             // A/А, L/Л — авторский/любительский одноголосый
)

// parseAudioCodes ищет в названии раздачи теговые поля с кодами озвучек и
// возвращает канонический ключ перевода (dub/multi/single) или "".
func parseAudioCodes(title string) string {
	var seen int
	fields := strings.Split(title, "|")
	for fi := 1; fi < len(fields); fi++ { // поле 0 — название/имя раздачи, не коды
		seen |= audioCodesMask(strings.TrimSpace(fields[fi]))
	}
	switch {
	case seen&audioCodeDub != 0:
		return "dub"
	case seen&audioCodeMulti != 0:
		return "multi"
	case seen&audioCodeSingle != 0:
		return "single"
	}
	return ""
}

// audioCodesMask возвращает битовую маску кодов, если поле — набор одиночных
// кодовых букв (разделители — любые не-буквы/не-цифры), иначе 0.
func audioCodesMask(field string) int {
	if field == "" {
		return 0
	}
	tokens := strings.FieldsFunc(field, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(tokens) == 0 {
		return 0
	}
	var mask int
	for _, tok := range tokens {
		rs := []rune(tok)
		if len(rs) != 1 { // слово/число из нескольких символов — не кодовое поле
			return 0
		}
		switch unicode.ToLower(rs[0]) {
		case 'd', 'д':
			mask |= audioCodeDub
		case 'p', 'р':
			mask |= audioCodeMulti
		case 'a', 'а', 'l', 'л':
			mask |= audioCodeSingle
		default:
			return 0 // посторонняя одиночная буква — это не коды озвучки
		}
	}
	return mask
}

// IsFullCollection — является ли заголовок раздачи «полным сборником» всех
// сезонов: содержит диапазон сезонов (S1-9, «сезоны 1-5» и т.п.). Такие
// раздачи не относятся к конкретному сезону — ParseTitle даёт им сезон 0,
// и его НЕЛЬЗЯ переопределять подсказкой искомого сезона (seasonHint).
func IsFullCollection(title string) bool {
	m := srcSeasonRe.FindStringSubmatch(strings.ToLower(title))
	return m != nil && m[1] != ""
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
