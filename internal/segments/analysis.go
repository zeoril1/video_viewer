package segments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	AnalysisVersion  = 1
	AnalysisMaxBytes = 1 << 20
)

var analysisMediaKey = regexp.MustCompile(`^segments\.[0-9a-f]{40}\.(0|[1-9][0-9]{0,6})$`)
var analysisFilmID = regexp.MustCompile(`^(tt[0-9]{7,8}|tmdb-tv-[1-9][0-9]{0,7})$`)

// AnalysisRecord belongs to one torrent file, including negative results.
// Fingerprints stay inside the service network and are never sent to players.
type AnalysisRecord struct {
	MediaKey    string    `json:"media_key"`
	FilmID      string    `json:"film_id"`
	Season      int       `json:"season"`
	Episode     int       `json:"episode"`
	Duration    float64   `json:"duration"`
	Version     int       `json:"version"`
	Status      string    `json:"status"`
	Segments    []Segment `json:"segments"`
	Fingerprint []uint32  `json:"fingerprint"`
	Step        float64   `json:"step"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func ValidMediaKey(key string) bool { return analysisMediaKey.MatchString(key) }

// ValidateAnalysis rejects incomplete work rather than replacing a durable
// result with a transient failure or a partial fingerprint.
func ValidateAnalysis(r AnalysisRecord) error {
	if !ValidMediaKey(r.MediaKey) || !analysisFilmID.MatchString(r.FilmID) ||
		r.Season < 0 || r.Season > 10000 || r.Episode < 1 || r.Episode > 100000 {
		return errors.New("invalid analysis identity")
	}
	if !finite(r.Duration) || r.Duration <= 0 || r.Duration > 21600 || r.Version < 1 || r.Version > 1000 {
		return errors.New("invalid analysis duration or version")
	}
	if r.Status != StatusReady && r.Status != StatusNotFound ||
		r.Status == StatusReady && len(r.Segments) == 0 ||
		r.Status == StatusNotFound && len(r.Segments) != 0 {
		return errors.New("incomplete analysis result")
	}
	if len(r.Segments) > 128 || len(r.Fingerprint) > 50000 || !finite(r.Step) ||
		r.Step < 0 || len(r.Fingerprint) > 0 && r.Step != 0.5 {
		return errors.New("invalid analysis payload size or step")
	}
	if len(r.Fingerprint) > 0 && math.Abs(float64(len(r.Fingerprint))*r.Step-r.Duration) > r.Step {
		return errors.New("incomplete analysis fingerprint")
	}
	for _, segment := range r.Segments {
		if !Validate(segment, r.Duration) || len(segment.Source) > 128 {
			return errors.New("invalid analysis segment")
		}
	}
	return nil
}

// AnalysisClient talks only to a base URL supplied by server configuration.
// An empty URL disables persistence without disabling local analysis.
type AnalysisClient struct {
	baseURL string
	client  *http.Client
}

func NewAnalysisClient(baseURL string) *AnalysisClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		baseURL = ""
	}
	return &AnalysisClient{baseURL: baseURL, client: &http.Client{Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *AnalysisClient) Get(ctx context.Context, key string) (AnalysisRecord, bool, error) {
	var record AnalysisRecord
	if !ValidMediaKey(key) {
		return record, false, errors.New("invalid media key")
	}
	if c == nil || c.baseURL == "" {
		return record, false, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/internal/segment-analysis/"+url.PathEscape(key), nil)
	if err != nil {
		return record, false, err
	}
	response, err := c.client.Do(req)
	if err != nil {
		return record, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return record, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return record, false, fmt.Errorf("analysis store returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, AnalysisMaxBytes+1))
	if err != nil {
		return record, false, err
	}
	if len(data) > AnalysisMaxBytes {
		return record, false, errors.New("analysis response exceeds size limit")
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, false, err
	}
	if record.MediaKey != key {
		return AnalysisRecord{}, false, errors.New("analysis response media mismatch")
	}
	if err := ValidateAnalysis(record); err != nil {
		return AnalysisRecord{}, false, err
	}
	return record, true, nil
}

func (c *AnalysisClient) Save(ctx context.Context, record AnalysisRecord) error {
	if err := ValidateAnalysis(record); err != nil {
		return err
	}
	if c == nil || c.baseURL == "" {
		return nil
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > AnalysisMaxBytes {
		return errors.New("analysis request exceeds size limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/segment-analysis", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("analysis store returned HTTP %d", response.StatusCode)
	}
	return nil
}
