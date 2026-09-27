package torrents

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/zeoril1/video_viewer/internal/disklimit"
)

func TestSpoolQuotaChecksSparseGrowthAndReclaimsSpace(t *testing.T) {
	dir := t.TempDir()
	b := disklimit.New(dir, 16, 0)
	f := &spoolFile{path: filepath.Join(dir, "first.spool"), budget: b}
	if _, err := f.writeAt(8, []byte("12345678")); err != nil {
		t.Fatal(err)
	}
	g := &spoolFile{path: filepath.Join(dir, "second.spool"), budget: b}
	if _, err := g.writeAt(0, []byte("x")); err == nil {
		t.Fatal("global quota exceeded")
	}
	info, _ := os.Stat(g.path)
	if info.Size() != 0 {
		t.Fatal("rejected write grew file")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.writeAt(0, []byte("x")); err != nil {
		t.Fatal("deleted space not reclaimed", err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
}

// newSpoolTestInfo создаёт настоящий metainfo.Info из временного файла заданного
// размера с предсказуемым содержимым (куски по pieceLength).
func newSpoolTestInfo(t *testing.T, size, pieceLength int64) (*metainfo.Info, []byte) {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i * 31) // непериодичный узор, чтобы ловить смещения
	}
	f := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(f, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info := &metainfo.Info{PieceLength: pieceLength}
	if err := info.BuildFromFilePath(f); err != nil {
		t.Fatalf("BuildFromFilePath: %v", err)
	}
	return info, data
}

// TestSpoolWriteReadPieces — запись/чтение всех кусков (один спул-файл на серию):
// проверяем данные и наличие файла на диске.
func TestSpoolWriteReadPieces(t *testing.T) {
	dir := t.TempDir()
	sp := newSpoolClient(dir)

	info, data := newSpoolTestInfo(t, 1000, 128)
	impl, err := sp.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatalf("OpenTorrent: %v", err)
	}

	n := info.NumPieces()
	if n == 0 {
		t.Fatal("expected non-zero pieces")
	}
	for i := 0; i < n; i++ {
		p := info.Piece(i)
		buf := data[p.Offset() : p.Offset()+p.Length()]
		got, err := impl.Piece(p).WriteAt(buf, 0)
		if err != nil {
			t.Fatalf("piece %d WriteAt: %v", i, err)
		}
		if got != len(buf) {
			t.Fatalf("piece %d: wrote %d of %d", i, got, len(buf))
		}
		if err := impl.Piece(p).MarkComplete(); err != nil {
			t.Fatalf("piece %d MarkComplete: %v", i, err)
		}
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "*.spool"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 spool file, got %d", len(matches))
	}

	for i := 0; i < n; i++ {
		p := info.Piece(i)
		want := data[p.Offset() : p.Offset()+p.Length()]
		got := make([]byte, p.Length())
		if _, err := impl.Piece(p).ReadAt(got, 0); err != nil {
			t.Fatalf("piece %d ReadAt: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("piece %d: data mismatch", i)
		}
		if c := impl.Piece(p).Completion(); !c.Ok || !c.Complete {
			t.Fatalf("piece %d: completion %+v", i, c)
		}
	}

	if err := impl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(matches[0]); !os.IsNotExist(err) {
		t.Fatalf("spool file должен быть удалён, err=%v", err)
	}
}

// TestSpoolConcurrent — параллельные WriteAt/ReadAt разных кусков. Запускать с -race.
func TestSpoolConcurrent(t *testing.T) {
	dir := t.TempDir()
	sp := newSpoolClient(dir)

	const pieceLength = 4096
	info, data := newSpoolTestInfo(t, pieceLength*16, pieceLength)
	impl, err := sp.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatalf("OpenTorrent: %v", err)
	}
	defer impl.Close()

	n := info.NumPieces()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := info.Piece(i)
			buf := data[p.Offset() : p.Offset()+p.Length()]
			if _, err := impl.Piece(p).WriteAt(buf, 0); err != nil {
				t.Errorf("piece %d WriteAt: %v", i, err)
				return
			}
			if err := impl.Piece(p).MarkComplete(); err != nil {
				t.Errorf("piece %d MarkComplete: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		p := info.Piece(i)
		want := data[p.Offset() : p.Offset()+p.Length()]
		got := make([]byte, p.Length())
		if _, err := impl.Piece(p).ReadAt(got, 0); err != nil {
			t.Fatalf("piece %d ReadAt: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("piece %d: data mismatch", i)
		}
	}
}

// TestSpoolPartialPieceRead — до MarkComplete данных нет: читаем EOF до записи.
func TestSpoolPartialPieceRead(t *testing.T) {
	dir := t.TempDir()
	sp := newSpoolClient(dir)

	info, _ := newSpoolTestInfo(t, 1000, 128)
	impl, err := sp.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatalf("OpenTorrent: %v", err)
	}
	defer impl.Close()

	p := info.Piece(0)
	if c := impl.Piece(p).Completion(); !c.Ok || c.Complete {
		t.Fatalf("пустой кусок не должен быть complete: %+v", c)
	}
	if _, err := impl.Piece(p).ReadAt(make([]byte, 16), 0); err != io.EOF {
		t.Fatalf("чтение незаписанного куска: ожидали EOF, got %v", err)
	}
}

// TestSpoolClientClose — Close клиента спула убирает осиротевшие *.spool, не трогая чужие файлы.
func TestSpoolClientClose(t *testing.T) {
	dir := t.TempDir()
	sp := newSpoolClient(dir)

	info, _ := newSpoolTestInfo(t, 100, 128)
	if _, err := sp.OpenTorrent(context.Background(), info, metainfo.Hash{}); err != nil {
		t.Fatalf("OpenTorrent: %v", err)
	}
	// «Чужой» файл в каталоге спула.
	other := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Осиротевший спул-файл (как после сбоя процесса).
	orphan := filepath.Join(dir, "deadbeef.3.spool")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := sp.Close(); err != nil {
		t.Fatalf("spool.Close: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.spool"))
	if len(matches) != 0 {
		t.Fatalf("после Close остались спул-файлы: %v", matches)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("чужие файлы не должны удаляться: %v", err)
	}
}

// TestManagerSpoolCleanup — Close менеджера со спулом удаляет осиротевшие *.spool,
// не трогая чужие файлы (silenceLogs — из manager_test).
func TestManagerSpoolCleanup(t *testing.T) {
	silenceLogs(t)
	dir := t.TempDir()
	orphan := filepath.Join(dir, "orphan.spool")
	other := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(Config{SpoolDir: dir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	mgr.Close()

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("осиротевший спул-файл должен быть удалён, err=%v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("чужие файлы не должны удаляться: %v", err)
	}
}

// TestSpoolLegacyCleanup — при старте спула удаляются файлы прежнего формата
// (<hash>.spool — единый на весь торрент), а новый (<hash>.<индекс>.spool) и чужие — нет.
func TestSpoolLegacyCleanup(t *testing.T) {
	dir := t.TempDir()
	const hash = "097fd9047fa928346e94ed5c1cb07b9531c15f90"

	legacy := filepath.Join(dir, hash+".spool")
	perSeries := filepath.Join(dir, hash+".3.spool")
	other := filepath.Join(dir, "keep.txt")
	for _, p := range []string{legacy, perSeries, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	newSpoolClient(dir)

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("файл старого формата должен быть удалён, err=%v", err)
	}
	if _, err := os.Stat(perSeries); err != nil {
		t.Fatalf("файл нового формата удалять нельзя: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("чужие файлы удалять нельзя: %v", err)
	}
}

// spoolTestFile — файл (серия) тестового многофайлового торрента.
type spoolTestFile struct {
	name string
	size int64
}

func seasonFileData(f spoolTestFile) []byte {
	buf := make([]byte, f.size)
	for i := range buf {
		buf[i] = byte(i*31 + len(f.name))
	}
	return buf
}

// newSpoolSeasonInfo создаёт многофайловый торрент («сезон-пак») и возвращает
// его данные, склеенные в порядке файлов торрента (виртуальное адресное пространство).
func newSpoolSeasonInfo(t *testing.T, files []spoolTestFile, pieceLength int64) (*metainfo.Info, []byte) {
	t.Helper()
	dir := t.TempDir()
	byName := make(map[string][]byte, len(files))
	for _, f := range files {
		byName[f.name] = seasonFileData(f)
		if err := os.WriteFile(filepath.Join(dir, f.name), byName[f.name], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	info := &metainfo.Info{PieceLength: pieceLength}
	if err := info.BuildFromFilePath(dir); err != nil {
		t.Fatalf("BuildFromFilePath: %v", err)
	}

	// Данные собираем в порядке файлов info (как их обошёл BuildFromFilePath).
	var all []byte
	for _, fi := range info.UpvertedFiles() {
		name := fi.Path[len(fi.Path)-1]
		all = append(all, byName[name]...)
	}
	if int64(len(all)) != info.TotalLength() {
		t.Fatalf("размер данных %d != TotalLength %d", len(all), info.TotalLength())
	}
	return info, all
}

// TestSpoolPerSeriesAllocation — главное требование: место выделяется под качаемую
// серию, а не под весь торрент (качаем последнюю серию → один файл размером с серию).
func TestSpoolPerSeriesAllocation(t *testing.T) {
	dir := t.TempDir()
	sp := newSpoolClient(dir)

	// Размеры кратны pieceLength — куски не пересекают границы серий.
	files := []spoolTestFile{
		{"S01E01.mkv", 3 * 1024},
		{"S01E02.mkv", 5 * 1024},
		{"S01E03.mkv", 7 * 1024},
	}
	info, all := newSpoolSeasonInfo(t, files, 1024)
	impl, err := sp.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatalf("OpenTorrent: %v", err)
	}
	defer impl.Close()

	// Открытие торрента (для списка серий) файлов не создаёт: место не выделено, пока серия не качается.
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.spool")); len(matches) != 0 {
		t.Fatalf("спул только что открытого торрента должен быть пуст: %v", matches)
	}

	fileInfos := info.UpvertedFiles()
	lastIdx := len(fileInfos) - 1
	last := fileInfos[lastIdx]

	// Пишем куски последней серии (как если бы её выбрал пользователь).
	writePiece := func(i int) {
		p := info.Piece(i)
		if _, err := impl.Piece(p).WriteAt(all[p.Offset():p.Offset()+p.Length()], 0); err != nil {
			t.Fatalf("piece %d WriteAt: %v", i, err)
		}
		if err := impl.Piece(p).MarkComplete(); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < info.NumPieces(); i++ {
		p := info.Piece(i)
		if p.Offset()+p.Length() <= last.TorrentOffset || p.Offset() >= last.TorrentOffset+last.Length {
			continue // кусок другой серии — не качаем
		}
		writePiece(i)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "*.spool"))
	if len(matches) != 1 {
		t.Fatalf("должен быть создан ровно один спул-файл (одна серия), got %v", matches)
	}
	want := fmt.Sprintf("%s.%d.spool", (metainfo.Hash{}).String(), lastIdx)
	if filepath.Base(matches[0]) != want {
		t.Fatalf("спул-файл %s, ожидали %s", filepath.Base(matches[0]), want)
	}
	fi, err := os.Stat(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != last.Length {
		t.Fatalf("размер спула %d, ожидали размер серии %d (торрент целиком %d)",
			fi.Size(), last.Length, info.TotalLength())
	}

	// Выбранная серия читается обратно без потерь.
	for i := 0; i < info.NumPieces(); i++ {
		p := info.Piece(i)
		if p.Offset()+p.Length() <= last.TorrentOffset || p.Offset() >= last.TorrentOffset+last.Length {
			continue
		}
		got := make([]byte, p.Length())
		if _, err := impl.Piece(p).ReadAt(got, 0); err != nil {
			t.Fatalf("piece %d ReadAt: %v", i, err)
		}
		if !bytes.Equal(got, all[p.Offset():p.Offset()+p.Length()]) {
			t.Fatalf("piece %d: данные выбранной серии не совпали", i)
		}
	}

	// Другие серии не качались — данных нет.
	first := info.Piece(0)
	if _, err := impl.Piece(first).ReadAt(make([]byte, 16), 0); err != io.EOF {
		t.Fatalf("первая серия не качалась: ожидали EOF, got %v", err)
	}

	if err := impl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(matches[0]); !os.IsNotExist(err) {
		t.Fatalf("спул-файл серии должен быть удалён, err=%v", err)
	}
}

// TestSpoolPieceAcrossSeries — кусок на границе двух серий пишется в оба файла серий
// и целиком читается обратно.
func TestSpoolPieceAcrossSeries(t *testing.T) {
	dir := t.TempDir()
	sp := newSpoolClient(dir)

	// 1000 не кратно 512: кусок 1 попадает на границу серий.
	files := []spoolTestFile{{"a.bin", 1000}, {"b.bin", 2000}}
	info, all := newSpoolSeasonInfo(t, files, 512)
	impl, err := sp.OpenTorrent(context.Background(), info, metainfo.Hash{})
	if err != nil {
		t.Fatalf("OpenTorrent: %v", err)
	}
	defer impl.Close()

	for i := 0; i < info.NumPieces(); i++ {
		p := info.Piece(i)
		if _, err := impl.Piece(p).WriteAt(all[p.Offset():p.Offset()+p.Length()], 0); err != nil {
			t.Fatalf("piece %d WriteAt: %v", i, err)
		}
		if err := impl.Piece(p).MarkComplete(); err != nil {
			t.Fatal(err)
		}
	}

	// Каждая серия — в своём файле размером с серию (а не с торрент).
	matches, _ := filepath.Glob(filepath.Join(dir, "*.spool"))
	fileInfos := info.UpvertedFiles()
	if len(matches) != len(fileInfos) {
		t.Fatalf("ожидали %d спул-файлов (по одному на серию), got %v", len(fileInfos), matches)
	}
	for i, f := range fileInfos {
		p := filepath.Join(dir, fmt.Sprintf("%s.%d.spool", (metainfo.Hash{}).String(), i))
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("серия %d: %v", i, err)
		}
		if fi.Size() != f.Length {
			t.Fatalf("серия %d: размер спула %d, ожидали %d", i, fi.Size(), f.Length)
		}
	}

	// Читаем весь торрент через куски: на границе серий данные не теряются.
	for i := 0; i < info.NumPieces(); i++ {
		p := info.Piece(i)
		got := make([]byte, p.Length())
		if _, err := impl.Piece(p).ReadAt(got, 0); err != nil {
			t.Fatalf("piece %d ReadAt: %v", i, err)
		}
		if !bytes.Equal(got, all[p.Offset():p.Offset()+p.Length()]) {
			t.Fatalf("piece %d: данные не совпали (граница серий)", i)
		}
	}
}
