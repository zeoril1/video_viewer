package catalogapi

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/zeoril1/video_viewer/internal/segments"
)

var segmentFilmID = regexp.MustCompile(`^(tt[0-9]{7,8}|tmdb-tv-[1-9][0-9]{0,7})$`)

func registerSegments(mux *http.ServeMux, cfg Config) {
	disabled := false
	if value := strings.TrimSpace(os.Getenv("SEGMENTS_EXTERNAL_ENABLED")); value != "" {
		if enabled, err := strconv.ParseBool(value); err == nil {
			disabled = !enabled
		}
	}
	provider := segments.NewProvider(segments.ProviderOptions{Disabled: disabled, APIKey: os.Getenv("THEINTRODB_API_KEY"), Context: cfg.Context})
	mux.HandleFunc("GET /api/films/{id}/segments", handleSegments(provider))
}

func handleSegments(provider *segments.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, ok := parseSegmentQuery(r)
		if !ok {
			http.Error(w, "invalid series, season, episode or duration", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(w).Encode(provider.Lookup(r.Context(), q))
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
