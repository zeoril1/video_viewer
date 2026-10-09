package streamapi

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zeoril1/video_viewer/internal/segmentdetect"
	"github.com/zeoril1/video_viewer/internal/segments"
)

type analysisStore interface {
	Get(context.Context, string) (segments.AnalysisRecord, bool, error)
	Save(context.Context, segments.AnalysisRecord) error
}

type episodeMedia struct {
	id, magnet            string
	file, season, episode int
}

type prepareAnalysisRequest struct{ next, previous episodeMedia }

var analysisSeriesID = regexp.MustCompile(`^(tt[0-9]{7,8}|tmdb-tv-[1-9][0-9]{0,7})$`)

// Explicit coordinates and file indexes avoid analysing an automatically chosen
// largest file or silently substituting another episode.
func parsePrepareAnalysis(r *http.Request) (prepareAnalysisRequest, bool, error) {
	q := r.URL.Query()
	var result prepareAnalysisRequest
	if q.Get("id") == "" {
		return result, false, nil
	} // legacy download-only caller
	if !analysisSeriesID.MatchString(q.Get("id")) {
		return result, false, errors.New("invalid series id")
	}
	result.next = episodeMedia{id: q.Get("id"), magnet: q.Get("magnet")}
	result.previous = episodeMedia{id: q.Get("id"), magnet: q.Get("previous_magnet")}
	values := []struct {
		key     string
		target  *int
		minimum int
	}{
		{"file", &result.next.file, 0}, {"season", &result.next.season, 0}, {"episode", &result.next.episode, 1},
		{"previous_file", &result.previous.file, 0}, {"previous_season", &result.previous.season, 0}, {"previous_episode", &result.previous.episode, 1},
	}
	for _, value := range values {
		n, err := strconv.Atoi(q.Get(value.key))
		if err != nil || n < value.minimum || n > 100000 {
			return result, false, errors.New("invalid episode coordinates")
		}
		*value.target = n
	}
	if result.next.season > 10000 || result.previous.season > 10000 ||
		segmentMediaKey(result.next.magnet, result.next.file) == "" || segmentMediaKey(result.previous.magnet, result.previous.file) == "" ||
		segmentMediaKey(result.next.magnet, result.next.file) == segmentMediaKey(result.previous.magnet, result.previous.file) {
		return result, false, errors.New("invalid episode files")
	}
	adjacent := result.next.season == result.previous.season && result.next.episode == result.previous.episode+1 ||
		result.next.season == result.previous.season+1 && result.next.episode == 1
	if !adjacent {
		return result, false, errors.New("next episode is not adjacent")
	}
	return result, true, nil
}

type episodeAnalyzer struct {
	store    analysisStore
	probe    func(context.Context, string, string, int) (probeResult, error)
	input    func(string, string, int) string
	extract  func(context.Context, string, float64) (segmentdetect.Fingerprint, error)
	detect   func(segments.AnalysisRecord, segments.AnalysisRecord) ([]segments.Segment, []segments.Segment)
	slots    chan struct{}
	disabled bool
	pressure func() bool
	ctx      context.Context
	cancel   context.CancelFunc
}

func newEpisodeAnalyzer(hls *hlsManager, cfg Config) *episodeAnalyzer {
	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	return &episodeAnalyzer{store: segments.NewAnalysisClient(cfg.AnalysisStoreURL), probe: hls.tracks, input: hls.inputURL,
		extract: segmentdetect.Extract, detect: segmentdetect.Detect, slots: make(chan struct{}, 1), disabled: cfg.DisableSegmentAnalysis || strings.TrimSpace(cfg.AnalysisStoreURL) == "",
		pressure: func() bool { return cfg.Torrents != nil && cfg.Torrents.DiskPressure() }, ctx: ctx, cancel: cancel}
}

type preparedAnalysis struct {
	DownloadComplete bool               `json:"download_complete"`
	AnalysisStatus   string             `json:"analysis_status"`
	MediaKey         string             `json:"media_key"`
	Segments         []segments.Segment `json:"segments"`
}

