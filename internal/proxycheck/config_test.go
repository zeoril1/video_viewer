package proxycheck

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestParsePublicListsAndRejectCredentialsPrivateAndOtherProtocols(t *testing.T) {
	for _, body := range []string{
		"8.8.8.8:8080\nhttp://8.8.8.8:8080\nhttps://1.1.1.1:8443\n127.0.0.1:80\n10.0.0.1:80\n198.51.100.4:80\nuser:pass@9.9.9.9:80\nsocks5://9.9.9.9:1080\n",
		`{"data":[{"ip":"8.8.8.8","port":8080},{"ip":"1.1.1.1","port":"8443","protocol":"https"},{"ip":"127.0.0.1","port":80}]}`,
		`["8.8.8.8:8080","https://1.1.1.1:8443","http://user:pass@9.9.9.9:80"]`,
	} {
		want := []string{"http://8.8.8.8:8080", "https://1.1.1.1:8443"}
		if got := parseList([]byte(body)); !reflect.DeepEqual(got, want) {
			t.Fatalf("parsed %v want %v", got, want)
		}
	}
	for _, raw := range []string{"http://localhost/list", "https://localhost/list", "https://127.0.0.1/list", "https://user:secret@example.com/list", "https://example.com:8443/list", "https://router.local/list", "https://metadata.google.internal/list"} {
		if validSourceURL(raw) {
			t.Errorf("unsafe source accepted: %s", raw)
		}
	}
	for _, host := range []string{"::1", "::ffff:127.0.0.1", "192.168.1.1", "172.18.0.3", "169.254.169.254", "100.64.1.2", "fc00::1", "fec0::1", "192.0.2.2", "203.0.113.1"} {
		if publicIP(netip.MustParseAddr(host)) {
			t.Errorf("nonpublic IP accepted %s", host)
		}
	}
}

func TestDNSPinningRejectsMixedAnswersBeforeDial(t *testing.T) {
	dials := 0
	dial := func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, errors.New("injected dial")
	}
	_, err := dialResolvedPublic(context.Background(), "tcp", "443", []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, dial)
	if err == nil || dials != 0 {
		t.Fatalf("mixed DNS answers reached dial: calls=%d err=%v", dials, err)
	}
	_, _ = dialResolvedPublic(context.Background(), "tcp", "443", []netip.Addr{netip.MustParseAddr("8.8.8.8")}, func(_ context.Context, _, address string) (net.Conn, error) {
		if address != "8.8.8.8:443" {
			t.Errorf("DNS was resolved a second time: %s", address)
		}
		dials++
		return nil, errors.New("injected dial")
	})
	if dials != 1 {
		t.Fatal("public checked IP was not dialed")
	}
}

func TestPersistentConfigAtomicReloadAndBadStoredRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxies.json")
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Status().Config.Enabled || len(m.Status().Config.Sources) != 3 {
		t.Fatal("missing default sources")
	}
	config := Config{Enabled: false, Sources: []string{"https://example.com/proxies.txt"}, Static: []string{"8.8.8.8:8080"}}
	if _, err = m.Replace(config); err != nil {
		t.Fatal(err)
	}
	m.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("configuration mode %o", info.Mode().Perm())
	}
	m, err = NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if got := m.Status().Config.Static; !reflect.DeepEqual(got, []string{"http://8.8.8.8:8080"}) {
		t.Fatalf("reloaded static %v", got)
	}
	before, _ := os.ReadFile(path)
	if _, err = m.Replace(Config{Static: []string{"http://user:password@8.8.8.8:80"}}); err == nil {
		t.Fatal("credentials accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid replacement changed persistent config")
	}
	m.Close()
	broken := []byte(`{"enabled":true,"sources": [this is broken`)
	if err := os.WriteFile(path, broken, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewManager(path)
	if err == nil || recovered == nil {
		t.Fatal("bad config did not return repairable manager and error")
	}
	defer recovered.Close()
	if s := recovered.Status(); s.Config.Enabled || s.LastError == "" || s.Available != 0 {
		t.Fatalf("unsafe bad-config state %#v", s)
	}
	if data, _ := os.ReadFile(path); string(data) != string(broken) {
		t.Fatal("corrupt original was overwritten")
	}
	if _, err := recovered.Replace(Config{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".proxy-config-*")); len(files) != 0 {
		t.Fatalf("temporary config leaked %v", files)
	}
}

func TestCandidateParserBound(t *testing.T) {
	lines := strings.Builder{}
	for i := 1; i <= 5000; i++ {
		lines.WriteString("8.8.8.8:" + strconv.Itoa(1000+i) + "\n")
	}
	if got := len(parseList([]byte(lines.String()))); got != maxParsedCandidates {
		t.Fatalf("parsed candidate count %d", got)
	}
}
