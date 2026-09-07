// Package sync — периодическая синхронизация чартов IMDb с БД.
package sync

import (
	"context"
	"log"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/imdb"
)

// DefaultInterval — периодичность синхронизации по умолчанию.
const DefaultInterval = 24 * time.Hour

// Syncer периодически загружает чарты IMDb («топ-250» и «популярные»)
// и сохраняет новые фильмы в БД.
type Syncer struct {
	db       *db.Repo
	imdb     *imdb.Client
	interval time.Duration
}

// New создаёт синхронизатор.
func New(db *db.Repo, im *imdb.Client, interval time.Duration) *Syncer {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Syncer{db: db, imdb: im, interval: interval}
}

// SyncOnce выполняет один цикл синхронизации: оба чарта, с резервом
// на отсутствие источника для какого-либо из них.
func (s *Syncer) SyncOnce(ctx context.Context) {
	charts := []imdb.ChartKind{imdb.ChartTop250, imdb.ChartPopular}
	log.Printf("sync: старт цикла (%d чартов)", len(charts))

	// Чарты качаются независимо — запускаем параллельно.
	type result struct {
		kind  imdb.ChartKind
		films []imdb.Film
		err   error
	}

	results := make(chan result, len(charts))
	for _, kind := range charts {
		go func(k imdb.ChartKind) {
			films, err := s.imdb.FetchChart(ctx, k)
			results <- result{kind: k, films: films, err: err}
		}(kind)
	}

	for range charts {
		res := <-results
		if res.err != nil {
			log.Printf("sync %s: %v", res.kind, res.err)
			continue
		}
		inserted, err := s.db.UpsertFilms(ctx, res.films, res.kind)
		if err != nil {
			log.Printf("sync %s: store: %v", res.kind, err)
			continue
		}
		log.Printf("sync %s: %d films (new: %d)", res.kind, len(res.films), inserted)
	}
}

// Run запускает синхронизацию сразу при старте и далее каждые interval.
// Блокирующий; остановить можно через отмену ctx.
func (s *Syncer) Run(ctx context.Context) {
	s.SyncOnce(ctx)

	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SyncOnce(ctx)
		}
	}
}
