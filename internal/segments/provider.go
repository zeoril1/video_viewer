package segments

import (
	"container/list"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	StatusReady          = "ready"
	StatusUnavailable    = "unavailable"
	StatusNotFound       = "not_found"
	StatusDisabled       = "disabled"
	providerEndpoint     = "https://api.theintrodb.org/v3/media"
	providerTimeout      = 3 * time.Second
	providerMaxBytes     = 256 << 10
	providerCacheEntries = 512
	providerPositiveTTL  = 24 * time.Hour
	providerNegativeTTL  = 10 * time.Minute
	providerFailureTTL   = 5 * time.Second
)

var imdbIDPattern = regexp.MustCompile(`^tt[0-9]{7,8}$`)

type Query struct {
	TMDBID   int64
	IMDBID   string
	Season   int
	Episode  int
	Duration float64
}

// Valid requires explicit episode coordinates to avoid accidentally getting
// movie timestamps when episode metadata is incomplete.
func (q Query) Valid() bool {
	return (q.TMDBID > 0 && q.TMDBID <= 10000000 || q.TMDBID == 0 && imdbIDPattern.MatchString(q.IMDBID)) &&
		q.Season >= 0 && q.Season <= 10000 && q.Episode > 0 && q.Episode <= 100000 &&
		finite(q.Duration) && q.Duration > 0 && q.Duration <= 21600
}

type Result struct {
	Segments []Segment `json:"segments"`
	Status   string    `json:"status"`
}

type ProviderOptions struct {
	Disabled bool
	APIKey   string
	// Endpoint and Client are configured by trusted server code, never requests.
	Endpoint string
	Client   *http.Client
	Context  context.Context
}

type cacheEntry struct {
	key     string
	result  Result
	expires time.Time
}
type pendingLookup struct {
	done   chan struct{}
	result Result
}

// Provider shares cached lookups, coalesces duplicate requests and bounds
// network work. Excess requests return unavailable immediately without queues.
type Provider struct {
	client           *http.Client
	endpoint, apiKey string
	disabled         bool
	ctx              context.Context
	mu               sync.Mutex
	cache            map[string]*list.Element
	lru              *list.List
	pending          map[string]*pendingLookup
	backoff          time.Time
	tokens           float64
	refilled         time.Time
}

func NewProvider(opts ProviderOptions) *Provider {
	client := opts.Client
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxConnsPerHost = 2
		client = &http.Client{Transport: transport, Timeout: providerTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = providerEndpoint
	}
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return &Provider{client: client, endpoint: endpoint, apiKey: strings.TrimSpace(opts.APIKey),
		disabled: opts.Disabled, ctx: ctx, cache: make(map[string]*list.Element), lru: list.New(),
		pending: make(map[string]*pendingLookup), tokens: 4, refilled: time.Now()}
}

func emptyResult(status string) Result { return Result{Segments: []Segment{}, Status: status} }
func copyResult(r Result) Result {
	r.Segments = append([]Segment{}, r.Segments...)
	return r
}

func (p *Provider) Lookup(ctx context.Context, q Query) Result {
	if p.disabled {
		return emptyResult(StatusDisabled)
	}
	if !q.Valid() || ctx.Err() != nil || p.ctx.Err() != nil {
		return emptyResult(StatusUnavailable)
	}
	// Millisecond precision is what the upstream API accepts; use identical
	// precision for cache keys so tiny playback duration differences coalesce.
	durationMS := int64(math.Round(q.Duration * 1000))
	q.Duration = float64(durationMS) / 1000
	values := url.Values{"season": {strconv.Itoa(q.Season)}, "episode": {strconv.Itoa(q.Episode)},
		"duration_ms": {strconv.FormatInt(durationMS, 10)}}
	if q.TMDBID > 0 {
		values.Set("tmdb_id", strconv.FormatInt(q.TMDBID, 10))
	} else {
		values.Set("imdb_id", q.IMDBID)
	}
	key := values.Encode()
	now := time.Now()
	p.mu.Lock()
	if e := p.cache[key]; e != nil {
		entry := e.Value.(cacheEntry)
		if now.Before(entry.expires) {
			p.lru.MoveToFront(e)
			p.mu.Unlock()
			return copyResult(entry.result)
		}
		p.lru.Remove(e)
		delete(p.cache, key)
	}
	if pending := p.pending[key]; pending != nil {
		p.mu.Unlock()
		return waitLookup(ctx, pending)
	}
	p.tokens = math.Min(4, p.tokens+now.Sub(p.refilled).Seconds()*4)
	p.refilled = now
	if now.Before(p.backoff) || len(p.pending) >= 2 || p.tokens < 1 {
		p.mu.Unlock()
		return emptyResult(StatusUnavailable)
	}
	p.tokens--
	pending := &pendingLookup{done: make(chan struct{})}
	p.pending[key] = pending
	p.mu.Unlock()
	go p.load(key, values, q, pending)
	return waitLookup(ctx, pending)
}

