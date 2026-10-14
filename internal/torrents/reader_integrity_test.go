package torrents

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"testing"
	"time"
)

func waitIntegrityInvalidation(t *testing.T, piece *spoolPiece) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for piece.Completion().Complete {
		if time.Now().After(deadline) {
			t.Fatal("cached corruption was not detected before reading")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestVerifiedReaderRechecksUsedCacheAfterRestartAndRepairs(t *testing.T) {
	silenceLogs(t)
	m, hash, info := loadedIntegrityManager(t)
	st, tor := m.spool.torrent(hash), m.open[hash]
	original := seasonFileData(spoolTestFile{"b.mkv", 2000})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first := NewVerifiedReader(ctx, tor.Files()[1], 512)
	got, err := io.ReadAll(first)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("initial cached playback: %d bytes, error %v", len(got), err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	// Same logical file size and written ranges: a range/EOF check cannot see this.
	if _, err := st.files[1].writeAt(30, []byte{original[30] ^ 0xff}); err != nil {
		t.Fatal(err)
	}
	second := NewVerifiedReader(ctx, tor.Files()[1], 512)
	defer second.Close()
	if _, err := second.Seek(24, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	type result struct {
		bytes []byte
		err   error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, 1024)
		n, err := second.Read(buf)
		done <- result{buf[:n], err}
	}()
	waitIntegrityInvalidation(t, st.pieces[2])
	select {
	case r := <-done:
		t.Fatalf("new reader published damaged bytes before repair: %x, error %v", r.bytes, r.err)
	default:
	}
	piece := info.Piece(2)
	start := piece.Offset() - tor.Files()[1].Offset()
	if _, err := st.pieces[2].WriteAt(original[start:start+piece.Length()], 0); err != nil {
		t.Fatal(err)
	}
	verifyIntegrityPiece(t, tor, 2)
	select {
	case r := <-done:
		if r.err != nil || !bytes.Equal(r.bytes, original[24:536]) {
			t.Fatalf("restarted reader after repair: %d bytes, error %v", len(r.bytes), r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not resume after SHA-verified repair")
	}
}

func TestVerifiedReaderChecksOnlyUsedPiecesAndHonorsFileOffset(t *testing.T) {
	silenceLogs(t)
	m, hash, _ := loadedIntegrityManager(t)
	st, tor := m.spool.torrent(hash), m.open[hash]
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reader := NewVerifiedReader(ctx, tor.Files()[1], 512).(*verifiedReader)
	defer reader.Close()
	got := make([]byte, 1024)
	// b.mkv begins 1000 bytes into the torrent, 24 bytes before the first
	// shared piece ends. A large Read may return only this verified prefix.
	if n, err := reader.Read(got); n != 24 || err != nil {
		t.Fatalf("first file range crossed an unchecked piece: %d, %v", n, err)
	}
	if len(reader.checked) != 1 || !reader.checked[1] {
		t.Fatalf("checked unrelated pieces or wrong file offset: %+v", reader.checked)
	}
	// Modify an unused piece. The first range must not have scanned it;
	// a distant seek within the same HTTP request must check it on demand.
	original := seasonFileData(spoolTestFile{"b.mkv", 2000})
	if _, err := st.files[1].writeAt(1900, []byte{original[1900] ^ 0xff}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Seek(1900, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(ctx)
	reader.ctx = ctx2
	reader.reader.SetContext(ctx2)
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() { n, err := reader.Read(got); done <- result{n, err} }()
	waitIntegrityInvalidation(t, st.pieces[5])
	cancel2()
	select {
	case r := <-done:
		if r.n != 0 || !errors.Is(r.err, context.Canceled) {
			t.Fatalf("cancelled reader returned unchecked bytes: %d, %v", r.n, r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not release reader/hash locks")
	}
}

func TestVerifiedReaderSeekBoundsAndCancelledContext(t *testing.T) {
	silenceLogs(t)
	m, hash, _ := loadedIntegrityManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	reader := NewVerifiedReader(ctx, m.open[hash].Files()[0], 512)
	defer reader.Close()
	defer cancel()
	if _, err := reader.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("negative seek accepted")
	}
	if pos, err := reader.Seek(1001, io.SeekStart); err != nil || pos != 1001 {
		t.Fatalf("ordinary seek beyond end: %d, %v", pos, err)
	}
	if n, err := reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read beyond end: %d, %v", n, err)
	}
	if _, err := reader.Seek(math.MaxInt64, io.SeekCurrent); err == nil {
		t.Fatal("overflowing relative seek accepted")
	}
	if _, err := reader.Seek(0, 42); err == nil {
		t.Fatal("invalid seek whence accepted")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	cancel()
	if n, err := reader.Read(make([]byte, 1)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("read with cancelled context: %d, %v", n, err)
	}
}
