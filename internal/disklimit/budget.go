// Package disklimit bounds logical file sizes, including sparse holes.
package disklimit

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var ErrFull = errors.New("server disk limit reached")

type Budget struct {
	mu                 sync.Mutex
	dir                string
	max, minimum, used int64
	sizes              map[string]int64
	checked            time.Time
	free               uint64
}

func New(dir string, maximum, minimum int64) *Budget {
	b := &Budget{dir: dir, max: maximum, minimum: minimum, sizes: make(map[string]int64)}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				b.sizes[filepath.Join(dir, e.Name())] = info.Size()
				b.used += info.Size()
			}
		}
	}
	return b
}

// Resize reserves before a write, serializing concurrent growth across files.
func (b *Budget) Resize(path string, size int64) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delta := size - b.sizes[path]
	if delta > 0 {
		if b.max > 0 && b.used+delta > b.max {
			return ErrFull
		}
		if b.minimum > 0 {
			if time.Since(b.checked) > time.Second {
				free, err := Free(b.dir)
				if err != nil {
					return err
				}
				b.free = free
				b.checked = time.Now()
			}
			if b.free < uint64(b.minimum)+uint64(delta) {
				return ErrFull
			}
			b.free -= uint64(delta)
		}
	}
	b.used += delta
	if size == 0 {
		delete(b.sizes, path)
	} else {
		b.sizes[path] = size
	}
	return nil
}
func (b *Budget) Pressure() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max > 0 && b.used >= b.max*9/10 {
		return true
	}
	if b.minimum > 0 {
		f, err := Free(b.dir)
		return err != nil || f < uint64(b.minimum)+(64<<20)
	}
	return false
}
