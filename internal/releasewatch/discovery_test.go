package releasewatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(value any) (*http.Response, error) {
	encoded, err := json.Marshal(value)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(encoded)))}, err
}

func discoveryFor(transport testTransport) *HTTPDiscovery {
	return &HTTPDiscovery{CatalogURL: "http://catalog", StreamURL: "http://stream", Client: &http.Client{Transport: transport}, PollInterval: time.Millisecond, SourceTimeout: time.Second, ScanTimeout: time.Second}
}

func testMagnet(n int) string { return fmt.Sprintf("magnet:?xt=urn:btih:%040x", n) }

func TestDiscoveryWaitsForReadyAndRechecksSamePack(t *testing.T) {
	var scans, fileCalls atomic.Int32
	transport := testTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/films/tt123":
			return jsonResponse(map[string]any{"kind": "tvSeries", "tmdb_id": "42"})
		case "/api/films/tt123/sources":
			n := scans.Add(1)
			if n == 1 {
				return jsonResponse(map[string]any{"status": "searching", "items": []releaseSource{{Title: "stale", Magnet: testMagnet(99)}}})
			}
			return jsonResponse(map[string]any{"status": "ready", "items": []releaseSource{{Title: "Show [S01-02] WEB-DL 1080p | LostFilm", Magnet: testMagnet(1)}}})
		case "/api/films/tt123/files":
			if r.Method != http.MethodPost {
				t.Error("metadata requests should keep magnet out of query URLs")
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["tmdb"] != "42" || body["magnet"] != testMagnet(1) {
				t.Errorf("bad canonical mapping request: %v", body)
			}
			n := fileCalls.Add(1)
			files := []releaseFile{{Name: "Show.S01E01.mkv", Season: 1, Episode: 1, Size: 1000}, {Name: "01. Серия.mkv", Season: 2, Episode: 1, Size: 1000}}
			if n == 2 {
				files = append(files, releaseFile{Name: "Show.S02E02.mkv", Season: 2, Episode: 2, Size: 1000})
			}
			return jsonResponse(map[string]any{"files": files})
		default:
			t.Errorf("unexpected request (must not probe or request media): %s", r.URL.Path)
			return jsonResponse(nil)
		}
	})
	d := discoveryFor(transport)
	sub := Subscription{FilmID: "tt123", Data: json.RawMessage(`{"kind":"feature"}`)}
	first, err := d.Scan(context.Background(), sub)
	if err != nil || len(first) != 2 {
		t.Fatalf("first scan = %v, %v", first, err)
	}
	second, err := d.Scan(context.Background(), sub)
	if err != nil || len(second) != 3 || second[2].Season != 2 || second[2].Episode != 2 {
		t.Fatalf("unchanged magnet must be rechecked for additional episode: %v, %v", second, err)
	}
	if scans.Load() != 3 || fileCalls.Load() != 2 {
		t.Fatalf("wrong polling/metadata calls: %d/%d", scans.Load(), fileCalls.Load())
	}
}

func TestDiscoveryDoesNotTruncateVoicesAndLimitsParallelism(t *testing.T) {
	var calls, active, maximum atomic.Int32
	d := discoveryFor(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sources"):
			var items []releaseSource
			for i := 1; i <= 12; i++ {
				items = append(items, releaseSource{Title: "Movie TS 1080p", Magnet: testMagnet(i)})
			}
			// Only the twelfth release declares the improved quality/new voice.
			items[11].Title = "Movie BRRip 1080p | NewStudio"
			return jsonResponse(map[string]any{"status": "ready", "items": items})
		case strings.HasSuffix(r.URL.Path, "/files"):
			calls.Add(1)
			n := active.Add(1)
			for old := maximum.Load(); old < n && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			return jsonResponse(map[string]any{"files": []releaseFile{{Name: "Movie.mkv", Size: 1000}}})
		default:
			return jsonResponse(map[string]any{"kind": "feature"})
		}
	})
	got, err := d.Scan(context.Background(), Subscription{FilmID: "tt123"})
	if err != nil || calls.Load() != 12 || len(got) != 2 {
		t.Fatalf("all releases must be inspected and semantically deduplicated: %v, %v, calls=%d", got, err, calls.Load())
	}
	if maximum.Load() > 3 {
		t.Fatalf("too many concurrent metadata requests: %d", maximum.Load())
	}
	if got[0].Format != "BD" || got[0].Voice != "NewStudio" || got[0].Season != 0 || got[0].Episode != 0 {
		t.Errorf("missing late-source movie upgrade: %v", got)
	}
}

func TestSearchingSnapshotCannotEstablishAvailability(t *testing.T) {
	var fileCalls atomic.Int32
	d := discoveryFor(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/sources") {
			return jsonResponse(map[string]any{"status": "searching", "items": []releaseSource{{Title: "Movie WEB-DL", Magnet: testMagnet(1)}}})
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			fileCalls.Add(1)
		}
		return jsonResponse(map[string]any{"kind": "feature"})
	})
	d.SourceTimeout = 8 * time.Millisecond
	if got, err := d.Scan(context.Background(), Subscription{FilmID: "tt123"}); err == nil || len(got) != 0 || fileCalls.Load() != 0 {
		t.Fatalf("unfinished cached source search must not notify: %v, %v", got, err)
	}
}

func TestDiscoveryPartialFailureKeepsConfirmedEvidence(t *testing.T) {
	for _, allFail := range []bool{false, true} {
		t.Run(fmt.Sprint(allFail), func(t *testing.T) {
			d := discoveryFor(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/sources") {
					return jsonResponse(map[string]any{"status": "ready", "items": []releaseSource{{Title: "Movie WEB-DL", Magnet: testMagnet(1)}, {Title: "Movie WEB-DL", Magnet: testMagnet(2)}}})
				}
				if strings.HasSuffix(r.URL.Path, "/files") {
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if allFail || body["magnet"] == testMagnet(2) {
						return &http.Response{StatusCode: 504, Body: io.NopCloser(strings.NewReader("metadata unavailable"))}, nil
					}
					return jsonResponse(map[string]any{"files": []releaseFile{{Name: "Movie.mkv", Size: 1000}}})
				}
				return jsonResponse(map[string]any{"kind": "feature"})
			})
			got, err := d.Scan(context.Background(), Subscription{FilmID: "tt123"})
			if allFail && err == nil {
				t.Fatal("unverifiable scan must not establish an empty baseline")
			}
			if !allFail && (err != nil || len(got) != 1) {
				t.Fatalf("confirmed positive must survive unrelated metadata failure: %v %v", got, err)
			}
		})
	}
}

func TestDiscoveryUsesSubscriptionKindWhenCatalogTemporarilyUnavailable(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	d := discoveryFor(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/sources") {
			return jsonResponse(map[string]any{"status": "ready", "items": []releaseSource{{Title: "Show WEB-DL", Magnet: testMagnet(1)}}})
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			return jsonResponse(map[string]any{"files": []releaseFile{{Name: "05. Эпизод.mkv", Season: 3, Episode: 5, Size: 1000}}})
		}
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})
	got, err := d.Scan(context.Background(), Subscription{FilmID: "tt123", Data: json.RawMessage(`{"kind":"tvMiniSeries"}`)})
	if err != nil || len(got) != 1 || got[0].Season != 3 || got[0].Episode != 5 {
		t.Fatalf("subscription snapshot should retain series classification: %v, %v (paths %v)", got, err, paths)
	}
}
