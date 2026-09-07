// Фоновый поиск источников (раздач) через Jackett (Torznab) — чтобы
// медленный трекер не блокировал HTTP-запрос и пользователя. Результаты
// кэшируются в БД (таблица sources) и отдаются из кэша; поиск обновляется
// в фоне.
package catalogapi

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/imdb"
	"github.com/zeoril1/video_viewer/internal/magnet"
)

// sourcesFreshFor — окно, в течение которого кэшированные источники
// считаются свежими (повторный поиск на трекере не запускается).
const sourcesFreshFor = 20 * time.Minute

// sourcesJobTimeout — максимальное время фонового поиска источников.
const sourcesJobTimeout = 3 * time.Minute

// sourcesManager выполняет поиск источников в фоне, кэширует результат
// в БД и отдаёт его по запросу. Пользователь видит карточку/плеер сразу,
// а варианты подгружаются по мере готовности (фронтенд опрашивает статус).
// Поиск идёт через единый провайдер — Jackett (Torznab), в котором
// настроены нужные трекеры (в Jackett: RuTracker и т.п.).
type sourcesManager struct {
	db     *db.Repo
	magnet magnet.Provider // Jackett (Torznab); может быть nil
	imdb   *imdb.Client
	ctx    context.Context // родительский контекст приложения (для фоновых заданий)

	mu       sync.Mutex
	running  map[string]bool      // идёт ли фоновый поиск для фильма
	lastDone map[string]time.Time // когда последний раз поиск завершился
	// semaphore ограничивает число одновременных фоновых поисков.
	semaphore chan struct{}
}

func newSourcesManager(cfg Config) *sourcesManager {
	parent := cfg.Context
	if parent == nil {
		parent = context.Background()
	}
	return &sourcesManager{
		db:        cfg.DB,
		magnet:    cfg.Magnet,
		imdb:      cfg.IMDB,
		ctx:       parent,
		running:   map[string]bool{},
		lastDone:  map[string]time.Time{},
		semaphore: make(chan struct{}, 3),
	}
}

// sourcesForView возвращает источники фильма для фронтенда и признак
// «готово» (ready=true — есть свежий кэш с переводами, повторный поиск не
// нужен). Запрос НЕ блокируется: поиск на трекере (Jackett) запускается
// сразу в фоне, а текущий кэш отдаётся немедленно — фронтенд показывает
// результаты по мере готовности (status=searching → ready).
func (m *sourcesManager) sourcesForView(ctx context.Context, filmID string) ([]db.Source, bool) {
	srcs := m.listSources(ctx, filmID)

	m.mu.Lock()
	running := m.running[filmID]
	lastDone, haveDone := m.lastDone[filmID]
	m.mu.Unlock()

	if running {
		return srcs, false // уже идёт фоновый поиск — отдаём текущий кэш
	}
	// Недавно завершённый поиск (в окне свежести) считаем готовым и НЕ
	// запускаем новый. ВАЖНО: раньше повторный поиск запускался на каждый
	// запрос, если в кэше не было распознанной озвучки (hasKnownAudio=false),
	// а статус при этом никогда не становился "ready" — фронтенд поллил
	// /sources каждые несколько секунд, и Jackett долбился бесконечным
	// циклом запросов на одну карточку. Теперь на карточку — один поиск
	// в окно sourcesFreshFor; переводы по названиям добираются повторным
	// поиском только после истечения окна свежести.
	if haveDone && time.Since(lastDone) < sourcesFreshFor {
		return srcs, true
	}
	m.refresh(filmID)
	return srcs, false
}

// listSources читает источники фильма из БД (без запуска поиска).

// listSources читает источники фильма из БД (без запуска поиска).
func (m *sourcesManager) listSources(ctx context.Context, filmID string) []db.Source {
	if m.db == nil {
		return nil
	}
	if list, err := m.db.ListSources(ctx, filmID); err == nil {
		return list
	}
	return nil
}

// refresh запускает фоновый поиск источников, если для фильма он ещё не идёт.
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
		m.semaphore <- struct{}{} // ждём свободное место (лимит параллельных поисков)
		defer func() { <-m.semaphore }()
		defer func() {
			m.mu.Lock()
			delete(m.running, filmID)
			m.lastDone[filmID] = time.Now()
			m.mu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(m.ctx, sourcesJobTimeout)
		defer cancel()
		log.Printf("sources: background %s: start", filmID)
		if err := m.run(ctx, filmID); err != nil {
			log.Printf("sources: background %s: %v", filmID, err)
		} else {
			log.Printf("sources: background %s: done", filmID)
		}
	}()
}

