package magnet

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zeoril1/video_viewer/internal/httpx"
)

func TestTorrentRoutingKeepsSearchLocalAndProtectsExternalRedirect(t *testing.T) {
	var proxyCalls, searchCalls, localDownloads atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		if r.Header.Get("Referer") != "" {
			t.Error("internal Jackett download URL leaked through Referer")
		}
		if r.URL.Host != "tracker.invalid" || r.URL.Path != "/release.torrent" {
			t.Error(r.URL)
		}
		// Deliberately invalid bencode: route selection is observable before parsing.
		io.WriteString(w, "metadata was fetched via VPN")
	}))
	defer proxy.Close()
	jackett := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/dl/") {
			localDownloads.Add(1)
			http.Redirect(w, r, "http://tracker.invalid/release.torrent", http.StatusFound)
			return
		}
		searchCalls.Add(1)
		io.WriteString(w, "<rss><channel></channel></rss>")
	}))
	defer jackett.Close()
	j := NewJackett(jackett.URL, "key", "test", nil)
	client, err := httpx.VPNHTTPClient(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	j.SetTorrentHTTPClient(client)
	if _, err := j.Search(context.Background(), "test", 5); err != nil {
		t.Fatal(err)
	}
	if proxyCalls.Load() != 0 || searchCalls.Load() != 1 {
		t.Fatal("Torznab search used the VPN")
	}
	if _, err := j.downloadMagnet(context.Background(), jackett.URL+"/dl/test?apikey=private"); err == nil {
		t.Fatal("invalid bencode unexpectedly parsed")
	}
	if proxyCalls.Load() != 1 || localDownloads.Load() != 1 {
		t.Fatal("external redirect did not use VPN", proxyCalls.Load(), localDownloads.Load())
	}
	_, _ = j.downloadMagnet(context.Background(), "http://tracker.invalid/release.torrent")
	if proxyCalls.Load() != 2 {
		t.Fatal("external torrent URL did not use VPN")
	}
}

func TestTorrentDownloadWithMissingVPNNeverDialsExternalOrigin(t *testing.T) {
	var direct atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1) }))
	defer origin.Close()
	j := NewJackett("http://jackett.invalid:9117", "key", "test", nil)
	if _, err := j.downloadMagnet(context.Background(), origin.URL+"/file.torrent"); err == nil {
		t.Fatal("missing VPN must fail closed")
	}
	if direct.Load() != 0 {
		t.Fatal("external torrent escaped to direct transport")
	}
}
