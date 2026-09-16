package iptv

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Реальный фрагмент плейлиста iptv-org (rus.m3u): атрибуты качества,
// http-referrer/http-user-agent, логотипы, группы.
const m3uSample = `#EXTM3U
#EXTINF:-1 tvg-id="2x2.ru@SD" tvg-logo="https://i.imgur.com/fhQFLEl.png" group-title="Entertainment",2x2 (576i)
https://bl.rutube.ru/livestream/392b4686b770bae2da6bf5ac4574add5/index.m3u8?e=2068731801&s=tenr-yHXUv1wibfka78s2A&scheme=https
#EXTINF:-1 tvg-id="Channel5.ru@SD" tvg-logo="https://i.imgur.com/KPXMa3U.png" http-referrer="https://televizor24tochka.ru/" http-user-agent="Mozilla/5.0 (Windows NT 10.0) Chrome/144.0" group-title="General",5 Канал
#EXTVLCOPT:http-user-agent=Mozilla/5.0 (Linux) Chrome/120.0
https://cdn.example.ru/5tv/index.m3u8
#EXTGRP:Кино
#EXTINF:-1,Канал без tvg-id
http://example.org/live/stream.ts
#EXTINF:-1 tvg-id="Dup.ru",Дубль id
http://example.org/dup.m3u8
#EXTINF:-1,Битый без URL
#EXTINF:-1 tvg-id="Named.ru" tvg-name="Из tvg-name",
https://example.org/named/index.m3u8
`

func TestParseM3U(t *testing.T) {
	chans, err := ParseM3U(strings.NewReader(m3uSample))
	if err != nil {
		t.Fatalf("ParseM3U: %v", err)
	}
	if len(chans) != 5 {
		t.Fatalf("каналов %d, ожидали 5: %+v", len(chans), chans)
	}

	// 1) HLS-канал с query в URL — тип определяется по .m3u8 до «?».
	c := chans[0]
	if c.ExtID != "2x2.ru@SD" || c.EPGID != "2x2.ru@SD" || c.Name != "2x2 (576i)" {
		t.Errorf("канал 0 разобран неверно: %+v", c)
	}
	if c.Group != "Entertainment" || c.Logo != "https://i.imgur.com/fhQFLEl.png" {
		t.Errorf("канал 0: группа/логотип: %+v", c)
	}
	if !c.IsHLS || c.Num != 1 {
		t.Errorf("канал 0: IsHLS=%v num=%d", c.IsHLS, c.Num)
	}

	// 2) UA/Referer из атрибутов #EXTINF.
	c = chans[1]
	if c.UA == "" || !strings.Contains(c.UA, "Chrome/144") {
		t.Errorf("канал 1: UA не разобран: %q", c.UA)
	}
	if c.Referer != "https://televizor24tochka.ru/" {
		t.Errorf("канал 1: Referer не разобран: %q", c.Referer)
	}

	// 3) #EXTGRP задаёт группу, если в #EXTINF её нет; .ts — не HLS.
	c = chans[2]
	if c.Group != "Кино" {
		t.Errorf("канал 2: группа из #EXTGRP не подхвачена: %q", c.Group)
	}
	if c.IsHLS {
		t.Errorf("канал 2: .ts не должен считаться HLS")
	}
	if c.ExtID == "" || c.ExtID == "3" && c.Name == "" {
		t.Errorf("канал 2: ext_id должен быть непустым: %+v", c)
	}

	// 4) Канал без URL («битый») не попадает в результат, поэтому 4-й
	// разобранный — «Из tvg-name» (имя берётся из tvg-name, т.к. после
	// запятой пусто).
	c = chans[4]
	if c.Name != "Из tvg-name" || !c.IsHLS {
		t.Errorf("канал 4: %+v", c)
	}
}

func TestParseM3UCRLF(t *testing.T) {
	// Реальные плейлисты приходят с CRLF — названия не должны тянуть \r.
	crlf := strings.ReplaceAll(m3uSample, "\n", "\r\n")
	chans, err := ParseM3U(strings.NewReader(crlf))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chans {
		if strings.ContainsAny(c.Name, "\r\n") || strings.ContainsAny(c.URL, "\r\n") {
			t.Fatalf("в разобранном канале остались CR/LF: %+v", c)
		}
	}
}

