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
	// Известные студии озвучки в заголовках раздач — чтобы такие раздачи
	// (без слов «многоголосый/одноголосый») попали в свой перевод.
	srcStudioMultiRe = regexp.MustCompile(`(?i)(сыендук|сиендук|syenduk|кураж[ -]бамбей|kurazh|jaskier|lostfilm|newstudio|novamedia|tvshows|baibako|amedia|gears\s?media|кипарис)`)
	// Авторский одноголосый (Кубик в Кубе, Гоблин, Доценко, Сербин,
	// Визгунов, Котов). \b не используем: в Go граница слова ASCII и
	// кириллицу («Котов») не видит.
	srcStudioSingleRe = regexp.MustCompile(`(?i)(кубик в кубе|кубик в куб|гоблин|доценко|doxa|serbin|визгунов|котов)`)
	// Сезон из заголовка раздачи. Группы: 1 — диапазон сезонов "S1-9"
	// (полный сборник → сезон 0), 2 — одиночный "S8"/"S8E5"/"[S8]",
	// 3 — «сезон N», 4 — "season N".
	srcSeasonRe = regexp.MustCompile(`(?i)(?:[sS](\d{1,2})\s*[-–]\s*[sS]?\d{1,2}|[sS](\d{1,2})(?:[eE]\d+|\s|[._\,\-\)\]]|$)|сезон\s*(\d+)|season\s*(\d+))`)
	// Диапазон сезонов («S1-14», «S01-02», «S1-3E1-71», «S01-02x01-41»).
	// Группы: 1 — первый сезон, 2 — последний. Одиночный сезон и диапазон
	// серий («S1E1-18») НЕ совпадают: после номера обязателен «-»/«–».
	srcSeasonRangeRe = regexp.MustCompile(`(?i)[sS](\d{1,2})\s*[-–]\s*[sS]?(\d{1,2})`)
	// Год выпуска в заголовке раздачи: «(2010)», «.2013.», «2010-2023»
	// (берётся первый найденный).
	srcYearRe = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d{2})(?:[^0-9]|$)`)
)

// NamesVoiceStudio сообщает, что в заголовке раздачи названа студия
// озвучки/перевода (LostFilm, TVShows, Novamedia, Jaskier, Amedia…) или
// авторский переводчик. Нужно при отборе раздач БЕЗ сидов: именно они часто
// единственный источник озвучки сезона; по этому же признаку отсекается
// мусор широкого запроса (чужие фильмы с тем же названием).
func NamesVoiceStudio(title string) bool {
	return srcStudioMultiRe.MatchString(title) || srcStudioSingleRe.MatchString(title)
}

// ParseTitle извлекает (качество, озвучку, сезон) из заголовка раздачи.
// Используется и в on-demand, и в фоновом поиске — чтобы метаданные
// в таблице sources заполнялись одинаково.
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
			// Диапазон "S1-9" — раздача со ВСЕМИ сезонами: конкретного сезона
			// нет, поэтому 0.
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
	// (на многих трекерах озвучка указана только так).
	if audio == "" {
		audio = parseAudioCodes(title)
	}
	return quality, audio, season
}

// Коды озвучек в теговых полях раздач MegaPeer/RuTracker/NoNaMe: после «|»
// идут одиночные буквы D/P/A (иногда кириллические А/Р) — дубляж /
// профессиональная многоголосая / авторская одноголосная («| D, P, A |»).
// D/L вторичны: D=дубляж, L=любительский. Разбираем ТОЛЬКО кодовые поля
// (поле целиком — набор одиночных букв): иначе одиночная «A» в английском
// названии («A Quiet Place») была бы принята за авторскую озвучку.
const (
	audioCodeDub    = 1 << iota // D/Д — дубляж
	audioCodeMulti              // P/Р — профессиональный многоголосый
	audioCodeSingle             // A/А, L/Л — авторский/любительский одноголосый
)

// parseAudioCodes ищет теговые поля с кодами озвучек и возвращает
// канонический ключ перевода (dub/multi/single) или "".
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

// IsFullCollection — заголовок описывает «полный сборник» всех сезонов
// (диапазон сезонов: S1-9 и т.п.). ParseTitle даёт таким раздачам сезон 0,
// и его НЕЛЬЗЯ переопределять подсказкой искомого сезона (seasonHint).
func IsFullCollection(title string) bool {
	m := srcSeasonRe.FindStringSubmatch(strings.ToLower(title))
	return m != nil && m[1] != ""
}

// SeasonRange возвращает диапазон сезонов из заголовка («S1-14» → 1,14;
// для одиночного сезона и заголовков без диапазона — нули).
func SeasonRange(title string) (from, to int) {
	m := srcSeasonRangeRe.FindStringSubmatch(title)
	if m == nil {
		return 0, 0
	}
	from = atoiOr(m[1], 0)
	to = atoiOr(m[2], 0)
	if from <= 0 || to <= from {
		return 0, 0
	}
	return from, to
}

// TitleYear возвращает год выпуска раздачи из заголовка (0 — не найден).
// Нужен для сопоставления сезонов трекера с TMDB: дробные трекерные сезоны
// идут теми же годами, поэтому год надёжнее номера.
func TitleYear(title string) int {
	m := srcYearRe.FindStringSubmatch(title)
	if m == nil {
		return 0
	}
	return atoiOr(m[1], 0)
}

// TitleSeason возвращает номер сезона, если в заголовке он ОДИН (0 — сезон
// не указан или указан диапазон: «S1-14» — сборник, а не сезон).
func TitleSeason(title string) int {
	if from, _ := SeasonRange(title); from > 0 {
		return 0
	}
	quality, _, season := ParseTitle(title)
	_ = quality
	return season
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
