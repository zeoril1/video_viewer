package iptvapi

// segment_test.go — проксирование сегментов IPTV-потока: вложенный плейлист
// должен переписываться и тогда, когда провайдер отдаёт его без
// Content-Length (chunked) — иначе относительные ссылки сегментов уходят в
// 404 (так и было: 720p/seg_*.ts отдавался как есть).

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleSegmentRewritesChunkedPlaylist(t *testing.T) {
	const variant = "720p/index.m3u8"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/live/"+variant {
			http.NotFound(w, r)
			return
		}
		// Без Content-Length: пишем и сбрасываем буфер — ответ chunked.
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "#EXTINF:4.000000,\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "seg_00001.ts\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	cfg := Config{DataDir: t.TempDir()}
	s := &Server{cfg: cfg, live: newLiveManager(cfg)}
	tok := s.live.addToken(upstream.URL+"/live/"+variant, "UA", "http://ref/")

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/iptv/seg/"+tok, nil)
	r.SetPathValue("token", tok)
	s.handleSegment(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, тело %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "seg_00001.ts") && !strings.Contains(body, "/api/iptv/seg/") {
		t.Fatalf("относительная ссылка не переписана: %q", body)
	}
	// Сегмент должен идти через наш токен и вести на upstream-адрес рядом
	// с плейлистом (RFC 3986: относительно /live/720p/).
	var segToken string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "/api/iptv/seg/") {
			segToken = strings.TrimPrefix(line, "/api/iptv/seg/")
		}
	}
	if segToken == "" {
		t.Fatalf("в плейлисте нет переписанных ссылок: %q", body)
	}
	ref, ok := s.live.lookupToken(segToken)
	if !ok {
		t.Fatal("токен сегмента не найден")
	}
	if want := upstream.URL + "/live/720p/seg_00001.ts"; ref.url != want {
		t.Errorf("upstream сегмента = %q, want %q", ref.url, want)
	}
}
