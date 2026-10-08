package streamapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/remoteauth"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

func TestWatchingCountsPlaybackInsteadOfSeekPosition(t *testing.T) {
	s := newViewingStore()
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	v := viewer{owner: "a", Session: "session", Hash: "hash", File: 0, FilmID: "movie", Playing: true}
	if !s.update(v) {
		t.Fatal("first update failed")
	}
	now = now.Add(10 * time.Second)
	v.Position = 3000 // seek forward; only ten seconds actually elapsed
	if !s.update(v) {
		t.Fatal("second update failed")
	}
	if got := s.snapshot()[0].WatchedSeconds; got != 10 {
		t.Fatal(got)
	}
	now = now.Add(4 * time.Second)
	v.Playing = false
	s.update(v)
	now = now.Add(20 * time.Second)
	s.update(v)
	if got := s.snapshot()[0].WatchedSeconds; got != 14 {
		t.Fatal(got)
	}
	v.Playing = true
	s.update(v)
	now = now.Add(25 * time.Second)
	if got := s.snapshot()[0].WatchedSeconds; got != 29 {
		t.Fatal("lost heartbeat added too much time", got)
	}
	s.update(v)
	if got := s.snapshot()[0].WatchedSeconds; got != 29 {
		t.Fatal("time regressed", got)
	}
	v.File = 1
	s.update(v)
	if got := s.snapshot()[0].WatchedSeconds; got != 0 {
		t.Fatal("new file retained elapsed time", got)
	}
	now = now.Add(viewerTTL + time.Second)
	if len(s.snapshot()) != 0 {
		t.Fatal("closed browser retained viewer")
	}
}

func TestViewSessionsHaveOwnerIsolationAndLimit(t *testing.T) {
	s := newViewingStore()
	for i := 0; i < 16; i++ {
		if !s.update(viewer{owner: "a", Session: string(rune('a' + i))}) {
			t.Fatal(i)
		}
	}
	if s.update(viewer{owner: "a", Session: "too-many"}) {
		t.Fatal("per-owner limit not applied")
	}
	if !s.update(viewer{owner: "b", Session: "a"}) {
		t.Fatal("different owner's session blocked")
	}
	if len(s.snapshot()) != 17 {
		t.Fatal("sessions with same name collided")
	}
}

func TestClosedViewerCannotBeResurrectedByPendingHeartbeat(t *testing.T) {
	s := newViewingStore()
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	v := viewer{owner: "a", Session: "old", Hash: "hash", File: 0}
	s.close(v.owner, v.Session) // DELETE can even precede the first POST.
	s.update(v)
	if len(s.snapshot()) != 0 {
		t.Fatal("late heartbeat resurrected a closed viewer")
	}
	v.Session = "new"
	s.update(v)
	if len(s.snapshot()) != 1 {
		t.Fatal("closing an old session blocked new playback")
	}
	s.close("other", "new")
	if len(s.snapshot()) != 1 {
		t.Fatal("another owner removed this viewer")
	}
	now = now.Add(time.Minute + time.Second)
	if len(s.snapshot()) != 0 || len(s.closed) != 0 {
		t.Fatal("expired session state retained")
	}
}

func TestViewerSourceLeasesReleaseOnSwitchCloseAndExpiry(t *testing.T) {
	s := newViewingStore()
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	released := 0
	v := viewer{owner: "owner", Session: "session", Hash: "first", release: func() { released++ }}
	s.update(v)
	v.release = nil
	s.update(v)
	if released != 0 {
		t.Fatal("normal heartbeat released active source")
	}
	v.Hash = "next"
	v.release = func() { released++ }
	s.update(v)
	if released != 1 {
		t.Fatal("source switch retained previous pin")
	}
	s.close(v.owner, v.Session)
	s.close(v.owner, v.Session)
	if released != 2 {
		t.Fatal("close failed to release exactly once", released)
	}
	v.Session = "another"
	s.update(v)
	now = now.Add(viewerTTL + time.Second)
	s.snapshot()
	if released != 3 {
		t.Fatal("abandoned viewer retained its source")
	}
}

