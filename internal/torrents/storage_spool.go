package torrents

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/zeoril1/video_viewer/internal/disklimit"
)

// spoolClient хранит скачанные куски в файлах на диске — ПО ОДНОМУ ФАЙЛУ НА ФАЙЛ
// ТОРРЕНТА (серию), а не в одном общем файле на весь торрент: в сезон-паке при
// едином файле первая же запись в серию из середины растягивала файл до её
// смещения (десятки ГБ вместо пары ГБ — размер файла равен размеру серии).
// Данные лежат в вытесняемом OS page cache, в heap — только карта завершённости
// кусков; спул временный (файлы удаляются при Drop/Close).
type spoolClient struct {
	budget *disklimit.Budget
	dir    string // каталог для файлов *.spool

	// mu защищает open; нужен, чтобы Close успел закрыть файлы до удаления
	// (на Windows нельзя удалить открытый файл).
	mu   sync.Mutex
	open map[string]*spoolTorrent
}

// newSpoolClient создаёт спул в каталоге и подчищает файлы старого формата (removeLegacySpool).
func newSpoolClient(dir string) *spoolClient {
	removeLegacySpool(dir)
	return &spoolClient{dir: dir, open: make(map[string]*spoolTorrent)}
}

// removeLegacySpool удаляет спул ПРЕЖНЕГО формата (<hash>.spool — один файл на
// весь торрент, до десятков ГБ); новый (<hash>.<индекс серии>.spool) не трогает.
// Вызывается при старте, когда живых файлов быть не может.
func removeLegacySpool(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // каталога ещё нет — нечего чистить
	}
	const suffix = ".spool"
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, suffix) {
			continue
		}
		key := strings.TrimSuffix(name, suffix)
		if len(key) != 40 || strings.Contains(key, ".") {
			continue // не legacy-имя (новый формат — с индексом серии)
		}
		var h metainfo.Hash
		if _, err := hex.Decode(h[:], []byte(key)); err != nil {
			continue
		}
		log.Printf("spool: удаляю осиротевший файл старого формата %s", name)
		_ = os.Remove(filepath.Join(dir, name))
	}
}

var errSpoolClosed = errors.New("spool: storage closed")

// Each spool directory belongs to one stream process. Completion maps live in
// memory, so data from an earlier process cannot safely be reused after restart.
func removeAbandonedSpool(dir string) {
	owned := regexp.MustCompile(`^[a-fA-F0-9]{40}\.[0-9]+\.spool$`)
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if !entry.IsDir() && owned.MatchString(entry.Name()) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// spoolTorrent — открытый торрент: по файлу на серию; кусок на границе серий
// разбит на сегменты (pieceSeg).
type spoolTorrent struct {
	client *spoolClient
	key    string // info hash (для реестра)
	files  []*spoolFile
	pieces []*spoolPiece

	// mu защищает closed и сериализует закрытие с чтением/записью
	// (операции держат RLock, Close — Lock).
	mu     sync.RWMutex
	closed bool
}

// spoolFile — дисковый файл серии; создаётся лениво (пока серия не качается, файла нет).
type spoolFile struct {
	budget *disklimit.Budget
	high   int64
	path   string
	offset int64 // глобальное смещение серии в торренте
	size   int64 // полный размер серии

	mu sync.Mutex
	f  *os.File // nil — в серию ещё ничего не писали
}

// pieceSeg — часть куска торрента, попадающая в конкретную серию.
type pieceSeg struct {
	file     *spoolFile
	pieceOff int64 // смещение сегмента внутри куска
	fileOff  int64 // смещение сегмента внутри файла серии
	length   int64
}

// spoolPiece — кусок торрента, данные лежат в сегментах (по сегменту на каждую
// перекрытую серию).
type spoolPiece struct {
	st     *spoolTorrent
	length int64
	segs   []pieceSeg

	// mu защищает complete; записи/чтения данных координируются в st.mu.
	mu       sync.Mutex
	complete bool
}

var (
	_ storage.ClientImplCloser = (*spoolClient)(nil)
	_ io.ReaderAt              = (*spoolPiece)(nil)
	_ io.WriterAt              = (*spoolPiece)(nil)
)

// OpenTorrent строит файлы и куски торрента; файлы на диске НЕ создаются
// (появятся при первой записи в серию, см. spoolFile.writeAt). Повторное
// открытие того же info hash переиспользует живой спул.
func (c *spoolClient) OpenTorrent(_ context.Context, info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	key := infoHash.String()

	c.mu.Lock()
	if st := c.open[key]; st != nil && st.isOpen() {
		c.mu.Unlock()
		return implFor(st), nil
	}
	c.mu.Unlock()

	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return storage.TorrentImpl{}, fmt.Errorf("spool: create dir %s: %w", c.dir, err)
	}

	st := newSpoolTorrent(c, key, info)

	c.mu.Lock()
	// Гонка одновременных открытий одного hash: побеждает первый.
	if prev := c.open[key]; prev != nil && prev.isOpen() {
		c.mu.Unlock()
		return implFor(prev), nil
	}
	c.open[key] = st
	c.mu.Unlock()
	return implFor(st), nil
}

