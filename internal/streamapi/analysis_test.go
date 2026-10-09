package streamapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zeoril1/video_viewer/internal/segmentdetect"
	"github.com/zeoril1/video_viewer/internal/segments"
)

const analysisTestMagnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"

type memoryAnalysisStore struct {
	records map[string]segments.AnalysisRecord
}

func (s *memoryAnalysisStore) Get(ctx context.Context, key string) (segments.AnalysisRecord, bool, error) {
	if ctx.Err() != nil {
		return segments.AnalysisRecord{}, false, ctx.Err()
	}
	r, found := s.records[key]
	return r, found, nil
}
func (s *memoryAnalysisStore) Save(ctx context.Context, r segments.AnalysisRecord) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := segments.ValidateAnalysis(r); err != nil {
		return err
	}
	s.records[r.MediaKey] = r
	return nil
}
func testPreparePair() prepareAnalysisRequest {
	return prepareAnalysisRequest{previous: episodeMedia{id: "tt1234567", magnet: analysisTestMagnet, file: 0, season: 1, episode: 1},
		next: episodeMedia{id: "tt1234567", magnet: analysisTestMagnet, file: 1, season: 1, episode: 2}}
}
func testEpisodeAnalyzer(store analysisStore) *episodeAnalyzer {
	return &episodeAnalyzer{store: store, slots: make(chan struct{}, 1), ctx: context.Background(),
		probe: func(context.Context, string, string, int) (probeResult, error) { return probeResult{Duration: 60}, nil },
		input: func(_ string, _ string, file int) string { return string(rune('0' + file)) },
		extract: func(context.Context, string, float64) (segmentdetect.Fingerprint, error) {
			return segmentdetect.Fingerprint{Frames: make([]uint32, 120), Step: 0.5}, nil
		},
		detect: func(segments.AnalysisRecord, segments.AnalysisRecord) ([]segments.Segment, []segments.Segment) {
			return nil, nil
		}}
}

func TestPairAnalysisPersistsBothFilesAndReusesFingerprintsAfterRestart(t *testing.T) {
	store := &memoryAnalysisStore{records: map[string]segments.AnalysisRecord{}}
	a := testEpisodeAnalyzer(store)
	decodes := 0
	a.extract = func(context.Context, string, float64) (segmentdetect.Fingerprint, error) {
		decodes++
		return segmentdetect.Fingerprint{Frames: make([]uint32, 120), Step: 0.5}, nil
	}
	a.detect = func(segments.AnalysisRecord, segments.AnalysisRecord) ([]segments.Segment, []segments.Segment) {
		ranges := []segments.Segment{{Type: segments.Intro, Start: 3, End: 25, Source: "audio_match"}}
		return ranges, ranges
	}
	request := testPreparePair()
	result := a.analyse(context.Background(), request)
	if !result.DownloadComplete || result.AnalysisStatus != segments.StatusReady || decodes != 2 || len(store.records) != 2 {
		t.Fatal(result, decodes, len(store.records))
	}
	if store.records[segmentMediaKey(analysisTestMagnet, 0)].Status != segments.StatusReady {
		t.Fatal("reference timings not persisted")
	}
	// A new worker reads the persistent records, not the former worker's memory.
	restarted := testEpisodeAnalyzer(store)
	restarted.probe = func(context.Context, string, string, int) (probeResult, error) {
		t.Fatal("cached file reprobed")
		return probeResult{}, nil
	}
	restarted.extract = func(context.Context, string, float64) (segmentdetect.Fingerprint, error) {
		t.Fatal("cached file decoded again")
		return segmentdetect.Fingerprint{}, nil
	}
	result = restarted.analyse(context.Background(), request)
	if result.AnalysisStatus != segments.StatusReady || len(result.Segments) != 1 {
		t.Fatal(result)
	}
}

