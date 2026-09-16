// Package iptv — разбор источников IPTV: плейлистов M3U/M3U8, Xtream Codes
// (player_api.php) и телепрограммы XMLTV. Клиент видит только id канала, а
// поток тянет наш сервер — креды провайдера и CORS остаются проблемами сервера.
package iptv

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Channel — один канал плейлиста.
type Channel struct {
	// ExtID — стабильный id внутри плейлиста: tvg-id, иначе порядковый
	// номер. По нему канал обновляется/удаляется при повторном синке.
	ExtID string
	Name  string
	Group string
	Logo  string
	// EPGID — tvg-id из плейлиста (для сопоставления с XMLTV).
	EPGID string
	URL   string
	// UA/Referer — заголовки из плейлиста (http-user-agent/http-referrer,
	// #EXTVLCOPT): многие российские каналы без них не отдают поток.
	UA      string
	Referer string
	IsHLS   bool
	Num     int
}

// ParseM3U разбирает плейлист M3U/M3U8: атрибуты #EXTINF (tvg-id, tvg-name,
// tvg-logo, group-title, http-user-agent/referrer), строку #EXTGRP,
// #EXTVLCOPT. Название — после запятой в #EXTINF (по нему ищется программа
// в XMLTV, если tvg-id не совпал).
func ParseM3U(r io.Reader) ([]Channel, error) {
	sc := bufio.NewScanner(r)
	// Строки плейлистов бывают длинными (URL с токенами) — поднимаем лимит.
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var (
		out          []Channel
		cur          Channel
		pending      bool   // прочитан #EXTINF, ждём URL
		pendingGroup string // группа из #EXTGRP (относится к следующему каналу)
		num          int
	)
	flush := func() {
		// Канал без URL и служебные ссылки (Telegram автора списка) пропускаем.
		if !pending || cur.URL == "" || !IsStreamURL(cur.URL) {
			pending = false
			cur = Channel{}
			return
		}
		num++
		cur.Num = num
		if cur.ExtID == "" {
			cur.ExtID = strconv.Itoa(num)
		}
		out = append(out, cur)
		pending = false
		cur = Channel{}
	}

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXTM3U"):
			continue
		case strings.HasPrefix(line, "#EXTINF:"):
			flush()
			attrs, name := parseExtinf(strings.TrimPrefix(line, "#EXTINF:"))
			cur.Name = name
			cur.EPGID = attrs["tvg-id"]
			cur.ExtID = attrs["tvg-id"]
			cur.Logo = attrs["tvg-logo"]
			cur.Group = attrs["group-title"]
			if v := attrs["tvg-name"]; v != "" && cur.Name == "" {
				cur.Name = v
			}
			cur.UA = attrs["http-user-agent"]
			cur.Referer = attrs["http-referrer"]
			// Группа из #EXTGRP относится к СЛЕДУЮЩЕМУ каналу.
			if cur.Group == "" {
				cur.Group = pendingGroup
			}
			pendingGroup = ""
			pending = true
		case strings.HasPrefix(line, "#EXTGRP:"):
			grp := strings.TrimSpace(strings.TrimPrefix(line, "#EXTGRP:"))
			switch {
			case pending && cur.Group == "":
				cur.Group = grp // #EXTGRP сразу после #EXTINF
			case !pending:
				pendingGroup = grp // перед #EXTINF — для следующего канала
			}
		case strings.HasPrefix(line, "#EXTVLCOPT:"):
			opt := strings.TrimSpace(strings.TrimPrefix(line, "#EXTVLCOPT:"))
			switch {
			case strings.HasPrefix(opt, "http-user-agent=") && cur.UA == "":
				cur.UA = strings.TrimPrefix(opt, "http-user-agent=")
			case strings.HasPrefix(opt, "http-referrer=") && cur.Referer == "":
				cur.Referer = strings.TrimPrefix(opt, "http-referrer=")
			}
		case strings.HasPrefix(line, "#"):
			// Прочие директивы (#KODIPROP, #EXT-X-*) игнорируем.
			continue
		default:
			if !pending {
				continue // URL без предшествующего #EXTINF — мусор
			}
			if strings.ContainsAny(line, " \t") {
				continue // «URL» с пробелами — не поток, а мусорная строка
			}
			cur.URL = line
			cur.IsHLS = IsHLSURL(line)
			flush()
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("parse m3u: %w", err)
	}
	return out, nil
}

