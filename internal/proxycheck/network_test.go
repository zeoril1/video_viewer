package proxycheck

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func localConnectProxy(t *testing.T, origin string, secure bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	count := new(atomic.Int32)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", 400)
			return
		}
		count.Add(1)
		upstream, err := net.DialTimeout("tcp", origin, time.Second)
		if err != nil {
			http.Error(w, "test origin unavailable", 502)
			return
		}
		client, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		_, _ = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { _, _ = io.Copy(client, upstream); client.Close(); upstream.Close() }()
		_, _ = io.Copy(upstream, client)
		client.Close()
		upstream.Close()
	})
	var proxy *httptest.Server
	if secure {
		proxy = httptest.NewTLSServer(handler)
	} else {
		proxy = httptest.NewServer(handler)
	}
	t.Cleanup(proxy.Close)
	return proxy, count
}

func TestCheckerUsesActualCONNECTTLSAndResponseCriteria(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("https_proxy_%t", secure), func(t *testing.T) {
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/valid" {
					w.WriteHeader(204)
				} else {
					fmt.Fprint(w, "wrong probe response")
				}
			}))
			defer origin.Close()
			proxy, connects := localConnectProxy(t, origin.Listener.Addr().String(), secure)
			m := testManager(t)
			roots := x509.NewCertPool()
			roots.AddCert(origin.Certificate())
			if secure {
				roots.AddCert(proxy.Certificate())
			}
			m.tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			m.targets = []probeTarget{{url: "https://example.com/invalid", status: 204}, {url: "https://example.com/valid", status: 204}}
			_, port, _ := net.SplitHostPort(proxy.Listener.Addr().String())
			scheme := "http"
			if secure {
				scheme = "https"
			}
			address := scheme + "://example.com:" + port
			m.dial = func(ctx context.Context, network, got string) (net.Conn, error) {
				if got != "example.com:"+port {
					t.Errorf("checker bypassed proxy: %s", got)
				}
				return (&net.Dialer{}).DialContext(ctx, network, proxy.Listener.Addr().String())
			}
			latency, err := m.checkProxy(context.Background(), address)
			if err != nil || latency < 1 || connects.Load() == 0 {
				t.Fatalf("real CONNECT/TLS check failed latency=%d connects=%d err=%v", latency, connects.Load(), err)
			}
			m.targets = []probeTarget{{url: "https://example.com/invalid", status: 204}}
			if _, err := m.checkProxy(context.Background(), address); err == nil {
				t.Fatal("wrong HTTP response was accepted")
			}
			m.tlsConfig = nil
			m.targets = []probeTarget{{url: "https://example.com/valid", status: 204}}
			if _, err := m.checkProxy(context.Background(), address); err == nil {
				t.Fatal("untrusted TLS certificate was accepted")
			}
		})
	}
}

func TestCheckerTriesSecondTargetAfterFirstTargetTimeout(t *testing.T) {
	var first, second atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			first.Add(1)
			<-r.Context().Done()
			return
		}
		second.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	proxy, _ := localConnectProxy(t, origin.Listener.Addr().String(), false)
	m := testManager(t)
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	m.tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	m.probeTimeout = 100 * time.Millisecond
	m.targets = []probeTarget{{url: "https://example.com/hang", status: 204}, {url: "https://example.com/valid", status: 204}}
	m.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, proxy.Listener.Addr().String())
	}
	_, port, _ := net.SplitHostPort(proxy.Listener.Addr().String())
	latency, err := m.checkProxy(context.Background(), "http://8.8.8.8:"+port)
	if err != nil || latency < 1 || first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("second target was starved: latency=%d first=%d second=%d err=%v", latency, first.Load(), second.Load(), err)
	}
}

func TestDialContextBuildsHTTPAndHTTPSProxyTunnels(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("https_proxy_%t", secure), func(t *testing.T) {
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "tunnel worked") }))
			defer origin.Close()
			proxy, connects := localConnectProxy(t, origin.Listener.Addr().String(), secure)
			m := testManager(t)
			roots := x509.NewCertPool()
			roots.AddCert(origin.Certificate())
			if secure {
				roots.AddCert(proxy.Certificate())
			}
			m.tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			_, port, _ := net.SplitHostPort(proxy.Listener.Addr().String())
			scheme := "http"
			if secure {
				scheme = "https"
			}
			address := scheme + "://example.com:" + port
			m.items[address] = Item{Address: address, CheckedAt: time.Now()}
			m.dial = func(ctx context.Context, network, got string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, proxy.Listener.Addr().String())
			}
			conn, err := m.DialContext(context.Background(), "tcp", "example.com:443")
			if err != nil {
				t.Fatal(err)
			}
			tlsConn := tls.Client(conn, &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12})
			defer tlsConn.Close()
			if err := tlsConn.Handshake(); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(tlsConn, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
			resp, err := http.ReadResponse(bufio.NewReader(tlsConn), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if string(body) != "tunnel worked" || connects.Load() != 1 {
				t.Fatalf("tunnel response=%q CONNECTs=%d", body, connects.Load())
			}
		})
	}
}

