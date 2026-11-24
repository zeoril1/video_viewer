package torrentvpn

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type proxyHandler struct {
	manager *Manager
	slots   chan struct{}
}

// NewProxyHandler accepts forward HTTP and CONNECT requests on an internal-only
// listener. Every upstream TCP connection and destination DNS lookup uses the
// active WireGuard netstack. There is no direct-network fallback.
func NewProxyHandler(manager *Manager) http.Handler {
	return &proxyHandler{manager: manager, slots: make(chan struct{}, 32)}
}

func (p *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "VPN proxy busy", http.StatusServiceUnavailable)
		return
	}
	if _, err := p.manager.active(); err != nil {
		http.Error(w, "VPN is not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" || r.URL.User != nil {
		http.Error(w, "absolute HTTP URL required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out := r.Clone(ctx)
	out.RequestURI = ""
	out.Header = r.Header.Clone()
	removeHopHeaders(out.Header)
	resp, err := p.manager.RoundTrip(out)
	if err != nil {
		proxyError(w, err)
		return
	}
	defer resp.Body.Close()
	removeHopHeaders(resp.Header)
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *proxyHandler) connect(w http.ResponseWriter, r *http.Request) {
	host, portText, err := net.SplitHostPort(r.Host)
	port, portErr := strconv.Atoi(portText)
	if err != nil || !validHost(host) || portErr != nil || port < 1 || port > 65535 {
		http.Error(w, "invalid CONNECT address", http.StatusBadRequest)
		return
	}
	upstream, err := p.manager.DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		proxyError(w, err)
		return
	}
	defer upstream.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "CONNECT unavailable", http.StatusInternalServerError)
		return
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	deadline := time.Now().Add(2 * time.Minute)
	_ = client.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = buffer.Flush(); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, buffer); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

func proxyError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnconfigured) {
		http.Error(w, "VPN is not configured", http.StatusServiceUnavailable)
	} else {
		http.Error(w, "VPN download connection failed", http.StatusBadGateway)
	}
}
func removeHopHeaders(header http.Header) {
	for _, name := range strings.Split(header.Get("Connection"), ",") {
		header.Del(strings.TrimSpace(name))
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}
