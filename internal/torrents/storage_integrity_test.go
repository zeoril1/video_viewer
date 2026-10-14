package torrents

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// These tests exercise real torrent hash/reader behavior against cached bytes,
// without peers, trackers or OS sockets (which can have reserved Windows ports).
func loadedIntegrityManager(t *testing.T) (*Manager, string, *metainfo.Info) {
	t.Helper()
	info, data := newSpoolSeasonInfo(t, []spoolTestFile{{"a.mkv", 1000}, {"b.mkv", 2000}}, 512)
	encoded, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	meta := &metainfo.MetaInfo{InfoBytes: encoded}
	spool := newSpoolClient(t.TempDir())
	cc := torrent.NewDefaultClientConfig()
	cc.DefaultStorage = spool
	cc.DisableTCP, cc.DisableUTP, cc.NoDHT = true, true, true
	cc.DisableTrackers, cc.DisableWebtorrent, cc.NoDefaultPortForwarding = true, true, true
	client, err := torrent.NewClient(cc)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{
		client: client, spool: spool,
		open: make(map[string]*torrent.Torrent), readers: make(map[string]int),
		dropTimers: make(map[string]*time.Timer), keepUntil: make(map[string]time.Time),
		fileWants: make(map[string]map[int]int), rates: make(map[string]downloadSample),
		cacheTTL: defaultCacheTTL, maxCached: defaultMaxCached,
	}
	t.Cleanup(m.Close)
	impl, err := spool.OpenTorrent(context.Background(), info, meta.HashInfoBytes())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < info.NumPieces(); i++ {
		p := info.Piece(i)
		if _, err := impl.Piece(p).WriteAt(data[p.Offset():p.Offset()+p.Length()], 0); err != nil {
			t.Fatal(err)
		}
		if err := impl.Piece(p).MarkComplete(); err != nil {
			t.Fatal(err)
		}
	}
	spec := torrent.TorrentSpecFromMetaInfo(meta)
	spec.DisallowDataDownload = true
	tor, _, err := client.AddTorrentSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range tor.Files() {
		file.SetPriority(torrent.PiecePriorityNone)
	}
	hash := meta.HashInfoBytes().String()
	m.open[hash] = tor
	return m, hash, info
}

func readIntegrityRange(t *testing.T, file *torrent.File, offset int64, size int) ([]byte, error) {
	t.Helper()
	reader := file.NewReader()
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reader.SetContext(ctx)
	if _, err := reader.Seek(offset, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, size)
	n, err := io.ReadFull(reader, got)
	return got[:n], err
}

func assertIntegrityRangeWaits(t *testing.T, file *torrent.File, offset int64) {
	t.Helper()
	reader := file.NewReader()
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	reader.SetContext(ctx)
	if _, err := reader.Seek(offset, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 16)
	if n, err := reader.Read(got); n != 0 || err == nil {
		t.Fatalf("damaged cache returned %d bytes with error %v instead of waiting for repair: %x", n, err, got[:n])
	}
}

func verifyIntegrityPiece(t *testing.T, tor *torrent.Torrent, index int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tor.Piece(index).VerifyDataContext(ctx); err != nil {
		t.Fatal(err)
	}
}

