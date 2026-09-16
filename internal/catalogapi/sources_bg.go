// Package catalogapi — HTTP API каталога: поиск раздач (Jackett/Torznab) идёт в фоне, результат кэшируется в БД.
package catalogapi

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/imdb"
	"github.com/zeoril1/video_viewer/internal/magnet"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

// sourcesFreshFor — окно свежести кэша источников: повторный поиск на трекере не запускается.
const sourcesFreshFor = 20 * time.Minute

const sourcesJobTimeout = 3 * time.Minute

// sourcesManager ищет источники в фоне (Jackett/Torznab), кэширует результат в БД и отдаёт по запросу из кэша.
type sourcesRepo interface {
	ListSources(context.Context, string) ([]db.Source, error)
	GetByIMDBID(context.Context, string) (db.Film, bool, error)
	SaveFilm(context.Context, imdb.Film) error
	UpdateSeasons(context.Context, string, int) error
	SaveSources(context.Context, string, []db.Source, ...bool) (int, int, int, error)
}

type sourcesManager struct {
	db     sourcesRepo
	magnet magnet.Provider // Jackett (Torznab); может быть nil
	imdb   *imdb.Client
	tmdb   *tmdb.Client    // для числа сезонов сериала (может быть nil)
	ctx    context.Context // родительский контекст приложения (для фоновых заданий)

	mu         sync.Mutex
	running    map[string]bool
	lastDone   map[string]time.Time
	semaphore  chan struct{}
	retryAfter map[string]time.Time
}

func newSourcesManager(cfg Config) *sourcesManager {
	parent := cfg.Context
	if parent == nil {
		parent = context.Background()
	}
	var repo sourcesRepo
	if cfg.DB != nil {
		repo = cfg.DB
	}
	return &sourcesManager{
		db:         repo,
		magnet:     cfg.Magnet,
		imdb:       cfg.IMDB,
		tmdb:       cfg.TMDB,
		ctx:        parent,
		running:    map[string]bool{},
		retryAfter: map[string]time.Time{},
		lastDone:   map[string]time.Time{},
		semaphore:  make(chan struct{}, 3),
	}
}

// sourcesForView отдаёт кэш источников и признак «готово»; поиск на трекере запускается в фоне, запрос не блокируется.
func (m *sourcesManager) sourcesForView(ctx context.Context, filmID string) ([]db.Source, bool) {
	srcs := m.listSources(ctx, filmID)

	m.mu.Lock()
	running := m.running[filmID]
	lastDone, haveDone := m.lastDone[filmID]
	retryAfter := m.retryAfter[filmID]
	m.mu.Unlock()

	if running {
		return srcs, false
	}
	// ВАЖНО: не чаще одного поиска в окно sourcesFreshFor — раньше поиск шёл на каждый запрос,
	// фронтенд поллил /sources и Jackett зацикливался, а статус так и не становился "ready".
	if haveDone && time.Since(lastDone) < sourcesFreshFor {
		return srcs, true
	}
	if !time.Now().Before(retryAfter) {
		m.refresh(filmID)
	}
	return srcs, false
}

func (m *sourcesManager) listSources(ctx context.Context, filmID string) []db.Source {
	if m.db == nil {
		return nil
	}
	if list, err := m.db.ListSources(ctx, filmID); err == nil {
		return list
	}
	return nil
}

func (m *sourcesManager) refresh(filmID string) {
	m.mu.Lock()
	if m.running[filmID] {
		m.mu.Unlock()
		return
	}
	m.pruneLocked()
	m.running[filmID] = true
	m.mu.Unlock()

	go func() {
		var jobErr error
		defer func() {
			m.mu.Lock()
			delete(m.running, filmID)
			if jobErr == nil {
				m.lastDone[filmID] = time.Now()
				delete(m.retryAfter, filmID)
			} else {
				if m.retryAfter == nil {
					m.retryAfter = map[string]time.Time{}
				}
				m.retryAfter[filmID] = time.Now().Add(30 * time.Second)
			}
			m.mu.Unlock()
		}()

		select {
		case m.semaphore <- struct{}{}:
		case <-m.ctx.Done():
			jobErr = m.ctx.Err()
			return
		}
		defer func() { <-m.semaphore }()
		ctx, cancel := context.WithTimeout(m.ctx, sourcesJobTimeout)
		defer cancel()
		log.Printf("sources: background %s: start", filmID)
		jobErr = m.run(ctx, filmID)
		if err := jobErr; err != nil {
			log.Printf("sources: background %s: %v", filmID, err)
		} else {
			log.Printf("sources: background %s: done", filmID)
		}
	}()
}

