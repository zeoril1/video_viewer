package iptv

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Названия каналов в плейлистах «грязные»: качество в имени («Моя Планета
// (1080p)»), пометки вещания («[Not 24/7]»), то латиница, то кириллица.
// Ниже — очистка имени для интерфейса, русское название по справочнику и
// ключ, по которому дубли каналов схлопываются, а варианты потока — заменяются лучшим.

// qualTagRe — пометка качества в скобках: «(1080p)», «(576i)», «(4K)», «(SD)».
var qualTagRe = regexp.MustCompile(`(?i)\s*\(\s*(?:\d{3,4}\s*[pik]|SD|HD|FHD|UHD|4K)\s*\)`)

// CleanChannelName убирает из названия технические пометки качества в
// скобках: «Моя Планета (1080p)» → «Моя Планета». Пометки доступности
// («[Not 24/7]», «[Geo-blocked]») сохраняются — они несут смысл для зрителя.
func CleanChannelName(s string) string {
	return strings.Join(strings.Fields(qualTagRe.ReplaceAllString(s, "")), " ")
}

// DisplayName — название канала для интерфейса: русское, если оно известно
// (см. RefIndex), и без технических пометок качества.
func DisplayName(name, nameRU string) string {
	if ru := strings.TrimSpace(nameRU); ru != "" {
		if clean := CleanChannelName(ru); clean != "" {
			return clean
		}
	}
	if clean := CleanChannelName(name); clean != "" {
		return clean
	}
	return strings.TrimSpace(name)
}

// NameKey — ключ названия: регистр, пунктуация и пометки качества не важны
// («Мир HD (720p)» и «МИР» → «мир»).
func NameKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(CleanChannelName(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', 'а' <= r && r <= 'я', r == 'ё':
			b.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r >= 0x0400 && r <= 0x04FF:
			b.WriteRune(r)
		}
	}
	out := b.String()
	for _, suf := range []string{"hd", "sd", "fhd", "uhd", "4k"} {
		if strings.HasSuffix(out, suf) && utf8.RuneCountInString(out) > len(suf)+1 {
			return strings.TrimSuffix(out, suf)
		}
	}
	return out
}

// ChannelIDBase — устойчивая часть идентификатора канала: tvg-id без пометки
// варианта («MoyaPlaneta.ru@SD» → «moyaplaneta.ru»). «"» означает, что id
// ничего не значит (его нет или это порядковый номер): в ключе дублей такой
// id участвовать не должен, иначе один канал из разных плейлистов даст разные ключи.
func ChannelIDBase(id string) string {
	s := strings.TrimSpace(id)
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	if s == "" || isDigits(s) {
		return ""
	}
	return strings.ToLower(s)
}

// ChannelKey — ключ, по которому схлопываются дубли каналов: один канал из
// разных плейлистов (и варианты его потока) получают один ключ.
// name — НАЗВАНИЕ ДЛЯ ПОКАЗА: «Моя Планета (1080p)» и «Moya Planeta (1080p)»
// с одним tvg-id — один канал, а «BBC Earth» и «BBC Earth Czechia» (тот же
// tvg-id) — разные ленты вещания, их ключи различаются названием.
func ChannelKey(id, name string) string {
	base := ChannelIDBase(id)
	key := NameKey(name)
	switch {
	case base == "":
		return key
	case key == "":
		return base
	default:
		return base + "|" + key
	}
}

// StreamQuality — оценка варианта потока одного канала: чем больше, тем
// предпочтительнее оставить его в списке (разрешение, HLS без перепаковки,
// отсутствие пометок «Not 24/7» и «Geo-blocked»).
func StreamQuality(name string, isHLS bool) int {
	q := resolutionScore(name) * 10
	if isHLS {
		q += 2
	}
	low := strings.ToLower(name)
	if strings.Contains(low, "not 24/7") {
		q -= 150
	}
	if strings.Contains(low, "geo-blocked") || strings.Contains(low, "geoblocked") {
		q -= 400
	}
	return q
}

// resolutionScore — оценка разрешения по пометке в названии (65 — не указано).
func resolutionScore(name string) int {
	low := strings.ToLower(name)
	switch {
	case strings.Contains(low, "2160"), strings.Contains(low, "4k"), strings.Contains(low, "uhd"):
		return 90
	case strings.Contains(low, "1440"):
		return 85
	case strings.Contains(low, "1080"):
		return 80
	case strings.Contains(low, "720"):
		return 70
	case strings.Contains(low, "576"):
		return 60
	case strings.Contains(low, "540"):
		return 55
	case strings.Contains(low, "480"):
		return 50
	case strings.Contains(low, "406"):
		return 45
	case strings.Contains(low, "360"):
		return 40
	case strings.Contains(low, "240"):
		return 30
	}
	return 65
}

// hasCyrillic сообщает, что в строке есть кириллица.
func hasCyrillic(s string) bool {
	for _, r := range s {
		if r >= 0x0400 && r <= 0x04FF {
			return true
		}
	}
	return false
}

// isDigits сообщает, что строка состоит только из цифр.
func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
