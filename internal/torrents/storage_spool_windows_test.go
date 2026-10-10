package torrents

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/zeoril1/video_viewer/internal/disklimit"
	"golang.org/x/sys/windows"
)

func denySpoolDeletion(t *testing.T, path string) func() {
	t.Helper()
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
	release := func() {
		if held {
			if err := windows.CloseHandle(handle); err != nil {
				t.Fatal(err)
			}
			held = false
		}
	}
	t.Cleanup(release)
	return release
}

func TestDroppedSpoolSharingViolationStaysVisibleAndRetryable(t *testing.T) {
	for _, whole := range []bool{false, true} {
		name := "file"
		if whole {
			name = "torrent"
		}
		t.Run(name, func(t *testing.T) {
			silenceLogs(t)
			m, hash, _ := loadedSpoolManager(t)
			st := m.spool.torrent(hash)
			budget := disklimit.New(m.spool.dir, 3000, 0)
			m.spool.budget = budget
			for _, f := range st.files {
				f.budget = budget
				if err := budget.Resize(f.path, f.high); err != nil {
					t.Fatal(err)
				}
			}
			path := st.files[0].path
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			release := denySpoolDeletion(t, path)
			m.mu.Lock()
			m.dropLocked(hash)
			m.mu.Unlock()
			if st.isOpen() || m.open[hash] != nil {
				t.Fatal("dropped torrent still exposes usable storage")
			}
			if m.spool.torrent(hash) != st {
				t.Fatal("failed removal forgot the spool")
			}
			status := m.StorageStatus()
			if len(status.Torrents) != 1 || !status.Torrents[0].PendingRemoval || status.Torrents[0].Files[0].Available || status.WrittenBytes != 1000 || status.LogicalBytes != 1000 {
				t.Fatalf("pending files were lost or exposed as playable: %+v", status)
			}
			if _, err := m.ResolveCachedFile(hash, 0); !errors.Is(err, ErrCacheNotFound) {
				t.Fatal("stopped torrent resolved as playable", err)
			}
			probe := filepath.Join(m.spool.dir, "quota-probe")
			if err := budget.Resize(probe, 2001); !errors.Is(err, disklimit.ErrFull) {
				t.Fatal("failed removal released retained file quota", err)
			}
			index := 0
			var requested *int
			if !whole {
				requested = &index
			}
			if err := m.RemoveCached(hash, requested); err == nil {
				t.Fatal("retry ignored the external deletion restriction")
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, original) {
				t.Fatal("failed retry changed the retained file", err)
			}
			release()
			if err := m.RemoveCached(hash, requested); err != nil {
				t.Fatal("retry after unlocking failed", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("retry left the file", err)
			}
			if m.spool.torrent(hash) != nil || len(m.StorageStatus().Torrents) != 0 {
				t.Fatal("successful retry retained inventory")
			}
			if err := budget.Resize(probe, 3000); err != nil {
				t.Fatal("successful retry did not release quota", err)
			}
			_ = budget.Resize(probe, 0)
		})
	}
}

func TestSpoolCannotReopenClosedStorageUntilLockedFileIsRemoved(t *testing.T) {
	sp := newSpoolClient(t.TempDir())
	info, data := newSpoolTestInfo(t, 32, 32)
	hash := metainfo.Hash{}
	impl, err := sp.OpenTorrent(context.Background(), info, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := impl.Piece(info.Piece(0)).WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	st := sp.torrent(hash.String())
	release := denySpoolDeletion(t, st.files[0].path)
	if err := impl.Close(); err == nil {
		t.Fatal("Close ignored the deletion restriction")
	}
	if _, err := sp.OpenTorrent(context.Background(), info, hash); err == nil {
		t.Fatal("locked closed storage was reopened")
	}
	if got, err := os.ReadFile(st.files[0].path); err != nil || !bytes.Equal(got, data) {
		t.Fatal("reopening attempt truncated retained data", err)
	}
	release()
	replacement, err := sp.OpenTorrent(context.Background(), info, hash)
	if err != nil {
		t.Fatal("unlocked spool could not reopen", err)
	}
	t.Cleanup(func() { _ = replacement.Close() })
	if sp.torrent(hash.String()) == st || st.files[0].exists() {
		t.Fatal("closed storage was reused instead of removed")
	}
}
