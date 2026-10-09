package catalogapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/zeoril1/video_viewer/internal/segments"
)

var segmentFilmID = regexp.MustCompile(`^(tt[0-9]{7,8}|tmdb-tv-[1-9][0-9]{0,7})$`)

type segmentAnalysisStore interface {
	GetSegmentAnalysis(context.Context, string) (segments.AnalysisRecord, bool, error)
	SaveSegmentAnalysis(context.Context, segments.AnalysisRecord) error
}

type segmentResponse struct {
	segments.Result
	MediaKey       string `json:"media_key,omitempty"`
	AnalysisStatus string `json:"analysis_status,omitempty"`
}

func registerSegments(mux *http.ServeMux, cfg Config) {
	disabled := false
	if value := strings.TrimSpace(os.Getenv("SEGMENTS_EXTERNAL_ENABLED")); value != "" {
		if enabled, err := strconv.ParseBool(value); err == nil {
			disabled = !enabled
		}
	}
	provider := segments.NewProvider(segments.ProviderOptions{Disabled: disabled, APIKey: os.Getenv("THEINTRODB_API_KEY"), Context: cfg.Context})
	var store segmentAnalysisStore
	if cfg.DB != nil {
		store = cfg.DB
	}
	mux.HandleFunc("GET /api/films/{id}/segments", handleSegments(provider, store))
	// The gateway does not proxy /internal/: analysis data and writes belong to
	// the service network, while the public endpoint returns skip ranges only.
	mux.HandleFunc("GET /internal/segment-analysis/{key}", handleGetSegmentAnalysis(store))
	mux.HandleFunc("POST /internal/segment-analysis", handleSaveSegmentAnalysis(store))
}

func handleSegments(provider *segments.Provider, stores ...segmentAnalysisStore) http.HandlerFunc {
	var store segmentAnalysisStore
	if len(stores) > 0 {
		store = stores[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		q, ok := parseSegmentQuery(r)
		if !ok {
			http.Error(w, "invalid series, season, episode or duration", http.StatusBadRequest)
			return
		}
		key := r.URL.Query().Get("media_key")
		if key != "" && !segments.ValidMediaKey(key) {
			http.Error(w, "invalid media key", http.StatusBadRequest)
			return
		}
		result := segmentResponse{MediaKey: key}
		var local []segments.Segment
		if key != "" {
			result.AnalysisStatus = "pending"
			if store == nil {
				result.AnalysisStatus = segments.StatusUnavailable
			} else {
				record, found, err := store.GetSegmentAnalysis(r.Context(), key)
				if err != nil {
					result.AnalysisStatus = segments.StatusUnavailable
				} else if found &&
					record.Version == segments.AnalysisVersion &&
					record.MediaKey == key && record.FilmID == r.PathValue("id") && record.Season == q.Season && record.Episode == q.Episode &&
					math.Abs(record.Duration-q.Duration) <= 1 && segments.ValidateAnalysis(record) == nil {
					result.AnalysisStatus = record.Status
					local = append([]segments.Segment{}, record.Segments...)
					for i := range local {
						local[i].End = math.Min(local[i].End, q.Duration)
					}
					local = segments.Normalize(local, q.Duration)
				}
			}
		}
		result.Result = provider.Lookup(r.Context(), q)
		if len(local) > 0 {
			// A suggestion for another release must not duplicate or override a
			// verified range for this file. Other types remain useful suggestions.
			types := map[string]bool{}
			for _, segment := range local {
				types[segment.Type] = true
			}
			merged := append([]segments.Segment{}, local...)
			for _, segment := range result.Segments {
				if !types[segment.Type] {
					merged = append(merged, segment)
				}
			}
			result.Segments = segments.Normalize(merged, q.Duration)
			result.Status = segments.StatusReady
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(w).Encode(result)
	}
}

func handleGetSegmentAnalysis(store segmentAnalysisStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if !segments.ValidMediaKey(key) {
			http.Error(w, "invalid media key", http.StatusBadRequest)
			return
		}
		if store == nil {
			http.Error(w, "analysis storage unavailable", http.StatusServiceUnavailable)
			return
		}
		record, found, err := store.GetSegmentAnalysis(r.Context(), key)
		if err != nil {
			http.Error(w, "analysis storage unavailable", http.StatusServiceUnavailable)
			return
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(record)
	}
}

func handleSaveSegmentAnalysis(store segmentAnalysisStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var record segments.AnalysisRecord
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, segments.AnalysisMaxBytes))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&record)
		if err == nil {
			var extra any
			err = decoder.Decode(&extra)
			if err == io.EOF {
				err = nil
			} else if err == nil {
				err = errors.New("multiple records")
			}
		}
		if err == nil {
			err = segments.ValidateAnalysis(record)
		}
		if err != nil {
			code := http.StatusBadRequest
			var oversized *http.MaxBytesError
			if errors.As(err, &oversized) {
				code = http.StatusRequestEntityTooLarge
			}
			http.Error(w, "invalid analysis record", code)
			return
		}
		if store == nil {
			http.Error(w, "analysis storage unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := store.SaveSegmentAnalysis(r.Context(), record); err != nil {
			http.Error(w, "analysis storage unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func parseSegmentQuery(r *http.Request) (segments.Query, bool) {
	id := r.PathValue("id")
	q := segments.Query{}
	if !segmentFilmID.MatchString(id) {
		return q, false
	}
	query := r.URL.Query()
	var err error
	// Both coordinates must be explicit, including season=0 for specials.
	q.Season, err = strconv.Atoi(query.Get("season"))
	if err != nil {
		return q, false
	}
	q.Episode, err = strconv.Atoi(query.Get("episode"))
	if err != nil {
		return q, false
	}
	q.Duration, err = strconv.ParseFloat(query.Get("duration"), 64)
	if err != nil || math.IsNaN(q.Duration) || math.IsInf(q.Duration, 0) {
		return q, false
	}
	if strings.HasPrefix(id, "tmdb-tv-") {
		q.TMDBID, err = strconv.ParseInt(strings.TrimPrefix(id, "tmdb-tv-"), 10, 64)
		if err != nil {
			return q, false
		}
	} else {
		q.IMDBID = id
	}
	if tmdb := query.Get("tmdb"); tmdb != "" {
		n, parseErr := strconv.ParseInt(tmdb, 10, 64)
		if parseErr != nil || n <= 0 || (q.TMDBID > 0 && q.TMDBID != n) {
			return q, false
		}
		q.TMDBID = n
	}
	return q, q.Valid()
}