func waitLookup(ctx context.Context, pending *pendingLookup) Result {
	select {
	case <-ctx.Done():
		return emptyResult(StatusUnavailable)
	case <-pending.done:
		return copyResult(pending.result)
	}
}

func (p *Provider) load(key string, values url.Values, q Query, pending *pendingLookup) {
	ctx, cancel := context.WithTimeout(p.ctx, providerTimeout)
	defer cancel()
	result, retryAt := p.fetch(ctx, values, q)
	ttl := providerFailureTTL
	if result.Status == StatusReady {
		ttl = providerPositiveTTL
	}
	if result.Status == StatusNotFound {
		ttl = providerNegativeTTL
	}
	p.mu.Lock()
	if retryAt.After(p.backoff) {
		p.backoff = retryAt
	}
	entry := cacheEntry{key: key, result: result, expires: time.Now().Add(ttl)}
	p.cache[key] = p.lru.PushFront(entry)
	for p.lru.Len() > providerCacheEntries {
		last := p.lru.Back()
		delete(p.cache, last.Value.(cacheEntry).key)
		p.lru.Remove(last)
	}
	pending.result = result
	delete(p.pending, key)
	close(pending.done)
	p.mu.Unlock()
}

type rawRange struct {
	// RawMessage distinguishes missing boundaries from explicit null.
	Start json.RawMessage `json:"start_ms"`
	End   json.RawMessage `json:"end_ms"`
}
type rawMedia struct {
	TMDBID  int64      `json:"tmdb_id"`
	Type    string     `json:"type"`
	Season  *int       `json:"season"`
	Episode *int       `json:"episode"`
	Intro   []rawRange `json:"intro"`
	Recap   []rawRange `json:"recap"`
	Credits []rawRange `json:"credits"`
}

func (p *Provider) fetch(ctx context.Context, values url.Values, q Query) (Result, time.Time) {
	unavailable := emptyResult(StatusUnavailable)
	u, err := url.Parse(p.endpoint)
	if err != nil {
		return unavailable, time.Time{}
	}
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return unavailable, time.Time{}
	}
	req.Header.Set("Accept", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	response, err := p.client.Do(req)
	if err != nil {
		return unavailable, time.Time{}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		return unavailable, retryAfter(response.Header.Get("Retry-After"), time.Now())
	}
	if response.StatusCode == http.StatusNotFound {
		return emptyResult(StatusNotFound), time.Time{}
	}
	if response.StatusCode != http.StatusOK {
		return unavailable, time.Time{}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, providerMaxBytes+1))
	if err != nil || len(body) > providerMaxBytes {
		return unavailable, time.Time{}
	}
	var media rawMedia
	if err := json.Unmarshal(body, &media); err != nil {
		return unavailable, time.Time{}
	}
	// A successful HTTP response can still describe a different movie/episode.
	if media.Type != "tv" || media.TMDBID <= 0 ||
		(q.TMDBID > 0 && media.TMDBID != q.TMDBID) ||
		media.Season == nil || *media.Season != q.Season ||
		media.Episode == nil || *media.Episode != q.Episode {
		return unavailable, time.Time{}
	}
	out := make([]Segment, 0)
	for _, group := range []struct {
		kind   string
		ranges []rawRange
	}{{Intro, media.Intro}, {Recap, media.Recap}, {Credits, media.Credits}} {
		for _, raw := range group.ranges {
			start, ok := boundary(raw.Start, 0, group.kind != Credits)
			if !ok {
				continue
			}
			end, ok := boundary(raw.End, q.Duration, group.kind == Credits)
			if !ok {
				continue
			}
			out = append(out, Segment{Type: group.kind, Start: start, End: end, Source: "theintrodb", AutoSkip: false})
		}
	}
	out = Normalize(out, q.Duration)
	if len(out) == 0 {
		return emptyResult(StatusNotFound), time.Time{}
	}
	return Result{Segments: out, Status: StatusReady}, time.Time{}
}

func boundary(raw json.RawMessage, nullValue float64, allowNull bool) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	if string(raw) == "null" {
		return nullValue, allowNull
	}
	var ms int64
	if json.Unmarshal(raw, &ms) != nil || ms < 0 {
		return 0, false
	}
	return float64(ms) / 1000, true
}

func retryAfter(raw string, now time.Time) time.Time {
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 && seconds <= int64((1<<63-1)/time.Second) {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if date, err := http.ParseTime(raw); err == nil && date.After(now) {
		return date
	}
	return now.Add(time.Minute)
}
