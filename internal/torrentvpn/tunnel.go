package torrentvpn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

type networkTunnel interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	RoundTrip(*http.Request) (*http.Response, error)
	Close()
}

type wireTunnel struct {
	dev         *device.Device
	net         *netstack.Net
	transport   *http.Transport
	mu          sync.Mutex
	closed      bool
	connections map[*trackedConn]struct{}
}

func openTunnel(c tunnelConfig) (networkTunnel, error) {
	host, port, _ := net.SplitHostPort(c.endpoint)
	if _, err := netip.ParseAddr(host); err != nil {
		// Only the outer WireGuard endpoint bootstrap uses the host resolver.
		// DNS for all HTTP destinations is performed by netstack through the VPN.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		cancel()
		if err != nil || len(ips) == 0 {
			return nil, errors.New("Не удалось определить адрес VPN-сервера")
		}
		c.endpoint = net.JoinHostPort(ips[0].String(), port)
	}
	tun, network, err := netstack.CreateNetTUN(c.addresses, c.dns, c.mtu)
	if err != nil {
		return nil, errors.New("Не удалось создать VPN-туннель")
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	var ipc strings.Builder
	fmt.Fprintf(&ipc, "private_key=%s\nreplace_peers=true\npublic_key=%s\nendpoint=%s\npersistent_keepalive_interval=%d\n", c.privateKey, c.publicKey, c.endpoint, c.keepalive)
	if c.presharedKey != "" {
		fmt.Fprintf(&ipc, "preshared_key=%s\n", c.presharedKey)
	}
	for _, p := range c.allowed {
		fmt.Fprintf(&ipc, "allowed_ip=%s\n", p)
	}
	if err := dev.IpcSet(ipc.String()); err != nil {
		dev.Close()
		return nil, errors.New("Не удалось применить параметры WireGuard")
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, errors.New("Не удалось запустить WireGuard")
	}
	w := &wireTunnel{dev: dev, net: network, connections: make(map[*trackedConn]struct{})}
	w.transport = &http.Transport{Proxy: nil, DialContext: w.DialContext,
		TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout: 30 * time.Second, MaxIdleConns: 16, MaxConnsPerHost: 8, ForceAttemptHTTP2: true}
	return w, nil
}

func (w *wireTunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, errors.New("VPN proxy supports TCP only")
	}
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := w.net.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		_ = c.Close()
		return nil, net.ErrClosed
	}
	tc := &trackedConn{Conn: c, owner: w}
	w.connections[tc] = struct{}{}
	return tc, nil
}

func (w *wireTunnel) RoundTrip(r *http.Request) (*http.Response, error) {
	return w.transport.RoundTrip(r)
}

func (w *wireTunnel) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	connections := make([]*trackedConn, 0, len(w.connections))
	for c := range w.connections {
		connections = append(connections, c)
	}
	w.mu.Unlock()
	w.transport.CloseIdleConnections()
	for _, c := range connections {
		_ = c.Close()
	}
	w.dev.Close()
}

type trackedConn struct {
	net.Conn
	owner *wireTunnel
	once  sync.Once
}

func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() { c.owner.mu.Lock(); delete(c.owner.connections, c); c.owner.mu.Unlock(); err = c.Conn.Close() })
	return err
}