// newSpoolTorrent раскладывает торрент по сериям и кускам (файлов на диске не создаёт).
func newSpoolTorrent(c *spoolClient, key string, info *metainfo.Info) *spoolTorrent {
	t := &spoolTorrent{client: c, key: key}

	// TorrentOffset — глобальное смещение файла в торренте; совпадает с File.Offset() anacrolix.
	fileInfos := info.UpvertedFiles()
	t.files = make([]*spoolFile, len(fileInfos))
	for i, fi := range fileInfos {
		t.files[i] = &spoolFile{
			budget: c.budget,
			path:   filepath.Join(c.dir, fmt.Sprintf("%s.%d.spool", key, i)),
			offset: fi.TorrentOffset,
			size:   fi.Length,
		}
	}

	n := info.NumPieces()
	t.pieces = make([]*spoolPiece, n)
	for i := 0; i < n; i++ {
		p := info.Piece(i)
		begin := p.Offset()
		t.pieces[i] = &spoolPiece{
			st:     t,
			length: p.Length(),
			segs:   t.segmentsFor(begin, begin+p.Length()),
		}
	}
	return t
}

// segmentsFor разбивает глобальный диапазон [begin, end) по сериям; байты вне
// серий (padding piece-aligned торрентов) отбрасываются — они не отдаются клиенту.
func (t *spoolTorrent) segmentsFor(begin, end int64) []pieceSeg {
	var segs []pieceSeg
	for _, f := range t.files {
		fBegin, fEnd := f.offset, f.offset+f.size
		if fEnd <= begin {
			continue
		}
		if fBegin >= end {
			break // файлы идут по возрастанию смещения
		}
		lo, hi := max(begin, fBegin), min(end, fEnd)
		if hi <= lo {
			continue
		}
		segs = append(segs, pieceSeg{
			file:     f,
			pieceOff: lo - begin,
			fileOff:  lo - fBegin,
			length:   hi - lo,
		})
	}
	return segs
}

// implFor собирает TorrentImpl; Piece и PieceWithHash отдают один и тот же кусок
// (как memoryStorage).
func implFor(st *spoolTorrent) storage.TorrentImpl {
	piece := func(p metainfo.Piece) storage.PieceImpl {
		return st.pieces[p.Index()]
	}
	return storage.TorrentImpl{
		Piece:         piece,
		PieceWithHash: func(p metainfo.Piece, _ g.Option[[]byte]) storage.PieceImpl { return piece(p) },
		Close:         st.Close,
	}
}

func (t *spoolTorrent) isOpen() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return !t.closed
}

func (c *spoolClient) forget(key string, st *spoolTorrent) {
	c.mu.Lock()
	if c.open[key] == st {
		delete(c.open, key)
	}
	c.mu.Unlock()
}

// usage возвращает число файлов серий в спуле торрента и их суммарный размер
// (для диагностики /api/debug/mem).
func (c *spoolClient) usage(key string) (files int, bytes int64) {
	c.mu.Lock()
	st := c.open[key]
	c.mu.Unlock()
	if st == nil {
		return 0, 0
	}
	return st.usage()
}

func (t *spoolTorrent) usage() (files int, bytes int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, f := range t.files {
		if !f.exists() {
			continue
		}
		files++
		if fi, err := os.Stat(f.path); err == nil {
			bytes += fi.Size()
		}
	}
	return files, bytes
}

// Close закрывает все торренты (каждый удаляет файлы своих серий) и убирает
// осиротевшие *.spool; чужие файлы в каталоге не трогает. Зовётся из Manager.Close.
func (c *spoolClient) Close() error {
	c.mu.Lock()
	sts := make([]*spoolTorrent, 0, len(c.open))
	for _, st := range c.open {
		sts = append(sts, st)
	}
	c.mu.Unlock()

	var errs []error
	for _, st := range sts {
		if err := st.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	matches, err := filepath.Glob(filepath.Join(c.dir, "*.spool"))
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, p := range matches {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("spool: remove %s: %w", p, err))
		}
	}
	_ = os.Remove(c.dir) // каталог пуст — убираем
	return errors.Join(errs...)
}

