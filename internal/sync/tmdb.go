// Периодическая синхронизация чартов TMDB с БД (заменяет чарты Кинопоиска, API которого под токеном с ограничениями).
package sync

import (
	"context"
	"log"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

// TMDBChartSyncer периодически загружает чарты TMDB («топ-250» и «популярные») и сохраняет их в БД, без дублей с IMDb.
type TMDBChartSyncer struct {
	db       *db.Repo
	tm       *tmdb.Client
	interval time.Duration
}

// NewTMDBChart создаёт синхронизатор чартов TMDB.
func NewTMDBChart(db *db.Repo, tm *tmdb.Client, interval time.Duration) *TMDBChartSyncer {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &TMDBChartSyncer{db: db, tm: tm, interval: interval}
}

// chartJob — один чарт TMDB для синхронизации: top_rated/popular и тип контента (фильмы/сериалы).
// Ранги фильмов и сериалов лежат в одних колонках, но чистятся по своему типу (см. db.ClearTMDBRank).
type chartJob struct {
	kind   tmdb.ChartKind
	series bool
}

// SyncOnce выполняет один цикл синхронизации: чарты фильмов (top_rated и popular через /movie/*) и сериалов (через /tv/*).
func (s *TMDBChartSyncer) SyncOnce(ctx context.Context) {
	charts := []chartJob{
		{kind: tmdb.ChartTopRated},
		{kind: tmdb.ChartPopular},
		{kind: tmdb.ChartTopRated, series: true},
		{kind: tmdb.ChartPopular, series: true},
	}
	log.Printf("sync tmdb: старт цикла (%d чартов)", len(charts))

	type result struct {
		job   chartJob
		films []tmdb.Film
		err   error
	}

	results := make(chan result, len(charts))
	for _, job := range charts {
		go func(j chartJob) {
			var (
				films []tmdb.Film
				err   error
			)
			switch {
			case j.series && j.kind == tmdb.ChartPopular:
				films, err = s.tm.TVPopular(ctx, 250)
			case j.series:
				films, err = s.tm.TVTopRated(ctx, 250)
			case j.kind == tmdb.ChartPopular:
				films, err = s.tm.Popular(ctx, 250)
			default:
				films, err = s.tm.TopRated(ctx, 250)
			}
			results <- result{job: j, films: films, err: err}
		}(job)
	}

	for range charts {
		res := <-results
		if res.err != nil {
			log.Printf("sync tmdb %s %s: %v", res.job.kind, mediaName(res.job.series), res.err)
			continue
		}
		// Сбрасываем старые позиции (только своего типа контента): сошедшие с чарта записи
		// не должны сохранять устаревшие ранги.
		if err := s.db.ClearTMDBRank(ctx, res.job.kind, res.job.series); err != nil {
			log.Printf("sync tmdb %s %s: clear ranks: %v", res.job.kind, mediaName(res.job.series), err)
		}
		inserted, err := s.db.UpsertTMDBFilms(ctx, res.films, res.job.kind)
		if err != nil {
			log.Printf("sync tmdb %s %s: store: %v", res.job.kind, mediaName(res.job.series), err)
			continue
		}
		log.Printf("sync tmdb %s %s: %d записей (new: %d)", res.job.kind, mediaName(res.job.series), len(res.films), inserted)
	}
}

// mediaName — имя типа контента для логов.
func mediaName(series bool) string {
	if series {
		return "сериалы"
	}
	return "фильмы"
}

// Run запускает синхронизацию сразу при старте и далее каждые interval.
func (s *TMDBChartSyncer) Run(ctx context.Context) {
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
