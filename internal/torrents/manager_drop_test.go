package torrents

import (
	"errors"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/zeoril1/video_viewer/internal/catalog"
)

func idleDropSource(t *testing.T) (*Manager, string, *torrent.Torrent, catalog.Item) {
	t.Helper()
	silenceLogs(t)
	m := newTestManager(t)
	item := catalog.Item{ID: "timer-test", Magnet: testMagnet}
	source, release, err := m.Acquire(item)
	if err != nil {
		t.Fatal(err)
	}
	release()
	hash, err := hashOf(item.Magnet)
	if err != nil {
		t.Fatal(err)
	}
	return m, hash, source, item
}

func scheduledDropNow(m *Manager, hash string, timer *time.Timer, source *torrent.Torrent) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropScheduledLocked(hash, timer, source)
}

func TestRunningDropCallbackCannotRemoveRenewedKeep(t *testing.T) {
	m, hash, source, item := idleDropSource(t)
	started := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan bool, 1)
	var old *time.Timer
	m.mu.Lock()
	m.cancelDropLocked(hash)
	old = time.AfterFunc(0, func() {
		close(started)
		<-resume
		m.mu.Lock()
		removed := m.dropScheduledLocked(hash, old, source)
		m.mu.Unlock()
		done <- removed
	})
	m.dropTimers[hash] = old
	m.mu.Unlock()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(resume)
		t.Fatal("drop callback did not start")
	}
	// The old callback is already executing, so Keep's Timer.Stop cannot
	// cancel it. Resume it only once Keep has installed the new schedule.
	m.Keep(item.Magnet, time.Hour)
	m.mu.Lock()
	renewed := m.dropTimers[hash]
	deadline := m.keepUntil[hash]
	m.mu.Unlock()
	close(resume)
	select {
	case removed := <-done:
		if removed {
			t.Fatal("running old callback removed renewed cache")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old callback did not finish")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if renewed == nil || renewed == old || m.dropTimers[hash] != renewed ||
		m.open[hash] != source || m.keepUntil[hash] != deadline {
		t.Fatal("old callback modified the new timer, cache deadline or source")
	}
}

func TestStaleDropCallbackPreservesSuccessiveKeepSchedules(t *testing.T) {
	m, hash, source, item := idleDropSource(t)
	var stale []*time.Timer
	for i := 1; i <= 3; i++ {
		m.mu.Lock()
		stale = append(stale, m.dropTimers[hash])
		m.mu.Unlock()
		m.Keep(item.Magnet, time.Duration(i)*time.Hour)
	}
	m.mu.Lock()
	current := m.dropTimers[hash]
	m.mu.Unlock()
	for _, timer := range stale {
		if scheduledDropNow(m, hash, timer, source) {
			t.Fatal("an earlier Keep schedule removed the source")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.dropTimers) != 1 || m.dropTimers[hash] != current || m.open[hash] != source {
		t.Fatal("stale callbacks changed the latest Keep schedule")
	}
}

func TestDropCallbackWaitingForMutexCannotRemoveNewSchedule(t *testing.T) {
	m, hash, source, _ := idleDropSource(t)
	started := make(chan struct{})
	done := make(chan bool, 1)
	var old *time.Timer
	m.mu.Lock()
	m.cancelDropLocked(hash)
	old = time.AfterFunc(0, func() {
		close(started)
		m.mu.Lock()
		removed := m.dropScheduledLocked(hash, old, source)
		m.mu.Unlock()
		done <- removed
	})
	m.dropTimers[hash] = old
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		m.mu.Unlock()
		t.Fatal("drop callback did not start")
	}
	// The callback now waits for mu. Perform the same deadline/timer update
	// as Keep, without releasing mu between renewal and invalidation.
	deadline := time.Now().Add(time.Hour)
	m.keepUntil[hash] = deadline
	m.scheduleDropLocked(hash)
	current := m.dropTimers[hash]
	m.mu.Unlock()
	select {
	case removed := <-done:
		if removed {
			t.Fatal("callback waiting for mutex removed the renewed source")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked callback did not finish")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.open[hash] != source || m.keepUntil[hash] != deadline || m.dropTimers[hash] != current {
		t.Fatal("blocked old callback changed renewed cache state")
	}
}

func TestDropCallbackCannotRemoveActiveReaderOrPlaybackPin(t *testing.T) {
	for _, kind := range []string{"reader", "playback pin"} {
		t.Run(kind, func(t *testing.T) {
			m, hash, source, item := idleDropSource(t)
			m.mu.Lock()
			old := m.dropTimers[hash]
			m.mu.Unlock()
			var release func()
			var err error
			if kind == "reader" {
				_, release, err = m.Acquire(item)
			} else {
				release, err = m.PinCached(hash)
			}
			if err != nil {
				t.Fatal(err)
			}
			if scheduledDropNow(m, hash, old, source) {
				t.Fatal("old callback removed active source")
			}
			m.mu.Lock()
			if m.open[hash] != source || m.readers[hash] != 1 || m.dropTimers[hash] != nil {
				m.mu.Unlock()
				t.Fatal("active source lost its lease or kept a timer")
			}
			m.mu.Unlock()
			release()
			m.mu.Lock()
			current := m.dropTimers[hash]
			m.mu.Unlock()
			if current == nil || current == old || scheduledDropNow(m, hash, old, source) {
				t.Fatal("last release did not create a protected new schedule")
			}
		})
	}
}

func TestFilePreparationProtectsCacheAndSchedulesAfterLastRelease(t *testing.T) {
	m, hash, source, item := idleDropSource(t)
	m.mu.Lock()
	old := m.dropTimers[hash]
	m.mu.Unlock()
	first := m.WantFile(item, 0)
	second := m.WantFile(item, 0)
	if scheduledDropNow(m, hash, old, source) {
		t.Fatal("old timer removed an active file preparation")
	}
	m.mu.Lock()
	if m.dropLocked(hash) || m.dropTimers[hash] != nil || m.fileWants[hash][0] != 2 {
		m.mu.Unlock()
		t.Fatal("file preparation did not protect cache")
	}
	m.mu.Unlock()
	first()
	first()
	m.mu.Lock()
	if m.dropTimers[hash] != nil || m.fileWants[hash][0] != 1 {
		m.mu.Unlock()
		t.Fatal("cleanup scheduled before the last preparation release")
	}
	m.mu.Unlock()
	second()
	m.mu.Lock()
	current := m.dropTimers[hash]
	if current == nil || len(m.fileWants[hash]) != 0 || m.readers[hash] != 0 {
		m.mu.Unlock()
		t.Fatal("last preparation release did not schedule cleanup")
	}
	m.mu.Unlock()
	if scheduledDropNow(m, hash, old, source) {
		t.Fatal("old timer removed a source with a new cleanup schedule")
	}
}

func TestCurrentDropCallbackRechecksKeepDeadline(t *testing.T) {
	m, hash, source, _ := idleDropSource(t)
	m.mu.Lock()
	old := m.dropTimers[hash]
	m.keepUntil[hash] = time.Now().Add(time.Hour)
	m.mu.Unlock()
	if scheduledDropNow(m, hash, old, source) {
		t.Fatal("callback ignored the current Keep deadline")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.open[hash] != source || m.dropTimers[hash] == nil || m.dropTimers[hash] == old {
		t.Fatal("future Keep deadline was not rescheduled")
	}
}

func TestCurrentDropCallbackRechecksActiveLeases(t *testing.T) {
	for _, kind := range []string{"reader", "file preparation"} {
		t.Run(kind, func(t *testing.T) {
			m, hash, source, _ := idleDropSource(t)
			m.mu.Lock()
			current := m.dropTimers[hash]
			if kind == "reader" {
				m.readers[hash] = 1
			} else {
				m.fileWants[hash] = map[int]int{0: 1}
			}
			m.mu.Unlock()
			if scheduledDropNow(m, hash, current, source) {
				t.Fatal("current callback ignored an active lease")
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.open[hash] != source || m.dropTimers[hash] != nil {
				t.Fatal("active lease did not cancel the current cleanup timer")
			}
		})
	}
}

func TestExpiredDropScheduleRemovesCacheAndCannotAffectReopenedSource(t *testing.T) {
	m, hash, source, item := idleDropSource(t)
	m.mu.Lock()
	old := m.dropTimers[hash]
	m.keepUntil[hash] = time.Now().Add(-time.Second)
	m.mu.Unlock()
	if !scheduledDropNow(m, hash, old, source) {
		t.Fatal("expired idle cache was not removed")
	}
	m.mu.Lock()
	_, kept := m.keepUntil[hash]
	_, scheduled := m.dropTimers[hash]
	_, open := m.open[hash]
	m.mu.Unlock()
	if kept || scheduled || open {
		t.Fatal("expired source left cache bookkeeping")
	}
	replacement, release, err := m.Acquire(item)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if replacement == source || scheduledDropNow(m, hash, old, source) {
		t.Fatal("old callback affected a reopened source")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.open[hash] != replacement || m.dropTimers[hash] == nil {
		t.Fatal("reopened source lost its cleanup schedule")
	}
}

func TestDropCallbackCannotRemoveReplacementAfterFileEviction(t *testing.T) {
	silenceLogs(t)
	m, hash, _ := loadedSpoolManager(t)
	m.mu.Lock()
	m.scheduleDropLocked(hash)
	old := m.dropTimers[hash]
	source := m.open[hash]
	m.mu.Unlock()
	index := 0
	if err := m.RemoveCached(hash, &index); err != nil {
		t.Fatal(err)
	}
	if scheduledDropNow(m, hash, old, source) {
		t.Fatal("old timer removed a replacement torrent")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.open[hash] == nil || m.open[hash] == source ||
		m.dropTimers[hash] == nil || m.dropTimers[hash] == old {
		t.Fatal("replacement torrent did not get its own cleanup schedule")
	}
}

func TestLateReaderAndPreparationReleaseCannotScheduleAfterClose(t *testing.T) {
	m, hash, source, item := idleDropSource(t)
	m.mu.Lock()
	old := m.dropTimers[hash]
	m.mu.Unlock()
	_, release, err := m.Acquire(item)
	if err != nil {
		t.Fatal(err)
	}
	unwant := m.WantFile(item, 0)
	m.Close()
	release()
	unwant()
	m.Keep(item.Magnet, time.Hour)
	if scheduledDropNow(m, hash, old, source) {
		t.Fatal("old timer acted after shutdown")
	}
	if _, _, err := m.Acquire(item); !errors.Is(err, ErrCacheNotFound) {
		t.Fatal("closed manager opened a new source", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.dropTimers) != 0 || !m.keepUntil[hash].IsZero() {
		t.Fatal("late release or Keep recreated cleanup state after shutdown")
	}
}
