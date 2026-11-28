package proxycheck

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type dialFunc func(context.Context, string, string) (net.Conn, error)

var errEndpointDNS = errors.New("public endpoint DNS failed")
var errNonPublicEndpoint = errors.New("public endpoint DNS returned a private or reserved address")

type probeFailure struct{ kinds []string }

func (e *probeFailure) Error() string {
	return "HTTPS CONNECT/TLS probe failed: " + strings.Join(e.kinds, "+")
}

func probeErrorKind(err error) string {
	if errors.Is(err, errNonPublicEndpoint) {
		return "nonpublic_endpoint"
	}
	if errors.Is(err, errEndpointDNS) {
		return "dns"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	var verification *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	if errors.As(err, &verification) || errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &invalid) || errors.As(err, &record) {
		return "tls"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns"
	}
	return "connect_or_protocol"
}

func dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !publicHost(host) {
		return nil, errors.New("invalid public endpoint")
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, errEndpointDNS
	}
	return dialResolvedPublic(ctx, network, port, ips, (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext)
}

func dialResolvedPublic(ctx context.Context, network, port string, ips []netip.Addr, dial dialFunc) (net.Conn, error) {
	if len(ips) == 0 {
		return nil, errors.New("public endpoint DNS returned no addresses")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errNonPublicEndpoint
		}
	}
	var last error
	for _, ip := range ips {
		conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

func (m *Manager) proxyTransport(raw string) *http.Transport {
	u, _ := url.Parse(raw)
	return &http.Transport{
		Proxy: http.ProxyURL(u), DialContext: m.dial,
		TLSClientConfig: m.tlsConfig, TLSHandshakeTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10,
		MaxIdleConns: 8, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
		ForceAttemptHTTP2: false,
	}
}

type probeTarget struct {
	url      string
	status   int
	contains []string
}

var probeTargets = []probeTarget{
	{url: "https://www.google.com/generate_204", status: http.StatusNoContent},
	{url: "https://www.cloudflare.com/cdn-cgi/trace", status: http.StatusOK, contains: []string{"ip=", "tls=TLS"}},
}

func (m *Manager) checkProxy(ctx context.Context, address string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := m.proxyTransport(address)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var kinds []string
	for _, target := range m.targets {
		started := time.Now()
		targetCtx, targetCancel := context.WithTimeout(ctx, m.probeTimeout)
		req, err := http.NewRequestWithContext(targetCtx, http.MethodGet, target.url, nil)
		if err != nil {
			targetCancel()
			kinds = append(kinds, "request")
			continue
		}
		req.Header.Set("User-Agent", "video-viewer-proxy-check/1")
		resp, err := client.Do(req)
		if err != nil {
			targetCancel()
			kinds = append(kinds, probeErrorKind(err))
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
		_ = resp.Body.Close()
		targetCancel()
		valid := readErr == nil && len(body) <= 4096 && resp.TLS != nil && resp.TLS.HandshakeComplete && resp.StatusCode == target.status
		if target.status == http.StatusNoContent {
			valid = valid && len(body) == 0
		}
		for _, part := range target.contains {
			valid = valid && bytes.Contains(body, []byte(part))
		}
		if valid {
			return max(1, time.Since(started).Milliseconds()), nil
		}
		kind := "content"
		if readErr != nil {
			kind = probeErrorKind(readErr)
		} else if resp.TLS == nil || !resp.TLS.HandshakeComplete {
			kind = "tls"
		} else if resp.StatusCode != target.status {
			kind = "http_status"
		}
		kinds = append(kinds, kind)
	}
	return 0, &probeFailure{kinds: kinds}
}

func (m *Manager) fetchSource(ctx context.Context, source string) ([]string, error) {
	if !validSourceURL(source) {
		return nil, errors.New("invalid proxy source")
	}
	transport := &http.Transport{DialContext: m.dial, TLSClientConfig: m.tlsConfig, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second, MaxResponseHeaderBytes: 32 << 10}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !validSourceURL(r.URL.String()) {
			return errors.New("unsafe source redirect")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("proxy list fetch failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy list HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes+1))
	if err != nil || len(data) > maxSourceBytes {
		return nil, errors.New("proxy list exceeds 1 MiB or could not be read")
	}
	return parseList(data), nil
}

func validDestination(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !publicHost(host) {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// openProxyTunnel writes only CONNECT before returning the connection. The
// caller performs the origin TLS handshake and owns the returned tunnel.
func (m *Manager) openProxyTunnel(ctx context.Context, proxy, destination string) (net.Conn, error) {
	u, err := url.Parse(proxy)
	if err != nil || !validDestination(destination) {
		return nil, errors.New("invalid proxy tunnel endpoint")
	}
	conn, err := m.dial(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	if d, has := ctx.Deadline(); has && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if u.Scheme == "https" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
		if m.tlsConfig != nil {
			cfg = m.tlsConfig.Clone()
			cfg.ServerName = u.Hostname()
		}
		tlsConn := tls.Client(conn, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = tlsConn
	}
	if _, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: video-viewer-proxy/1\r\n\r\n", destination, destination); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	header := make([]byte, 0, 1024)
	for len(header) < 16<<10 {
		b, readErr := reader.ReadByte()
		if readErr != nil {
			return nil, readErr
		}
		header = append(header, b)
		if len(header) >= 4 && bytes.Equal(header[len(header)-4:], []byte("\r\n\r\n")) {
			break
		}
	}
	if !bytes.HasSuffix(header, []byte("\r\n\r\n")) {
		return nil, errors.New("proxy CONNECT headers too large")
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(header)), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, errors.New("invalid proxy CONNECT response")
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy CONNECT HTTP %d", resp.StatusCode)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

func requestDestination(r *http.Request) bool {
	if r == nil || r.URL == nil || (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.User != nil {
		return false
	}
	port := r.URL.Port()
	if port == "" {
		port = "80"
		if r.URL.Scheme == "https" {
			port = "443"
		}
	}
	return validDestination(net.JoinHostPort(r.URL.Hostname(), port)) && !strings.ContainsAny(r.Host, "\r\n")
}