func TestIsHLSURL(t *testing.T) {
	cases := map[string]bool{
		"http://a/b/index.m3u8?e=1&s=2":       true,
		"https://cdn-dvr.ntv.ru/x/index.m3u8": true,
		"http://a/b/playlist.m3u":             true,
		"http://a:8080/live/u/p/123.ts":       false,
		"http://a:8080/live/u/p/123":          false,
		"rtmp://a/live/1":                     false,
	}
	for u, want := range cases {
		if got := IsHLSURL(u); got != want {
			t.Errorf("IsHLSURL(%q) = %v, want %v", u, got, want)
		}
	}
}

// TestIsStreamURL — служебные ссылки плейлистов (Telegram автора списка) не
// потоки, каналами их считать нельзя.
func TestIsStreamURL(t *testing.T) {
	cases := map[string]bool{
		"https://t.me/loganettv_support":                                        false,
		"https://telegram.me/loganettv_original":                                false,
		"http://tvchannelstream1.tvzvezda.ru/cdn/tvzvezda/playlist_sdhigh.m3u8": true,
		"iptv.crimea.net:8787":                                                  true,
		"http://user:pass@cdn.example/live/1.m3u8":                              true,
		"": false,
	}
	for u, want := range cases {
		if got := IsStreamURL(u); got != want {
			t.Errorf("IsStreamURL(%q) = %v, want %v", u, got, want)
		}
	}
}

// TestParseM3USkipsNonStreams — записи-ссылки пропускаются, а нумерация каналов
// не сдвигается их присутствием.
func TestParseM3USkipsNonStreams(t *testing.T) {
	pl := `#EXTM3U
#EXTINF:-1 group-title="Общие", loganettv all
https://t.me/loganettv_support
#EXTINF:-1 group-title="Общие" tvg-id="zvezda", Звезда
http://tvchannelstream1.tvzvezda.ru/cdn/tvzvezda/playlist_sdhigh.m3u8
#EXTINF:-1 group-title="Общие", telegram - t.me/loganettv_original
https://telegram.me/loganettv_original
#EXTINF:-1 group-title="Общие", Мир
http://uiptv.do.am/1ufc/113500247/playlist.m3u8
`
	chans, err := ParseM3U(strings.NewReader(pl))
	if err != nil {
		t.Fatalf("разбор плейлиста: %v", err)
	}
	if len(chans) != 2 {
		t.Fatalf("каналов %d, ждали 2: %+v", len(chans), chans)
	}
	if chans[0].Name != "Звезда" || chans[0].Num != 1 {
		t.Errorf("первый канал %q (num=%d), ждали «Звезда» (num=1)", chans[0].Name, chans[0].Num)
	}
	if chans[1].Name != "Мир" || chans[1].Num != 2 {
		t.Errorf("второй канал %q (num=%d), ждали «Мир» (num=2)", chans[1].Name, chans[1].Num)
	}
}

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"Матч ТВ":           "матчтв",
		"Матч ТВ (1080p)":   "матчтв",
		"Россия 1 HD":       "россия1",
		"ТНТ4 [1080p]":      "тнт4",
		"Первый канал (SD)": "первыйканал",
		"CHELSEA TV":        "chelseatv",
		"2x2 (576i) Россия": "2x2россия",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// xmltvSample — фрагмент реального XMLTV (open-epg russia1): id каналов на
// кириллице, время «20260910120000 +0300».
const xmltvSample = `<?xml version="1.0" encoding="UTF-8" ?>
<tv generator-info-name="test">
<channel id="Матч ТВ.ru"><display-name>Матч ТВ.ru</display-name><icon src="https://example.org/logo.png"/></channel>
<channel id="2x2.ru"><display-name>2x2</display-name></channel>
<programme start="20260910120000 +0300" stop="20260910130000 +0300" channel="Матч ТВ.ru">
<title lang="ru">Новости спорта</title>
<desc lang="ru">Итоги дня</desc>
<category lang="ru">Спорт</category>
</programme>
<programme start="20260910130000 +0300" stop="20260910143000 +0300" channel="Матч ТВ.ru">
<title lang="ru">Футбол. Обзор</title>
</programme>
<programme start="20260910120000 +0300" channel="2x2.ru">
<title lang="ru">Мультфильмы</title>
</programme>
<programme start="20260910120000 +0300" stop="20260910130000 +0300" channel="">
<title>Без канала — пропускаем</title>
</programme>
</tv>`

