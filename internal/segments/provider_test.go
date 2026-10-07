package segments

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const episodePayload = `{"tmdb_id":42,"type":"tv","season":1,"episode":2,"intro":[{"start_ms":null,"end_ms":90000},{"start_ms":-1,"end_ms":50},{"end_ms":500}],"recap":[{"start_ms":90000,"end_ms":120000}],"credits":[{"start_ms":1800000,"end_ms":1900000},{"start_ms":2000000,"end_ms":null}]}`

func episodeQuery() Query { return Query{TMDBID: 42, Season: 1, Episode: 2, Duration: 2100} }

func TestProviderLookupConvertsAllRangesAndCachesCopies(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("tmdb_id") != "42" || q.Get("imdb_id") != "" || q.Get("season") != "1" || q.Get("episode") != "2" || q.Get("duration_ms") != "2100000" {
			t.Errorf("wrong query: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("wrong auth header")
		}
		fmt.Fprint(w, episodePayload)
	}))
	defer srv.Close()
	p := NewProvider(ProviderOptions{Endpoint: srv.URL, APIKey: "test-key"})
	result := p.Lookup(context.Background(), episodeQuery())
	if result.Status != StatusReady || len(result.Segments) != 4 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Segments[0].Start != 0 || result.Segments[0].End != 90 || result.Segments[3].End != 2100 {
		t.Fatalf("incorrect time normalization: %+v", result)
	}
	for _, s := range result.Segments {
		if s.AutoSkip || s.Source != "theintrodb" {
			t.Fatalf("unsafe external range: %+v", s)
		}
	}
	result.Segments[0].End = 999
	if cached := p.Lookup(context.Background(), episodeQuery()); cached.Segments[0].End != 90 || calls.Load() != 1 {
		t.Fatalf("cache mutated or refetched: %+v, calls=%d", cached, calls.Load())
	}
}

func TestProviderCoalescesLookupsAndCallerCanCancel(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		fmt.Fprint(w, episodePayload)
	}))
	defer srv.Close()
	p := NewProvider(ProviderOptions{Endpoint: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan Result, 1)
	go func() { first <- p.Lookup(ctx, episodeQuery()) }()
	<-entered
	cancel()
	if result := <-first; result.Status != StatusUnavailable {
		t.Fatalf("canceled request: %+v", result)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result := p.Lookup(context.Background(), episodeQuery()); result.Status != StatusReady {
				t.Errorf("shared request failed: %+v", result)
			}
		}()
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("made %d calls for same episode", calls.Load())
	}
}

func TestProviderRejectsBadResponseAndMismatchedMedia(t *testing.T) {
	for name, payload := range map[string]string{
		"movie":            `{"tmdb_id":42,"type":"movie","intro":[{"start_ms":0,"end_ms":90000}]}`,
		"other episode":    strings.Replace(episodePayload, `"episode":2`, `"episode":3`, 1),
		"other show":       strings.Replace(episodePayload, `"tmdb_id":42`, `"tmdb_id":43`, 1),
		"missing identity": `{"intro":[{"start_ms":0,"end_ms":90000}]}`,
		"malformed":        `{"intro":`,
		"oversized":        strings.Repeat(" ", providerMaxBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) }))
			defer srv.Close()
			result := NewProvider(ProviderOptions{Endpoint: srv.URL}).Lookup(context.Background(), episodeQuery())
			if result.Status != StatusUnavailable || len(result.Segments) != 0 {
				t.Fatalf("bad response accepted: %+v", result)
			}
		})
	}
}

func TestProviderRejectsInvalidAndOpenEndedIntroRanges(t *testing.T) {
	payload := `{"tmdb_id":42,"type":"tv","season":1,"episode":2,"intro":[{"start_ms":100,"end_ms":null},{"start_ms":0,"end_ms":2100001},{"start_ms":40000,"end_ms":30000},{"start_ms":null}],"credits":[{"start_ms":null,"end_ms":null},{"start_ms":2000000},{"start_ms":2000000,"end_ms":2100000.5}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, payload) }))
	defer srv.Close()
	if result := NewProvider(ProviderOptions{Endpoint: srv.URL}).Lookup(context.Background(), episodeQuery()); result.Status != StatusNotFound || len(result.Segments) != 0 {
		t.Fatalf("invalid ranges accepted: %+v", result)
	}
}

func TestProviderCachesNotFoundAndRecoversAfterFailureTTL(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusBadGateway} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(status)
					return
				}
				fmt.Fprint(w, episodePayload)
			}))
			defer srv.Close()
			p := NewProvider(ProviderOptions{Endpoint: srv.URL})
			result := p.Lookup(context.Background(), episodeQuery())
			if status == 404 && result.Status != StatusNotFound || status == 502 && result.Status != StatusUnavailable {
				t.Fatalf("wrong status: %+v", result)
			}
			p.Lookup(context.Background(), episodeQuery())
			if calls.Load() != 1 {
				t.Fatal("failure was not cached")
			}
			p.mu.Lock()
			for _, element := range p.cache {
				entry := element.Value.(cacheEntry)
				if status == 502 && time.Until(entry.expires) > providerFailureTTL {
					t.Fatal("transient failure cached too long")
				}
				entry.expires = time.Now().Add(-time.Second)
				element.Value = entry
			}
			p.mu.Unlock()
			if result := p.Lookup(context.Background(), episodeQuery()); result.Status != StatusReady || calls.Load() != 2 {
				t.Fatalf("did not recover: %+v", result)
			}
		})
	}
}

func TestProviderBackoffAndInvalidQueriesAvoidNetwork(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	p := NewProvider(ProviderOptions{Endpoint: srv.URL})
	p.Lookup(context.Background(), episodeQuery())
	other := episodeQuery()
	other.Episode++
	if result := p.Lookup(context.Background(), other); result.Status != StatusUnavailable || calls.Load() != 1 {
		t.Fatalf("rate limit ignored: %+v", result)
	}
	if time.Until(p.backoff) < 119*time.Second {
		t.Fatal("Retry-After not honored")
	}
	invalid := episodeQuery()
	invalid.Episode = 0
	p.Lookup(context.Background(), invalid)
	if result := NewProvider(ProviderOptions{Endpoint: srv.URL, Disabled: true}).Lookup(context.Background(), episodeQuery()); result.Status != StatusDisabled || calls.Load() != 1 {
		t.Fatalf("disabled provider requested upstream: %+v", result)
	}
}

func TestProviderBoundedCacheAndRateLimit(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(404) }))
	defer srv.Close()
	p := NewProvider(ProviderOptions{Endpoint: srv.URL})
	for i := 0; i < providerCacheEntries+10; i++ {
		p.mu.Lock()
		p.tokens = 4
		p.mu.Unlock()
		q := episodeQuery()
		q.Episode = i + 1
		p.Lookup(context.Background(), q)
	}
	if len(p.cache) != providerCacheEntries || p.lru.Len() != providerCacheEntries {
		t.Fatalf("cache grew without bound: %d", len(p.cache))
	}
	q := episodeQuery()
	q.Episode = 5000
	p.mu.Lock()
	p.tokens = 0
	p.refilled = time.Now()
	p.mu.Unlock()
	before := calls.Load()
	if result := p.Lookup(context.Background(), q); result.Status != StatusUnavailable || calls.Load() != before {
		t.Fatal("request limit ignored")
	}
}
