package releasewatch

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/zeoril1/video_viewer/internal/magnet"
)

const UnknownVoice = "Озвучка не указана"

var formatPatterns = []struct {
	format string
	re     *regexp.Regexp
}{
	{"CAM", regexp.MustCompile(`(?i)(?:^|[^a-z])(?:hdcam|camrip|cam)(?:$|[^a-z])`)},
	{"TS", regexp.MustCompile(`(?i)(?:^|[^a-z])(?:hdts|telesync|ts)(?:$|[^a-z])`)},
	{"TC", regexp.MustCompile(`(?i)(?:^|[^a-z])(?:hdtc|telecine|tc)(?:$|[^a-z])`)},
	{"SCR", regexp.MustCompile(`(?i)(?:^|[^a-z])(?:dvdscr|bdscr|screener|scr)(?:$|[^a-z])`)},
	{"BD", regexp.MustCompile(`(?i)(?:^|[^a-z])(?:blu[ ._-]?ray|b[dr](?:rip|remux)?|bdmv)(?:$|[^a-z])`)},
	{"WEB", regexp.MustCompile(`(?i)(?:^|[^a-z])web(?:[ ._-]?(?:dl|rip))?(?:$|[^a-z])`)},
	{"HDTV", regexp.MustCompile(`(?i)(?:^|[^a-z])hdtv(?:rip)?(?:$|[^a-z])`)},
	{"DVD", regexp.MustCompile(`(?i)(?:^|[^a-z])dvd(?:rip|remux|[59])?(?:$|[^a-z])`)},
}

func normalizeFormat(format string) string {
	format = strings.TrimSpace(format)
	matched := ""
	for _, p := range formatPatterns {
		if p.re.MatchString(format) {
			if matched != "" && matched != p.format {
				return "UNKNOWN"
			}
			matched = p.format
		}
	}
	if matched != "" {
		return matched
	}
	return "UNKNOWN"
}

// FormatRank compares source provenance separately from pixel resolution.
func FormatRank(format string) int {
	switch normalizeFormat(format) {
	case "CAM":
		return 10
	case "TS":
		return 20
	case "TC":
		return 30
	case "SCR":
		return 40
	case "DVD":
		return 50
	case "HDTV":
		return 55
	case "WEB":
		return 60
	case "BD":
		return 70
	}
	return 0
}

