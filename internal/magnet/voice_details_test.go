package magnet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/encoding/charmap"
)

type voiceDetailsRoundTripper func(*http.Request) (*http.Response, error)

func (f voiceDetailsRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func detailsResponse(r *http.Request, body, contentType string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

type unreadableDetailsBody struct{ t *testing.T }

func (b unreadableDetailsBody) Read([]byte) (int, error) {
	b.t.Error("non-HTML body must be rejected before reading torrent/video bytes")
	return 0, io.EOF
}

func (unreadableDetailsBody) Close() error { return nil }

func TestJackettVoicesFromRSSDescription(t *testing.T) {
	rss := `<rss><channel><item><title>Unchanged movie title (2026) D</title>
<description><![CDATA[<b>Перевод:</b> MovieDalen, HDRezka Studio<br><b>Аудио 2:</b> TVShows<br><p>В ролях: LostFilm</p>]]></description>
<comments>https://rutor.info/torrent/1109187/film/</comments>
<link>magnet:?xt=urn:btih:1111222233334444555566667777888899990000</link>
</item></channel></rss>`
	items := parseJackettItems(rss)
	if len(items) != 1 || items[0].detailsURL != "https://rutor.info/torrent/1109187/film/" {
		t.Fatalf("parsed items: %#v", items)
	}
	want := []string{"MovieDalen", "HDRezka Studio", "TVShows"}
	if got := voicesFromDescription(items[0].description); !reflect.DeepEqual(got, want) {
		t.Fatalf("voices %v, want %v", got, want)
	}
	if items[0].Title != "Unchanged movie title (2026) D" {
		t.Fatalf("description changed title: %q", items[0].Title)
	}
}

func TestVoicesMetadataRejectsSynopsisAndVideoNames(t *testing.T) {
	description := "Сюжет: Red Head Sound и LostFilm\nПеревод: Movie-Dalen\nAudio #1\nTitle : HDRezka Studio\nAudio #2\nTitle : ViruseProject\nVideo\nTitle : TVShows\nАудио 3: 4K HDR"
	want := []string{"MovieDalen", "HDRezka Studio", "ViruseProject"}
	if got := voicesFromDescription(description); !reflect.DeepEqual(got, want) {
		t.Fatalf("voices %v, want %v", got, want)
	}
}

func TestVoicesFromConfirmedNoNameTranslationLines(t *testing.T) {
	// Public topic descriptions 1886030, 1884069 and 1884723 respectively.
	for _, tt := range []struct{ line, voice string }{
		{"Перевод: Многоголосый (Смотри кино)", "Смотри кино"},
		{"Перевод: Дублированный (Dragon Money Studio, звук с TS)", "Dragon Money Studio"},
		{"Перевод: Профессиональный двухголосый (ViruseProject)", "ViruseProject"},
	} {
		if got := voicesFromDescription(tt.line); !reflect.DeepEqual(got, []string{tt.voice}) {
			t.Errorf("%q: %v, want %s", tt.line, got, tt.voice)
		}
	}
}

func TestVoicesFromConfirmedRedHeadSoundRelease(t *testing.T) {
	// Audio labels from Rutor release 1115875. Red Head Sound belongs to this
	// release itself, unlike its mention among release 1109187's recommendations.
	description := `<b>Перевод:</b><br>
* Дублированный (неофициальный) | Red Head Sound<br>
<b>Аудио:</b> АC3, 48 kHz, 6 ch, ~640.00 kbps avg | русский | DUB | Red Head Sound<br>
<pre>Audio #1
Title : DUB | MovieDalen
Audio #2
Title : DUB | неофициальный | Red Head Sound
Audio #3
Title : DUB | неофициальный | HDRezka Studio
Text #1
Title : Full | iTunes
</pre>`
	want := []string{"Red Head Sound", "MovieDalen", "HDRezka Studio"}
	if got := voicesFromDescription(description); !reflect.DeepEqual(got, want) {
		t.Fatalf("release 1115875 voices = %v, want %v", got, want)
	}
}

func TestJackettDetailsRealRutorFixtureAndCache(t *testing.T) {
	fixture, err := os.ReadFile("testdata/rutor_voice_details.html")
	if err != nil {
		t.Fatal(err)
	}
	e := newVoiceDetailsEnricher()
	var calls atomic.Int32
	e.client.Transport = voiceDetailsRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "rutor.info" || !strings.HasPrefix(r.URL.Path, "/torrent/") {
			t.Errorf("unexpected request: %s", r.URL.Redacted())
		}
		return detailsResponse(r, string(fixture), "text/html; charset=utf-8"), nil
	})
	want := []string{"MovieDalen", "HDRezka Studio", "TVShows", "ViruseProject"}
	for range 2 {
		got := e.lookup(context.Background(), "rutor", "https://rutor.info/torrent/1109187/film/", "1111222233334444555566667777888899990000")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("voices %v, want %v; related RHS must be excluded", got, want)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cache missed: %d requests", calls.Load())
	}
	e.lookup(context.Background(), "rutor", "https://rutor.info/torrent/1109187/film/", "2222222233334444555566667777888899990000")
	if calls.Load() != 2 {
		t.Fatalf("new torrent hash reused old topic metadata: %d requests", calls.Load())
	}
}

