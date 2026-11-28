package torrentvpn

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zeoril1/video_viewer/internal/httpx"
)

type fallbackTestNetwork struct {
	round  func(*http.Request) (*http.Response, error)
	dial   func(context.Context, string, string) (net.Conn, error)
	rounds atomic.Int32
	dials  atomic.Int32
	closed atomic.Bool
}

func (n *fallbackTestNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.rounds.Add(1)
	if n.round != nil {
		return n.round(r)
	}
	return fallbackTestResponse(http.StatusOK, "fallback"), nil
}

func (n *fallbackTestNetwork) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	n.dials.Add(1)
	if n.dial != nil {
		return n.dial(ctx, network, address)
	}
	local, remote := net.Pipe()
	_ = remote.Close()
	return local, nil
}

func (n *fallbackTestNetwork) Close() { n.closed.Store(true) }

func fallbackTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func fallbackTestManager(t *testing.T, primary *fallbackTestNetwork, configured bool) *Manager {
	t.Helper()
	m := newManager(filepath.Join(t.TempDir(), "wg0.conf"), func(tunnelConfig) (networkTunnel, error) { return primary, nil })
	t.Cleanup(m.Close)
	if configured {
		if _, err := m.Replace("primary.conf", testConfig()); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func fallbackTestRequest(t *testing.T, ctx context.Context, method string, body io.Reader) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, method, "http://metadata.invalid/release.torrent", body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFallbackRouterKeepsSuccessfulVPNAndOriginHTTPStatuses(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			primary := &fallbackTestNetwork{round: func(*http.Request) (*http.Response, error) {
				return fallbackTestResponse(status, "origin response"), nil
			}}
			fallback := &fallbackTestNetwork{}
			router := newFallbackRouter(fallbackTestManager(t, primary, true), fallback)
			response, err := router.RoundTrip(fallbackTestRequest(t, context.Background(), http.MethodGet, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != status || string(body) != "origin response" || fallback.rounds.Load() != 0 {
				t.Fatal("an origin HTTP response was incorrectly retried", response.StatusCode, string(body), err, fallback.rounds.Load())
			}
		})
	}
}

func TestFallbackRouterHandlesMissingVPNBeforeReadingPOSTBody(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			var received string
			primary := &fallbackTestNetwork{}
			fallback := &fallbackTestNetwork{round: func(r *http.Request) (*http.Response, error) {
				if r.Body != nil {
					defer r.Body.Close()
					body, err := io.ReadAll(r.Body)
					if err != nil {
						return nil, err
					}
					received = string(body)
				}
				return fallbackTestResponse(http.StatusOK, "via proxy"), nil
			}}
			router := newFallbackRouter(fallbackTestManager(t, primary, false), fallback)
			request := fallbackTestRequest(t, context.Background(), method, strings.NewReader("tracker=form"))
			request.GetBody = nil // Incoming proxy requests do not have a replay factory.
			response, err := router.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if received != "tracker=form" || fallback.rounds.Load() != 1 || primary.rounds.Load() != 0 {
				t.Fatal("missing VPN did not preserve the untouched request", received, fallback.rounds.Load(), primary.rounds.Load())
			}
		})
	}
}

