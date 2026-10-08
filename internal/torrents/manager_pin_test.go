package torrents

import (
	"errors"
	"testing"
)

func TestBufferedPlaybackPinProtectsCachedFileUntilReleased(t *testing.T) {
	silenceLogs(t)
	m, hash, _ := loadedSpoolManager(t)
	first, err := m.PinCached(hash)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.PinCached(hash)
	if err != nil {
		t.Fatal(err)
	}
	index := 0
	if err := m.RemoveCached(hash, &index); !errors.Is(err, ErrCacheBusy) {
		t.Fatal("buffered source could be removed", err)
	}
	m.mu.Lock()
	dropped := m.dropLocked(hash)
	m.mu.Unlock()
	if dropped {
		t.Fatal("idle timer dropped buffered playback")
	}
	first()
	first()
	if m.readers[hash] != 1 {
		t.Fatal("repeated release removed another viewer's lease")
	}
	second()
	if m.readers[hash] != 0 || m.dropTimers[hash] == nil {
		t.Fatal("last viewer failed to schedule cleanup")
	}
	if err := m.RemoveCached(hash, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PinCached(hash); !errors.Is(err, ErrCacheNotFound) {
		t.Fatal("pin reopened missing source", err)
	}
	if _, err := m.PinCached("../file"); !errors.Is(err, ErrInvalidCacheKey) {
		t.Fatal(err)
	}
}

func TestSourcePinReleasedAfterShutdownDoesNotScheduleAnotherDrop(t *testing.T) {
	silenceLogs(t)
	m, hash, _ := loadedSpoolManager(t)
	release, err := m.PinCached(hash)
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	release()
	if len(m.dropTimers) != 0 {
		t.Fatal("shutdown source got new cleanup timer")
	}
	if _, err := m.PinCached(hash); !errors.Is(err, ErrCacheNotFound) {
		t.Fatal("closed manager accepted pin", err)
	}
}
