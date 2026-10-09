package catalogapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	catalogsync "github.com/zeoril1/video_viewer/internal/sync"
)

const adminRefreshWorkers = 4

var adminBatchRunning atomic.Bool
var adminRefreshSlots = make(chan struct{}, adminRefreshWorkers)
var adminActive sync.Map

func refreshAdminFilm(ctx context.Context, cfg Config, id string) {
	if _, loaded := adminActive.LoadOrStore(id, true); loaded {
		return
	}
	defer adminActive.Delete(id)
	select {
	case adminRefreshSlots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-adminRefreshSlots }()
	filmCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	adminLog.add("info", "обновление "+id+": начало")
	// IMDb rating is not required by the admin repair workflow.
	err := catalogsync.RefreshFilmData(filmCtx, cfg.DB, cfg.TMDB, nil, id)
	if err == nil {
		var incomplete bool
		incomplete, err = cfg.DB.AdminFilmIncomplete(filmCtx, id)
		if err == nil && incomplete {
			film, _, readErr := cfg.DB.GetByIMDBID(filmCtx, id)
			if readErr != nil {
				err = readErr
			} else {
				err = fmt.Errorf("остались пустые поля: %s", strings.Join(db.MissingAdminFields(film, time.Now().In(time.FixedZone("MSK", 3*3600))), ", "))
			}
		}
	}
	// Persist even if the upstream request used its entire timeout.
	persistCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	if saveErr := cfg.DB.SetAdminRefreshResult(persistCtx, id, reason); saveErr != nil {
		adminLog.add("error", "обновление "+id+": ошибка сохранения результата: "+saveErr.Error())
		return
	}
	if err != nil {
		adminLog.add("error", "обновление "+id+": "+reason+"; повтор через неделю")
	} else {
		adminLog.add("ok", "обновление "+id+": готово")
	}
}

func runAdminWorkers(ctx context.Context, films []db.Film, refresh func(context.Context, string)) {
	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < adminRefreshWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				refresh(ctx, id)
			}
		}()
	}
	for _, film := range films {
		select {
		case jobs <- film.IMDBID:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
}
