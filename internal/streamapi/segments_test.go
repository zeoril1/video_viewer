package streamapi

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zeoril1/video_viewer/internal/segments"
)

func TestChapterSegments(t *testing.T) {
	var chapters []mediaChapter
	err := json.Unmarshal([]byte(`[
		{"start_time":"0.000000","end_time":"25.5","tags":{"title":"В предыдущих сериях"}},
		{"start_time":80,"end_time":150,"tags":{"title":"Opening Credits"}},
		{"start_time":150,"end_time":1900,"tags":{"title":"Chapter 03"}},
		{"start_time":1900,"end_time":1970,"tags":{"title":"Титры"}},
		{"start_time":1970,"end_time":2000,"tags":{"title":"Post-credits scene"}},
		{"start_time":2000,"end_time":2100,"tags":{"title":"End Credits"}}
	]`), &chapters)
	if err != nil {
		t.Fatal(err)
	}
	got := chapterSegments(chapters, 2100)
	if len(got) != 4 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Type != segments.Recap || got[0].End != 25.5 || got[1].Type != segments.Intro || got[2].End != 1970 || got[3].Start != 2000 {
		t.Fatalf("lost chapter type/bounds or post-credits scene: %+v", got)
	}
	for _, s := range got {
		if !s.AutoSkip || s.Source != "chapter" {
			t.Fatalf("expected local chapter: %+v", s)
		}
	}
}

func TestTracksReturnsCachedSegmentsAndMediaIdentity(t *testing.T) {
	id, magnet := "tt1234567", "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	key := segmentMediaKey(magnet, 3)
	hls := &hlsManager{probeCache: map[string]probeResult{probeKey(id, magnet, -1): {
		Duration: 2100, MediaKey: key, Segments: []segments.Segment{
			{Type: segments.Intro, Start: 70, End: 140, Source: "chapter", AutoSkip: true},
		},
	}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/films/{id}/tracks", handleTracks(hls, nil))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/films/"+id+"/tracks?magnet="+"magnet%3A%3Fxt%3Durn%3Abtih%3A0123456789abcdef0123456789abcdef01234567", nil))
	var data struct {
		MediaKey string             `json:"media_key"`
		Segments []segments.Segment `json:"segments"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &data) != nil || data.MediaKey != key || len(data.Segments) != 1 || data.Segments[0].End != 140 {
		t.Fatalf("tracks: status=%d body=%s", w.Code, w.Body)
	}
}

func TestChapterSegmentsRejectUnsafeRanges(t *testing.T) {
	chapter := func(title string, start, end float64) mediaChapter {
		c := mediaChapter{Start: ffprobeNum{Val: start}, End: ffprobeNum{Val: end}}
		c.Tags.Title = title
		return c
	}
	for _, c := range []mediaChapter{
		chapter("Chapter 01", 0, 60), chapter("Credits", 0, 120),
		chapter("Intro", 0, 900), chapter("Intro", 800, 880),
		chapter("Recap", 1300, 1360), chapter("Ending", 1700, 2101),
		chapter("Credits", 2000, 1900), chapter("Intro", math.NaN(), 90),
		chapter("Intro", 0, math.Inf(1)), chapter("Intro", -1, 90),
	} {
		if got := chapterSegments([]mediaChapter{c}, 2100); len(got) != 0 {
			t.Errorf("accepted %+v: %+v", c, got)
		}
	}
	got := chapterSegments([]mediaChapter{chapter("Intro", 40, 100), chapter("Recap", 80, 120)}, 2100)
	if len(got) != 2 || got[0].AutoSkip || got[1].AutoSkip {
		t.Fatalf("overlapping chapters auto-skipped: %+v", got)
	}
}

func TestSegmentMediaKey(t *testing.T) {
	hex := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	base32 := "magnet:?xt=urn:btih:AERUKZ4JVPG66AJDIVTYTK6N54ASGRLH"
	want := "segments.0123456789abcdef0123456789abcdef01234567.3"
	if got := segmentMediaKey(hex+"&dn=Release&tr=https://tracker.invalid/one", 3); got != want {
		t.Fatal(got)
	}
	if got := segmentMediaKey(base32, 3); got != want {
		t.Fatalf("base32 identity: %s", got)
	}
	if segmentMediaKey(hex, 4) == want || segmentMediaKey(hex, -1) != "" || segmentMediaKey("broken", 0) != "" {
		t.Fatal("media key must bind to a real file and valid torrent")
	}
}