func TestJackettSearchEnrichesWithoutDownloadingMedia(t *testing.T) {
	fixture, err := os.ReadFile("testdata/rutor_voice_details.html")
	if err != nil {
		t.Fatal(err)
	}
	var jackettCalls, detailCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jackettCalls.Add(1)
		fmt.Fprint(w, `<rss><channel><item><title>Original title</title><description>Заголовок</description><comments>https://rutor.info/torrent/1109187/film/</comments><link>magnet:?xt=urn:btih:1111222233334444555566667777888899990000</link></item></channel></rss>`)
	}))
	defer srv.Close()
	j := NewJackett(srv.URL, "test", "rutor", nil)
	j.voiceDetails.client.Transport = voiceDetailsRoundTripper(func(r *http.Request) (*http.Response, error) {
		detailCalls.Add(1)
		return detailsResponse(r, string(fixture), "text/html; charset=utf-8"), nil
	})
	res, err := j.Search(context.Background(), "test", 60)
	if err != nil || len(res) != 1 {
		t.Fatalf("search: results=%v err=%v", res, err)
	}
	if res[0].Title != "Original title" || len(res[0].Voices) != 4 {
		t.Fatalf("result title/voices: %q %v", res[0].Title, res[0].Voices)
	}
	if jackettCalls.Load() != 1 || detailCalls.Load() != 1 {
		t.Fatalf("requests: Jackett=%d HTML=%d; no torrent/video expected", jackettCalls.Load(), detailCalls.Load())
	}
}

func TestJackettAllEnrichesOnlyKnownTrackerDetails(t *testing.T) {
	fixture, err := os.ReadFile("testdata/rutor_voice_details.html")
	if err != nil {
		t.Fatal(err)
	}
	var detailCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2.0/indexers/all/results/torznab/api" {
			t.Errorf("unexpected Jackett path: %s", r.URL.Path)
		}
		fmt.Fprint(w, `<rss><channel>
<item><title>Known tracker release</title><comments>https://rutor.info/torrent/1109187/film/</comments><link>magnet:?xt=urn:btih:1111222233334444555566667777888899990000</link></item>
<item><title>Unknown tracker release</title><comments>https://unknown.example/torrent/123/</comments><link>magnet:?xt=urn:btih:2222222233334444555566667777888899990000</link></item>
</channel></rss>`)
	}))
	defer srv.Close()
	j := NewJackett(srv.URL, "test", "all", nil)
	j.voiceDetails.client.Transport = voiceDetailsRoundTripper(func(r *http.Request) (*http.Response, error) {
		detailCalls.Add(1)
		if r.URL.Host != "rutor.info" {
			t.Errorf("aggregate lookup fetched unknown tracker: %s", r.URL.Redacted())
		}
		return detailsResponse(r, string(fixture), "text/html; charset=utf-8"), nil
	})
	res, err := j.Search(context.Background(), "test", 60)
	if err != nil || len(res) != 2 {
		t.Fatalf("aggregate search: results=%v err=%v", res, err)
	}
	if res[0].Title != "Known tracker release" || len(res[0].Voices) != 4 ||
		res[1].Title != "Unknown tracker release" || len(res[1].Voices) != 0 || detailCalls.Load() != 1 {
		t.Fatalf("aggregate voices: first=%v second=%v HTML requests=%d", res[0].Voices, res[1].Voices, detailCalls.Load())
	}
}

func TestDetailsURLAllowlist(t *testing.T) {
	for _, tt := range []struct {
		id, url string
		allowed bool
	}{
		{"rutor", "https://rutor.info/torrent/123/film/", true},
		{"noname-club", "https://nnmclub.to/forum/viewtopic.php?t=123", true},
		{"rutracker-ru", "http://rutracker.ru/forum/viewtopic.php?t=123", true},
		{"bigfangroup", "https://bigfangroup.org/details.php?id=123", true},
		{"rutor", "https://rutor.info.evil.test/torrent/123/", false},
		{"rutor", "https://127.0.0.1/torrent/123/", false},
		{"rutor", "http://192.168.1.1/torrent/123/", false},
		{"rutor", "http://rutor.info:9117/torrent/123/", false},
		{"rutor", "https://user:secret@rutor.info/torrent/123/", false},
		{"rutor", "https://rutor.info/torrent/123/../../download", false},
		{"rutor", "https://rutor.info/download/123/", false},
		{"rutor", "https://rutor.info/torrent/not-a-release/", false},
		{"all", "https://rutor.info/torrent/123/", false},
	} {
		if got := allowedDetailsURL(tt.id, tt.url); got != tt.allowed {
			t.Errorf("%s %s: %t, want %t", tt.id, tt.url, got, tt.allowed)
		}
	}
	for _, raw := range []string{"127.0.0.1", "192.168.1.2", "10.0.0.1", "172.18.0.4", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "::ffff:127.0.0.1", "198.18.0.1"} {
		if publicDetailsIP(netip.MustParseAddr(raw)) {
			t.Errorf("private/special IP allowed: %s", raw)
		}
	}
	if !publicDetailsIP(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("public address rejected")
	}
	if conn, err := dialPublicDetails(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		conn.Close()
		t.Fatal("dialer reached a private address")
	}
	transport := newVoiceDetailsEnricher().client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("release descriptions must ignore global HTTP proxies")
	}
}

