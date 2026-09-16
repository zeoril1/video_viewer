package streamapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlaybackSessionsAreIndependent(t *testing.T) {
	m := newFakeHlsSession(t, 0, "English")
	first := m.sessions["tt123"]
	delete(m.sessions, "tt123")
	m.sessions[hlsSessionKey("tt123", "first")] = first
	second := *first
	second.dir = t.TempDir()
	second.track = 2
	m.sessions[hlsSessionKey("tt123", "second")] = &second
	got, err := m.ensure(context.Background(), "tt123", first.magnet, first.file, first.track, first.subs, first.start, first.quality, "first")
	if err != nil || got != first {
		t.Fatalf("did not reuse first session: %v", err)
	}
	req := httptest.NewRequest("GET", "/?session=first", nil)
	req.SetPathValue("id", "tt123")
	for _, serve := range []func(){
		func() {
			rec := httptest.NewRecorder()
			m.serveMaster(rec, req, first)
			if !strings.Contains(rec.Body.String(), "&session=first") {
				t.Fatal(rec.Body.String())
			}
		},
		func() {
			rec := httptest.NewRecorder()
			m.serveMediaPlaylistFrom(rec, req, first)
			if !strings.Contains(rec.Body.String(), "&session=first") {
				t.Fatal(rec.Body.String())
			}
		},
	} {
		serve()
	}
	m.stopSessions(hlsSessionKey("tt123", "first"))
	if _, ok := m.findSession(hlsSessionKey("tt123", "second"), second.magnet, second.track, second.file); !ok {
		t.Fatal("stopping first viewer stopped second")
	}
}
