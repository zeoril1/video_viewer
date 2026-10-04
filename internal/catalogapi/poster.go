package catalogapi

import (
	"container/list"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	posterMaxBytes     = 4 << 20
	posterCacheBytes   = 32 << 20
	posterCacheEntries = 512
	posterTTL          = 24 * time.Hour
)

var tmdbPosterPath = regexp.MustCompile(`^/t/p/(w92|w154|w185|w342|w500|w780|original)/[a-zA-Z0-9_-]+\.(jpg|jpeg|png|webp)$`)
var imdbPosterPath = regexp.MustCompile(`^/images/[a-zA-Z0-9_@.,!()/+-]+\.(jpg|jpeg|png|webp)$`)

func allowedPosterURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, fmt.Errorf("invalid poster URL")
	}
	u.Host = strings.ToLower(u.Host)
	switch u.Host {
	case "image.tmdb.org":
		if tmdbPosterPath.MatchString(u.Path) {
			return u, nil
		}
	case "m.media-amazon.com", "images-na.ssl-images-amazon.com", "ia.media-imdb.com":
		if imdbPosterPath.MatchString(u.Path) && !strings.Contains(u.Path, "..") {
			return u, nil
		}
	}
	return nil, fmt.Errorf("unsupported poster URL")
}

type posterEntry struct {
	key, contentType, etag string
	body                   []byte
	expires                time.Time
}

type posterProxy struct {
	client  *http.Client
	slots   chan struct{}
	mu      sync.Mutex
	lru     *list.List
	entries map[string]*list.Element
	bytes   int
}

func newPosterProxy() *posterProxy {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment // Gluetun in the catalog container.
	transport.MaxConnsPerHost = 8
	return &posterProxy{
		client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many image redirects")
			}
			_, err := allowedPosterURL(req.URL.String())
			return err
		}},
		slots: make(chan struct{}, 8), lru: list.New(), entries: map[string]*list.Element{},
	}
}

func (p *posterProxy) cached(key string) (posterEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[key]
	if e == nil {
		return posterEntry{}, false
	}
	v := e.Value.(posterEntry)
	if time.Now().After(v.expires) {
		p.remove(e)
		return posterEntry{}, false
	}
	p.lru.MoveToFront(e)
	return v, true
}

func (p *posterProxy) remove(e *list.Element) {
	v := e.Value.(posterEntry)
	p.bytes -= len(v.body)
	delete(p.entries, v.key)
	p.lru.Remove(e)
}

func (p *posterProxy) put(v posterEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[v.key]; e != nil {
		p.remove(e)
	}
	for p.bytes+len(v.body) > posterCacheBytes || len(p.entries) >= posterCacheEntries {
		p.remove(p.lru.Back())
	}
	p.entries[v.key] = p.lru.PushFront(v)
	p.bytes += len(v.body)
}

func writePoster(w http.ResponseWriter, r *http.Request, v posterEntry) {
	w.Header().Set("Content-Type", v.contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("ETag", v.etag)
	if r.Header.Get("If-None-Match") == v.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(v.body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(v.body)
	}
}

func (p *posterProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u, err := allowedPosterURL(r.URL.Query().Get("url"))
	if err != nil {
		http.Error(w, "unsupported poster URL", http.StatusBadRequest)
		return
	}
	key := u.String()
	if v, ok := p.cached(key); ok {
		writePoster(w, r, v)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		http.Error(w, "image request timed out", http.StatusGatewayTimeout)
		return
	}
	if v, ok := p.cached(key); ok {
		writePoster(w, r, v)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
	if err != nil {
		http.Error(w, "invalid image request", http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", "video_viewer/1.0")
	req.Header.Set("Accept", "image/jpeg,image/png,image/webp,image/gif")
	resp, err := p.client.Do(req)
	if err != nil {
		http.Error(w, "image upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		status := http.StatusBadGateway
		if resp.StatusCode == http.StatusNotFound {
			status = http.StatusNotFound
		}
		http.Error(w, "image upstream unavailable", status)
		return
	}
	if resp.ContentLength > posterMaxBytes {
		http.Error(w, "image too large", http.StatusBadGateway)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, posterMaxBytes+1))
	if err != nil || len(body) > posterMaxBytes {
		http.Error(w, "invalid image response", http.StatusBadGateway)
		return
	}
	contentType := http.DetectContentType(body)
	switch contentType {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
	default:
		http.Error(w, "upstream did not return an image", http.StatusBadGateway)
		return
	}
	v := posterEntry{key: key, contentType: contentType, body: body, etag: fmt.Sprintf(`"%x"`, sha256.Sum256(body)), expires: time.Now().Add(posterTTL)}
	p.put(v)
	writePoster(w, r, v)
}