func TestDetailsRejectsRedirectsOversizedAndNonHTML(t *testing.T) {
	for _, mode := range []string{"redirect", "oversized", "torrent"} {
		t.Run(mode, func(t *testing.T) {
			e := newVoiceDetailsEnricher()
			var calls atomic.Int32
			e.client.Transport = voiceDetailsRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				switch mode {
				case "redirect":
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"http://127.0.0.1/admin"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				case "oversized":
					return detailsResponse(r, strings.Repeat("x", maxVoiceDetailsBytes+1), "text/html"), nil
				default:
					resp := detailsResponse(r, "", "application/x-bittorrent")
					resp.Body = unreadableDetailsBody{t}
					return resp, nil
				}
			})
			voices, err := e.fetch(context.Background(), "rutor", "https://rutor.info/torrent/123/")
			if err == nil || len(voices) != 0 || calls.Load() != 1 {
				t.Fatalf("voices=%v err=%v requests=%d", voices, err, calls.Load())
			}
		})
	}
}

func TestDetailsLimitConcurrencyAndFailureIsolation(t *testing.T) {
	e := newVoiceDetailsEnricher()
	var calls, active, peak atomic.Int32
	e.client.Transport = voiceDetailsRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
		}
		select {
		case <-time.After(2 * time.Millisecond):
		case <-r.Context().Done():
		}
		return nil, fmt.Errorf("tracker unavailable")
	})
	items := make([]jackettItem, maxVoiceDetails+5)
	out := make([]Result, len(items))
	for i := range items {
		items[i].detailsURL = fmt.Sprintf("https://rutor.info/torrent/%d/", i+1)
		out[i] = Result{Title: "Preserved", Magnet: "magnet:?xt=urn:btih:1111222233334444555566667777888899990000", Voices: []string{"MovieDalen"}}
	}
	e.enrich(context.Background(), "rutor", items, out)
	if calls.Load() != maxVoiceDetails || peak.Load() > 3 {
		t.Fatalf("details requests=%d peak=%d", calls.Load(), peak.Load())
	}
	for _, r := range out {
		if r.Title != "Preserved" || !reflect.DeepEqual(r.Voices, []string{"MovieDalen"}) {
			t.Fatalf("failed enrichment discarded source: %#v", r)
		}
	}
	for i := range voiceDetailsCacheMax + 5 {
		e.lookup(context.Background(), "rutor", fmt.Sprintf("https://rutor.info/torrent/%d/", 1000+i), "1111222233334444555566667777888899990000")
	}
	if len(e.cache) != voiceDetailsCacheMax {
		t.Fatalf("unbounded cache: %d", len(e.cache))
	}
}

func TestDetailsDecodesNoNameAndSkipsOtherPosts(t *testing.T) {
	body := `<div class="postbody"><b>Перевод:</b> MovieDalen<br><b>Аудио 2:</b> TVShows</div><div class="postbody">Перевод: Red Head Sound</div>`
	encoded, err := charmap.Windows1251.NewEncoder().String(body)
	if err != nil {
		t.Fatal(err)
	}
	e := newVoiceDetailsEnricher()
	e.client.Transport = voiceDetailsRoundTripper(func(r *http.Request) (*http.Response, error) {
		return detailsResponse(r, encoded, "text/html"), nil
	})
	voices, err := e.fetch(context.Background(), "noname-club", "https://nnmclub.to/forum/viewtopic.php?t=123")
	if err != nil || !reflect.DeepEqual(voices, []string{"MovieDalen", "TVShows"}) {
		t.Fatalf("voices=%v err=%v", voices, err)
	}
}

func TestDedupJackettMergesVoicesAndPreservesFirstTitle(t *testing.T) {
	m := "magnet:?xt=urn:btih:1111222233334444555566667777888899990000"
	got := dedupJackettResults([]Result{
		{Title: "First", Magnet: m, Voices: []string{"MovieDalen", "HDRezka"}},
		{Title: "Second", Magnet: m, Voices: []string{"HDRezka Studio", "TVShows", "MovieDalen"}},
	})
	if len(got) != 1 || got[0].Title != "First" || !reflect.DeepEqual(got[0].Voices, []string{"MovieDalen", "HDRezka Studio", "TVShows"}) {
		t.Fatalf("dedup result: %#v", got)
	}
}
