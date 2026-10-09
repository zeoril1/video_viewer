package torrents

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zeoril1/video_viewer/internal/disklimit"
	"golang.org/x/sys/windows"
)

// A normal external reader on Windows may share reads and writes but deny
// deletion. The failed admin operation must retain data and remain retryable.
func TestRemoveCachedFileSharingViolationPreservesAndCanRetry(t *testing.T) {
	silenceLogs(t)
	m, hash, _ := loadedSpoolManager(t)
	st := m.spool.torrent(hash)
	budget := disklimit.New(m.spool.dir, 3000, 0)
	m.spool.budget = budget
	for _, file := range st.files {
		file.budget = budget
		// Directory enumeration on Windows can cache an open file's old EOF;
		// reserve its current logical length just as normal writes do.
		if err := budget.Resize(file.path, file.high); err != nil {
			t.Fatal(err)
		}
	}
	path := st.files[0].path
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	neighbor, err := os.ReadFile(st.files[1].path)
	if err != nil {
		t.Fatal(err)
	}
	before := m.StorageStatus()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	held := true
	t.Cleanup(func() {
		if held {
			_ = windows.CloseHandle(handle)
		}
	})
	index := 0
	if err := m.RemoveCached(hash, &index); err == nil {
		t.Fatal("removal succeeded despite external delete sharing restriction")
	}
	afterFailure := m.StorageStatus()
	if afterFailure.WrittenBytes != before.WrittenBytes || afterFailure.LogicalBytes != before.LogicalBytes || afterFailure.UsedBytes != before.UsedBytes {
		t.Fatalf("failed removal changed cache accounting: before=%+v after=%+v", before, afterFailure)
	}
	probe := filepath.Join(m.spool.dir, "quota-probe")
	if err := budget.Resize(probe, 1); !errors.Is(err, disklimit.ErrFull) {
		t.Fatal("failed removal prematurely released its quota reservation", err)
	}
	file := afterFailure.Torrents[0].Files[0]
	if !file.Available || file.Downloaded != int64(len(original)) || !cacheFileExists(st, 0) {
		t.Fatalf("failed file became invisible or incomplete: %+v", file)
	}
	if st.files[0].reader() == nil || st.files[0].high != int64(len(original)) {
		t.Fatal("original handle/accounting was not restored")
	}
	for i, p := range st.pieces {
		if !p.Completion().Complete {
			t.Fatalf("failed removal invalidated retained piece %d", i)
		}
	}
	// Retained data must reopen without O_TRUNC on the next write, including
	// bytes outside the write itself, and duplicate bytes must not grow usage.
	if _, err := st.files[0].writeAt(0, original[:1]); err != nil {
		t.Fatal(err)
	}
	retained, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(retained, original) {
		t.Fatal("retained data was truncated/changed by subsequent write", err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	held = false
	if err := m.RemoveCached(hash, &index); err != nil {
		t.Fatal("retry after external handle closed failed", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("successful retry left deleted file", err)
	}
	afterRetry := m.StorageStatus()
	if afterRetry.WrittenBytes != int64(len(neighbor)) || afterRetry.Torrents[0].Files[0].Available {
		t.Fatalf("successful retry did not reclaim only selected data: %+v", afterRetry)
	}
	if err := budget.Resize(probe, int64(len(original))); err != nil {
		t.Fatal("successful retry did not release its quota reservation", err)
	}
	_ = budget.Resize(probe, 0)
	neighborAfter, err := os.ReadFile(st.files[1].path)
	if err != nil || !bytes.Equal(neighborAfter, neighbor) {
		t.Fatal("neighbor data changed", err)
	}
}

func TestRemoveCachedTorrentSharingViolationKeepsRemainingFilesRetryable(t *testing.T) {
	silenceLogs(t)
	m, hash, _ := loadedSpoolManager(t)
	st := m.spool.torrent(hash)
	budget := disklimit.New(m.spool.dir, 3000, 0)
	m.spool.budget = budget
	for _, file := range st.files {
		file.budget = budget
		if err := budget.Resize(file.path, file.high); err != nil {
			t.Fatal(err)
		}
	}
	lockedPath, otherPath := st.files[0].path, st.files[1].path
	original, err := os.ReadFile(lockedPath)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(lockedPath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	held := true
	t.Cleanup(func() {
		if held {
			_ = windows.CloseHandle(handle)
		}
	})
	m.Keep("magnet:?xt=urn:btih:"+hash, time.Hour)
	m.mu.Lock()
	m.scheduleDropLocked(hash)
	m.mu.Unlock()
	if err := m.RemoveCached(hash, nil); err == nil {
		t.Fatal("whole-release deletion reported success despite locked file")
	}
	status := m.StorageStatus()
	if len(status.Torrents) != 1 || !status.Torrents[0].Files[0].Available || status.Torrents[0].Files[1].Available || status.WrittenBytes != 1000 || status.LogicalBytes != 1000 {
		t.Fatalf("remaining cache was lost or partially removed file still listed: %+v", status)
	}
	retained, err := os.ReadFile(lockedPath)
	if err != nil || !bytes.Equal(retained, original) {
		t.Fatal("locked file changed on failed whole-release deletion", err)
	}
	if _, err := os.Stat(otherPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unlocked file was not removed", err)
	}
	probe := filepath.Join(m.spool.dir, "quota-probe")
	if err := budget.Resize(probe, 2000); err != nil {
		t.Fatal("removed file's quota was not released", err)
	}
	if err := budget.Resize(probe, 2001); !errors.Is(err, disklimit.ErrFull) {
		t.Fatal("retained file's quota was released prematurely", err)
	}
	_ = budget.Resize(probe, 0)
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	held = false
	if err := m.RemoveCached(hash, nil); err != nil {
		t.Fatal("whole-release retry failed", err)
	}
	if len(m.StorageStatus().Torrents) != 0 {
		t.Fatal("fully removed torrent remained in inventory")
	}
	if _, err := os.Stat(lockedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("whole-release retry left locked file", err)
	}
	m.mu.Lock()
	_, timer := m.dropTimers[hash]
	_, kept := m.keepUntil[hash]
	m.mu.Unlock()
	if timer || kept {
		t.Fatal("whole-release retry left cache timer/Keep state")
	}
	if err := budget.Resize(probe, 3000); err != nil {
		t.Fatal("successful whole-release retry did not release all quota", err)
	}
	_ = budget.Resize(probe, 0)
}