// pruneLocked удаляет устаревшие записи о завершённых поисках, чтобы карты
// running/lastDone не росли бесконечно с числом просмотренных фильмов.
// Вызывается с захваченным m.mu.
func (m *sourcesManager) pruneLocked() {
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

// run выполняет поиск раздач через Jackett и сохраняет результат в БД.
// Работает с фоновым контекстом (не зависит от HTTP-запроса).
func (m *sourcesManager) run(ctx context.Context, filmID string) error {
	if m.db == nil || m.magnet == nil {
		return nil
	}

	// Фильм из БД; если его нет — догружаем из IMDb.
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
			log.Printf("sources: save %s: %v", filmID, err)
		}
		film = db.FromIMDB(f)
	}

	// Дозаполняем число сезонов сериала (из Wikidata через IMDb-клиент),
	// если запись сохранена по источнику без сезонов.
	if isSeriesKind(film.Kind) && film.Seasons <= 0 && m.imdb != nil {
		if n, err := m.imdb.Seasons(ctx, filmID); err == nil && n > 0 {
			film.Seasons = n
			if err := m.db.UpdateSeasons(ctx, filmID, n); err != nil {
				log.Printf("sources: update seasons %s: %v", filmID, err)
			}
		}
	}

	// Запросы к трекеру (у сериалов — по сезонам).
	queries := sourceQueries(film)
	log.Printf("sources: %s: запросов %d (kind=%s seasons=%d)", filmID, len(queries), film.Kind, film.Seasons)

	items := runQueries(ctx, m.magnet.Search, filmID, queries, m.magnet.Name())
	if len(items) == 0 {
		// Поиск не дал результатов — это не ошибка HTTP, просто пусто.
		log.Printf("sources: %s: поиск пуст (0 вариантов)", filmID)
		return nil
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
	if added, updated, removed, err := m.db.SaveSources(ctx, filmID, srcs); err != nil {
		log.Printf("sources: save %s: %v", filmID, err)
	} else if added+updated+removed > 0 {
		log.Printf("sources: save %s: +%d ~%d -%d", filmID, added, updated, removed)
	}
	return nil
}

// runQueries выполняет поиск по списку запросов одной поисковой функцией
// и собирает варианты (с дедупом внутри провайдера). providerName
// записывается в каждый вариант.
func runQueries(ctx context.Context, fn func(c context.Context, q string, limit int) ([]magnet.Result, error), filmID string, queries []sourceQuery, providerName string) []sourceItem {
	seen := map[string]bool{}
	seenTS := map[string]bool{}
	items := make([]sourceItem, 0, 16)
	for i, sq := range queries {
		// Для сериала запросов несколько (по сезону) — ищем ШИРЕ, чтобы не
		// терять раздачи: у одного сезона бывают разные студии озвучки, в
		// т.ч. богатые (например, «[S01] … LostFilm+NewStudio+BaibaKo+Eng»).
		limit := 8
		if i == 0 {
			limit = 12 // основной запрос — побольше вариантов
		}
		res, err := fn(ctx, sq.q, limit)
		if err != nil {
			log.Printf("sources: %s поиск %q (%s): %v", filmID, sq.q, providerName, err)
			continue
		}
		log.Printf("sources: %s поиск %q (%s): найдено %d", filmID, sq.q, providerName, len(res))
		for _, s := range res {
			tsKey := strings.ToLower(strings.TrimSpace(s.Title)) + "\x00" + s.Size
			if s.Seeds <= 0 || s.Magnet == "" || seen[s.Magnet] || seenTS[tsKey] {
				continue
			}
			seen[s.Magnet] = true
			seenTS[tsKey] = true
			quality, audio, season := magnet.ParseTitle(s.Title)
			// Подсказка искомого сезона применяется ТОЛЬКО если сезон не
			// распознался из названия раздачи. Полный сборник («S1-9»,
			// «сезоны 1-5») парсится как сезон 0 и остаётся им — иначе
			// сборник всех сезонов ошибочно привязывается к одному сезону.
			if season <= 0 && sq.seasonHint > 0 && !magnet.IsFullCollection(s.Title) {
				season = sq.seasonHint
			}
			items = append(items, sourceItem{
				Title: s.Title, Size: s.Size, Seeds: s.Seeds, Magnet: s.Magnet,
				Quality: quality, Audio: audio, Season: season, Provider: providerName,
			})
		}
		// Раньше цикл обрывался уже при 6 вариантах после ПЕРВОГО запроса:
		// у сериалов терялись остальные сезоны и богатые раздачи. Теперь
		// проходим все запросы, предохранитель — только от аномально
		// большого числа вариантов.
		if len(items) >= 40 {
			break
		}
	}
	return items
}

// sortSourceItems ранжирует варианты: озвучка > качество > сиды. Для
// сериалов сначала группирует по сезону (по возрастанию), внутри сезона —
// по рангу; раздачи без сезона (полные сборники) идут последними.
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

// sourceItemsFromDB превращает сохранённые в БД источники в варианты для
// фронтенда (с тем же ранжированием, что и после живого поиска).
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