func (a *episodeAnalyzer) analyse(ctx context.Context, request prepareAnalysisRequest) preparedAnalysis {
	result := preparedAnalysis{DownloadComplete: true, AnalysisStatus: segments.StatusDisabled,
		MediaKey: segmentMediaKey(request.next.magnet, request.next.file), Segments: []segments.Segment{}}
	if a == nil || a.disabled {
		return result
	}
	result.AnalysisStatus = segments.StatusUnavailable
	if ctx.Err() != nil {
		return result
	}
	if a.pressure != nil && a.pressure() {
		return result
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	default:
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if a.ctx != nil {
		stop := context.AfterFunc(a.ctx, cancel)
		defer stop()
	}
	previous, err := a.record(ctx, request.previous)
	if err != nil {
		log.Printf("skip analysis: previous file: %v", err)
		return result
	}
	next, err := a.record(ctx, request.next)
	if err != nil {
		log.Printf("skip analysis: next file: %v", err)
		return result
	}
	if ctx.Err() != nil {
		return result
	}
	// Pair matching is limited to the same season. A finale and a premiere may
	// share themes without having the same opening; chapters still work there.
	if previous.Season == next.Season {
		oldRanges, newRanges := a.detect(previous, next)
		previous.Segments = mergeAnalysisRanges(previous.Segments, oldRanges, previous.Duration)
		next.Segments = mergeAnalysisRanges(next.Segments, newRanges, next.Duration)
	}
	for _, record := range []*segments.AnalysisRecord{&previous, &next} {
		record.Status = segments.StatusNotFound
		if len(record.Segments) > 0 {
			record.Status = segments.StatusReady
		}
		if ctx.Err() != nil {
			return result
		}
		if err := a.store.Save(ctx, *record); err != nil {
			log.Printf("skip analysis: persist: %v", err)
			return result
		}
	}
	result.AnalysisStatus = next.Status
	result.Segments = next.Segments
	return result
}

func (a *episodeAnalyzer) record(ctx context.Context, media episodeMedia) (segments.AnalysisRecord, error) {
	key := segmentMediaKey(media.magnet, media.file)
	if cached, found, err := a.store.Get(ctx, key); err != nil {
		return segments.AnalysisRecord{}, err
	} else if found && cached.Version == segments.AnalysisVersion && cached.FilmID == media.id &&
		cached.Season == media.season && cached.Episode == media.episode && len(cached.Fingerprint) > 0 {
		return cached, nil
	}
	if a.pressure != nil && a.pressure() {
		return segments.AnalysisRecord{}, errResources
	}
	pr, err := a.probe(ctx, media.id, media.magnet, media.file)
	if err != nil {
		return segments.AnalysisRecord{}, err
	}
	if math.IsNaN(pr.Duration) || math.IsInf(pr.Duration, 0) || pr.Duration <= 0 || pr.Duration > 21600 {
		return segments.AnalysisRecord{}, errors.New("invalid media duration")
	}
	fingerprint, err := a.extract(ctx, a.input(media.id, media.magnet, media.file), pr.Duration)
	if err != nil {
		return segments.AnalysisRecord{}, err
	}
	record := segments.AnalysisRecord{MediaKey: key, FilmID: media.id, Season: media.season, Episode: media.episode,
		Duration: pr.Duration, Version: segments.AnalysisVersion, Status: segments.StatusNotFound,
		Segments: segments.Normalize(pr.Segments, pr.Duration), Fingerprint: fingerprint.Frames, Step: fingerprint.Step}
	if len(record.Segments) > 0 {
		record.Status = segments.StatusReady
	}
	if err := segments.ValidateAnalysis(record); err != nil {
		return segments.AnalysisRecord{}, err
	}
	// Persist each complete fingerprint before starting its neighbour. A failed
	// next decode then cannot discard the current result or force it to repeat.
	if err := a.store.Save(ctx, record); err != nil {
		return segments.AnalysisRecord{}, err
	}
	return record, nil
}

func mergeAnalysisRanges(existing, detected []segments.Segment, duration float64) []segments.Segment {
	chapters := map[string]bool{}
	for _, item := range existing {
		if item.Source == "chapter" {
			chapters[item.Type] = true
		}
	}
	merged := append([]segments.Segment{}, existing...)
	for _, item := range detected {
		if chapters[item.Type] {
			continue
		}
		duplicate := false
		for _, kept := range merged {
			if kept.Type == item.Type && (item.Type == segments.Intro || kept.Start < item.End && item.Start < kept.End) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			merged = append(merged, item)
		}
	}
	return segments.Normalize(merged, duration)
}
