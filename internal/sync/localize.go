package sync

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/imdb"
)

// Localizer фоново заполняет в БД русские название и описание фильмов
// каталога:
//
//  1. названия — пакетно через Wikidata Query Service (SPARQL), это быстро
//     и не упирается в rate-limit поштучного API;
//  2. описания — из статей русской Википедии (по сохранённым заголовкам),
//     вежливо, с учётом rate-limit.
//
// Все результаты сохраняются в PostgreSQL — БД остаётся источником
// истины для обоих языков.
type Localizer struct {
	db          *db.Repo
	imdb        *imdb.Client
	titleBatch  int // сколько фильмов за один SPARQL-запрос
	plotBatch   int // сколько фильмов за проход описаний
	plotWorkers int // параллельные запросы к ru.wikipedia.org
	interval    time.Duration
}

// NewLocalizer создаёт фоновый локализатор.
func NewLocalizer(db *db.Repo, im *imdb.Client, titleBatch, plotBatch, plotWorkers int, interval time.Duration) *Localizer {
	if titleBatch <= 0 {
		titleBatch = 200
	}
	if plotBatch <= 0 {
		plotBatch = 40
	}
	if plotWorkers <= 0 {
		plotWorkers = 3
	}
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	return &Localizer{db: db, imdb: im, titleBatch: titleBatch, plotBatch: plotBatch, plotWorkers: plotWorkers, interval: interval}
}

// Run запускает локализацию сразу при старте и далее каждые interval.
// Блокирующий; остановить можно отменой ctx.
func (l *Localizer) Run(ctx context.Context) {
	l.LocalizeOnce(ctx)

	t := time.NewTicker(l.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.LocalizeOnce(ctx)
		}
	}
}

// LocalizeOnce выполняет один проход локализации: сначала названия, затем описания.
func (l *Localizer) LocalizeOnce(ctx context.Context) {
	l.localizeTitles(ctx)
	l.localizePlots(ctx)
}

// localizeTitles пакетно получает русские названия для всех фильмов,
// у которых их нет, и сохраняет в БД.
func (l *Localizer) localizeTitles(ctx context.Context) {
	for {
		ids, err := l.db.FilmsMissingTitleRU(ctx, l.titleBatch)
		if err != nil {
			log.Printf("localize: titles: list: %v", err)
			return
		}
		if len(ids) == 0 {
			return
		}

		res, err := l.imdb.LocalizeTitles(ctx, ids)
		if err != nil {
			log.Printf("localize: titles: %v", err)
			return
		}

		n := 0
		for _, id := range ids {
			loc, ok := res[id]
			if !ok || loc.TitleRU == "" {
				continue // нет русской метки в Wikidata
			}
			if err := l.db.SetTitleLocalization(ctx, id, loc.TitleRU, loc.RuWikiTitle); err != nil {
				log.Printf("localize: titles: update %s: %v", id, err)
				continue
			}
			n++
		}
		if n > 0 {
			log.Printf("localize: titles: %d films", n)
		}
		if len(ids) < l.titleBatch {
			return
		}
	}
}

// localizePlots загружает русские описания из статей ru-wiki (по
// сохранённым заголовкам), вежливо ограничивая параллельность.
func (l *Localizer) localizePlots(ctx context.Context) {
	for {
		films, err := l.db.FilmsWithRuWikiNoPlot(ctx, l.plotBatch)
		if err != nil {
			log.Printf("localize: plots: list: %v", err)
			return
		}
		if len(films) == 0 {
			return
		}

		l.fetchPlots(ctx, films)
		if len(films) < l.plotBatch {
			return
		}
	}
}

func (l *Localizer) fetchPlots(ctx context.Context, films []db.RuWikiFilm) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, l.plotWorkers)

	for _, f := range films {
		wg.Add(1)
		go func(f db.RuWikiFilm) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			plot, err := l.imdb.PlotFromRuWiki(ctx, f.RuWikiTitle)
			if err != nil {
				// Rate-limit или нет статьи — попробуем в следующий проход.
				return
			}
			if err := l.db.SetPlotRU(ctx, f.IMDBID, plot); err != nil {
				log.Printf("localize: plots: update %s: %v", f.IMDBID, err)
			}
		}(f)
	}
	wg.Wait()
}