var studios = []struct {
	name string
	re   *regexp.Regexp
}{
	{"LostFilm", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])(?:lost[ ._-]?film|лостфильм|lf)(?:$|[^a-zа-яё])`)},
	{"NewStudio", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])(?:new[ ._-]?studio|ньюстудио)(?:$|[^a-zа-яё])`)},
	{"NovaFilm", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])(?:nova[ ._-]?film|новафильм)(?:$|[^a-zа-яё])`)},
	{"НоваМедиа", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])(?:nova[ ._-]?media|новамедиа)(?:$|[^a-zа-яё])`)},
	{"Kravec", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])(?:kravec|kravets|кравец)(?:$|[^a-zа-яё])`)},
	{"Рен-ТВ", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])(?:рен[ ._-]?тв|ren[ ._-]?tv)(?:$|[^a-zа-яё])`)},
	{"TVShows", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])(?:tv[ ._-]?shows|tvs)(?:$|[^a-zа-яё])`)},
	{"HDRezka Studio", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])hdrezka[ ._-]*studio(?:$|[^a-zа-яё])`)},
	{"HDRezka", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])hdrezka(?:$|[^a-zа-яё])`)},
	{"BaibaKo", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])baiba[ ._-]?ko(?:$|[^a-zа-яё])`)},
	{"Jaskier", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])(?:jaskier|яскер)(?:$|[^a-zа-яё])`)},
	{"Gears Media", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])gears[ ._-]?media(?:$|[^a-zа-яё])`)},
	{"Amedia", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])amedia(?:$|[^a-zа-яё])`)},
	{"Невафильм", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])(?:невафильм|nevafilm)(?:$|[^a-zа-яё])`)},
	{"ColdFilm", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])cold[ ._-]?film(?:$|[^a-zа-яё])`)},
	{"NewComers", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])new[ ._-]?comers(?:$|[^a-zа-яё])`)},
	{"LE-Production", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])le[ ._-]?production(?:$|[^a-zа-яё])`)},
	{"Red Head Sound", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])(?:red[ ._-]?head[ ._-]?sound|rhs)(?:$|[^a-zа-яё])`)},
	{"RuDub", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])ru[ ._-]?dub(?:$|[^a-zа-яё])`)},
	{"Ultradox", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])ultradox(?:$|[^a-zа-яё])`)},
	{"AlexFilm", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])alex[ ._-]?film(?:$|[^a-zа-яё])`)},
	{"DreamCast", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])dream[ ._-]?cast(?:$|[^a-zа-яё])`)},
	{"AniLibria", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])ani[ ._-]?libria(?:$|[^a-zа-яё])`)},
	{"AniDub", regexp.MustCompile(`(?i)(?:^|[^a-zа-яё])ani[ ._-]?dub(?:$|[^a-zа-яё])`)},
	{"AniDub", regexp.MustCompile(`(?i)анидаб`)},
	{"Сыендук", regexp.MustCompile(`(?i)(?:сыендук|сиендук|съендук|syenduk)`)},
	{"Кураж-Бамбей", regexp.MustCompile(`(?i)(?:кураж[ ._-]?бамбей|kurazh[ ._-]?bambey)`)},
	{"Кубик в кубе", regexp.MustCompile(`(?i)(?:кубик в кубе|kubik v kube)`)},
	{"Гоблин", regexp.MustCompile(`(?i)(?:гоблин|goblin|пучков)`)},
	{"Сербин", regexp.MustCompile(`(?i)(?:сербин|serbin)`)},
	{"Доценко", regexp.MustCompile(`(?i)(?:доценко|dotsenko)`)},
	{"Кипарис", regexp.MustCompile(`(?i)кипарис`)},
	{"Кириллица", regexp.MustCompile(`(?i)кири?ллица`)},
	{"Оригинал", regexp.MustCompile(`(?i)(?:оригинал|(?:^|[^a-z])(?:original|eng)(?:$|[^a-z]))`)},
}

// Unknown studios can still be retained when the source explicitly labels the
// field as voice/translation. An arbitrary release-group suffix is not a voice.
var explicitVoiceRE = regexp.MustCompile(`(?i)(?:озвучка|озвучено|перевод|voice|audio)\s*[:=–-]\s*([^|\[\]\r\n]{1,160})`)

func declaredVoices(title, audio string) []string {
	var out []string
	for _, s := range studios {
		if s.re.MatchString(title) {
			if s.name == "HDRezka" && containsString(out, "HDRezka Studio") {
				continue
			}
			out = append(out, s.name)
		}
	}
	if match := explicitVoiceRE.FindStringSubmatch(title); match != nil {
		for _, label := range strings.FieldsFunc(match[1], func(r rune) bool { return r == ',' || r == ';' }) {
			label = strings.Trim(strings.Join(strings.Fields(label), " "), " .-–()")
			label = normalizeVoice(label)
			if label != UnknownVoice && !containsString(out, label) {
				out = append(out, label)
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	// ParseTitle's generic labels are evidence, not guessed studio names.
	_, parsed, _ := magnet.ParseTitle(title)
	if parsed == "" {
		parsed = strings.ToLower(strings.TrimSpace(audio))
	}
	labels := map[string]string{"dub": "Дубляж", "multi": "Многоголосый перевод", "two": "Двухголосый перевод", "single": "Одноголосый перевод", "original": "Оригинал", "subs": "Субтитры"}
	if label := labels[parsed]; label != "" {
		return []string{label}
	}
	return []string{UnknownVoice}
}

func containsString(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

func normalizeVoice(voice string) string {
	voice = strings.Join(strings.Fields(voice), " ")
	if voice == "" {
		return UnknownVoice
	}
	for _, studio := range studios {
		if studio.re.MatchString(voice) {
			return studio.name
		}
	}
	return voice
}

func sourceResolution(title, quality string) int {
	tokens := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	matched := 0
	for _, token := range tokens {
		n := 0
		switch token {
		case "2160", "2160p", "2160i", "4k", "uhd":
			n = 2160
		case "1080", "1080p", "1080i", "fullhd", "fhd":
			n = 1080
		case "720", "720p", "720i", "hd":
			n = 720
		case "576", "576p", "576i":
			n = 576
		case "480", "480p", "480i", "sd":
			n = 480
		}
		if n > 0 {
			if matched > 0 && matched != n {
				return 0
			}
			matched = n
		}
	}
	if matched > 0 {
		return matched
	}
	if n, _ := strconv.Atoi(quality); n == 2160 || n == 1080 || n == 720 || n == 576 || n == 480 {
		return n
	}
	return 0
}

// NormalizeVariants removes tracker duplicates and uses a deterministic order for
// persisted comparisons. Resolution alone is never promoted to WEB or Blu-ray.
func NormalizeVariants(variants []Variant) []Variant {
	seen := map[string]bool{}
	out := make([]Variant, 0, len(variants))
	for _, v := range variants {
		if v.Season < 0 || v.Episode < 0 || v.Resolution < 0 {
			continue
		}
		v.Format, v.Voice = normalizeFormat(v.Format), normalizeVoice(v.Voice)
		key := v.Key()
		if !seen[key] {
			out, seen[key] = append(out, v), true
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		if a.Episode != b.Episode {
			return a.Episode < b.Episode
		}
		if a.Voice != b.Voice {
			return a.Voice < b.Voice
		}
		if a.Format != b.Format {
			return FormatRank(a.Format) < FormatRank(b.Format)
		}
		return a.Resolution < b.Resolution
	})
	return out
}
