package iptv

import (
	"regexp"
	"strings"
)

// Отличия каналов-однофамильцев: «National Geographic» вещает в десятках
// стран, у одного канала бывают ленты на разные регионы («@East») и сдвиг по
// времени («@Plus4»). Названия одинаковые, поэтому к названию дописывается
// уточнение — страна или, если она неизвестна, вариант вещания из @-пометки.

var (
	// qualityFeedRe — пометка качества в @-части id: «@SD», «@1080p».
	qualityFeedRe = regexp.MustCompile(`(?i)^(?:\d{3,4}\s*[pi]|sd|hd|fhd|uhd|4k|8k)$`)
	// plusFeedRe — сдвиг по времени: «@Plus4» → «+4».
	plusFeedRe = regexp.MustCompile(`(?i)^plus\s*(\d{1,2})$`)
	// hdPrefixRe — качество, слипшееся с названием ленты: «@HDEast» → «@East».
	hdPrefixRe = regexp.MustCompile(`(?i)^(?:fhd|uhd|hd|4k)`)
)

// FeedTag — вариант вещания канала из @-пометки его id: «NatGeo.us@East» →
// «East», «Первый@Plus4» → «+4». Пометки качества вариантом не считаются
// (в том числе слипшиеся с лентой: «@HDEast» → «East»); пустая строка — у
// канала варианта вещания нет.
func FeedTag(id string) string {
	i := strings.IndexByte(id, '@')
	if i < 0 {
		return ""
	}
	s := strings.TrimSpace(id[i+1:])
	if s == "" || qualityFeedRe.MatchString(s) {
		return ""
	}
	if m := plusFeedRe.FindStringSubmatch(s); m != nil {
		return "+" + m[1]
	}
	if s = hdPrefixRe.ReplaceAllString(s, ""); s == "" || qualityFeedRe.MatchString(s) {
		return ""
	}
	return s
}

// ChannelQualifier — уточнение к названию, когда одинаковых названий
// несколько: страна вещания («National Geographic · Болгария»), а если страна
// неизвестна или это Россия — вариант вещания («Первый канал · +4»).
//
// Россию не дописываем: и плейлисты, и телепрограмма здесь российские —
// «Россия» в каждой второй строке была бы шумом.
func ChannelQualifier(id, name, country string) string {
	if country != "" && !strings.EqualFold(country, "RU") {
		return CountryRU(country)
	}
	feed := FeedTag(id)
	if feed == "" || strings.Contains(strings.ToLower(name), strings.ToLower(feed)) {
		return ""
	}
	return feed
}
