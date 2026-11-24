package magnet

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zeoril1/video_viewer/internal/httpx"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const (
	maxVoiceDetails      = 24
	maxVoiceDetailsBytes = 1 << 20
	voiceDetailsCacheMax = 256
	voiceDetailsBudget   = 8 * time.Second
)

type voiceDetailsEntry struct {
	voices  []string
	expires time.Time
}

type voiceDetailsEnricher struct {
	client *http.Client
	sem    chan struct{}
	mu     sync.Mutex
	cache  map[string]voiceDetailsEntry
	active map[string]chan struct{}
}

func newVoiceDetailsEnricher() *voiceDetailsEnricher {
	transport := httpx.DirectTransport()
	// Resolve once and dial the checked IP, rather than checking DNS and then
	// allowing a second resolution to route a request to a private address.
	transport.DialContext = dialPublicDetails
	return &voiceDetailsEnricher{
		client: &http.Client{Timeout: 3 * time.Second, Transport: transport},
		sem:    make(chan struct{}, 3),
		cache:  make(map[string]voiceDetailsEntry),
		active: make(map[string]chan struct{}),
	}
}

func (e *voiceDetailsEnricher) enrich(ctx context.Context, id string, items []jackettItem, out []Result) {
	if id != "all" && !knownDetailsIndexer(id) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, voiceDetailsBudget)
	defer cancel()
	var wg sync.WaitGroup
	count := 0
	for i, item := range items {
		indexerID := id
		if id == "all" {
			indexerID = ""
			for _, candidate := range []string{"rutor", "noname-club", "rutracker-ru", "bigfangroup"} {
				if allowedDetailsURL(candidate, item.detailsURL) {
					indexerID = candidate
					break
				}
			}
		}
		hash := magnetInfoHash(out[i].Magnet)
		if hash == "" || !allowedDetailsURL(indexerID, item.detailsURL) || count >= maxVoiceDetails {
			continue
		}
		count++
		wg.Add(1)
		go func(i int, indexerID, detailsURL, hash string) {
			defer wg.Done()
			voices := e.lookup(ctx, indexerID, detailsURL, hash)
			out[i].Voices = mergeVoices(out[i].Voices, voices)
		}(i, indexerID, item.detailsURL, hash)
	}
	wg.Wait()
}

func knownDetailsIndexer(id string) bool {
	switch id {
	case "rutor", "noname-club", "rutracker-ru", "bigfangroup":
		return true
	}
	return false
}