func TestNextAnalysisFailureRetainsDownloadAndPreviouslyCompletedFingerprint(t *testing.T) {
	store := &memoryAnalysisStore{records: map[string]segments.AnalysisRecord{}}
	a := testEpisodeAnalyzer(store)
	a.extract = func(_ context.Context, input string, _ float64) (segmentdetect.Fingerprint, error) {
		if input == "1" {
			return segmentdetect.Fingerprint{}, errors.New("unsupported audio")
		}
		return segmentdetect.Fingerprint{Frames: make([]uint32, 120), Step: 0.5}, nil
	}
	result := a.analyse(context.Background(), testPreparePair())
	if !result.DownloadComplete || result.AnalysisStatus != segments.StatusUnavailable {
		t.Fatal(result)
	}
	previous, found := store.records[segmentMediaKey(analysisTestMagnet, 0)]
	if !found || previous.Status != segments.StatusNotFound || len(previous.Fingerprint) != 120 {
		t.Fatal("completed reference was discarded", previous)
	}
	if _, found = store.records[segmentMediaKey(analysisTestMagnet, 1)]; found {
		t.Fatal("failed analysis persisted as a negative result")
	}
}

func TestCancelledAnalysisAndBusyWorkerDoNotDiscardPlayback(t *testing.T) {
	store := &memoryAnalysisStore{records: map[string]segments.AnalysisRecord{}}
	a := testEpisodeAnalyzer(store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := a.analyse(ctx, testPreparePair())
	if result.AnalysisStatus != segments.StatusUnavailable || !result.DownloadComplete || len(store.records) != 0 || len(a.slots) != 0 {
		t.Fatal(result, store.records)
	}
	a.slots <- struct{}{}
	result = a.analyse(context.Background(), testPreparePair())
	if result.AnalysisStatus != segments.StatusUnavailable || !result.DownloadComplete || len(store.records) != 0 {
		t.Fatal(result)
	}
	var disabled *episodeAnalyzer
	if disabled.analyse(context.Background(), testPreparePair()).AnalysisStatus != segments.StatusDisabled {
		t.Fatal("nil worker not disabled")
	}
}

func TestNamedChaptersAndSeparateCreditScenesSurvivePairMerging(t *testing.T) {
	existing := []segments.Segment{{Type: segments.Intro, Start: 5, End: 25, Source: "chapter", AutoSkip: true},
		{Type: segments.Credits, Start: 80, End: 90, Source: "audio_match"}}
	detected := []segments.Segment{{Type: segments.Intro, Start: 3, End: 28, Source: "audio_match"},
		{Type: segments.Credits, Start: 79, End: 90, Source: "audio_match"}, {Type: segments.Credits, Start: 95, End: 100, Source: "audio_match"}}
	result := mergeAnalysisRanges(existing, detected, 100)
	if len(result) != 3 || result[0].Source != "chapter" || !result[0].AutoSkip || result[1].End != 90 || result[2].Start != 95 {
		t.Fatal(result)
	}
}

func TestPrepareAnalysisRequiresExactAdjacentFiles(t *testing.T) {
	q := url.Values{"id": {"tt1234567"}, "magnet": {analysisTestMagnet}, "file": {"1"}, "season": {"1"}, "episode": {"2"},
		"previous_magnet": {analysisTestMagnet}, "previous_file": {"0"}, "previous_season": {"1"}, "previous_episode": {"1"}}
	_, enabled, err := parsePrepareAnalysis(httptest.NewRequest("POST", "/api/stream/prepare?"+q.Encode(), nil))
	if err != nil || !enabled {
		t.Fatal(enabled, err)
	}
	for _, change := range []struct{ key, value string }{{"file", "0"}, {"previous_file", "-1"}, {"episode", "4"}, {"id", "bad"}, {"season", "20000"}, {"previous_magnet", ""}} {
		bad := url.Values{}
		for key, values := range q {
			bad[key] = append([]string{}, values...)
		}
		bad.Set(change.key, change.value)
		if _, _, err := parsePrepareAnalysis(httptest.NewRequest("POST", "/api/stream/prepare?"+bad.Encode(), nil)); err == nil {
			t.Fatal("invalid request accepted", change)
		}
	}
	_, enabled, err = parsePrepareAnalysis(httptest.NewRequest("POST", "/api/stream/prepare?file=1", nil))
	if enabled || err != nil {
		t.Fatal("legacy download caller rejected")
	}
}

func TestDownloadStatusUnavailableWithoutTorrentManager(t *testing.T) {
	w := httptest.NewRecorder()
	downloadStatusHandler(nil)(w, httptest.NewRequest("GET", "/api/stream/download-status?file=0", nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), `"complete":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