func TestStaleAndClosedViewerDoNotRenewHLSOrPinSource(t *testing.T) {
	s := newViewingStore()
	v := viewer{owner: "owner", Session: "session", Hash: "hash"}
	s.close(v.owner, v.Session)
	called := false
	s.update(v, func(*viewer, *viewer) bool { called = true; return true })
	if called || len(s.snapshot()) != 0 {
		t.Fatal("late heartbeat renewed closed HLS")
	}
	v.Session = "new"
	s.update(v, func(*viewer, *viewer) bool { return false })
	if len(s.snapshot()) != 0 {
		t.Fatal("stale stream registered a new viewer")
	}
}

func TestViewingHeartbeatHoldsAutomaticFileWithoutSegmentRequests(t *testing.T) {
	info, err := bencode.Marshal(metainfo.Info{Name: "source.mkv", Length: 16, PieceLength: 16, Pieces: make([]byte, 20)})
	if err != nil {
		t.Fatal(err)
	}
	meta := &metainfo.MetaInfo{InfoBytes: info}
	hash := meta.HashInfoBytes().HexString()
	magnet := "magnet:?xt=urn:btih:" + hash
	mgr, err := torrents.NewManager(torrents.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	tor, releaseReader, err := mgr.Acquire(catalog.Item{Magnet: magnet})
	if err != nil {
		t.Fatal(err)
	}
	if err := tor.SetInfoBytes(info); err != nil {
		t.Fatal(err)
	}
	defer releaseReader()
	hls := newHLSManager("")
	token := strings.Repeat("b", 32)
	session := &hlsSession{id: "film", magnet: magnet, file: -1, track: 1, subs: -1, start: 300, quality: "source", lastUsed: time.Now().Add(-2 * time.Minute)}
	hls.sessions[hlsSessionKey("film", token)] = session
	store := newViewingStore()
	handle := store.handle(mgr, remoteauth.New(""), hls)
	viewToken := strings.Repeat("a", 32)
	state := map[string]any{"session": viewToken, "film_id": "film", "magnet": magnet, "file": -1,
		"position": 345, "duration": 1200, "playing": true, "hls_session": token,
		"stream_start": 300, "track": 1, "subs": -1, "quality": "source"}
	report := func(method string) {
		t.Helper()
		body, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, "/api/stream/viewing", bytes.NewReader(body))
		w := httptest.NewRecorder()
		handle(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatal("heartbeat rejected", w.Code, w.Body.String())
		}
	}
	report(http.MethodPost)
	releaseReader() // FFmpeg's reader ends; the browser still holds buffered media.
	if session.playhead != 45 || time.Since(session.lastUsed) > time.Second || len(store.snapshot()) != 1 || store.snapshot()[0].File != 0 {
		t.Fatal("heartbeat did not retain the exact stream and resolve its automatic file")
	}
	if err := mgr.RemoveCached(hash, nil); !errors.Is(err, torrents.ErrCacheBusy) {
		t.Fatal("buffered playback lost its source after the raw reader closed", err)
	}
	lastUsed := session.lastUsed
	state["stream_start"], state["position"] = 0, 999
	report(http.MethodPost)
	if !session.lastUsed.Equal(lastUsed) || store.snapshot()[0].Position != 345 {
		t.Fatal("queued pre-seek heartbeat renewed or changed the current stream")
	}
	report(http.MethodDelete)
	state["stream_start"] = 300
	report(http.MethodPost)
	if len(store.snapshot()) != 0 || !session.lastUsed.Equal(lastUsed) {
		t.Fatal("late heartbeat recreated a closed viewer")
	}
	if err := mgr.RemoveCached(hash, nil); err != nil {
		t.Fatal("closing the viewer failed to release its cached source", err)
	}
}
