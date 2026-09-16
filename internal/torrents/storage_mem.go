package torrents

import (
	"context"
	"errors"
	"io"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// memoryStorage хранит все куски торрента в оперативной памяти — данные никогда не
// пишутся на диск (хранилище по умолчанию, когда Config.SpoolDir пуст). Это прежняя
// часть требования «на сервере файлы не хранятся»: то же самое даёт временный спул
// на диске (storage_spool.go), который удаляется после просмотра.
type memoryStorage struct{}

// OpenTorrent создаёт in-memory представление кусков; буферы аллоцируются лениво
// (при первой записи), поэтому память растёт по мере скачивания.
func (memoryStorage) OpenTorrent(_ context.Context, info *metainfo.Info, _ metainfo.Hash) (storage.TorrentImpl, error) {
	pieces := make([]*memoryPiece, info.NumPieces())
	for i := range pieces {
		pieces[i] = &memoryPiece{length: info.Piece(i).Length()}
	}
	mt := &memoryTorrent{pieces: pieces}

	// TorrentImpl в v1.61 — структура с полями-функциями; клиент всегда зовёт
	// PieceWithHash, поэтому оба поля делаем одним способом.
	piece := func(p metainfo.Piece) storage.PieceImpl {
		return mt.pieces[p.Index()]
	}
	return storage.TorrentImpl{
		Piece:         piece,
		PieceWithHash: func(p metainfo.Piece, _ g.Option[[]byte]) storage.PieceImpl { return piece(p) },
		Close:         mt.Close,
	}, nil
}

func (memoryStorage) Close() error { return nil }

type memoryTorrent struct {
	pieces []*memoryPiece
}

// Close освобождает ссылки на куски, чтобы GC сразу вернул память после выгрузки.
func (t *memoryTorrent) Close() error {
	for i := range t.pieces {
		t.pieces[i] = nil
	}
	t.pieces = nil
	return nil
}

type memoryPiece struct {
	length   int64
	data     []byte
	complete bool
}

func (p *memoryPiece) ensure() {
	if p.data == nil {
		p.data = make([]byte, p.length)
	}
}

func (p *memoryPiece) ReadAt(b []byte, off int64) (int, error) {
	if p.data == nil {
		return 0, io.EOF
	}
	if off < 0 || off >= int64(len(p.data)) {
		return 0, io.EOF
	}
	n := copy(b, p.data[off:])
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

func (p *memoryPiece) WriteAt(b []byte, off int64) (int, error) {
	p.ensure()
	if off+int64(len(b)) > int64(len(p.data)) {
		return 0, errors.New("write beyond piece length")
	}
	return copy(p.data[off:], b), nil
}

func (p *memoryPiece) MarkComplete() error {
	p.complete = true
	return nil
}

func (p *memoryPiece) MarkNotComplete() error {
	p.complete = false
	return nil
}

func (p *memoryPiece) Completion() storage.Completion {
	return storage.Completion{Complete: p.complete, Ok: true}
}

var (
	_ storage.ClientImpl = memoryStorage{}
	_ io.ReaderAt        = (*memoryPiece)(nil)
	_ io.WriterAt        = (*memoryPiece)(nil)
)