func TestFallbackRouterVPNTimeoutUsesOriginalContextForFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	primary := &fallbackTestNetwork{round: func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}}
	var fallbackContextErr error
	var fallbackDeadline time.Time
	fallback := &fallbackTestNetwork{round: func(r *http.Request) (*http.Response, error) {
		fallbackContextErr = r.Context().Err()
		fallbackDeadline, _ = r.Context().Deadline()
		return fallbackTestResponse(http.StatusOK, "proxy survived VPN timeout"), nil
	}}
	router := newFallbackRouter(fallbackTestManager(t, primary, true), fallback)
	router.primaryTimeout = 5 * time.Millisecond
	response, err := router.RoundTrip(fallbackTestRequest(t, ctx, http.MethodGet, nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if fallbackContextErr != nil || !fallbackDeadline.Equal(deadline) || fallback.rounds.Load() != 1 {
		t.Fatal("VPN attempt canceled the fallback's original request budget", fallbackContextErr, fallbackDeadline, deadline)
	}
}

func TestFallbackRouterDialRespectsVPNBudgetAndClientCancellation(t *testing.T) {
	for _, mode := range []string{"success", "timeout", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			primary := &fallbackTestNetwork{dial: func(attempt context.Context, _, _ string) (net.Conn, error) {
				if mode == "success" {
					local, remote := net.Pipe()
					remote.Close()
					return local, nil
				}
				if mode == "canceled" {
					cancel()
				}
				<-attempt.Done()
				return nil, attempt.Err()
			}}
			var fallbackContextErr error
			var fallbackDeadline time.Time
			fallback := &fallbackTestNetwork{dial: func(attempt context.Context, _, _ string) (net.Conn, error) {
				fallbackContextErr = attempt.Err()
				fallbackDeadline, _ = attempt.Deadline()
				local, remote := net.Pipe()
				remote.Close()
				return local, nil
			}}
			router := newFallbackRouter(fallbackTestManager(t, primary, true), fallback)
			router.primaryTimeout = 5 * time.Millisecond
			connection, err := router.DialContext(ctx, "tcp", "metadata.invalid:443")
			if connection != nil {
				connection.Close()
			}
			if mode == "canceled" {
				if !errors.Is(err, context.Canceled) || fallback.dials.Load() != 0 {
					t.Fatal("canceled CONNECT activated fallback", err, fallback.dials.Load())
				}
				return
			}
			if err != nil || connection == nil {
				t.Fatal("CONNECT route failed", err)
			}
			if mode == "success" && fallback.dials.Load() != 0 {
				t.Fatal("successful VPN CONNECT activated fallback")
			}
			if mode == "timeout" && (fallback.dials.Load() != 1 || fallbackContextErr != nil || !fallbackDeadline.Equal(deadline)) {
				t.Fatal("timed-out VPN CONNECT consumed the fallback context", fallbackContextErr, fallbackDeadline)
			}
		})
	}
}

type fallbackContextBody struct {
	ctx context.Context
	*strings.Reader
}

func (b *fallbackContextBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	return b.Reader.Read(p)
}
func (b *fallbackContextBody) Close() error { return nil }

