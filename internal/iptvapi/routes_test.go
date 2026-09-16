package iptvapi

import (
	"net/http/httptest"
	"testing"
)

// TestNewServerBuildsRoutes — регрессия: мультиплексор Go 1.22 паникует при
// регистрации шаблона вида «{id}.m3u8» (wildcard должен занимать сегмент
// целиком). Раньше сервис падал на старте именно из-за этого.
func TestNewServerBuildsRoutes(t *testing.T) {
	handler, stop := NewServer(Config{DataDir: t.TempDir()})
	defer stop()
	if handler == nil {
		t.Fatal("NewServer вернул nil-обработчик")
	}
}

// TestPlayID — разбор id канала из пути /api/iptv/play/{id}[.m3u8|.m3u].
func TestPlayID(t *testing.T) {
	cases := map[string]int64{
		"/api/iptv/play/12.m3u8": 12,
		"/api/iptv/play/12":      12,
		"/api/iptv/play/12.m3u":  12,
		"/api/iptv/play/abc":     0,
		"/api/iptv/play/":        0,
	}
	for p, want := range cases {
		r := httptest.NewRequest("GET", p, nil)
		if got := playID(r); got != want {
			t.Errorf("playID(%q) = %d, want %d", p, got, want)
		}
	}
}
