package streamapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestHLSAdmissionAndReuse(t *testing.T) {
	m := newHLSManager("http://localhost")
	m.slots = make(chan struct{}, 1)
	m.slots <- struct{}{}
	key := hlsSessionKey("tt1", "owner")
	existing := &hlsSession{magnet: "m", file: 0, track: 0, subs: -1, start: 10, quality: "source", dir: t.TempDir()}
	m.sessions[key] = existing
	got, err := m.ensure(context.Background(), "tt1", "m", 0, 0, -1, 10, "source", "owner")
	if err != nil || got != existing {
		t.Fatal("existing stream rejected at capacity")
	}
	if _, err = m.ensure(context.Background(), "tt2", "m", 0, 0, -1, 10, "source", "other"); !errors.Is(err, errResources) {
		t.Fatal("capacity not enforced", err)
	}
	if len(m.launches) != 0 {
		t.Fatal("reservation leaked")
	}
	w := httptest.NewRecorder()
	resourceError(w, errResources)
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatal("missing retry response")
	}
}
func TestPrepareUnavailable(t *testing.T) {
	w := httptest.NewRecorder()
	prepareHandler(nil)(w, httptest.NewRequest("POST", "/api/stream/prepare?file=0", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestFailedLaunchReturnsSlot(t *testing.T) {
	m := newHLSManager("http://localhost")
	m.dataDir = t.TempDir() + "/missing"
	m.slots = make(chan struct{}, 1)
	_, err := m.ensure(context.Background(), "tt1", "m", 0, 0, -1, 10, "source", "viewer")
	if err == nil || len(m.slots) != 0 || len(m.launches) != 0 {
		t.Fatal("failed launch leaked its reservation")
	}
}
