package httpx

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DirectTransport deliberately ignores process-wide proxy variables. VPN routing
// belongs to explicit poster/.torrent clients, never to video or internal requests.
func DirectTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return t
}

type unavailableTorrentVPN struct{}

func (unavailableTorrentVPN) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("VPN proxy is not configured")
}

// VPNHTTPClient never falls back to a direct connection, including when
// the proxy is unset or down. Explicit ProxyURL also ignores NO_PROXY bypasses.
func VPNHTTPClient(proxy string) (*http.Client, error) {
	client := &http.Client{Timeout: 30 * time.Second, Transport: unavailableTorrentVPN{}}
	if strings.TrimSpace(proxy) == "" {
		return client, nil
	}
	u, err := url.Parse(strings.TrimSpace(proxy))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid VPN proxy URL")
	}
	t := DirectTransport()
	t.Proxy = http.ProxyURL(u)
	client.Transport = t
	return client, nil
}
