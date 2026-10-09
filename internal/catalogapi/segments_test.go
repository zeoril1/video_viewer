package catalogapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/segments"
)

func TestParseSegmentQueryRequiresEpisodeAndValidIdentity(t *testing.T) {
	for _, test := range []struct {
		id, query string
		valid     bool
		tmdb      int64
		season    int
	}{
		{"tt1234567", "season=1&episode=2&duration=2100", true, 0, 1},
		{"tt1234567", "tmdb=42&season=1&episode=2&duration=2100.5", true, 42, 1},
		{"tmdb-tv-42", "season=0&episode=1&duration=2100", true, 42, 0},
		{"tmdb-tv-42", "tmdb=43&season=1&episode=2&duration=2100", false, 0, 0},
		{"tmdb-movie-42", "season=1&episode=2&duration=2100", false, 0, 0},
		{"tt1234567", "season=1&duration=2100", false, 0, 0},
		{"tt1234567", "episode=2&duration=2100", false, 0, 0},
		{"tt1234567", "season=1&episode=0&duration=2100", false, 0, 0},
		{"tt1234567", "season=-1&episode=2&duration=2100", false, 0, 0},
		{"tt1234567", "season=1&episode=2&duration=NaN", false, 0, 0},
		{"tt1234567", "season=1&episode=2&duration=Inf", false, 0, 0},
		{"tt1234567", "season=1&episode=2&duration=0", false, 0, 0},
		{"arbitrary-url", "tmdb=42&season=1&episode=2&duration=2100", false, 0, 0},
	} {
		r := httptest.NewRequest(http.MethodGet, "/?"+test.query, nil)
		r.SetPathValue("id", test.id)
		q, ok := parseSegmentQuery(r)
		if ok != test.valid || ok && (q.TMDBID != test.tmdb || q.Season != test.season) {
			t.Errorf("%s?%s: query=%+v valid=%v", test.id, test.query, q, ok)
		}
	}
}

func TestSegmentsRouteDisabledAndInvalidParameters(t *testing.T) {
	t.Setenv("SEGMENTS_EXTERNAL_ENABLED", "false")
	server := NewServer(Config{})
	r := httptest.NewRequest(http.MethodGet, "/api/films/tmdb-tv-42/segments?season=1&episode=2&duration=2100", nil)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"disabled"`) || !strings.Contains(w.Body.String(), `"segments":[]`) {
		t.Fatalf("disabled route failed: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/films/tmdb-tv-42/segments?duration=2100", nil)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("incomplete episode accepted: %d", w.Code)
	}
}

func TestSegmentsHandlerPublicLookup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("imdb_id") != "tt1234567" {
			t.Errorf("missing IMDb fallback")
		}
		w.Write([]byte(`{"tmdb_id":42,"type":"tv","season":1,"episode":2,"intro":[{"start_ms":0,"end_ms":90000}]}`))
	}))
	defer upstream.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/films/{id}/segments", handleSegments(segments.NewProvider(segments.ProviderOptions{Endpoint: upstream.URL})))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/films/tt1234567/segments?season=1&episode=2&duration=2100", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"ready"`) || !strings.Contains(w.Body.String(), `"auto_skip":false`) {
		t.Fatalf("lookup failed: %d %s", w.Code, w.Body.String())
	}
}