// A torrent reader normally trusts the original SHA check. Damaging the spool
// afterward must not turn missing bytes into successfully served zero padding.
func TestSpoolIntegrityReaderRejectsMissingCompletedRanges(t *testing.T) {
	for _, damage := range []string{"truncated", "missing-file", "sparse-hole"} {
		t.Run(damage, func(t *testing.T) {
			silenceLogs(t)
			m, hash, info := loadedIntegrityManager(t)
			st, tor := m.spool.torrent(hash), m.open[hash]
			original := seasonFileData(spoolTestFile{"b.mkv", 2000})
			file := st.files[1]
			index, offset := 5, int64(1560) // final piece is entirely inside b.mkv
			switch damage {
			case "truncated":
				file.mu.Lock()
				err := file.f.Truncate(1500)
				file.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "missing-file":
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			case "sparse-hole":
				file.mu.Lock()
				err := file.f.Truncate(0)
				file.ranges, file.high = nil, 0
				file.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.writeAt(1500, original[1500:]); err != nil {
					t.Fatal(err)
				}
				index, offset = 2, 24 // file has the full size but this range is a hole
			}
			if !st.pieces[index].Completion().Complete {
				t.Fatal("fixture must retain the stale completed bit before reading")
			}
			assertIntegrityRangeWaits(t, tor.Files()[1], offset)
			if st.pieces[index].Completion().Complete {
				t.Fatal("missing range retained a completed bit")
			}
			piece := info.Piece(index)
			start := piece.Offset() - tor.Files()[1].Offset()
			want := original[start : start+piece.Length()]
			if _, err := st.pieces[index].WriteAt(want, 0); err != nil {
				t.Fatal(err)
			}
			verifyIntegrityPiece(t, tor, index)
			if !st.pieces[index].Completion().Complete {
				t.Fatal("SHA-verified repair was not marked complete")
			}
			got, err := readIntegrityRange(t, tor.Files()[1], offset, 16)
			if err != nil || !bytes.Equal(got, original[offset:offset+16]) {
				t.Fatalf("new reader after repair: got %x, error %v", got, err)
			}
		})
	}
}

// Removing a neighboring episode invalidates the shared torrent piece, even
// though the requested episode's own first bytes remain present on disk.
func TestSpoolIntegrityReaderWaitsForEvictedBoundaryPiece(t *testing.T) {
	silenceLogs(t)
	m, hash, info := loadedIntegrityManager(t)
	st := m.spool.torrent(hash)
	index := 0
	if err := m.RemoveCached(hash, &index); err != nil {
		t.Fatal(err)
	}
	tor := m.open[hash]
	assertIntegrityRangeWaits(t, tor.Files()[1], 0)
	if st.pieces[1].Completion().Complete {
		t.Fatal("piece spanning the deleted episode is still complete")
	}
	all := append(seasonFileData(spoolTestFile{"a.mkv", 1000}), seasonFileData(spoolTestFile{"b.mkv", 2000})...)
	piece := info.Piece(1)
	if _, err := st.pieces[1].WriteAt(all[piece.Offset():piece.Offset()+piece.Length()], 0); err != nil {
		t.Fatal(err)
	}
	verifyIntegrityPiece(t, tor, 1)
	got, err := readIntegrityRange(t, tor.Files()[1], 0, 2000)
	if err != nil || !bytes.Equal(got, all[1000:]) {
		t.Fatalf("retained episode after shared piece repair: %d bytes, error %v", len(got), err)
	}
}

// Unlike an EOF/range check, the torrent SHA check also catches byte corruption
// that does not change file length. It must block the real reader until repair.
func TestSpoolIntegrityRehashRejectsSameLengthCorruption(t *testing.T) {
	silenceLogs(t)
	m, hash, info := loadedIntegrityManager(t)
	st, tor := m.spool.torrent(hash), m.open[hash]
	original := seasonFileData(spoolTestFile{"a.mkv", 1000})
	if _, err := st.files[0].writeAt(7, []byte{original[7] ^ 0xff}); err != nil {
		t.Fatal(err)
	}
	verifyIntegrityPiece(t, tor, 0)
	if st.pieces[0].Completion().Complete {
		t.Fatal("SHA check accepted same-length corruption")
	}
	assertIntegrityRangeWaits(t, tor.Files()[0], 0)
	piece := info.Piece(0)
	if _, err := st.pieces[0].WriteAt(original[:piece.Length()], 0); err != nil {
		t.Fatal(err)
	}
	verifyIntegrityPiece(t, tor, 0)
	got, err := readIntegrityRange(t, tor.Files()[0], 0, int(piece.Length()))
	if err != nil || !bytes.Equal(got, original[:piece.Length()]) {
		t.Fatalf("reader after SHA-verified repair: %d bytes, error %v", len(got), err)
	}
}