func TestFallbackRouterSuccessKeepsBodyReadableAfterHeaderBudget(t *testing.T) {
	primary := &fallbackTestNetwork{round: func(r *http.Request) (*http.Response, error) {
		response := fallbackTestResponse(http.StatusOK, "")
		response.Body = &fallbackContextBody{ctx: r.Context(), Reader: strings.NewReader("poster body after headers")}
		return response, nil
	}}
	fallback := &fallbackTestNetwork{}
	router := newFallbackRouter(fallbackTestManager(t, primary, true), fallback)
	router.primaryTimeout = 5 * time.Millisecond
	response, err := router.RoundTrip(fallbackTestRequest(t, context.Background(), http.MethodGet, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	time.Sleep(20 * time.Millisecond)
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "poster body after headers" || fallback.rounds.Load() != 0 {
		t.Fatal("successful headers left a short deadline attached to the response body", string(body), err)
	}
}

func TestFallbackRouterCanceledRequestDoesNotActivateFallback(t *testing.T) {
	for _, cancelBefore := range []bool{true, false} {
		t.Run(map[bool]string{true: "before attempt", false: "during attempt"}[cancelBefore], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			primary := &fallbackTestNetwork{round: func(r *http.Request) (*http.Response, error) {
				cancel()
				<-r.Context().Done()
				return nil, r.Context().Err()
			}}
			fallback := &fallbackTestNetwork{}
			router := newFallbackRouter(fallbackTestManager(t, primary, true), fallback)
			if cancelBefore {
				cancel()
			}
			response, err := router.RoundTrip(fallbackTestRequest(t, ctx, http.MethodGet, nil))
			if response != nil {
				response.Body.Close()
			}
			if !errors.Is(err, context.Canceled) || fallback.rounds.Load() != 0 {
				t.Fatal("a canceled client request triggered a new route", err, fallback.rounds.Load())
			}
		})
	}
}

func TestFallbackRouterDoesNotReplayNonIdempotentPOSTAfterAttempt(t *testing.T) {
	var submitted string
	primary := &fallbackTestNetwork{round: func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		submitted = string(body)
		return nil, errors.New("upstream disconnected after accepting form")
	}}
	fallback := &fallbackTestNetwork{}
	router := newFallbackRouter(fallbackTestManager(t, primary, true), fallback)
	request := fallbackTestRequest(t, context.Background(), http.MethodPost, strings.NewReader("tracker=submit-once"))
	// A recreatable body is not proof that repeating this POST is safe.
	if request.GetBody == nil {
		t.Fatal("test must cover POST even when GetBody exists")
	}
	response, err := router.RoundTrip(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || submitted != "tracker=submit-once" || fallback.rounds.Load() != 0 {
		t.Fatal("a possibly submitted POST was replayed", err, submitted, fallback.rounds.Load())
	}
}

func TestFallbackRouterCooldownSkipsFailedVPNAndRetriesLater(t *testing.T) {
	primary := &fallbackTestNetwork{round: func(*http.Request) (*http.Response, error) {
		return nil, errors.New("VPN offline")
	}}
	fallback := &fallbackTestNetwork{}
	router := newFallbackRouter(fallbackTestManager(t, primary, true), fallback)
	router.retryDelay = 20 * time.Millisecond
	get := func() {
		t.Helper()
		response, err := router.RoundTrip(fallbackTestRequest(t, context.Background(), http.MethodGet, nil))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	get()
	get()
	if primary.rounds.Load() != 1 || fallback.rounds.Load() != 2 {
		t.Fatal("failed VPN was retried inside the cooldown", primary.rounds.Load(), fallback.rounds.Load())
	}
	connection, err := router.DialContext(context.Background(), "tcp", "metadata.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	if primary.dials.Load() != 0 || fallback.dials.Load() != 1 {
		t.Fatal("CONNECT did not share the failed VPN cooldown")
	}
	time.Sleep(40 * time.Millisecond)
	get()
	if primary.rounds.Load() != 2 {
		t.Fatal("VPN was never retried after its cooldown", primary.rounds.Load())
	}
}

func TestFallbackRouterConfigurationReplacementClearsCooldown(t *testing.T) {
	failed := &fallbackTestNetwork{round: func(*http.Request) (*http.Response, error) { return nil, errors.New("old VPN offline") }}
	recovered := &fallbackTestNetwork{round: func(*http.Request) (*http.Response, error) {
		return fallbackTestResponse(http.StatusOK, "new VPN"), nil
	}}
	var opened int
	m := newManager(filepath.Join(t.TempDir(), "wg0.conf"), func(tunnelConfig) (networkTunnel, error) {
		opened++
		if opened == 1 {
			return failed, nil
		}
		return recovered, nil
	})
	t.Cleanup(m.Close)
	if _, err := m.Replace("old.conf", testConfig()); err != nil {
		t.Fatal(err)
	}
	fallback := &fallbackTestNetwork{}
	router := newFallbackRouter(m, fallback)
	router.retryDelay = time.Hour
	response, err := router.RoundTrip(fallbackTestRequest(t, context.Background(), http.MethodGet, nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if _, err := m.Replace("new.conf", testConfig()); err != nil {
		t.Fatal(err)
	}
	response, err = router.RoundTrip(fallbackTestRequest(t, context.Background(), http.MethodGet, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "new VPN" || recovered.rounds.Load() != 1 || fallback.rounds.Load() != 1 || !failed.closed.Load() {
		t.Fatal("replacement VPN remained stuck in the old tunnel's cooldown", string(body), err)
	}
}

func TestProxyHandlerFallbackCarriesRealHTTPAndHTTPSWithoutDestinationDNS(t *testing.T) {
	for _, https := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP", true: "HTTPS CONNECT"}[https], func(t *testing.T) {
			var originCalls atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originCalls.Add(1)
				if r.URL.Path != "/release.torrent" || r.Header.Get("Cookie") != "tracker=session" {
					http.Error(w, "lost tracker request metadata", http.StatusBadRequest)
					return
				}
				_, _ = io.WriteString(w, "metadata through fallback")
			})
			var origin *httptest.Server
			if https {
				origin = httptest.NewTLSServer(handler)
			} else {
				origin = httptest.NewServer(handler)
			}
			defer origin.Close()
			primary := &fallbackTestNetwork{
				round: func(*http.Request) (*http.Response, error) { return nil, errors.New("VPN offline") },
				dial:  func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("VPN offline") },
			}
			direct := httpx.DirectTransport()
			defer direct.CloseIdleConnections()
			fallback := &fallbackTestNetwork{
				round: func(r *http.Request) (*http.Response, error) {
					copy := r.Clone(r.Context())
					destination := *r.URL
					destination.Host = origin.Listener.Addr().String()
					copy.URL = &destination
					return direct.RoundTrip(copy)
				},
				dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					if address != "metadata.invalid:443" {
						return nil, errors.New("CONNECT destination changed")
					}
					return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
				},
			}
			central := httptest.NewServer(NewProxyHandler(fallbackTestManager(t, primary, true), fallback))
			defer central.Close()
			proxyURL, _ := url.Parse(central.URL)
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			scheme := "http"
			if https {
				scheme = "https"
			}
			request, _ := http.NewRequest(http.MethodGet, scheme+"://metadata.invalid/release.torrent", nil)
			request.Header.Set("Cookie", "tracker=session")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != 200 || string(body) != "metadata through fallback" || originCalls.Load() != 1 {
				t.Fatal("fallback did not carry the real request", string(body), err, originCalls.Load())
			}
			if https && (fallback.dials.Load() != 1 || fallback.rounds.Load() != 0) {
				t.Fatal("HTTPS did not use an opaque fallback tunnel")
			}
			if !https && (fallback.rounds.Load() != 1 || fallback.dials.Load() != 0) {
				t.Fatal("HTTP did not use fallback RoundTrip")
			}
		})
	}
}

