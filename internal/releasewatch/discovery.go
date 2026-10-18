package releasewatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/imdb"
)

// HTTPDiscovery only reads release titles and torrent file metadata. It never
// starts playback, probes tracks or requests video data.
type HTTPDiscovery struct {
	CatalogURL string
	StreamURL  string
	Client     *http.Client

	PollInterval   time.Duration
	RequestTimeout time.Duration
	SourceTimeout  time.Duration
	ScanTimeout    time.Duration
}

type filmMetadata struct {
	Kind   string          `json:"kind"`
	TMDBID json.RawMessage `json:"tmdb_id"`
}

type releaseSource struct {
	Title   string `json:"title"`
	Magnet  string `json:"magnet"`
	Quality string `json:"quality"`
	Audio   string `json:"audio"`
}

type releaseFile struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
}

func (d *HTTPDiscovery) Scan(ctx context.Context, subscription Subscription) ([]Variant, error) {
	if subscription.FilmID == "" || strings.ContainsAny(subscription.FilmID, `/\\?#`) {
		return nil, errors.New("invalid subscription film ID")
	}
	ctx, cancel := context.WithTimeout(ctx, durationOr(d.ScanTimeout, 15*time.Minute))
	defer cancel()
	film, err := d.film(ctx, subscription)
	if err != nil {
		return nil, err
	}
	sources, err := d.sources(ctx, subscription.FilmID)
	if err != nil {
		return nil, err
	}
	isSeries := isSeriesMetadata(film.Kind)

	// All releases are inspected, including zero-seed releases and changing
	// season packs. A fixed worker count limits pressure without hiding voices
	// after an arbitrary first N uploads.
	jobs := make(chan releaseSource)
	var mu sync.Mutex
	var variants []Variant
	attempted, succeeded := 0, 0
	var workers sync.WaitGroup
	for range 3 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for source := range jobs {
				files, err := d.files(ctx, subscription.FilmID, film, source)
				mu.Lock()
				attempted++
				if err == nil {
					succeeded++
					variants = append(variants, variantsForFiles(source, files, isSeries)...)
				}
				mu.Unlock()
			}
		}()
	}
	for _, source := range sources {
		if !validMagnet(source.Magnet) {
			continue
		}
		select {
		case jobs <- source:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return nil, fmt.Errorf("release scan incomplete: %w", ctx.Err())
		}
	}
	close(jobs)
	workers.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("release scan incomplete: %w", ctx.Err())
	}
	if attempted > succeeded {
		log.Printf("releasewatch: %s metadata available for %d/%d releases", subscription.FilmID, succeeded, attempted)
	}
	if attempted > 0 && succeeded == 0 {
		return nil, errors.New("no release metadata could be verified")
	}
	return NormalizeVariants(variants), nil
}

func durationOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func isSeriesMetadata(kind string) bool {
	switch imdb.NormalizeKind(kind) {
	case "tvSeries", "tvMiniSeries", "animation":
		return true
	}
	return false
}

func (d *HTTPDiscovery) film(ctx context.Context, subscription Subscription) (filmMetadata, error) {
	var snapshot filmMetadata
	_ = json.Unmarshal(subscription.Data, &snapshot)
	var film filmMetadata
	err := d.json(ctx, http.MethodGet, d.CatalogURL, "/api/films/"+url.PathEscape(subscription.FilmID), nil, &film)
	if err != nil {
		if snapshot.Kind == "" {
			return film, fmt.Errorf("film metadata unavailable: %w", err)
		}
		film = snapshot
	}
	if film.Kind == "" {
		film.Kind = snapshot.Kind
	}
	if film.Kind == "" {
		switch {
		case strings.HasPrefix(subscription.FilmID, "tmdb-tv-"):
			film.Kind = "tvSeries"
		case strings.HasPrefix(subscription.FilmID, "tmdb-movie-"):
			film.Kind = "feature"
		default:
			return film, errors.New("film type is not known")
		}
	}
	if len(film.TMDBID) == 0 || string(film.TMDBID) == `""` {
		film.TMDBID = snapshot.TMDBID
	}
	return film, nil
}

func (d *HTTPDiscovery) sources(ctx context.Context, filmID string) ([]releaseSource, error) {
	ctx, cancel := context.WithTimeout(ctx, durationOr(d.SourceTimeout, 4*time.Minute))
	defer cancel()
	for {
		var response struct {
			Status string          `json:"status"`
			Items  []releaseSource `json:"items"`
		}
		if err := d.json(ctx, http.MethodGet, d.CatalogURL, "/api/films/"+url.PathEscape(filmID)+"/sources", nil, &response); err != nil {
			return nil, fmt.Errorf("release search unavailable: %w", err)
		}
		switch response.Status {
		case "ready":
			return response.Items, nil
		case "searching":
			// Cached items returned while searching are not the finished result.
		default:
			return nil, errors.New("release search did not complete")
		}
		timer := time.NewTimer(durationOr(d.PollInterval, 2*time.Second))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("release search did not complete: %w", ctx.Err())
		}
	}
}

