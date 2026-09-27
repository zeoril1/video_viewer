package disklimit

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentReservationsAndRelease(t *testing.T) {
	b := New(t.TempDir(), 100, 0)
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if b.Resize(fmt.Sprint(i), 10) == nil {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 10 || b.used != 100 {
		t.Fatalf("quota exceeded: %d / %d", accepted.Load(), b.used)
	}
	for key := range b.sizes {
		if err := b.Resize(key, 0); err != nil {
			t.Fatal(err)
		}
		break
	}
	if err := b.Resize("replacement", 10); err != nil {
		t.Fatal("space not reclaimed", err)
	}
	if err := b.Resize("replacement", 11); !errors.Is(err, ErrFull) {
		t.Fatal("growth accepted", err)
	}
	if err := b.Resize("replacement", 10); err != nil {
		t.Fatal("overwrite rejected", err)
	}
}
func TestDiskReserve(t *testing.T) {
	b := New(t.TempDir(), 100, 1<<62)
	if err := b.Resize("file", 1); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
}
