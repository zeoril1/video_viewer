package streamapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isTextSubtitleCodec: текстовые кодеки включаются, растровые — нет.
func TestIsTextSubtitleCodec(t *testing.T) {
	text := []string{"subrip", "srt", "ass", "ssa", "mov_text", "webvtt", "text"}
	for _, c := range text {
		if !isTextSubtitleCodec(c) {
			t.Errorf("isTextSubtitleCodec(%q) = false, want true", c)
		}
	}
	bitmap := []string{"hdmv_pgs_subtitle", "dvd_subtitle", "hdmv_text_subtitle", "xsub", "dvb_subtitle"}
	for _, c := range bitmap {
		if isTextSubtitleCodec(c) {
			t.Errorf("isTextSubtitleCodec(%q) = true, want false", c)
		}
	}
}

// subtitleLabel: title > язык > «Субтитры N».
func TestSelectSubtitleAfterBitmapStreams(t *testing.T) {
	// Video=0, audio=1, PGS=2, SRT=3, PGS=4, ASS=5.
	items := []subtitleTrack{
		{Index: 3, Ordinal: 0, Codec: "subrip"},
		{Index: 5, Ordinal: 1, Codec: "ass"},
	}
	for ordinal, index := range []int{3, 5} {
		got, err := selectSubtitle(items, ordinal)
		if err != nil || got.Index != index {
			t.Fatalf("subtitle %d: got %+v, %v; want stream %d", ordinal, got, err, index)
		}
	}
	if _, err := selectSubtitle(items, 2); err == nil {
		t.Fatal("missing subtitle accepted")
	}
}

func TestSubtitleLabel(t *testing.T) {
	if got := subtitleLabel(subtitleTrack{Ordinal: 0, Title: "Русские", Language: "rus"}); got != "Русские" {
		t.Errorf("label(title) = %q, want Русские", got)
	}
	if got := subtitleLabel(subtitleTrack{Ordinal: 1, Language: "eng"}); got != "ENG" {
		t.Errorf("label(lang) = %q, want ENG", got)
	}
	if got := subtitleLabel(subtitleTrack{Ordinal: 2}); got != "Субтитры 3" {
		t.Errorf("label(fallback) = %q, want Субтитры 3", got)
	}
}

// newFakeHlsSession — hlsManager с сессией, чьи плейлисты повторяют вывод
// ffmpeg 6.1 для HLS с субтитрами (-var_stream_map/-master_pl_name).
func newFakeHlsSession(t *testing.T, subs int, subsLabel string) *hlsManager {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("master.m3u8", ""+
		"#EXTM3U\n"+
		"#EXT-X-VERSION:7\n"+
		"#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subtitle\",NAME=\"subtitle_0\",DEFAULT=YES,URI=\"playlist_vtt.m3u8\"\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=105600,RESOLUTION=320x240,CODECS=\"avc1.f4000c,mp4a.40.2\",SUBTITLES=\"subtitle\"\n"+
		"playlist.m3u8\n")
	write("playlist.m3u8", ""+
		"#EXTM3U\n"+
		"#EXT-X-VERSION:7\n"+
		"#EXT-X-TARGETDURATION:12\n"+
		"#EXT-X-MEDIA-SEQUENCE:0\n"+
		"#EXT-X-INDEPENDENT-SEGMENTS\n"+
		"#EXT-X-MAP:URI=\"init.mp4\"\n"+
		"#EXTINF:12.000000,\n"+
		"seg_00000.m4s\n")
	write("playlist_vtt.m3u8", ""+
		"#EXTM3U\n"+
		"#EXT-X-VERSION:7\n"+
		"#EXT-X-TARGETDURATION:12\n"+
		"#EXT-X-MEDIA-SEQUENCE:0\n"+
		"#EXTINF:12.000000,\n"+
		"playlist0.vtt\n")
	m := newHLSManager("http://127.0.0.1:8082")
	m.sessions["tt123"] = &hlsSession{
		id: "tt123", magnet: "magnet:?xt=urn:btih:ABCD", file: 2, track: 1, quality: "source",
		subs: subs, subsLabel: subsLabel, dir: dir,
		playlist: filepath.Join(dir, "playlist.m3u8"),
	}
	return m
}

