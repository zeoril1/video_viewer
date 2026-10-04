package catalogapi

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
)

func TestAdminWorkersParallelBoundedAndComplete(t *testing.T) {
	films := make([]db.Film, 605)
	for i := range films {
		films[i].IMDBID = fmt.Sprint(i)
	}
	started := make(chan struct{}, adminRefreshWorkers)
	release := make(chan struct{})
	done := make(chan struct{})
	var mu sync.Mutex
	active, maxActive := 0, 0
	seen := map[string]int{}
	go func() {
		runAdminWorkers(context.Background(), films, func(_ context.Context, id string) {
			mu.Lock()
			active++
			seen[id]++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			mu.Lock()
			active--
			mu.Unlock()
		})
		close(done)
	}()
	for i := 0; i < adminRefreshWorkers; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("workers did not run concurrently")
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("queue did not finish")
	}
	if maxActive != adminRefreshWorkers || len(seen) != len(films) {
		t.Fatalf("peak=%d processed=%d", maxActive, len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("%s processed %d times", id, count)
		}
	}
}

func TestAdminWorkersRespectCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runAdminWorkers(ctx, []db.Film{{IMDBID: "tt1"}}, func(context.Context, string) { t.Error("cancelled job ran") })
}
