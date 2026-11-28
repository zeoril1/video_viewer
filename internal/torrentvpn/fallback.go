package torrentvpn

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// FallbackNetwork carries protected downloads through checked proxies only.
// Implementations must never fall back to an ordinary origin connection.
type FallbackNetwork interface {
	RoundTrip(*http.Request) (*http.Response, error)
	DialContext(context.Context, string, string) (net.Conn, error)
}

type fallbackRouter struct {
	primary        *Manager
	fallback       FallbackNetwork
	primaryTimeout time.Duration
	retryDelay     time.Duration
	mu             sync.Mutex
	configuration  string
	retryAt        time.Time
}

// NewFallbackRouter is shared by the forward proxy and its admin status API.
func NewFallbackRouter(primary *Manager, fallback FallbackNetwork) *fallbackRouter {
	return newFallbackRouter(primary, fallback)
}

func newFallbackRouter(primary *Manager, fallback FallbackNetwork) *fallbackRouter {
	return &fallbackRouter{primary: primary, fallback: fallback,
		primaryTimeout: 5 * time.Second, retryDelay: 20 * time.Second}
}

func configurationKey(status Status) string {
	return status.Name + "\x00" + status.UpdatedAt + "\x00" + status.State
}

// VPNStatus describes the last observed route, not a claim that a loaded
// WireGuard configuration has completed an end-to-end health check.
func (p *fallbackRouter) VPNStatus() (configured bool, retryAt time.Time) {
	status := p.primary.Status()
	p.mu.Lock()
	defer p.mu.Unlock()
	if key := configurationKey(status); p.configuration != key {
		p.configuration, p.retryAt = key, time.Time{}
	}
	return status.Configured, p.retryAt
}

func (p *fallbackRouter) tryPrimary() (bool, string) {
	configured, retryAt := p.VPNStatus()
	return configured && !time.Now().Before(retryAt), configurationKey(p.primary.Status())
}

func (p *fallbackRouter) failed(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.configuration == key {
		p.retryAt = time.Now().Add(p.retryDelay)
	}
}

func (p *fallbackRouter) succeeded(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.configuration == key {
		p.retryAt = time.Time{}
	}
}

func (p *fallbackRouter) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.fallback == nil {
		return p.primary.DialContext(ctx, network, address)
	}
	if attempt, key := p.tryPrimary(); attempt {
		first, cancel := context.WithTimeout(ctx, p.primaryTimeout)
		conn, err := p.primary.DialContext(first, network, address)
		cancel()
		if err == nil {
			p.succeeded(key)
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		p.failed(key)
	}
	return p.fallback.DialContext(ctx, network, address)
}

func (p *fallbackRouter) RoundTrip(r *http.Request) (response *http.Response, resultErr error) {
	defer func() {
		if resultErr != nil && r.Body != nil {
			_ = r.Body.Close()
		}
	}()
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	if p.fallback == nil {
		return p.primary.RoundTrip(r)
	}
	if attempt, key := p.tryPrimary(); attempt {
		resp, err := p.primaryRoundTrip(r)
		if err == nil {
			p.succeeded(key)
			return resp, nil
		}
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		p.failed(key)
		// A failed POST may already have changed upstream state. Only replay
		// reads; a non-rewindable body cannot be sent for a second attempt.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return nil, err
		}
		if r.Body != nil && r.Body != http.NoBody {
			if r.GetBody == nil {
				return nil, err
			}
			body, bodyErr := r.GetBody()
			if bodyErr != nil {
				return nil, bodyErr
			}
			r = r.Clone(r.Context())
			r.Body = body
		}
	}
	return p.fallback.RoundTrip(r)
}

func (p *fallbackRouter) primaryRoundTrip(r *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(r.Context())
	expired := make(chan struct{})
	timer := time.AfterFunc(p.primaryTimeout, func() { cancel(); close(expired) })
	resp, err := p.primary.RoundTrip(r.Clone(ctx))
	if !timer.Stop() {
		<-expired
	}
	if err != nil || ctx.Err() != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			err = ctx.Err()
		}
		cancel()
		return nil, err
	}
	if resp == nil {
		cancel()
		return nil, errors.New("VPN returned no response")
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	// The short timeout covers connection/headers only. Keep the context alive
	// until the response body is closed, so successful downloads can finish.
	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	if err != nil {
		b.cancel()
	}
	return n, err
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