func TestProxyHandlerMissingVPNUsesConfiguredFallbackAndKeepsNilStrict(t *testing.T) {
	m := fallbackTestManager(t, &fallbackTestNetwork{}, false)
	fallback := &fallbackTestNetwork{}
	for _, enabled := range []bool{false, true} {
		var handler http.Handler
		if enabled {
			handler = NewProxyHandler(m, fallback)
		} else {
			handler = NewProxyHandler(m, nil)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://metadata.invalid/release.torrent", nil))
		if enabled && (response.Code != http.StatusOK || response.Body.String() != "fallback" || fallback.rounds.Load() != 1) {
			t.Fatal("unconfigured VPN prevented the configured fallback route", response.Code, response.Body.String())
		}
		if !enabled && (response.Code != http.StatusServiceUnavailable || fallback.rounds.Load() != 0) {
			t.Fatal("nil fallback changed the original strict behavior", response.Code)
		}
	}
}

func TestFallbackDoesNotCaptureExistingDirectTransport(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "direct route")
	}))
	defer origin.Close()
	primary, fallback := &fallbackTestNetwork{}, &fallbackTestNetwork{}
	central := httptest.NewServer(NewProxyHandler(fallbackTestManager(t, primary, false), fallback))
	defer central.Close()
	t.Setenv("HTTP_PROXY", central.URL)
	t.Setenv("HTTPS_PROXY", central.URL)
	transport := httpx.DirectTransport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get(origin.URL + "/tracker-or-api")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if calls.Load() != 1 || primary.rounds.Load() != 0 || fallback.rounds.Load() != 0 || fallback.dials.Load() != 0 {
		t.Fatal("adding central fallback captured a direct-only caller")
	}
}
