package torrentvpn

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrUnconfigured = errors.New("VPN не настроен: загрузите WireGuard-конфиг")

type Status struct {
	Configured bool   `json:"configured"`
	Name       string `json:"name"`
	UpdatedAt  string `json:"updated_at"`
	State      string `json:"status"`
	Message    string `json:"message,omitempty"`
}

type Manager struct {
	path   string
	update sync.Mutex
	mu     sync.RWMutex
	tunnel networkTunnel
	status Status
	closed bool
	open   func(tunnelConfig) (networkTunnel, error)
}

func NewManager(path string) *Manager { return newManager(path, openTunnel) }

func newManager(path string, opener func(tunnelConfig) (networkTunnel, error)) *Manager {
	m := &Manager{path: path, open: opener, status: Status{State: "unconfigured"}}
	data, err := readStoredConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return m
	}
	if err != nil {
		m.status = Status{State: "error", Message: "Не удалось прочитать сохранённый VPN-конфиг"}
		return m
	}
	c, err := parseConfig(withoutMetadata(data))
	if err != nil {
		m.status = Status{State: "error", Message: "Сохранённый VPN-конфиг некорректен"}
		return m
	}
	t, err := opener(c)
	if err != nil {
		m.status = Status{State: "error", Message: "Не удалось применить сохранённый VPN-конфиг"}
		return m
	}
	m.tunnel = t
	m.status = Status{Configured: true, Name: filepath.Base(path), State: "ready"}
	if info, err := os.Stat(path); err == nil {
		m.status.UpdatedAt = info.ModTime().UTC().Format(time.RFC3339Nano)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "# video_viewer_name: "); ok {
			if b, err := base64.RawStdEncoding.DecodeString(value); err == nil {
				if name, err := configName(string(b)); err == nil {
					m.status.Name = name
				}
			}
		}
		if value, ok := strings.CutPrefix(line, "# video_viewer_updated_at: "); ok {
			if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
				m.status.UpdatedAt = value
			}
		}
	}
	return m
}

func readStoredConfig(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const maxStoredBytes = MaxConfigBytes + 1024
	data, err := io.ReadAll(io.LimitReader(f, maxStoredBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxStoredBytes {
		return nil, errors.New("saved VPN configuration is too large")
	}
	return data, nil
}

func configName(name string) (string, error) {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if len(name) == 0 || len(name) > 180 || strings.ToLower(filepath.Ext(name)) != ".conf" || strings.ContainsAny(name, "\r\n\x00") {
		return "", errors.New("Выберите файл WireGuard с расширением .conf")
	}
	return name, nil
}

func (m *Manager) Replace(name string, data []byte) (Status, error) {
	name, err := configName(name)
	if err != nil {
		return m.Status(), err
	}
	c, err := parseConfig(data)
	if err != nil {
		return m.Status(), err
	}
	m.update.Lock()
	defer m.update.Unlock()
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()
	if closed {
		return m.Status(), net.ErrClosed
	}
	t, err := m.open(c)
	if err != nil {
		return m.Status(), err
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	// Name and timestamp are comments in the same atomic file as the keys. A
	// crash cannot leave metadata describing a different active configuration.
	stored := []byte("# video_viewer_name: " + base64.RawStdEncoding.EncodeToString([]byte(name)) + "\n# video_viewer_updated_at: " + stamp + "\n")
	stored = append(stored, withoutMetadata(data)...)
	if err := saveAtomic(m.path, stored); err != nil {
		t.Close()
		return m.Status(), errors.New("Не удалось сохранить VPN-конфиг; прежняя конфигурация сохранена")
	}
	m.mu.Lock()
	old := m.tunnel
	m.tunnel = t
	m.status = Status{Configured: true, Name: name, UpdatedAt: stamp, State: "ready"}
	status := m.status
	m.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return status, nil
}

func withoutMetadata(data []byte) []byte {
	var out strings.Builder
	for _, line := range strings.SplitAfter(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "# video_viewer_") {
			out.WriteString(line)
		}
	}
	return []byte(out.String())
}

func saveAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".wireguard-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (m *Manager) Status() Status { m.mu.RLock(); defer m.mu.RUnlock(); return m.status }
func (m *Manager) active() (networkTunnel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, net.ErrClosed
	}
	if m.tunnel == nil {
		return nil, ErrUnconfigured
	}
	return m.tunnel, nil
}
func (m *Manager) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	t, err := m.active()
	if err != nil {
		return nil, err
	}
	return t.DialContext(ctx, network, address)
}
func (m *Manager) RoundTrip(r *http.Request) (*http.Response, error) {
	t, err := m.active()
	if err != nil {
		return nil, err
	}
	return t.RoundTrip(r)
}
func (m *Manager) Close() {
	m.update.Lock()
	defer m.update.Unlock()
	m.mu.Lock()
	m.closed = true
	t := m.tunnel
	m.tunnel = nil
	m.mu.Unlock()
	if t != nil {
		t.Close()
	}
}