// Only ordinary release-description pages are eligible. In particular, never
// follow an RSS comments URL to Jackett, a local service or a torrent endpoint.
func allowedDetailsURL(id, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" ||
		(u.Scheme != "https" && u.Scheme != "http") ||
		(u.Port() != "" && u.Port() != "443" && u.Port() != "80") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	path := u.Path
	switch id {
	case "rutor":
		parts := strings.Split(strings.TrimPrefix(path, "/torrent/"), "/")
		return (host == "rutor.info" || host == "rutor.is" || host == "6tor.org") &&
			strings.HasPrefix(path, "/torrent/") && positiveID(parts[0]) &&
			!strings.Contains(path, "/..") && !strings.Contains(path, `\`) && u.RawQuery == ""
	case "noname-club":
		return (host == "nnmclub.to" || host == "nnm-club.name" || host == "nnm-club.me") &&
			(path == "/forum/viewtopic.php" || path == "/viewtopic.php") && positiveID(u.Query().Get("t"))
	case "rutracker-ru":
		return (host == "rutracker.ru" || host == "rutracker.org") &&
			path == "/forum/viewtopic.php" && positiveID(u.Query().Get("t"))
	case "bigfangroup":
		return (host == "bigfangroup.org" || host == "www.freebfg.org" || host == "freebfg.org") &&
			path == "/details.php" && positiveID(u.Query().Get("id"))
	}
	return false
}

func positiveID(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	positive := false
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		positive = positive || r != '0'
	}
	return positive
}

func (e *voiceDetailsEnricher) lookup(ctx context.Context, id, rawURL, hash string) []string {
	// Topics can replace their torrent while keeping the same details URL.
	// Never transfer cached audio metadata from an old torrent to the new hash.
	key := id + " " + rawURL + " " + hash
	for {
		e.mu.Lock()
		if entry, ok := e.cache[key]; ok && time.Now().Before(entry.expires) {
			e.mu.Unlock()
			return append([]string(nil), entry.voices...)
		}
		if active := e.active[key]; active != nil {
			e.mu.Unlock()
			select {
			case <-active:
				continue
			case <-ctx.Done():
				return nil
			}
		}
		active := make(chan struct{})
		e.active[key] = active
		e.mu.Unlock()
		voices, err := e.fetch(ctx, id, rawURL)
		e.mu.Lock()
		delete(e.active, key)
		if len(e.cache) >= voiceDetailsCacheMax {
			var oldest string
			var expires time.Time
			for k, entry := range e.cache {
				if oldest == "" || entry.expires.Before(expires) {
					oldest, expires = k, entry.expires
				}
			}
			delete(e.cache, oldest)
		}
		ttl := 6 * time.Hour
		if err != nil {
			ttl = 5 * time.Minute
		}
		e.cache[key] = voiceDetailsEntry{voices: append([]string(nil), voices...), expires: time.Now().Add(ttl)}
		close(active)
		e.mu.Unlock()
		return voices
	}
}

func (e *voiceDetailsEnricher) fetch(ctx context.Context, id, rawURL string) ([]string, error) {
	if !allowedDetailsURL(id, rawURL) {
		return nil, fmt.Errorf("unsupported release details URL")
	}
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Copy the client so concurrent indexers get their own redirect allowlist.
	client := *e.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !allowedDetailsURL(id, req.URL.String()) {
			return fmt.Errorf("unsupported release details redirect")
		}
		req.Header.Del("Referer")
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release details HTTP %d", resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(strings.ToLower(contentType), "text/html") &&
		!strings.Contains(strings.ToLower(contentType), "application/xhtml+xml") {
		return nil, fmt.Errorf("release details are not HTML")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxVoiceDetailsBytes+1))
	if err != nil || len(body) > maxVoiceDetailsBytes {
		return nil, fmt.Errorf("release details body unavailable or too large")
	}
	// Respect header/meta encoding. Some tracker templates still serve CP1251
	// while others have moved to UTF-8 without declaring the encoding.
	if !strings.Contains(strings.ToLower(contentType), "charset=") &&
		!strings.Contains(strings.ToLower(string(body[:min(len(body), 1024)])), "charset") {
		contentType = "text/html; charset=utf-8"
		if !utf8.Valid(body) {
			contentType = "text/html; charset=windows-1251"
		}
	}
	reader, err := charset.NewReader(strings.NewReader(string(body)), contentType)
	if err != nil {
		return nil, err
	}
	doc, err := xhtml.Parse(reader)
	if err != nil {
		return nil, err
	}
	main := mainReleaseDescription(doc, id)
	if main == nil {
		return nil, nil
	}
	return voicesFromMetadataText(metadataText(main)), nil
}

func mainReleaseDescription(doc *xhtml.Node, id string) *xhtml.Node {
	if id == "rutor" {
		details := findHTMLNode(doc, func(n *xhtml.Node) bool { return nodeAttr(n, "id") == "details" })
		if details == nil {
			return nil
		}
		// #details also wraps recommendations. Only its first row is the
		// release description; later rows contain related torrent titles.
		row := findHTMLNode(details, func(n *xhtml.Node) bool { return n.Data == "tr" })
		if row == nil {
			return nil
		}
		var cells []*xhtml.Node
		for c := row.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == xhtml.ElementNode && c.Data == "td" {
				cells = append(cells, c)
			}
		}
		if len(cells) > 1 {
			return cells[1]
		}
		return nil
	}
	if id == "noname-club" || id == "rutracker-ru" {
		return findHTMLNode(doc, func(n *xhtml.Node) bool {
			for _, class := range strings.Fields(nodeAttr(n, "class")) {
				if class == "postbody" {
					return true
				}
			}
			return false
		})
	}
	if id == "bigfangroup" {
		if container := findHTMLNode(doc, func(n *xhtml.Node) bool {
			switch strings.ToLower(nodeAttr(n, "id")) {
			case "description", "descr", "torrent-description":
				return true
			}
			return false
		}); container != nil {
			return container
		}
		// TorrentTrader variants label the description in a separate table
		// cell. Do not fall back to a page-wide scan if the layout changes.
		row := findHTMLNode(doc, func(n *xhtml.Node) bool {
			if n.Data != "tr" {
				return false
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == xhtml.ElementNode && c.Data == "td" {
					label := strings.ToLower(strings.Trim(strings.TrimSpace(metadataText(c)), ":"))
					return label == "описание" || label == "description"
				}
			}
			return false
		})
		if row != nil {
			seenLabel := false
			for c := row.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == xhtml.ElementNode && c.Data == "td" {
					if seenLabel {
						return c
					}
					seenLabel = true
				}
			}
		}
	}
	return nil
}

func findHTMLNode(n *xhtml.Node, match func(*xhtml.Node) bool) *xhtml.Node {
	if n.Type == xhtml.ElementNode && match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findHTMLNode(c, match); found != nil {
			return found
		}
	}
	return nil
}

func nodeAttr(n *xhtml.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

var nonPublicDetailsNets = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func publicDetailsIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, block := range nonPublicDetailsNets {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

func dialPublicDetails(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !publicDetailsIP(ip) {
			return nil, fmt.Errorf("release details DNS returned a non-public address")
		}
	}
	var lastErr error
	for _, ip := range ips {
		dialer := net.Dialer{Timeout: 3 * time.Second}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("release details DNS returned no addresses")
	}
	return nil, lastErr
}
