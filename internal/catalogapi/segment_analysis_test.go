package catalogapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/segments"
)

const segmentStoreTestKey = "segments.0123456789abcdef0123456789abcdef01234567.2"

type fakeSegmentAnalysisStore struct {
	record segments.AnalysisRecord
	found  bool
	err    error
	writes int
}

func (s *fakeSegmentAnalysisStore) GetSegmentAnalysis(context.Context, string) (segments.AnalysisRecord, bool, error) {
	return s.record, s.found, s.err
}
func (s *fakeSegmentAnalysisStore) SaveSegmentAnalysis(_ context.Context, record segments.AnalysisRecord) error {
	s.writes++
	s.record = record
	s.found = true
	return s.err
}
func catalogAnalysisRecord() segments.AnalysisRecord {
	frames := make([]uint32, 4200)
	frames[0], frames[1] = 123, 456
	return segments.AnalysisRecord{MediaKey: segmentStoreTestKey, FilmID: "tt1234567", Season: 1, Episode: 2, Duration: 2100, Version: 1, Status: segments.StatusReady, Step: 0.5, Fingerprint: frames, Segments: []segments.Segment{{Type: segments.Intro, Start: 10, End: 85, Source: "audio_match", AutoSkip: true}}}
}

func TestSegmentsPublicExactFileRangesOverrideExternalWithoutFingerprint(t *testing.T) {
	store := &fakeSegmentAnalysisStore{record: catalogAnalysisRecord(), found: true}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tmdb_id":42,"type":"tv","season":1,"episode":2,"intro":[{"start_ms":0,"end_ms":90000}],"credits":[{"start_ms":2000000,"end_ms":2100000}]}`))
	}))
	defer upstream.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/films/{id}/segments", handleSegments(segments.NewProvider(segments.ProviderOptions{Endpoint: upstream.URL}), store))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/films/tt1234567/segments?tmdb=42&season=1&episode=2&duration=2100&media_key="+segmentStoreTestKey, nil))
	var result segmentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Status != segments.StatusReady || result.AnalysisStatus != segments.StatusReady || result.MediaKey != segmentStoreTestKey || len(result.Segments) != 2 || result.Segments[0].Start != 10 || !result.Segments[0].AutoSkip || result.Segments[1].AutoSkip {
		t.Fatalf("exact-file precedence: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "fingerprint") || strings.Contains(w.Body.String(), "step") {
		t.Fatalf("private fingerprint leaked: %s", w.Body.String())
	}
}

func TestSegmentsPublicRejectsForeignMetadataAndRetainsExternalFallback(t *testing.T) {
	for name, change := range map[string]func(*segments.AnalysisRecord){
		"media":    func(r *segments.AnalysisRecord) { r.MediaKey = "segments.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.2" },
		"film":     func(r *segments.AnalysisRecord) { r.FilmID = "tt7654321" },
		"season":   func(r *segments.AnalysisRecord) { r.Season = 2 },
		"episode":  func(r *segments.AnalysisRecord) { r.Episode = 3 },
		"duration": func(r *segments.AnalysisRecord) { r.Duration = 2000 },
		"version":  func(r *segments.AnalysisRecord) { r.Version = segments.AnalysisVersion + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			record := catalogAnalysisRecord()
			change(&record)
			store := &fakeSegmentAnalysisStore{record: record, found: true}
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/films/{id}/segments", handleSegments(segments.NewProvider(segments.ProviderOptions{Disabled: true}), store))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/films/tt1234567/segments?season=1&episode=2&duration=2100&media_key="+segmentStoreTestKey, nil))
			if w.Code != 200 || strings.Contains(w.Body.String(), "audio_match") || !strings.Contains(w.Body.String(), `"analysis_status":"pending"`) {
				t.Fatalf("mixed release identity: %d %s", w.Code, w.Body.String())
			}
		})
	}
	store := &fakeSegmentAnalysisStore{err: errors.New("offline")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/films/{id}/segments", handleSegments(segments.NewProvider(segments.ProviderOptions{Disabled: true}), store))
	for _, key := range []string{segmentStoreTestKey, "invalid"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/films/tt1234567/segments?season=1&episode=2&duration=2100&media_key="+key, nil))
		want := 200
		if key == "invalid" {
			want = 400
		}
		if w.Code != want {
			t.Fatalf("key %s: %d %s", key, w.Code, w.Body.String())
		}
	}
}

func TestInternalSegmentAnalysisValidatesWritesBeforeReplacingStore(t *testing.T) {
	store := &fakeSegmentAnalysisStore{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/segment-analysis", handleSaveSegmentAnalysis(store))
	mux.HandleFunc("GET /internal/segment-analysis/{key}", handleGetSegmentAnalysis(store))
	good, _ := json.Marshal(catalogAnalysisRecord())
	for name, payload := range map[string][]byte{
		"partial":          []byte(`{"status":"analyzing"}`),
		"unknown fields":   append(append([]byte{}, good[:len(good)-1]...), []byte(`,"url":"http://untrusted"}`)...),
		"multiple records": append(append([]byte{}, good...), good...),
		"oversized":        []byte(strings.Repeat(" ", segments.AnalysisMaxBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/internal/segment-analysis", bytes.NewReader(payload)))
			want := 400
			if name == "oversized" {
				want = 413
			}
			if w.Code != want || store.writes != 0 {
				t.Fatalf("invalid write accepted: %d writes=%d", w.Code, store.writes)
			}
		})
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/internal/segment-analysis", bytes.NewReader(good)))
	if w.Code != 204 || store.writes != 1 {
		t.Fatalf("valid save: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/segment-analysis/"+segmentStoreTestKey, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"fingerprint":[123,456,0`) {
		t.Fatalf("internal read: %d %s", w.Code, w.Body.String())
	}
	store.found = false
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/segment-analysis/"+segmentStoreTestKey, nil))
	if w.Code != 404 {
		t.Fatalf("missing read: %d", w.Code)
	}
	store.err = errors.New("database credentials must not leak")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/segment-analysis/"+segmentStoreTestKey, nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), "credentials") {
		t.Fatalf("store error leak: %d %s", w.Code, w.Body.String())
	}
}