func TestParseXMLTV(t *testing.T) {
	chans, progs, err := ParseXMLTV(strings.NewReader(xmltvSample))
	if err != nil {
		t.Fatalf("ParseXMLTV: %v", err)
	}
	if len(chans) != 2 {
		t.Fatalf("каналов %d, ожидали 2: %+v", len(chans), chans)
	}
	if chans[0].ID != "Матч ТВ.ru" || chans[0].Name != "Матч ТВ.ru" || chans[0].Icon == "" {
		t.Errorf("канал 0: %+v", chans[0])
	}
	if len(progs) != 3 {
		t.Fatalf("передач %d, ожидали 3 (без channel — пропуск): %+v", len(progs), progs)
	}
	p := progs[0]
	if p.ChannelID != "Матч ТВ.ru" || p.Title != "Новости спорта" || p.Desc != "Итоги дня" || p.Category != "Спорт" {
		t.Errorf("программа 0: %+v", p)
	}
	want := time.Date(2026, 9, 10, 12, 0, 0, 0, time.FixedZone("", 3*3600))
	if !p.Start.Equal(want) {
		t.Errorf("start = %v, want %v", p.Start, want)
	}
	if d := p.Stop.Sub(p.Start); d != time.Hour {
		t.Errorf("длительность = %v, want 1h", d)
	}
	// Передача без stop получает дефолтный час.
	if d := progs[2].Stop.Sub(progs[2].Start); d != time.Hour {
		t.Errorf("передача без stop: длительность %v, want 1h", d)
	}
}

// TestParseXMLTVGzip — телепрограмма часто отдаётся в .xml.gz.
func TestParseXMLTVGzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(xmltvSample)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	_, progs, err := ParseXMLTV(zr)
	if err != nil {
		t.Fatalf("ParseXMLTV(gzip): %v", err)
	}
	if len(progs) != 3 {
		t.Fatalf("передач %d, ожидали 3", len(progs))
	}
}

// TestXtreamNormalizeBase — адрес панели может прийти как host:port,
// с /player_api.php и query — всё это должно отрезаться.
func TestXtreamNormalizeBase(t *testing.T) {
	for _, in := range []string{
		"http://tv.example.com:8080",
		"http://tv.example.com:8080/",
		"http://tv.example.com:8080/player_api.php",
		"tv.example.com:8080",
		"http://tv.example.com:8080/get.php?username=u&password=p&type=m3u_plus",
	} {
		x := Xtream{BaseURL: in, Username: "u", Password: "p"}
		got, err := x.normalizeBase()
		if err != nil {
			t.Fatalf("normalizeBase(%q): %v", in, err)
		}
		if got != "http://tv.example.com:8080" {
			t.Errorf("normalizeBase(%q) = %q", in, got)
		}
		epg, err := x.EPGURL()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(epg, "/xmltv.php?") || !strings.Contains(epg, "username=u") {
			t.Errorf("EPGURL(%q) = %q", in, epg)
		}
	}
	// Пустой адрес — ошибка, а не паника.
	if _, err := (Xtream{}).normalizeBase(); err == nil {
		t.Error("пустой адрес панели должен быть ошибкой")
	}
}

// TestParseM3ULongLine — строки с длинными токенами (URL > 64 КБ) не должны
// ломать разбор.
func TestParseM3ULongLine(t *testing.T) {
	long := "https://example.org/live/index.m3u8?token=" + strings.Repeat("a", 200*1024)
	pl := fmt.Sprintf("#EXTM3U\n#EXTINF:-1 tvg-id=\"x\",Длинный\n%s\n", long)
	chans, err := ParseM3U(strings.NewReader(pl))
	if err != nil {
		t.Fatalf("ParseM3U: %v", err)
	}
	if len(chans) != 1 || len(chans[0].URL) != len(long) {
		t.Fatalf("длинный URL разобран неверно: %d каналов", len(chans))
	}
}
