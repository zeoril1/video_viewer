package torrentvpn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeTunnel struct {
	closed atomic.Bool
	rounds atomic.Int32
	dials  atomic.Int32
	fail   bool
}

func (f *fakeTunnel) DialContext(context.Context, string, string) (net.Conn, error) {
	f.dials.Add(1)
	return nil, errors.New("network unavailable")
}
func (f *fakeTunnel) RoundTrip(r *http.Request) (*http.Response, error) {
	f.rounds.Add(1)
	if f.fail {
		return nil, errors.New("VPN down")
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("torrent"))}, nil
}
func (f *fakeTunnel) Close() { f.closed.Store(true) }

func TestManagerReplacesAtomicallyAndPreservesConfigOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn", "wg0.conf")
	var opened []*fakeTunnel
	opener := func(tunnelConfig) (networkTunnel, error) {
		f := &fakeTunnel{}
		opened = append(opened, f)
		return f, nil
	}
	m := newManager(path, opener)
	defer m.Close()
	if m.Status().State != "unconfigured" {
		t.Fatal("new manager should allow an empty first boot")
	}
	first, err := m.Replace("one.conf", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatal("private config is readable by other users")
	}
	if _, err := m.Replace("bad.conf", []byte("[Interface]\nPrivateKey=secret")); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || m.Status() != first || opened[0].closed.Load() {
		t.Fatal("invalid replacement changed current config")
	}
	second, err := m.Replace("two.conf", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !opened[0].closed.Load() || opened[1].closed.Load() || second.Name != "two.conf" {
		t.Fatal("old tunnel was not closed during replacement")
	}
	reloaded := newManager(path, opener)
	defer reloaded.Close()
	if reloaded.Status() != second {
		t.Fatalf("persisted status differs: %#v vs %#v", reloaded.Status(), second)
	}
	b, _ := json.Marshal(reloaded.Status())
	if strings.Contains(string(b), "PrivateKey") || strings.Contains(string(b), "YWFh") {
		t.Fatal("status leaked configuration")
	}
}

func TestManagerCannotApplyUnpersistedCandidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wg0.conf")
	var opened []*fakeTunnel
	m := newManager(path, func(tunnelConfig) (networkTunnel, error) {
		f := &fakeTunnel{}
		opened = append(opened, f)
		return f, nil
	})
	defer m.Close()
	status, err := m.Replace("first.conf", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Replace("second.conf", testConfig()); err == nil {
		t.Fatal("directory replaced by config unexpectedly")
	}
	if m.Status() != status || opened[0].closed.Load() || !opened[1].closed.Load() {
		t.Fatal("save error changed the active tunnel or leaked candidate")
	}
}

func TestManagerReloadAcceptsFullSizeConfigWithMetadata(t *testing.T) {
	data := append(testConfig(), []byte("#"+strings.Repeat("x", MaxConfigBytes-len(testConfig())-1))...)
	path := filepath.Join(t.TempDir(), "wg0.conf")
	opener := func(tunnelConfig) (networkTunnel, error) { return &fakeTunnel{}, nil }
	m := newManager(path, opener)
	defer m.Close()
	if _, err := m.Replace("max.conf", data); err != nil {
		t.Fatal(err)
	}
	reloaded := newManager(path, opener)
	defer reloaded.Close()
	if !reloaded.Status().Configured {
		t.Fatalf("maximum size config failed to reload: %#v", reloaded.Status())
	}
}

func TestManagerRejectsOversizedSavedFileBeforeOpeningTunnel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wg0.conf")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxConfigBytes+1025)), 0600); err != nil {
		t.Fatal(err)
	}
	opened := false
	m := newManager(path, func(tunnelConfig) (networkTunnel, error) { opened = true; return &fakeTunnel{}, nil })
	defer m.Close()
	if opened || m.Status().Configured || m.Status().State != "error" {
		t.Fatal("oversized stored configuration was loaded")
	}
}
