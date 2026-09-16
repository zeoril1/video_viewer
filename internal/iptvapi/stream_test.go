package iptvapi

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/iptv"
)

// TestRewriteProxyRewritesAllURIs — все ссылки провайдерского плейлиста
// (варианты, сегменты, ключ шифрования, init) уходят через /api/iptv/seg:
// клиент не видит ни адреса, ни логина провайдера.
func TestRewriteProxyRewritesAllURIs(t *testing.T) {
	m := newLiveManager(Config{DataDir: t.TempDir()})
	base, err := url.Parse("http://iptv.example.com:8080/live/user/pass/123/index.m3u8?token=abc")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(strings.Join([]string{
		"#EXTM3U",
		`#EXT-X-KEY:METHOD=AES-128,URI="key.bin?k=1",IV=0x1234`,
		`#EXT-X-MAP:URI="init.mp4"`,
		"#EXTINF:6.0,",
		"seg_00001.ts",
		"#EXTINF:6.0,",
		"http://cdn.other-host.net/media/seg_00002.ts",
		"#EXT-X-ENDLIST",
		"",
	}, "\n"))

	out := string(m.rewriteProxy(body, base, "UA-Custom", "http://ref.example/"))

	// Ни один провайдерский адрес не должен остаться в тексте.
	for _, leak := range []string{"iptv.example.com", "cdn.other-host.net", "pass", "key.bin", "init.mp4", "seg_00001.ts", "seg_00002.ts"} {
		if strings.Contains(out, leak) {
			t.Errorf("в плейлисте остался провайдерский адрес %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "#EXT-X-KEY:METHOD=AES-128,URI=\"/api/iptv/seg/") {
		t.Errorf("URI ключа не переписан: %s", out)
	}
	if !strings.Contains(out, "#EXT-X-MAP:URI=\"/api/iptv/seg/") {
		t.Errorf("URI init-сегмента не переписан: %s", out)
	}
	if !strings.Contains(out, "#EXT-X-ENDLIST") {
		t.Errorf("прочие теги должны остаться как есть: %s", out)
	}

	// Токены: относительные ссылки разрешаются от каталога плейлиста
	// (с сохранением query), абсолютная берётся как есть.
	toks := extractTokens(out)
	if len(toks) != 4 {
		t.Fatalf("токенов %d, ожидали 4: %v", len(toks), toks)
	}
	want := []string{
		"http://iptv.example.com:8080/live/user/pass/123/key.bin?k=1",
		"http://iptv.example.com:8080/live/user/pass/123/init.mp4",
		"http://iptv.example.com:8080/live/user/pass/123/seg_00001.ts",
		"http://cdn.other-host.net/media/seg_00002.ts",
	}
	got := map[string]bool{}
	for _, tok := range toks {
		ref, ok := m.lookupToken(tok)
		if !ok {
			t.Fatalf("токен %s не зарегистрирован", tok)
		}
		got[ref.url] = true
		if ref.ua != "UA-Custom" || ref.referer != "http://ref.example/" {
			t.Errorf("в токене потеряны заголовки канала: %+v", ref)
		}
	}
	for _, u := range want {
		if !got[u] {
			t.Errorf("не зарегистрирован upstream %q; получили %v", u, got)
		}
	}
}

// TestLookupTokenExpiry — просроченный токен не отдаётся (защита от SSRF).
func TestLookupTokenExpiry(t *testing.T) {
	m := newLiveManager(Config{DataDir: t.TempDir()})
	tok := m.addToken("http://example.org/a.ts", "", "")
	if _, ok := m.lookupToken(tok); !ok {
		t.Fatal("свежий токен должен находиться")
	}
	if _, ok := m.lookupToken("00000000000000000000000000000000"); ok {
		t.Fatal("чужой токен не должен находиться")
	}
	m.mu.Lock()
	m.tokens[tok] = tokenRef{url: "http://example.org/a.ts", expires: time.Now().Add(-time.Second)}
	m.mu.Unlock()
	if _, ok := m.lookupToken(tok); ok {
		t.Fatal("просроченный токен не должен находиться")
	}
}

// TestRewriteHLSFFmpegSegments — в режиме ffmpeg ссылки на локальные сегменты
// переписываются на /api/iptv/live/{id}/{name}.
func TestRewriteHLSFFmpegSegments(t *testing.T) {
	body := []byte("#EXTM3U\n#EXT-X-VERSION:6\n#EXTINF:4.0,\nseg_00001.ts\n#EXTINF:4.0,\nseg_00002.ts\n")
	out := string(rewriteHLS(body, func(raw string) string {
		return "/api/iptv/live/42/" + strings.TrimSpace(raw)
	}))
	if strings.Contains(out, "\nseg_00001.ts") {
		t.Errorf("локальные имена сегментов не переписаны:\n%s", out)
	}
	if strings.Count(out, "/api/iptv/live/42/seg_") != 2 {
		t.Errorf("ожидали 2 ссылки на сегменты:\n%s", out)
	}
	if !strings.Contains(out, "#EXT-X-VERSION:6") {
		t.Errorf("теги HLS должны сохраняться:\n%s", out)
	}
}

func TestIsHLSPlaylist(t *testing.T) {
	if !isHLSPlaylist([]byte("#EXTM3U\n#EXT-X-VERSION:3")) {
		t.Error("плейлист не распознан")
	}
	if isHLSPlaylist([]byte{0x47, 0x40, 0x11, 0x10}) {
		t.Error("MPEG-TS не должен считаться плейлистом")
	}
}

func TestSafeName(t *testing.T) {
	ok := []string{"index.m3u8", "seg_00001.ts", "init.mp4"}
	bad := []string{"", "../evil", "a/b", `a\b`, "..", "/etc/passwd"}
	for _, n := range ok {
		if !safeName(n) {
			t.Errorf("safeName(%q) = false, ожидали true", n)
		}
	}
	for _, n := range bad {
		if safeName(n) {
			t.Errorf("safeName(%q) = true, ожидали false", n)
		}
	}
}

// TestNowNext — «сейчас/далее» из списка передач (если текущей нет —
// следующая отдаётся как next).
func TestNowNext(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 30, 0, 0, time.UTC)
	list := []db.IPTVProgram{
		{Title: "Утро", Start: now.Add(-2 * time.Hour), Stop: now.Add(-time.Hour)},
		{Title: "Новости", Start: now.Add(-30 * time.Minute), Stop: now.Add(30 * time.Minute)},
		{Title: "Фильм", Start: now.Add(30 * time.Minute), Stop: now.Add(2 * time.Hour)},
	}
	cur, next := nowNext(list, now)
	if cur == nil || cur.Title != "Новости" {
		t.Fatalf("текущая передача: %+v", cur)
	}
	if next == nil || next.Title != "Фильм" {
		t.Fatalf("следующая передача: %+v", next)
	}

	// Между передачами: текущей нет, показываем следующую.
	gap := []db.IPTVProgram{{Title: "Позже", Start: now.Add(10 * time.Minute), Stop: now.Add(time.Hour)}}
	cur, next = nowNext(gap, now)
	if cur != nil {
		t.Errorf("текущей передачи быть не должно: %+v", cur)
	}
	if next == nil || next.Title != "Позже" {
		t.Fatalf("next: %+v", next)
	}
	if _, n := nowNext(nil, now); n != nil {
		t.Error("пустой список должен давать nil")
	}
}

// TestMatchEPG — порядок сопоставления канала с программой: точный tvg-id,
// название канала, нормализованный tvg-id, затем упрощённые ключи.
func TestMatchEPG(t *testing.T) {
	byID := map[string]string{"matchtv.ru": "Матч ТВ.ru", "2x2.ru": "2x2.ru"}
	byNorm := map[string]string{
		"матчтв": "Матч ТВ.ru",
		"2x2ru":  "2x2.ru",
		"тнтru":  "ТНТ.ru",
	}
	// Упрощённые ключи: сюда попадают id программы без «.ru».
	byBase := map[string]string{
		"матчтв": "Матч ТВ.ru",
		"2x2":    "2x2.ru",
		"5канал": "5 канал.ru",
	}
	cases := []struct {
		in   db.IPTVChannel
		want string
	}{
		{db.IPTVChannel{EPGID: "MatchTV.ru"}, "Матч ТВ.ru"},     // точный id (регистр не важен)
		{db.IPTVChannel{Name: "Матч ТВ (1080p)"}, "Матч ТВ.ru"}, // по названию канала
		{db.IPTVChannel{EPGID: "2x2.ru@SD"}, "2x2.ru"},          // реальный id iptv-org (@SD — пометка качества)
		{db.IPTVChannel{EPGID: "ТНТ.ru@SD"}, "ТНТ.ru"},          // нормализованный id
		// iptv-org: id «Channel5.ru@SD», имя «5 Канал»; open-epg: «5 канал.ru».
		{db.IPTVChannel{EPGID: "Channel5.ru@SD", Name: "5 Канал"}, "5 канал.ru"},
		{db.IPTVChannel{Name: "Неизвестный канал"}, ""},
	}
	for _, c := range cases {
		if got := matchEPG(c.in, byID, byNorm, byBase); got != c.want {
			t.Errorf("matchEPG(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestToDBChannelsHeaders — заголовки канала: свои, иначе общие плейлиста,
// иначе браузерный User-Agent (часть провайдеров без него не отдаёт поток).
func TestToDBChannelsHeaders(t *testing.T) {
	pl := db.IPTVPlaylist{ID: 7, UA: "PL-UA", Referer: "http://pl.example/"}
	chans := []iptv.Channel{
		{ExtID: "1", Name: "Свой UA", URL: "http://a/1.m3u8", IsHLS: true, UA: "CH-UA", Referer: "http://ch.example/", Num: 1},
		{ExtID: "2", Name: "Заголовки плейлиста", URL: "http://a/2.m3u8", IsHLS: true, Num: 2},
		{ExtID: "3", Name: "Без заголовков", URL: "http://a/3.ts", Num: 3},
	}
	out := toDBChannels(chans, pl)
	if len(out) != 3 {
		t.Fatalf("каналов %d, ожидали 3", len(out))
	}
	if out[0].UA != "CH-UA" || out[0].Referer != "http://ch.example/" {
		t.Errorf("свои заголовки канала потеряны: %+v", out[0])
	}
	if out[1].UA != "PL-UA" || out[1].Referer != "http://pl.example/" {
		t.Errorf("не подставились заголовки плейлиста: %+v", out[1])
	}
	if out[2].UA != "PL-UA" || out[2].Referer != "http://pl.example/" {
		t.Errorf("заголовки плейлиста должны применяться и к каналу без своих: %+v", out[2])
	}
	// Без заголовков нигде — подставляем браузерный UA.
	bare := toDBChannels(chans, db.IPTVPlaylist{ID: 7})
	if bare[2].UA != defaultUA {
		t.Errorf("не подставился UA по умолчанию: %+v", bare[2])
	}
	if out[0].PlaylistID != 7 {
		t.Errorf("playlist_id не проставлен: %+v", out[0])
	}
}

// extractTokens вытаскивает токены из переписанного плейлиста.
func extractTokens(playlist string) []string {
	var out []string
	for _, part := range strings.Split(playlist, "/api/iptv/seg/")[1:] {
		tok := part
		if i := strings.IndexAny(part, "\"\n\r"); i >= 0 {
			tok = part[:i]
		}
		if tok != "" {
			out = append(out, tok)
		}
	}
	return out
}
