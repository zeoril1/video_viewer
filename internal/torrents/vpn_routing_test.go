package torrents

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// Run the real torrent client's magnet xs/as and tracker requests. The peer
// network is disabled so this regression test never contacts public peers/DHT.
func TestOnlyMetainfoHTTPUsesVPNWhileTrackerRemainsDirect(t *testing.T) {
	for _, source := range []string{"xs", "as"} {
		t.Run(source, func(t *testing.T) {
			var vpnCalls, directCalls atomic.Int32
			trackerHit := make(chan struct{}, 1)
			tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				directCalls.Add(1)
				select {
				case trackerHit <- struct{}{}:
				default:
				}
				io.WriteString(w, "d8:intervali1800e5:peers0:e")
			}))
			defer tracker.Close()
			info := metainfo.Info{Name: "VPN routing test", Length: 1, PieceLength: 16384, Pieces: make([]byte, 20)}
			encoded, err := bencode.Marshal(info)
			if err != nil {
				t.Fatal(err)
			}
			meta := metainfo.MetaInfo{InfoBytes: encoded}
			var metadata bytes.Buffer
			if err := meta.Write(&metadata); err != nil {
				t.Fatal(err)
			}
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				vpnCalls.Add(1)
				if r.URL.Host != "metadata.invalid" || r.URL.Path != "/release.torrent" {
					t.Error("non-metadata request entered VPN", r.URL)
				}
				w.Header().Set("Content-Type", "application/x-bittorrent")
				w.Write(metadata.Bytes())
			}))
			defer proxy.Close()
			cc := torrent.NewDefaultClientConfig()
			cc.DefaultStorage = memoryStorage{}
			if err := configureNetwork(cc, proxy.URL); err != nil {
				t.Fatal(err)
			}
			cc.NoDHT = true
			cc.DisableWebtorrent = true
			cc.DisableIPv6 = true
			cc.DisableUTP = true
			cc.NoDefaultPortForwarding = true
			cc.DialForPeerConns = false
			cc.AcceptPeerConnections = false
			cc.ListenPort = 0
			client, err := torrent.NewClient(cc)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			magnet := meta.Magnet(nil, &info).String() + "&" + source + "=" + url.QueryEscape("http://metadata.invalid/release.torrent") + "&tr=" + url.QueryEscape(tracker.URL+"/announce")
			torrent, err := client.AddMagnet(magnet)
			if err != nil {
				t.Fatal(err)
			}
			defer torrent.Drop()
			select {
			case <-torrent.GotInfo():
			case <-time.After(5 * time.Second):
				t.Fatal(".torrent metadata not received through VPN proxy")
			}
			select {
			case <-trackerHit:
			case <-time.After(5 * time.Second):
				t.Fatal("direct tracker was not contacted")
			}
			if vpnCalls.Load() != 1 || directCalls.Load() < 1 {
				t.Fatal("unexpected traffic routes", vpnCalls.Load(), directCalls.Load())
			}
		})
	}
}