// parseExtinf разбирает «-1 tvg-id="x" group-title="y",Название» на атрибуты
// и название канала (название — всё после первой запятой ВНЕ кавычек).
func parseExtinf(s string) (attrs map[string]string, name string) {
	attrs = make(map[string]string, 6)
	// Название — всё после первой запятой ВНЕ кавычек.
	inQuotes := false
	comma := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuotes = !inQuotes
		case ',':
			if !inQuotes {
				comma = i
			}
		}
		if comma >= 0 {
			break
		}
	}
	head := s
	if comma >= 0 {
		head = s[:comma]
		name = strings.TrimSpace(s[comma+1:])
	}
	// Атрибуты: ключ="значение" или ключ=значение (до пробела).
	for i := 0; i < len(head); {
		for i < len(head) && (head[i] == ' ' || head[i] == '\t') {
			i++
		}
		start := i
		for i < len(head) && head[i] != '=' && head[i] != ' ' && head[i] != '\t' {
			i++
		}
		if i >= len(head) || head[i] != '=' {
			i++
			continue
		}
		key := strings.ToLower(head[start:i])
		i++ // '='
		var val string
		if i < len(head) && head[i] == '"' {
			i++
			vs := i
			for i < len(head) && head[i] != '"' {
				i++
			}
			val = head[vs:i]
			i++ // закрывающая кавычка
		} else {
			vs := i
			for i < len(head) && head[i] != ' ' && head[i] != '\t' {
				i++
			}
			val = head[vs:i]
		}
		if key != "" {
			attrs[key] = strings.TrimSpace(val)
		}
	}
	return attrs, name
}

// IsHLSURL сообщает, что поток — HLS-плейлист (.m3u8/.m3u), который можно
// отдать браузеру через прокси без перепаковки.
func IsHLSURL(u string) bool {
	low := strings.ToLower(u)
	if i := strings.IndexByte(low, '?'); i >= 0 {
		low = low[:i]
	}
	low = strings.TrimSuffix(strings.TrimSpace(low), "/")
	return strings.HasSuffix(low, ".m3u8") || strings.HasSuffix(low, ".m3u")
}

// nonStreamHosts — хосты служебных записей плейлистов: авторы вставляют
// вместо канала ссылку на свой Telegram («loganettv all» → t.me/...).
var nonStreamHosts = map[string]bool{
	"t.me": true, "www.t.me": true, "telegram.me": true,
	"telegram.dog": true, "telegram.org": true,
}

// IsStreamURL сообщает, что ссылка похожа на поток, а не на служебную запись
// списка: такие записи каналом не считаем (открыть их нельзя, а место они занимают).
func IsStreamURL(u string) bool {
	low := strings.ToLower(strings.TrimSpace(u))
	if i := strings.Index(low, "://"); i >= 0 {
		low = low[i+3:]
	}
	if i := strings.IndexAny(low, "/?#"); i >= 0 {
		low = low[:i]
	}
	if i := strings.LastIndexByte(low, '@'); i >= 0 { // user:pass@host
		low = low[i+1:]
	}
	if i := strings.LastIndexByte(low, ':'); i >= 0 { // порт
		low = low[:i]
	}
	return low != "" && !nonStreamHosts[low]
}

// NormalizeName приводит название канала к виду для сопоставления с XMLTV:
// нижний регистр, без пометок качества/пунктуации и мусора в скобках.
// Пример: «Матч ТВ (1080p)» → «матчтв».
func NormalizeName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	low := strings.ToLower(s)
	for i := 0; i < len(low); {
		c := low[i]
		// Отрезаем типовые пометки качества и мусор в скобках.
		if c == '(' || c == '[' {
			for i < len(low) && low[i] != ')' && low[i] != ']' {
				i++
			}
			i++
			continue
		}
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteByte(c)
			i++
		case c >= 0x80: // кириллица и прочий unicode — копируем байтами
			b.WriteByte(c)
			i++
		default:
			i++
		}
	}
	out := b.String()
	for _, suf := range []string{"sd", "hd", "fhd", "uhd", "4k", "hevc", "h264", "h265"} {
		if strings.HasSuffix(out, suf) && len(out) > len(suf)+1 {
			out = strings.TrimSuffix(out, suf)
		}
	}
	return out
}

// BaseKey — упрощённый ключ сопоставления «канал ↔ передача»: то же, что
// NormalizeName, но ещё без служебных суффиксов, которые источники пишут
// по-разному: tvg-id «MatchTV.ru@SD», id программы «Матч ТВ.ru» и имя канала
// «Матч ТВ (1080p)» дают один ключ «матчтв».
func BaseKey(s string) string {
	s = strings.TrimSpace(s)
	// «@SD»/«@HD» — пометка варианта (feed) в tvg-id.
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	// «.ru»/«.kz»/«.ua» — домен-суффикс в tvg-id и id программы.
	// Отрезаем только если хвост похож на TLD: иначе «RU.TV (1080p)»
	// превратился бы в «ru» и ложно совпал с каналом «.ru» из программы.
	if i := strings.LastIndexByte(s, '.'); i > 0 && isTLD(s[i+1:]) {
		s = s[:i]
	}
	out := NormalizeName(s)
	// «ru», «тв» и подобное — слишком коротко: на таких ключах много ложных пар.
	if utf8.RuneCountInString(out) < 3 {
		return ""
	}
	return out
}

// isTLD сообщает, что s похоже на домен верхнего уровня: «ru», «kz», «com».
func isTLD(s string) bool {
	if len(s) < 2 || len(s) > 4 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i] | 0x20; c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