func (d *HTTPDiscovery) files(ctx context.Context, filmID string, film filmMetadata, source releaseSource) ([]releaseFile, error) {
	var response struct {
		Files []releaseFile `json:"files"`
	}
	var tmdbID string
	if len(film.TMDBID) > 0 {
		if json.Unmarshal(film.TMDBID, &tmdbID) != nil {
			tmdbID = string(film.TMDBID)
		}
	}
	if n, _ := strconv.ParseInt(tmdbID, 10, 64); n <= 0 {
		tmdbID = ""
	}
	request := map[string]string{"magnet": source.Magnet, "title": source.Title, "tmdb": tmdbID}
	if err := d.json(ctx, http.MethodPost, d.StreamURL, "/api/films/"+url.PathEscape(filmID)+"/files", request, &response); err != nil {
		return nil, err
	}
	if len(response.Files) == 0 {
		return nil, errors.New("torrent has no video files")
	}
	return response.Files, nil
}

func (d *HTTPDiscovery) json(ctx context.Context, method, base, endpoint string, body any, target any) error {
	if strings.TrimSpace(base) == "" {
		return errors.New("release service URL is not configured")
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(ctx, durationOr(d.RequestTimeout, 70*time.Second))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("release service returned HTTP %d", response.StatusCode)
	}
	const maxJSONBytes = 8 << 20
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil {
		return err
	}
	if len(encoded) > maxJSONBytes {
		return errors.New("release metadata response is too large")
	}
	return json.Unmarshal(encoded, target)
}

func validMagnet(value string) bool {
	u, err := url.Parse(value)
	if err != nil || !strings.EqualFold(u.Scheme, "magnet") {
		return false
	}
	for _, xt := range u.Query()["xt"] {
		if strings.HasPrefix(strings.ToLower(xt), "urn:btih:") && len(xt) > len("urn:btih:") {
			return true
		}
	}
	return false
}

var episodeEvidenceRE = regexp.MustCompile(`(?i)(?:s\d{1,2}[\s._-]*e\d{1,3}|\d{1,2}x\d{1,3}|(?:серия|эпизод|episode|\bep\b|\be\b)\s*\.?\s*\d{1,3}|^\s*\d{1,3}(?:[.\s_-]+|$)|(?:^|[^0-9])\d{1,3}\s*$|\d{1,4}\s*[.)]?\s*(?:seriy|sery|серия|серии|серий))`)
var extraFileRE = regexp.MustCompile(`(?i)(?:^|[\s._/\\-])(?:sample|trailer|teaser|featurette|extras?|deleted[ ._-]scenes|трейлер|тизер|образец|дополнения|интервью)(?:$|[\s._/\\-])`)

func variantsForFiles(source releaseSource, files []releaseFile, series bool) []Variant {
	voices := declaredVoices(source.Title, source.Audio)
	format := normalizeFormat(source.Title)
	resolution := sourceResolution(source.Title, source.Quality)
	var variants []Variant
	for _, file := range files {
		base := path.Base(strings.ReplaceAll(file.Name, `\`, "/"))
		name := strings.TrimSuffix(base, path.Ext(base))
		if file.Size <= 0 || extraFileRE.MatchString(name) {
			continue
		}
		if series && (file.Season <= 0 || file.Episode <= 0 || !episodeEvidenceRE.MatchString(name)) {
			// /files numbers unrecognized files for player navigation; those
			// invented coordinates are not evidence of an available episode.
			continue
		}
		fileVoices := declaredVoices(name, "")
		if len(fileVoices) == 1 && fileVoices[0] == UnknownVoice {
			fileVoices = voices
		}
		fileFormat := normalizeFormat(name)
		if fileFormat == "UNKNOWN" {
			fileFormat = format
		}
		fileResolution := sourceResolution(name, "")
		if fileResolution == 0 {
			fileResolution = resolution
		}
		for _, voice := range fileVoices {
			variant := Variant{Format: fileFormat, Resolution: fileResolution, Voice: voice}
			if series {
				variant.Season, variant.Episode = file.Season, file.Episode
			}
			variants = append(variants, variant)
		}
	}
	return variants
}