// serveMaster: реальное имя субтитров, DEFAULT=NO, абсолютные URL плейлистов.
func TestServeMasterRewrite(t *testing.T) {
	m := newFakeHlsSession(t, 0, "Русские")
	req := httptest.NewRequest(http.MethodGet, "/api/films/tt123/hls.m3u8?magnet=m&track=1&file=2", nil)
	req.SetPathValue("id", "tt123")
	rec := httptest.NewRecorder()
	m.serveMaster(rec, req, m.sessions["tt123"])

	body := rec.Body.String()
	for _, want := range []string{
		`NAME="Русские"`,
		`DEFAULT=NO`,
		`URI="/api/films/tt123/hls/subs/0.m3u8?magnet=magnet%3A%3Fxt%3Durn%3Abtih%3AABCD&track=1&file=2"`,
		"/api/films/tt123/hls/pl.m3u8?magnet=magnet%3A%3Fxt%3Durn%3Abtih%3AABCD&track=1&file=2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("master-плейлист не содержит %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "subtitle_0") || strings.Contains(body, "DEFAULT=YES") {
		t.Errorf("master-плейлист содержит дефолтное имя/DEFAULT=YES:\n%s", body)
	}
}

// serveMediaPlaylistFrom: init/seg переписываются в абсолютные URL сегментов.
func TestServeMediaPlaylistRewrite(t *testing.T) {
	m := newFakeHlsSession(t, 0, "Русские")
	req := httptest.NewRequest(http.MethodGet, "/api/films/tt123/hls/pl.m3u8?magnet=m&track=1&file=2", nil)
	req.SetPathValue("id", "tt123")
	rec := httptest.NewRecorder()
	m.serveMediaPlaylistFrom(rec, req, m.sessions["tt123"])

	body := rec.Body.String()
	for _, want := range []string{
		`URI="/api/films/tt123/hls/segments/init.mp4?magnet=magnet%3A%3Fxt%3Durn%3Abtih%3AABCD&track=1&file=2"`,
		"/api/films/tt123/hls/segments/seg_00000.m4s?magnet=magnet%3A%3Fxt%3Durn%3Abtih%3AABCD&track=1&file=2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("media-плейлист не содержит %q:\n%s", want, body)
		}
	}
}

// serveSubPlaylist: .vtt переписываются в абсолютные URL сегментов.
func TestServeSubPlaylistRewrite(t *testing.T) {
	m := newFakeHlsSession(t, 0, "Русские")
	req := httptest.NewRequest(http.MethodGet,
		"/api/films/tt123/hls/subs/0.m3u8?magnet="+url.QueryEscape("magnet:?xt=urn:btih:ABCD")+"&track=1&file=2", nil)
	req.SetPathValue("id", "tt123")
	rec := httptest.NewRecorder()
	m.serveSubPlaylist(rec, req)

	body := rec.Body.String()
	want := "/api/films/tt123/hls/segments/playlist0.vtt?magnet=magnet%3A%3Fxt%3Durn%3Abtih%3AABCD&track=1&file=2"
	if !strings.Contains(body, want) {
		t.Errorf("субтитр-плейлист не содержит %q:\n%s", want, body)
	}
}

// subsParam: отсутствующий/битый параметр -> -1 (без субтитров).
func TestSubsParam(t *testing.T) {
	if got := subsParam(httptest.NewRequest(http.MethodGet, "/x", nil)); got != -1 {
		t.Errorf("subsParam(без параметра) = %d, want -1", got)
	}
	if got := subsParam(httptest.NewRequest(http.MethodGet, "/x?subs=abc", nil)); got != -1 {
		t.Errorf("subsParam(abc) = %d, want -1", got)
	}
	if got := subsParam(httptest.NewRequest(http.MethodGet, "/x?subs=2", nil)); got != 2 {
		t.Errorf("subsParam(2) = %d, want 2", got)
	}
}
