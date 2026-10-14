package torrents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/anacrolix/torrent"
)

// NewVerifiedReader rechecks only cached pieces reached by this reader, rather
// than scanning an entire episode or season before playback. Fresh downloads
// already pass the torrent's normal SHA check before Reader.Read returns them.
// Keep the Manager.Acquire lease until this reader is closed.
func NewVerifiedReader(ctx context.Context, file *torrent.File, readahead int64) io.ReadSeekCloser {
	reader := file.NewReader()
	reader.SetContext(ctx)
	reader.SetReadahead(readahead)
	return &verifiedReader{
		reader: reader, ctx: ctx, file: file,
		checked: make(map[int]bool),
	}
}

type verifiedReader struct {
	reader  torrent.Reader
	ctx     context.Context
	file    *torrent.File
	checked map[int]bool
	pos     int64
}

func (r *verifiedReader) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.pos >= r.file.Length() {
		return 0, io.EOF
	}
	tor := r.file.Torrent()
	select {
	case <-tor.Closed():
		return 0, errors.New("torrent closed")
	default:
	}
	pieceLength := tor.Info().PieceLength
	global := r.file.Offset() + r.pos
	index := int(global / pieceLength)
	if !r.checked[index] {
		piece := tor.Piece(index)
		if piece.State().Complete {
			if err := piece.VerifyDataContext(r.ctx); err != nil {
				return 0, fmt.Errorf("verify cached piece %d: %w", index, err)
			}
			r.checked[index] = true
		}
	}
	// A single successful read must not cross into an unchecked next piece,
	// including the first piece shared with the preceding torrent file.
	limit := min(int64(len(dst)), pieceLength-global%pieceLength, r.file.Length()-r.pos)
	n, err := r.reader.Read(dst[:int(limit)])
	if n > 0 {
		r.checked[index] = true // fresh downloads have now passed the normal SHA check
		r.pos += int64(n)
	}
	return n, err
}

func (r *verifiedReader) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = r.pos
	case io.SeekEnd:
		base = r.file.Length()
	default:
		return r.pos, errors.New("invalid seek whence")
	}
	if offset < -base || offset > math.MaxInt64-base {
		return r.pos, errors.New("invalid seek offset")
	}
	position, err := r.reader.Seek(base+offset, io.SeekStart)
	if err == nil {
		r.pos = position
	}
	return position, err
}

func (r *verifiedReader) Close() error { return r.reader.Close() }
