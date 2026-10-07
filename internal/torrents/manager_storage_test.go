package torrents

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/zeoril1/video_viewer/internal/catalog"
)

func TestWrittenRangesExcludeHolesAndDuplicateWrites(t *testing.T) {
	f := &spoolFile{path: filepath.Join(t.TempDir(), "part.spool")}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.writeAt(4096, []byte("tail")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.writeAt(4096, []byte("tail")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.writeAt(0, []byte("head")); err != nil {
		t.Fatal(err)
	}
	u := f.usage()
	if !u.available || u.logical != 4100 || u.written != 8 || u.allocated <= 0 {
		t.Fatalf("sparse/overlapping usage: %+v", u)
	}
	f.ranges = addWrittenRange(f.ranges, 4, 4096)
	if len(f.ranges) != 1 || f.ranges[0] != (byteRange{0, 4100}) {
		t.Fatalf("adjacent ranges were not joined: %+v", f.ranges)
	}
}

func TestSpoolRemoveFileInvalidatesBoundaryOnly(t *testing.T) {
	sp := newSpoolClient(t.TempDir())
	info, data := newSpoolSeasonInfo(t, []spoolTestFile{{"a.mkv", 1000}, {"b.mkv", 2000}}, 512)
	impl, err := sp.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = impl.Close() })
	st := sp.torrent((metainfo.Hash{}).String())
	for i, p := range st.pieces {
		piece := info.Piece(i)
		if _, err := p.WriteAt(data[piece.Offset():piece.Offset()+piece.Length()], 0); err != nil {
			t.Fatal(err)
		}
		_ = p.MarkComplete()
	}
	neighborBefore, err := os.ReadFile(st.files[1].path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.removeFile(0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.files[0].path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("selected file remains", err)
	}
	neighborAfter, err := os.ReadFile(st.files[1].path)
	if err != nil || !bytes.Equal(neighborBefore, neighborAfter) {
		t.Fatal("neighboring file changed", err)
	}
	for i, p := range st.pieces {
		wantComplete := info.Piece(i).Offset() >= 1000
		if p.Completion().Complete != wantComplete {
			t.Fatalf("piece %d completion, want %v", i, wantComplete)
		}
	}
	if st.files[0].usage().written != 0 || st.files[1].usage().written != 2000 {
		t.Fatal("written accounting was not reset independently")
	}
}

func TestDownloadSamplesHandleResetAndRefresh(t *testing.T) {
	now := time.Now()
	first := nextDownloadSample(downloadSample{}, now, 100, []int64{20, 40})
	second := nextDownloadSample(first, now.Add(2*time.Second), 500, []int64{220, 240})
	if second.rate != 200 || second.fileRates[0] != 100 || second.fileRates[1] != 100 {
		t.Fatalf("incorrect rates: %+v", second)
	}
	quick := nextDownloadSample(second, now.Add(2100*time.Millisecond), 510, []int64{225, 245})
	if quick.rate != 200 {
		t.Fatal("rapid refresh erased rate")
	}
	reset := nextDownloadSample(second, now.Add(4*time.Second), 0, []int64{0, 0})
	if reset.rate != 0 || reset.fileRates[0] != 0 {
		t.Fatal("reset produced a negative rate")
	}
}

func TestRemoveCachedValidationAndBusy(t *testing.T) {
	silenceLogs(t)
	m := newTestManager(t)
	item := catalog.Item{ID: "film", Magnet: testMagnet}
	_, release, err := m.Acquire(item)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := hashOf(testMagnet)
	if err := m.RemoveCached("../../outside", nil); !errors.Is(err, ErrInvalidCacheKey) {
		t.Fatal("unsafe key accepted", err)
	}
	if err := m.RemoveCached(hash, nil); !errors.Is(err, ErrCacheBusy) {
		t.Fatal("active reader removed", err)
	}
	unwant := m.WantFile(item, 0)
	release()
	if err := m.RemoveCached(hash, nil); !errors.Is(err, ErrCacheBusy) {
		t.Fatal("wanted preparation removed", err)
	}
	unwant()
	status := m.StorageStatus()
	if status.Mode != "memory" || len(status.Torrents) != 1 || status.Torrents[0].MetadataReady || status.Torrents[0].Busy {
		t.Fatalf("metadata-pending status: %+v", status)
	}
	if err := m.RemoveCached(hash, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveCached(hash, nil); !errors.Is(err, ErrCacheNotFound) {
		t.Fatal("missing torrent accepted", err)
	}
	if len(m.StorageStatus().Torrents) != 0 {
		t.Fatal("removed torrent listed")
	}
}

func loadedSpoolManager(t *testing.T) (*Manager, string, *metainfo.Info) {
	t.Helper()
	info, data := newSpoolSeasonInfo(t, []spoolTestFile{{"a.mkv", 1000}, {"b.mkv", 2000}}, 512)
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	meta := &metainfo.MetaInfo{InfoBytes: infoBytes}
	hash := meta.HashInfoBytes().String()
	m, err := NewManager(Config{SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	impl, err := m.spool.OpenTorrent(context.Background(), info, meta.HashInfoBytes())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < info.NumPieces(); i++ {
		p := info.Piece(i)
		if _, err := impl.Piece(p).WriteAt(data[p.Offset():p.Offset()+p.Length()], 0); err != nil {
			t.Fatal(err)
		}
		_ = impl.Piece(p).MarkComplete()
	}
	spec := torrent.TorrentSpecFromMetaInfo(meta)
	spec.DisallowDataDownload = true
	tor, _, err := m.client.AddTorrentSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range tor.Files() {
		f.SetPriority(torrent.PiecePriorityNone)
	}
	m.mu.Lock()
	m.open[hash] = tor
	m.mu.Unlock()
	return m, hash, info
}

func TestRemoveCachedFileRebuildsTorrentAndPreservesOtherFile(t *testing.T) {
	silenceLogs(t)
	m, hash, _ := loadedSpoolManager(t)
	before := m.StorageStatus()
	if before.Mode != "disk" || before.WrittenBytes != 3000 || before.LogicalBytes != 3000 || before.UsedBytes == 0 {
		t.Fatalf("before removal: %+v", before)
	}
	if before.Disk.TotalBytes == 0 || !before.Torrents[0].Files[0].Available || before.Torrents[0].Downloaded != 3000 {
		t.Fatalf("incomplete status: %+v", before)
	}
	st := m.spool.torrent(hash)
	neighbor, err := os.ReadFile(st.files[1].path)
	if err != nil {
		t.Fatal(err)
	}
	previous := m.open[hash]
	index := 0
	if err := m.RemoveCached(hash, &index); err != nil {
		t.Fatal(err)
	}
	if m.open[hash] == previous {
		t.Fatal("client chunk bookkeeping was not reset")
	}
	after := m.StorageStatus()
	if after.WrittenBytes != 2000 || after.LogicalBytes != 2000 || after.Torrents[0].Files[0].Available || !after.Torrents[0].Files[1].Available {
		t.Fatalf("after removal: %+v", after)
	}
	if after.Torrents[0].Files[0].Downloaded != 0 {
		t.Fatal("removed file still counts as downloaded")
	}
	retained, err := os.ReadFile(st.files[1].path)
	if err != nil || !bytes.Equal(neighbor, retained) {
		t.Fatal("neighbor did not survive", err)
	}
	if err := m.RemoveCached(hash, &index); !errors.Is(err, ErrCacheNotFound) {
		t.Fatal("deleting absent file did not report missing", err)
	}
	index = 2
	if err := m.RemoveCached(hash, &index); !errors.Is(err, ErrInvalidCacheKey) {
		t.Fatal("out of bounds accepted", err)
	}
	if resolved, err := m.ResolveCachedFile(hash, -1); err != nil || resolved != 1 {
		t.Fatalf("auto video selection: %d %v", resolved, err)
	}
	if _, err := m.ResolveCachedFile(hash, -2); !errors.Is(err, ErrInvalidCacheKey) {
		t.Fatal("invalid automatic file index accepted")
	}
	if err := m.RemoveCached(hash, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.files[1].path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("whole-torrent removal left files", err)
	}
}

func TestSpoolCleanupPreservesUnownedSpoolFiles(t *testing.T) {
	dir := t.TempDir()
	unowned := filepath.Join(dir, "keep.spool")
	owned := filepath.Join(dir, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.2.spool")
	for _, path := range []string{unowned, owned} {
		if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sp := newSpoolClient(dir)
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unowned); err != nil {
		t.Fatal("unowned .spool removed", err)
	}
	if _, err := os.Stat(owned); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned orphan remains", err)
	}
}