// pruneLocked чистит карты running/lastDone, чтобы они не росли бесконечно; вызывается под m.mu.
func (m *sourcesManager) pruneLocked() {
	for id, retry := range m.retryAfter {
		if time.Now().After(retry) {
			delete(m.retryAfter, id)
		}
	}

	if len(m.lastDone) <= 512 {
		return
	}
	cutoff := time.Now().Add(-sourcesFreshFor * 4)
	for k, t := range m.lastDone {
		if t.Before(cutoff) {
			delete(m.lastDone, k)
		}
	}
}

// run ищет раздачи через Jackett и сохраняет их в БД (фоновый контекст, не зависит от HTTP-запроса).
func (m *sourcesManager) run(ctx context.Context, filmID string) error {
	if m.db == nil || m.magnet == nil {
		return nil
	}

	film, ok, err := m.db.GetByIMDBID(ctx, filmID)
	if err != nil {
		return err
	}
	if !ok {
		if m.imdb == nil {
			return nil
		}
		f, err := m.imdb.GetByID(ctx, filmID)
		if err != nil {
			return err
		}
		if err := m.db.SaveFilm(ctx, f); err != nil {
			return fmt.Errorf("save sources %s: %w", filmID, err)
		}
		film = db.FromIMDB(f)
	}

	// Сезоны TMDB (канон) загружаем до поиска по трекерам; IMDb — только запасной вариант.
	structure := m.seasonStructure(ctx, film)
	previousSeasons := film.Seasons
	if len(structure) > 0 {
		film.Seasons = structure[len(structure)-1].Number
	} else if isSeriesKind(film.Kind) && film.Seasons <= 0 && m.imdb != nil {
		if n, err := m.imdb.Seasons(ctx, filmID); err == nil && n > 0 {
			film.Seasons = n
		}
	}
	if film.Seasons > 0 && film.Seasons != previousSeasons {
		if err := m.db.UpdateSeasons(ctx, filmID, film.Seasons); err != nil {
			log.Printf("sources: update seasons %s: %v", filmID, err)
		}
	}

	// У сериалов один общий запрос по названию, недостающие сезоны добираются точечно; есть запасной проход по исходному названию.
	isSeries := isSeriesKind(film.Kind)
	matcher := newTitleMatcher(trackerTitles(film))
	var searchErr error
	search := func(ctx context.Context, q string, limit int) ([]magnet.Result, error) {
		results, err := m.magnet.Search(ctx, q, limit)
		if err != nil {
			searchErr = err
		}
		return results, err
	}
	searchBy := func(title string) []sourceItem {
		queries := sourceQueriesFor(film, title)
		if len(queries) == 0 {
			return nil
		}
		log.Printf("sources: %s: запросов %d (kind=%s seasons=%d, название %q)",
			filmID, len(queries), film.Kind, film.Seasons, title)
		items := matcher.filter(filmID, runQueries(ctx, search, filmID, queries, m.magnet.Name(), isSeries))

		// Трекеры держат сезон отдельной раздачей: недостающие сезоны добираем запросами «… N сезон», иначе в UI дыры.
		if isSeries {
			if extra := missingSeasonQueriesFor(film, title, items, queries); len(extra) > 0 {
				log.Printf("sources: %s: добираю сезоны: запросов %d", filmID, len(extra))
				more := matcher.filter(filmID, runQueries(ctx, search, filmID, extra, m.magnet.Name(), true))
				log.Printf("sources: %s: добор сезонов дал %d вариантов", filmID, len(more))
				items = mergeSourceItems(items, more)
			}
		}
		return items
	}

	items := searchBy(trackerTitle(film))
	// У зарубежных сериалов раздачи названы оригиналом — пробуем исходное название.
	if len(items) == 0 {
		if alt := trackerTitleAlt(film); alt != "" {
			log.Printf("sources: %s: по названию %q пусто, пробую %q", filmID, trackerTitle(film), alt)
			items = searchBy(alt)
		}
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(items) == 0 {
		if searchErr != nil {
			return searchErr
		}
		// Пустая выдача — не ошибка HTTP.
		log.Printf("sources: %s: поиск пуст (0 вариантов)", filmID)
		return nil
	}

	// Сезоны трекера приводим к нумерации TMDB по году: у трекеров дробная нарезка (S14 = TMDB-сезон 10), иначе в UI появлялись лишние сезоны.
	if isSeriesKind(film.Kind) {
		if st := structure; len(st) > 0 {
			for i := range items {
				if items[i].Season <= 0 {
					continue // полный сборник остаётся сборником
				}
				if s := tmdb.SeasonByYear(st, magnet.TitleYear(items[i].Title)); s > 0 {
					items[i].Season = s
				}
			}
		}
	}

	sortSourceItems(items, isSeriesKind(film.Kind))

	srcs := make([]db.Source, 0, len(items))
	for _, it := range items {
		srcs = append(srcs, db.Source{
			FilmID: filmID, Magnet: it.Magnet, Title: it.Title,
			Size: it.Size, Seeds: it.Seeds, Quality: it.Quality,
			Audio: it.Audio, Season: it.Season, Provider: it.Provider,
		})
	}
	if added, updated, removed, err := m.db.SaveSources(ctx, filmID, srcs, searchErr != nil); err != nil {
		return fmt.Errorf("save sources %s: %w", filmID, err)
	} else if added+updated+removed > 0 {
		log.Printf("sources: save %s: +%d ~%d -%d", filmID, added, updated, removed)
	}
	return searchErr
}

// runQueries ищет по списку запросов и собирает варианты с дедупом; isSeries включает правила отбора сериалов (нужны и раздачи без сидов, см. ниже).
func runQueries(ctx context.Context, fn func(c context.Context, q string, limit int) ([]magnet.Result, error), filmID string, queries []sourceQuery, providerName string, isSeries bool) []sourceItem {
	seen := map[string]bool{}
	seenTS := map[string]bool{}
	items := make([]sourceItem, 0, 16)
	for _, sq := range queries {
		// У сериала запросы ищем ШИРЕ (у сезона бывают разные студии озвучки), а общий запрос по названию
		// должен отдать ВСЕ сезоны: RuTracker сортирует по дате, и при лимите 30 свежие сезоны вытесняли старые.
		limit := 12
		if isSeries {
			if sq.seasonHint > 0 {
				limit = 20 // запрос по конкретному сезону
			} else {
				limit = seriesAllSeasonsLimit // общий запрос по названию (все сезоны)
			}
		}
		res, err := fn(ctx, sq.q, limit)
		if err != nil {
			log.Printf("sources: %s поиск %q (%s): %v", filmID, sq.q, providerName, err)
		}
		log.Printf("sources: %s поиск %q (%s): найдено %d", filmID, sq.q, providerName, len(res))
		for _, s := range res {
			tsKey := strings.ToLower(strings.TrimSpace(s.Title)) + "\x00" + s.Size
			if s.Magnet == "" || seen[s.Magnet] || seenTS[tsKey] {
				continue
			}
			seen[s.Magnet] = true
			seenTS[tsKey] = true
			quality, audio, season := magnet.ParseTitle(s.Title)
			// Сезон из заголовка (до подстановки подсказки) — отличаем раздачу нужного сезона от мусора широкого запроса.
			namedSeason := season > 0
			// Подсказка сезона — ТОЛЬКО если сезон не распознан из названия: полный сборник («S1-9»)
			// парсится как сезон 0 и не должен привязываться к одному сезону.
			if season <= 0 && sq.seasonHint > 0 && !magnet.IsFullCollection(s.Title) {
				season = sq.seasonHint
			}
			// Раздачи без сидов: у фильма отбрасываем (мёртвую раздачу не посмотреть), у сериала оставляем
			// при названной студии озвучки, полном сборнике или явном сезоне — старые сезоны длинных сериалов
			// часто лежат только без сидов. Явный сезон/студия отсекают мусор широкой выдачи.
			if s.Seeds <= 0 && !(isSeries && (namedSeason || magnet.NamesVoiceStudio(s.Title) || magnet.IsFullCollection(s.Title))) {
				continue
			}
			items = append(items, sourceItem{
				Title: s.Title, Size: s.Size, Seeds: s.Seeds, Magnet: s.Magnet,
				Quality: quality, Audio: audio, Season: season, Provider: providerName,
			})
		}
		// Все запросы проходим целиком (лишнее отсекает дедуп); обрыв только на maxSourceItems:
		// порог должен вмещать последние индексары (RuTracker со старыми сезонами идёт после rutor).
		if len(items) >= maxSourceItems {
			break
		}
	}
	return items
}

// seasonProbeLimit — сколько сезонов пробовать отдельными запросами, если число сезонов неизвестно (Wikidata/TMDB не ответили).
const seasonProbeLimit = 8

// seriesAllSeasonsLimit — лимит общего запроса по названию сериала: должен вмещать ВСЕ раздачи (RuTracker сортирует по дате, при 30 терялись старые сезоны).
const seriesAllSeasonsLimit = 100

// maxSourceItems — предохранитель от аномальной выдачи: порог должен вмещать хотя бы 2–3 индексара целиком, иначе последние (RuTracker) отрезаются.
const maxSourceItems = 400

// seasonStructure возвращает структуру сезонов из TMDB (nil — нет клиента или tmdb_id); нужна для приведения трекерных сезонов к нумерации TMDB.
func (m *sourcesManager) seasonStructure(ctx context.Context, film db.Film) []tmdb.SeasonInfo {
	if m.tmdb == nil {
		return nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(film.TMDBID), 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	if repo, ok := m.db.(interface {
		SeriesSeasons(context.Context, int64, func(context.Context, int64) ([]tmdb.SeasonInfo, error)) ([]tmdb.SeasonInfo, error)
	}); ok {
		seasons, err := repo.SeriesSeasons(ctx, id, m.tmdb.SeasonStructure)
		if err != nil {
			log.Printf("seasons: refresh %d: %v", id, err)
		}
		return seasons
	}
	return m.tmdb.Structure(ctx, id)
}

// missingSeasonQueries добирает сезоны, которых нет в выдаче (трекеры держат сезон отдельной раздачей).
// Уже запрошенные сезоны не повторяем, число новых запросов ограничено maxSeasonQueries.
func missingSeasonQueries(f db.Film, items []sourceItem, used []sourceQuery) []sourceQuery {
	return missingSeasonQueriesFor(f, trackerTitle(f), items, used)
}

// missingSeasonQueriesFor — как missingSeasonQueries, но по заданному названию (второй проход по исходному).
func missingSeasonQueriesFor(f db.Film, title string, items []sourceItem, used []sourceQuery) []sourceQuery {
	have := map[int]bool{}
	top := f.Seasons
	for _, it := range items {
		if it.Season > 0 {
			have[it.Season] = true
			if it.Season > top {
				top = it.Season
			}
		}
		// Полный сборник («S1-14») даёт верхнюю границу: сериал может быть длиннее, чем считают TMDB/Wikidata.
		if _, to := magnet.SeasonRange(it.Title); to > top {
			top = to
		}
	}
	if top <= 0 {
		top = seasonProbeLimit
	}
	asked := map[int]bool{}
	for _, sq := range used {
		if sq.seasonHint > 0 {
			asked[sq.seasonHint] = true
		}
	}
	var out []sourceQuery
	for s := 1; s <= top && len(out) < maxSeasonQueries; s++ {
		if have[s] || asked[s] {
			continue
		}
		q := fmt.Sprintf("%s %d сезон", title, s)
		if s == 1 && f.Year > 0 {
			q = fmt.Sprintf("%s %d", title, f.Year)
		}
		out = append(out, sourceQuery{q: q, seasonHint: s})
	}
	return out
}

// mergeSourceItems объединяет проходы поиска, убирая дубли по магнет-ссылке (один релиз приходит на разные запросы).
func mergeSourceItems(base, extra []sourceItem) []sourceItem {
	seen := make(map[string]bool, len(base)+len(extra))
	for _, it := range base {
		seen[it.Magnet] = true
	}
	for _, it := range extra {
		if it.Magnet != "" && seen[it.Magnet] {
			continue
		}
		seen[it.Magnet] = true
		base = append(base, it)
	}
	return base
}

// sortSourceItems ранжирует: озвучка > качество > сиды; у сериалов — по сезону, внутри сезона по рангу, сборники без сезона последними.
func sortSourceItems(items []sourceItem, isSeries bool) {
	sort.SliceStable(items, func(i, j int) bool {
		if isSeries {
			si, sj := items[i].Season, items[j].Season
			si = normSeason(si)
			sj = normSeason(sj)
			if si != sj {
				return si < sj
			}
		}
		return srcRank(items[i].Quality, items[i].Audio, items[i].Seeds) >
			srcRank(items[j].Quality, items[j].Audio, items[j].Seeds)
	})
}

// sourceItemsFromDB превращает источники из БД в варианты с тем же ранжированием, что после живого поиска.
func sourceItemsFromDB(srcs []db.Source, film db.Film) []sourceItem {
	items := make([]sourceItem, 0, len(srcs))
	for _, s := range srcs {
		if s.Magnet == "" {
			continue
		}
		items = append(items, sourceItem{
			Title: s.Title, Size: s.Size, Seeds: s.Seeds, Magnet: s.Magnet,
			Quality: s.Quality, Audio: s.Audio, Season: s.Season, Provider: s.Provider,
		})
	}
	sortSourceItems(items, isSeriesKind(film.Kind))
	return items
}