// Close закрывает дисковые файлы серий торрента и удаляет их (Drop торрента или
// закрытие клиента → данные с диска освобождаются).
func (t *spoolTorrent) Close() error {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()

	if t.client != nil {
		t.client.forget(t.key, t)
	}

	var errs []error
	for _, f := range t.files {
		if err := f.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f *spoolFile) exists() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.f != nil
}

func (f *spoolFile) reader() *os.File {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.f
}

// writeAt пишет данные серии, создавая её файл при первой записи (лениво —
// место выделяется только под качаемые серии).
func (f *spoolFile) writeAt(off int64, b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.f == nil {
		file, err := os.OpenFile(f.path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
		if err != nil {
			return 0, err
		}
		f.f = file
		_ = f.budget.Resize(f.path, 0)
	}
	high := max(f.high, off+int64(len(b)))
	if err := f.budget.Resize(f.path, high); err != nil {
		return 0, err
	}
	n, err := f.f.WriteAt(b, off)
	if info, e := f.f.Stat(); e == nil {
		f.high = info.Size()
		_ = f.budget.Resize(f.path, f.high)
	} else {
		f.high = high
	}
	if n < len(b) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}

func (f *spoolFile) Close() error {
	f.mu.Lock()
	file := f.f
	f.f = nil
	f.mu.Unlock()

	if file == nil {
		return nil
	}
	closeErr := file.Close()
	// Файл удаляем после закрытия дескриптора (важно на Windows).
	if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(closeErr, fmt.Errorf("spool: remove %s: %w", f.path, err))
	}
	_ = f.budget.Resize(f.path, 0)
	return closeErr
}

// ReadAt читает диапазон куска (off — смещение внутри куска), разложенный по файлам
// серий; пока не записан ни один сегмент — io.EOF (читатель ждёт скачивания куска).
func (p *spoolPiece) ReadAt(b []byte, off int64) (int, error) {
	if off < 0 || off >= p.length {
		return 0, io.EOF
	}
	t := p.st
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return 0, errSpoolClosed
	}

	end := min(off+int64(len(b)), p.length)
	ready := false
	for _, s := range p.segs {
		if s.file.reader() != nil {
			ready = true
			break
		}
	}
	if !ready {
		return 0, io.EOF
	}

	for _, s := range p.segs {
		if s.pieceOff >= end {
			break
		}
		if s.pieceOff+s.length <= off {
			continue
		}
		lo, hi := max(off, s.pieceOff), min(end, s.pieceOff+s.length)
		dst := b[lo-off : hi-off]

		f := s.file.reader()
		if f == nil {
			clear(dst) // серия ещё не качалась
			continue
		}
		n, err := f.ReadAt(dst, s.fileOff+(lo-s.pieceOff))
		if n < len(dst) {
			clear(dst[n:]) // хвост серии ещё не записан
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
	}
	return int(end - off), nil
}

// WriteAt пишет диапазон куска (off — смещение внутри куска) в файлы затронутых
// серий: кусок на границе серий попадает в оба.
func (p *spoolPiece) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(b)) > p.length {
		return 0, errors.New("spool: write beyond piece length")
	}
	if len(b) == 0 {
		return 0, nil
	}
	t := p.st
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		return 0, errSpoolClosed
	}

	end := off + int64(len(b))
	for _, s := range p.segs {
		if s.pieceOff >= end {
			break
		}
		if s.pieceOff+s.length <= off {
			continue
		}
		lo, hi := max(off, s.pieceOff), min(end, s.pieceOff+s.length)
		if _, err := s.file.writeAt(s.fileOff+(lo-s.pieceOff), b[lo-off:hi-off]); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func (p *spoolPiece) MarkComplete() error {
	p.mu.Lock()
	p.complete = true
	p.mu.Unlock()
	return nil
}

func (p *spoolPiece) MarkNotComplete() error {
	p.mu.Lock()
	p.complete = false
	p.mu.Unlock()
	return nil
}

func (p *spoolPiece) Completion() storage.Completion {
	p.mu.Lock()
	complete := p.complete
	p.mu.Unlock()
	return storage.Completion{Complete: complete, Ok: true}
}
