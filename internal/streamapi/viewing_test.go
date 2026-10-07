package streamapi

import (
	"testing"
	"time"
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
