package torrents

import (
	"errors"
	"fmt"
	"os"

	"github.com/zeoril1/video_viewer/internal/disklimit"
)

type byteRange struct{ begin, end int64 }

// addWrittenRange keeps disjoint, sorted ranges. Writing a distant piece does
// not count the intervening hole as downloaded and rewriting it counts once.
func addWrittenRange(ranges []byteRange, begin, end int64) []byteRange {
	if end <= begin {
		return ranges
	}
	first := 0
	for first < len(ranges) && ranges[first].end < begin {
		first++
	}
	last := first
	for last < len(ranges) && ranges[last].begin <= end {
		begin = min(begin, ranges[last].begin)
		end = max(end, ranges[last].end)
		last++
	}
	if first == last {
		ranges = append(ranges, byteRange{})
		copy(ranges[first+1:], ranges[first:])
	} else {
		copy(ranges[first+1:], ranges[last:])
		ranges = ranges[:len(ranges)-(last-first)+1]
	}
	ranges[first] = byteRange{begin, end}
	return ranges
}

type spoolFileUsage struct {
	allocated, logical, written int64
	available                   bool
}

func (f *spoolFile) usage() spoolFileUsage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.f == nil && !f.present {
		return spoolFileUsage{}
	}
	u := spoolFileUsage{available: true}
	// On filesystems without allocation accounting, leave allocation unknown
	// (zero); never confuse logical sparse size with physical used space.
	if f.f != nil {
		if info, err := f.f.Stat(); err == nil {
			u.logical = info.Size()
		}
		if allocated, err := disklimit.Allocated(f.f); err == nil {
			f.allocated = allocated
		}
	} else {
		// Keep failed removals visible and retryable, even if another process
		// currently prevents reopening the descriptor.
		if info, err := os.Stat(f.path); err == nil {
			u.logical = info.Size()
		} else if errors.Is(err, os.ErrNotExist) {
			return spoolFileUsage{}
		}
		if allocated, err := disklimit.AllocatedBytes(f.path); err == nil {
			f.allocated = allocated
		}
	}
	u.allocated = f.allocated
	for _, r := range f.ranges {
		u.written += r.end - r.begin
	}
	return u
}

func (c *spoolClient) torrent(key string) *spoolTorrent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open[key]
}

func (c *spoolClient) fileUsage(key string) []spoolFileUsage {
	st := c.torrent(key)
	if st == nil {
		return nil
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	if st.closed {
		return nil
	}
	result := make([]spoolFileUsage, len(st.files))
	for i, f := range st.files {
		result[i] = f.usage()
	}
	return result
}

// removeFile is called only after Torrent.Drop has finished storage users.
// Storage itself stays alive for the replacement torrent. A piece straddling
// files becomes incomplete, while bytes in the neighboring file remain intact.
func (st *spoolTorrent) removeFile(index int) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed || index < 0 || index >= len(st.files) {
		return ErrCacheNotFound
	}
	f := st.files[index]
	removed, err := f.removeCacheData()
	if !removed {
		return err
	}
	for _, p := range st.pieces {
		for _, segment := range p.segs {
			if segment.file == f {
				_ = p.MarkNotComplete()
				break
			}
		}
	}
	return err
}

// removeCacheData is used only with the torrent's exclusive storage lock.
// Windows requires closing our descriptor first, but a different process can
// still deny deletion. In that case preserve all accounting and reopen without
// truncation, so the replacement torrent and dashboard retain the original data.
func (f *spoolFile) removeCacheData() (removed bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.f == nil && !f.present {
		return false, ErrCacheNotFound
	}
	var closeErr error
	if f.f != nil {
		if allocated, err := disklimit.Allocated(f.f); err == nil {
			f.allocated = allocated
		}
		closeErr = f.f.Close()
		f.f = nil
	}
	if removeErr := os.Remove(f.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		f.present = true
		var reopenErr error
		f.f, reopenErr = os.OpenFile(f.path, os.O_RDWR, 0)
		return false, errors.Join(closeErr, fmt.Errorf("spool: remove %s: %w", f.path, removeErr), reopenErr)
	}
	f.present = false
	f.high = 0
	f.ranges = nil
	f.allocated = 0
	_ = f.budget.Resize(f.path, 0)
	return true, closeErr
}

func cacheFileExists(st *spoolTorrent, index int) bool {
	if st == nil || index < 0 || index >= len(st.files) {
		return false
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	if st.closed {
		return false
	}
	f := st.files[index]
	if !f.exists() {
		return false
	}
	_, err := os.Stat(f.path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}
