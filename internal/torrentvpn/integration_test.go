package torrentvpn

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// This test creates two real encrypted WireGuard peers over loopback UDP. The
// HTTP/DNS servers exist only inside the other peer's userspace network: ordinary
// host networking cannot reach them. It covers both HTTP proxy and HTTPS CONNECT.
func TestWireGuardProxyCarriesHTTPHTTPSAndDNSInsideTunnel(t *testing.T) {
	clientKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverIP := netip.MustParseAddr("10.44.0.2")
	tun, serverNet, err := netstack.CreateNetTUN([]netip.Addr{serverIP}, nil, 1420)
	if err != nil {
		t.Fatal(err)
	}
	serverDev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	defer serverDev.Close()
	ipc := fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=10.44.0.1/32\n", hex.EncodeToString(serverKey.Bytes()), hex.EncodeToString(clientKey.PublicKey().Bytes()))
	if err := serverDev.IpcSet(ipc); err != nil {
		t.Fatal("could not configure test WireGuard peer")
	}
	if err := serverDev.Up(); err != nil {
		t.Fatal("could not start test WireGuard peer")
	}
	state, err := serverDev.IpcGet()
	if err != nil {
		t.Fatal("could not inspect test WireGuard listener")
	}
	port := 0
	for _, line := range strings.Split(state, "\n") {
		if value, ok := strings.CutPrefix(line, "listen_port="); ok {
			port, _ = strconv.Atoi(value)
		}
	}
	if port == 0 {
		t.Fatal("test WireGuard peer did not bind a UDP port")
	}

	var dnsQueries atomic.Int32
	dnsConn, err := serverNet.ListenUDPAddrPort(netip.AddrPortFrom(serverIP, 53))
	if err != nil {
		t.Fatal(err)
	}
	defer dnsConn.Close()
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, address, err := dnsConn.ReadFrom(buffer)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(buffer[:n]); err != nil {
				continue
			}
			dnsQueries.Add(1)
			response := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true, RecursionAvailable: true}, Questions: query.Questions}
			for _, q := range query.Questions {
				if q.Type == dnsmessage.TypeA {
					response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: serverIP.As4()}})
				}
			}
			packet, err := response.Pack()
			if err == nil {
				_, _ = dnsConn.WriteTo(packet, address)
			}
		}
	}()
	var received atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = io.WriteString(w, "encrypted-torrent-metadata")
	})
	listener, err := serverNet.ListenTCPAddrPort(netip.AddrPortFrom(serverIP, 80))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go httpServer.Serve(listener)
	defer httpServer.Close()
	tlsListener, err := serverNet.ListenTCPAddrPort(netip.AddrPortFrom(serverIP, 443))
	if err != nil {
		t.Fatal(err)
	}
	tlsServer := httptest.NewUnstartedServer(handler)
	_ = tlsServer.Listener.Close()
	tlsServer.Listener = tlsListener
	tlsServer.StartTLS()
	defer tlsServer.Close()

	config := fmt.Sprintf("[Interface]\nPrivateKey=%s\nAddress=10.44.0.1/32\nDNS=10.44.0.2\n[Peer]\nPublicKey=%s\nEndpoint=127.0.0.1:%d\nAllowedIPs=0.0.0.0/0\n", base64.StdEncoding.EncodeToString(clientKey.Bytes()), base64.StdEncoding.EncodeToString(serverKey.PublicKey().Bytes()), port)
	m := NewManager(filepath.Join(t.TempDir(), "wg0.conf"))
	defer m.Close()
	if _, err := m.Replace("loopback.conf", []byte(config)); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(NewProxyHandler(m))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	for _, target := range []string{"http://torrent.test/file.torrent", "https://torrent.test/file.torrent"} {
		response, err := client.Get(target)
		if err != nil {
			t.Fatalf("encrypted proxy request failed: %v", err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(body) != "encrypted-torrent-metadata" {
			t.Fatalf("encrypted request returned status %d / err %v", response.StatusCode, err)
		}
	}
	if received.Load() != 2 || dnsQueries.Load() == 0 {
		t.Fatal("HTTP or destination DNS did not cross the encrypted tunnel")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	oldConnection, err := m.DialContext(ctx, "tcp", "torrent.test:80")
	cancel()
	if err != nil {
		t.Fatal("could not create connection before replacement")
	}
	defer oldConnection.Close()
	if _, err := m.Replace("replacement.conf", []byte(config)); err != nil {
		t.Fatal(err)
	}
	if _, err := oldConnection.Write([]byte("GET / HTTP/1.0\r\n\r\n")); err == nil {
		t.Fatal("replacement left a connection on the old VPN alive")
	}
	// Close invalidates existing TCP/CONNECT sessions, and a dead tunnel must not
	// accidentally use the ordinary host network for a still-reachable target.
	m.Close()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.DialContext(ctx, "tcp", net.JoinHostPort(serverIP.String(), "80")); err == nil {
		t.Fatal("closed manager continued to open VPN connections")
	}
}