func TestTunnelPreservesBufferedBytesAndCancelsBlockedCONNECT(t *testing.T) {
	m := testManager(t)
	m.dial = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			_, _ = http.ReadRequest(bufio.NewReader(server))
			_, _ = io.WriteString(server, "HTTP/1.1 200 Connection Established\r\n\r\nHELLO")
		}()
		return client, nil
	}
	conn, err := m.openProxyTunnel(context.Background(), "http://8.8.8.8:8080", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 5)
	_, err = io.ReadFull(conn, data)
	conn.Close()
	if err != nil || string(data) != "HELLO" {
		t.Fatalf("buffered bytes lost: %q %v", data, err)
	}
	done := make(chan struct{})
	m.dial = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() { defer close(done); defer server.Close(); _, _ = io.Copy(io.Discard, server) }()
		return client, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if conn, err := m.openProxyTunnel(ctx, "http://8.8.8.8:8080", "example.com:443"); err == nil {
		conn.Close()
		t.Fatal("blocked CONNECT ignored cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled CONNECT leaked its socket")
	}
}

func TestRoundTripGETNoBodyFailoverButNeverReplaysPOST(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			m := testManager(t)
			m.items["http://8.8.8.8:8080"] = Item{Address: "http://8.8.8.8:8080", CheckedAt: time.Now(), LatencyMS: 1}
			m.items["http://1.1.1.1:8080"] = Item{Address: "http://1.1.1.1:8080", CheckedAt: time.Now(), LatencyMS: 2}
			var dials atomic.Int32
			m.dial = func(_ context.Context, _, address string) (net.Conn, error) {
				dials.Add(1)
				if address == "8.8.8.8:8080" {
					return nil, errors.New("injected first proxy failure")
				}
				if address != "1.1.1.1:8080" {
					t.Errorf("direct fallback attempted: %s", address)
					return nil, errors.New("unexpected address")
				}
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					r, err := http.ReadRequest(bufio.NewReader(server))
					if err == nil && !r.URL.IsAbs() {
						t.Error("HTTP origin was reached directly")
					}
					_, _ = io.WriteString(server, "HTTP/1.1 200 OK\r\nContent-Length: 7\r\nConnection: close\r\n\r\nproxied")
				}()
				return client, nil
			}
			req, _ := http.NewRequest(method, "http://example.com/file", nil)
			req.Body = http.NoBody
			if method == http.MethodPost {
				req, _ = http.NewRequest(method, "http://example.com/file", strings.NewReader("replayable but non-idempotent"))
			}
			resp, err := m.RoundTrip(req)
			if method == http.MethodPost {
				if err == nil || dials.Load() != 1 {
					t.Fatalf("POST retried: dials=%d err=%v", dials.Load(), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "proxied" || dials.Load() != 2 || m.Status().Available != 1 {
				t.Fatalf("GET failover body=%q calls=%d status=%#v", body, dials.Load(), m.Status())
			}
		})
	}
}

func TestSourceFetchHTTPSBoundsAndRedirectSSRF(t *testing.T) {
	for _, mode := range []string{"oversized", "private_redirect", "list"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "oversized":
					fmt.Fprint(w, strings.Repeat("x", maxSourceBytes+1))
				case "private_redirect":
					http.Redirect(w, r, "https://127.0.0.1/private", 302)
				default:
					fmt.Fprint(w, "8.8.8.8:8080\n127.0.0.1:80\n")
				}
			}))
			defer server.Close()
			m := testManager(t)
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			m.tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			m.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			list, err := m.fetchSource(context.Background(), "https://example.com/proxies.txt")
			if mode != "list" {
				if err == nil {
					t.Fatalf("unsafe source response %s was accepted", mode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0] != "http://8.8.8.8:8080" {
				t.Fatalf("public list %v", list)
			}
		})
	}
	if validSourceURL("https://127.0.0.1/private") {
		t.Fatal("private redirect URL accepted")
	}
}

func TestParentCancellationDoesNotRemoveHealthyProxy(t *testing.T) {
	for _, kind := range []string{"http", "connect"} {
		t.Run(kind, func(t *testing.T) {
			m := testManager(t)
			address := "http://8.8.8.8:8080"
			m.items[address] = Item{Address: address, CheckedAt: time.Now()}
			entered := make(chan struct{})
			m.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if kind == "connect" {
					conn, err := m.DialContext(ctx, "tcp", "example.com:443")
					if conn != nil {
						conn.Close()
					}
					done <- err
				} else {
					req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/file", nil)
					resp, err := m.RoundTrip(req)
					if resp != nil {
						resp.Body.Close()
					}
					done <- err
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("proxy dial never began")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation result: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not stop request")
			}
			if m.Status().Available != 1 {
				t.Fatal("caller cancellation marked healthy proxy broken")
			}
		})
	}
}
