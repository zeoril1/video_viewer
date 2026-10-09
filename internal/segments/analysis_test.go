package segments

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const analysisTestKey = "segments.0123456789abcdef0123456789abcdef01234567.2"

func testAnalysisRecord() AnalysisRecord {
	frames := make([]uint32, 4200)
	frames[0], frames[1], frames[2] = 0, 123, 456
	return AnalysisRecord{MediaKey: analysisTestKey, FilmID: "tt1234567", Season: 1, Episode: 2, Duration: 2100,
		Version: AnalysisVersion, Status: StatusReady, Step: 0.5, Fingerprint: frames,
		Segments: []Segment{{Type: Intro, Start: 12, End: 80, Source: "audio_match", AutoSkip: true}}}
}

func TestAnalysisValidationRejectsIncompleteOrUnboundedRecords(t *testing.T) {
	for name, mutate := range map[string]func(*AnalysisRecord){
		"path traversal":        func(r *AnalysisRecord) { r.MediaKey = "../wrong" },
		"wrong hash length":     func(r *AnalysisRecord) { r.MediaKey = "segments.abc.1" },
		"invalid film":          func(r *AnalysisRecord) { r.FilmID = "https://example.com" },
		"missing episode":       func(r *AnalysisRecord) { r.Episode = 0 },
		"invalid duration":      func(r *AnalysisRecord) { r.Duration = math.Inf(1) },
		"long duration":         func(r *AnalysisRecord) { r.Duration = 21601 },
		"out of bounds segment": func(r *AnalysisRecord) { r.Segments[0].End = 2101 },
		"empty ready":           func(r *AnalysisRecord) { r.Segments = nil },
		"partial status":        func(r *AnalysisRecord) { r.Status = "analyzing" },
		"negative with ranges":  func(r *AnalysisRecord) { r.Status = StatusNotFound },
		"bad frame step":        func(r *AnalysisRecord) { r.Step = math.NaN() },
		"wrong frame step":      func(r *AnalysisRecord) { r.Step = 1 },
		"truncated fingerprint": func(r *AnalysisRecord) { r.Fingerprint = r.Fingerprint[:3] },
		"too many frames":       func(r *AnalysisRecord) { r.Fingerprint = make([]uint32, 50001) },
		"too many ranges":       func(r *AnalysisRecord) { r.Segments = make([]Segment, 129) },
	} {
		t.Run(name, func(t *testing.T) {
			record := testAnalysisRecord()
			mutate(&record)
			if ValidateAnalysis(record) == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
	record := testAnalysisRecord()
	if err := ValidateAnalysis(record); err != nil {
		t.Fatal(err)
	}
	record.Status = StatusNotFound
	record.Segments = nil
	if err := ValidateAnalysis(record); err != nil {
		t.Fatalf("negative fingerprint cannot be persisted: %v", err)
	}
}

func TestAnalysisClientRoundTripAndMissingRecord(t *testing.T) {
	record := testAnalysisRecord()
	var saved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var got AnalysisRecord
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got.MediaKey != record.MediaKey || len(got.Fingerprint) != 4200 {
				t.Errorf("bad save: %+v %v", got, err)
			}
			saved.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !saved.Load() {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(record)
	}))
	defer server.Close()
	client := NewAnalysisClient(server.URL)
	if _, found, err := client.Get(context.Background(), record.MediaKey); err != nil || found {
		t.Fatalf("missing: found=%v err=%v", found, err)
	}
	if err := client.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	got, found, err := client.Get(context.Background(), record.MediaKey)
	if err != nil || !found || len(got.Fingerprint) != 4200 || got.Segments[0].End != 80 {
		t.Fatalf("round trip: %+v %v %v", got, found, err)
	}
	for _, url := range []string{"", "ftp://store", "http://store?untrusted=value"} {
		disabled := NewAnalysisClient(url)
		if _, found, err := disabled.Get(context.Background(), record.MediaKey); err != nil || found {
			t.Fatalf("disabled: %v %v", found, err)
		}
		if err := disabled.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnalysisClientRejectsWrongMediaLargeResponsesAndRedirects(t *testing.T) {
	for name, payload := range map[string]string{
		"wrong media": strings.Replace(func() string { b, _ := json.Marshal(testAnalysisRecord()); return string(b) }(), analysisTestKey, "segments.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.2", 1),
		"oversized":   strings.Repeat(" ", AnalysisMaxBytes+1),
		"malformed":   `{"status":"ready"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(payload)) }))
			defer server.Close()
			if _, found, err := NewAnalysisClient(server.URL).Get(context.Background(), analysisTestKey); err == nil || found {
				t.Fatalf("bad response accepted: %v %v", found, err)
			}
		})
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed redirect") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	if _, _, err := NewAnalysisClient(server.URL).Get(context.Background(), analysisTestKey); err == nil {
		t.Fatal("redirect accepted")
	}
}
