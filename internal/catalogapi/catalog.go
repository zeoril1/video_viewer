package catalogapi

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/imdb"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

// CatalogItem — единая запись каталога для фронтенда.
type CatalogItem struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	TitleRU     string   `json:"title_ru,omitempty"`
	Kind        string   `json:"kind,omitempty"` // feature, tvSeries, tvMovie, short, animation, ...
	Poster      string   `json:"poster,omitempty"`
	Category    string   `json:"category,omitempty"`
	Size        string   `json:"size,omitempty"`
	Year        int      `json:"year,omitempty"`
	ReleaseDate string   `json:"release_date,omitempty"` // "YYYY-MM-DD" (если известна)
	Rating      float64  `json:"rating,omitempty"`       // IMDb
	RatingTMDB  float64  `json:"rating_tmdb,omitempty"`
	Plot        string   `json:"plot,omitempty"`
	PlotRU      string   `json:"plot_ru,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	IMDbID      string   `json:"imdb_id,omitempty"`
	TMDBID      string   `json:"tmdb_id,omitempty"`
	Seasons     int      `json:"seasons,omitempty"`
	MovieLength int      `json:"movie_length,omitempty"`
	Countries   []string `json:"countries,omitempty"`
	Director    string   `json:"director,omitempty"`
	Actors      []string `json:"actors,omitempty"`
	Source      string   `json:"source"` // "imdb", "tmdb" или "magnet"
	HasMagnet   bool     `json:"has_magnet"`
}

// catalogEntry — внутреннее представление записи с магнет-ссылкой.
type catalogEntry struct {
	item   CatalogItem
	magnet string
}

// catalogCacheTTL — время жизни кэша каталога и меты: без него /api/catalog/meta на каждый клик по вкладкам/жанрам сканировал бы таблицу films.
const catalogCacheTTL = 30 * time.Second

// catalogService объединяет записи локального каталога (магнеты), фильмы БД (IMDb + TMDB) и on-demand внешнего поиска.
type catalogService struct {
	jsonCat *catalog.Catalog
	db      *db.Repo
	imdb    *imdb.Client // может быть nil — внешний поиск отключён
	tm      *tmdb.Client // может быть nil

	// Кэш с TTL: allCache — объединённый каталог, metaCache — результаты Meta по ключу "q|genre".
	cacheMu     sync.Mutex
	allCache    []catalogEntry
	allCachedAt time.Time
	metaCache   map[string]metaCacheEntry
}

type metaCacheEntry struct {
	kinds  map[string]int
	genres []string
	at     time.Time
}

func newCatalogService(jsonCat *catalog.Catalog, repo *db.Repo, im *imdb.Client, tm *tmdb.Client) *catalogService {
	return &catalogService{
		jsonCat:   jsonCat,
		db:        repo,
		imdb:      im,
		tm:        tm,
		metaCache: make(map[string]metaCacheEntry),
	}
}

// All возвращает объединённый каталог: сначала фильмы БД (топ-250, популярные, затем остальные), потом магнет-записи.
func (s *catalogService) All(ctx context.Context) []catalogEntry {
	s.cacheMu.Lock()
	if s.allCache != nil && time.Since(s.allCachedAt) < catalogCacheTTL {
		cached := s.allCache
		s.cacheMu.Unlock()
		return cached
	}
	s.cacheMu.Unlock()

	var out []catalogEntry

	if s.db != nil {
		// Лёгкая выборка без описаний — они догружаются при открытии фильма (GET /api/films/{id}).
		films, err := s.db.ListFilmsLite(ctx)
		if err != nil {
			log.Printf("catalog: list films: %v", err)
		} else {
			for _, f := range films {
				out = append(out, dbFilmToEntry(f))
			}
		}
	}

	for _, it := range s.jsonCat.Items {
		out = append(out, catalogEntry{
			item: CatalogItem{
				ID:        it.ID,
				Title:     it.Title,
				Poster:    it.Poster,
				Category:  it.Category,
				Size:      it.Size,
				Source:    "magnet",
				HasMagnet: true,
			},
			magnet: it.Magnet,
		})
	}
	log.Printf("catalog: all: %d записей", len(out))

	s.cacheMu.Lock()
	s.allCache = out
	s.allCachedAt = time.Now()
	s.cacheMu.Unlock()
	return out
}

// SearchPage возвращает страницу каталога: фильтр по q (название, русское название, категория, IMDb ID),
// section, genre и onlyReleased, затем сортировка по sortBy и разбивка на страницы.
// Секция popular и подборки (collection — "best" = чарт top_rated, "popular" = «Популярные сериалы»,
// пусто — обычный раздел) сортировку не применяют: там порядок чарта.
// sortBy: "year" (по умолчанию — новые сверху), "rating" или "title".
func (s *catalogService) SearchPage(ctx context.Context, q, section, genre, sortBy, collection string, onlyReleased bool, page, perPage int) ([]catalogEntry, int) {
	// Подборки работают только внутри разделов «Фильмы»/«Сериалы».
	if collection != "" && section != "movie" && section != "series" {
		collection = ""
	}
	var all []catalogEntry
	switch {
	case collection == "best":
		all = s.Best(ctx, section == "series")
	case collection == "popular":
		all = s.PopularKind(ctx, section == "series")
	case section == "popular":
		all = s.Popular(ctx)
	default:
		all = s.All(ctx)
	}
	filtered := make([]catalogEntry, 0, len(all))

	for _, e := range all {
		it := e.item
		if q != "" && !matchesQuery(it, q) {
			continue
		}
		if !matchesSection(it, section) {
			continue
		}
		if genre != "" && !hasGenre(it, genre) {
			continue
		}
		if onlyReleased && !isReleased(it) {
			continue
		}
		filtered = append(filtered, e)
	}
	all = filtered

	// В «Популярном» и подборках порядок чарта, остальные разделы сортируем (по умолчанию — по дате выпуска).
	if section != "popular" && collection == "" {
		if sortBy == "" {
			sortBy = "year"
		}
		sortCatalogEntries(all, sortBy)
	}

	total := len(all)
	if perPage <= 0 {
		perPage = 30
	}
	if page <= 0 {
		page = 1
	}
	start := (page - 1) * perPage
	if start >= total {
		log.Printf("catalog: search q=%q section=%s genre=%s page=%d -> 0/%d", q, section, genre, page, total)
		return []catalogEntry{}, total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	log.Printf("catalog: search q=%q section=%s genre=%s page=%d -> %d/%d", q, section, genre, page, end-start, total)
	return all[start:end], total
}

// sortCatalogEntries сортирует записи по sortBy: "year" (по умолчанию — полная дата выпуска, новые сверху),
// "rating" (лучший из доступных рейтингов) или "title".
// У всех вариантов детерминированный tiebreaker по ID — порядок стабилен между запросами (важно для бесконечной прокрутки).
func sortCatalogEntries(entries []catalogEntry, sortBy string) {
	switch sortBy {
	case "rating":
		sort.SliceStable(entries, func(i, j int) bool {
			ri, rj := bestRating(entries[i].item), bestRating(entries[j].item)
			if ri != rj {
				return ri > rj
			}
			if k1, k2 := releaseKey(entries[i].item), releaseKey(entries[j].item); k1 != k2 {
				return cmpReleaseKey(k1, k2) > 0
			}
			return entries[i].item.ID < entries[j].item.ID
		})
	case "title":
		sort.SliceStable(entries, func(i, j int) bool {
			ti := strings.ToLower(sortTitle(entries[i].item))
			tj := strings.ToLower(sortTitle(entries[j].item))
			if ti != tj {
				return ti < tj
			}
			return entries[i].item.ID < entries[j].item.ID
		})
	default: // "year" — дата выпуска (полная дата; неизвестная — в конец)
		sort.SliceStable(entries, func(i, j int) bool {
			k1, k2 := releaseKey(entries[i].item), releaseKey(entries[j].item)
			if k1 != k2 {
				return cmpReleaseKey(k1, k2) > 0
			}
			return entries[i].item.ID < entries[j].item.ID
		})
	}
}

// releaseKey — сортировочный ключ даты выпуска: сама дата "YYYY-MM-DD" (с валидацией),
// иначе год в виде "YYYY-00-00", при отсутствии года — пустая строка (всегда в конце).
func releaseKey(it CatalogItem) string {
	if it.ReleaseDate != "" {
		if _, err := time.Parse("2006-01-02", it.ReleaseDate); err == nil {
			return it.ReleaseDate
		}
	}
	if it.Year > 0 {
		return fmt.Sprintf("%04d-00-00", it.Year)
	}
	return ""
}

// cmpReleaseKey сравнивает ключи дат по убыванию; пустой ключ (нет данных) всегда меньше непустого (в конец).
func cmpReleaseKey(a, b string) int {
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	case b == "":
		return 1
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// isReleased — вышел ли фильм (дата не в будущем); записи без даты считаем вышедшими (не знаем — не скрываем).
func isReleased(it CatalogItem) bool {
	if it.ReleaseDate == "" {
		return true
	}
	t, err := time.Parse("2006-01-02", it.ReleaseDate)
	if err != nil {
		return true
	}
	return !t.After(time.Now())
}

// bestRating — лучший из доступных рейтингов записи (IMDb или TMDB).
func bestRating(it CatalogItem) float64 {
	r := it.Rating
	if it.RatingTMDB > r {
		r = it.RatingTMDB
	}
	return r
}

// sortTitle — название для сортировки: русское при наличии, иначе английское.
func sortTitle(it CatalogItem) string {
	if it.TitleRU != "" {
		return it.TitleRU
	}
	return it.Title
}

// Meta отдаёт статистику каталога для фильтров: счётчики записей по секциям (с учётом активных q и genre)
// и полный отсортированный список жанров.
func (s *catalogService) Meta(ctx context.Context, q, genre string) (map[string]int, []string) {
	key := strings.TrimSpace(q) + "\x00" + genre
	s.cacheMu.Lock()
	if e, ok := s.metaCache[key]; ok && time.Since(e.at) < catalogCacheTTL {
		kinds, genres := e.kinds, e.genres
		s.cacheMu.Unlock()
		return kinds, genres
	}
	s.cacheMu.Unlock()

	kinds := map[string]int{}
	genreSet := map[string]bool{}

	q = strings.TrimSpace(q)
	for _, e := range s.All(ctx) {
		it := e.item

		// Полный список жанров — независимо от активных фильтров, иначе выпадающий список «схлопывался».
		for _, g := range it.Genres {
			if g = strings.TrimSpace(g); g != "" {
				genreSet[g] = true
			}
		}

		// Счётчики секций — только по записям под активными фильтрами.
		if q != "" && !matchesQuery(it, q) {
			continue
		}
		if genre != "" && !hasGenre(it, genre) {
			continue
		}
		kinds[sectionForItem(it)]++
	}

	// Счётчик раздела «Популярное» — объединённый список IMDb + TMDB.
	if s.db != nil {
		if films, err := s.db.ListPopular(ctx); err == nil {
			kinds["popular"] = 0
			for _, f := range films {
				it := dbFilmToEntry(f).item
				if q != "" && !matchesQuery(it, q) {
					continue
				}
				if genre != "" && !hasGenre(it, genre) {
					continue
				}
				kinds["popular"]++
			}
		}
	}

	genres := make([]string, 0, len(genreSet))
	for g := range genreSet {
		genres = append(genres, g)
	}
	sort.Strings(genres)

	s.cacheMu.Lock()
	if len(s.metaCache) >= 128 {
		s.metaCache = make(map[string]metaCacheEntry)
	}
	s.metaCache[key] = metaCacheEntry{kinds: kinds, genres: genres, at: time.Now()}
	s.cacheMu.Unlock()
	return kinds, genres
}

// matchesQuery проверяет запись на совпадение с запросом. Запрос может содержать русское и/или
// английское название и год, в т.ч. через разделители ("Одержимость / Whiplash 2014») — все слова запроса
// должны встретиться в названиях, категории, ID или годе записи.
func matchesQuery(it CatalogItem, q string) bool {
	norm := strings.ToLower(strings.TrimSpace(q))
	if norm == "" {
		return true
	}
	// Разделители "/" и "\\" превращаем в пробелы, чтобы "RU / EN год" разбивался на отдельные слова.
	norm = strings.NewReplacer("/", " ", "\\", " ").Replace(norm)

	hay := strings.ToLower(strings.Join([]string{
		it.Title, it.TitleRU, it.Category, it.IMDbID, strconv.Itoa(it.Year),
	}, " "))

	for _, term := range strings.Fields(norm) {
		if term == "" {
			continue
		}
		if !strings.Contains(hay, term) {
			return false
		}
	}
	return true
}

// cleanSearchQuery готовит запрос для внешних API (TMDB/IMDb): часть до первого "/" (обычно русское название) без года —
// внешние поисковики не понимают формат «RU / EN год» ("Одержимость / Whiplash 2014" -> "одержимость").
func cleanSearchQuery(q string) string {
	q = strings.TrimSpace(q)
	if i := strings.IndexByte(q, '/'); i >= 0 {
		q = q[:i]
	}
	// Убираем год в конце (4 цифры, возможно после пробела/скобок).
	for _, r := range []string{` (19\d\d)`, ` (20\d\d)`, ` 19\d\d`, ` 20\d\d`} {
		if re := regexp.MustCompile(r + `$`); re.MatchString(q) {
			q = re.ReplaceAllString(q, "")
		}
	}
	return strings.TrimSpace(q)
}

func hasGenre(it CatalogItem, genre string) bool {
	gl := strings.ToLower(genre)
	for _, g := range it.Genres {
		if strings.ToLower(strings.TrimSpace(g)) == gl {
			return true
		}
	}
	return false
}

// knownKindsSet — все типы IMDb, отнесённые к конкретным секциям (не входящие в него попадают в секцию "other").
var knownKindsSet = map[string]bool{
	"":             true, // нет типа (например, магнеты) — считаем фильмом
	"feature":      true,
	"tvSeries":     true,
	"tvMiniSeries": true,
	"tvMovie":      true,
	"short":        true,
	"tvShort":      true,
	"video":        true,
	"tvEpisode":    true,
	"tvSpecial":    true,
	"animation":    true,
}

// sectionForKind сопоставляет тип IMDb секции каталога.
func sectionForKind(kind string) string {
	switch imdb.NormalizeKind(kind) {
	case "feature":
		return "movie"
	case "tvSeries", "tvMiniSeries":
		return "series"
	case "tvMovie":
		return "tv_movie"
	case "short", "tvShort":
		return "short"
	case "video":
		return "video"
	case "tvEpisode":
		return "episode"
	case "animation":
		return "cartoon"
	default:
		return "other"
	}
}

// sectionForItem определяет секцию записи с учётом жанров (аниме — по жанру Anime; остальные мультфильмы — по типу/жанру Animation).
func sectionForItem(it CatalogItem) string {
	if isAnime(it) {
		return "anime"
	}
	if isAnimation(it) {
		return "cartoon"
	}
	return sectionForKind(it.Kind)
}

// isAnimation — анимация ли запись (мультфильм или аниме): по нормализованному типу animation либо по жанру.
func isAnimation(it CatalogItem) bool {
	if imdb.NormalizeKind(it.Kind) == "animation" {
		return true
	}
	for _, g := range it.Genres {
		switch strings.ToLower(strings.TrimSpace(g)) {
		case "animation", "anime", "мультфильм", "multfilm", "cartoon", "аниме":
			return true
		}
	}
	return false
}

// isAnime сообщает, относится ли запись к разделу «Аниме» (по жанру).
func isAnime(it CatalogItem) bool {
	for _, g := range it.Genres {
		switch strings.ToLower(strings.TrimSpace(g)) {
		case "anime", "аниме":
			return true
		}
	}
	return false
}

// matchesSection проверяет, что запись относится к секции section; используется в SearchPage и при фильтрации
// внешних (on-demand) результатов, чтобы вкладки работали и во время поиска.
func matchesSection(it CatalogItem, section string) bool {
	switch section {
	case "anime":
		return isAnime(it)
	case "cartoon":
		return isAnimation(it) && !isAnime(it)
	case "other":
		// «Другое» — не-анимационные записи с неизвестным типом (анимация/аниме — в своих разделах).
		return !isAnimation(it) && !knownKindsSet[imdb.NormalizeKind(it.Kind)]
	}
	// Анимация и аниме имеют собственные разделы — исключаем их из типовых («Фильмы», «Сериалы»),
	// иначе мультфильмы с kind=feature и жанром Animation попадали в «Фильмы».
	// Разделы «Все» и «Популярное» по типу не фильтруются.
	if section != "all" && section != "popular" && isAnimation(it) {
		return false
	}
	kindFilter := kindsForSection(section)
	if kindFilter == nil {
		return true // all / popular — без фильтра по типу
	}
	return kindFilter[imdb.NormalizeKind(it.Kind)]
}

// Popular возвращает объединённый список «популярных» (чарт IMDb moviemeter + популярные TMDB), без дублей.
func (s *catalogService) Popular(ctx context.Context) []catalogEntry {
	var out []catalogEntry
	if s.db == nil {
		return out
	}
	films, err := s.db.ListPopular(ctx)
	if err != nil {
		log.Printf("catalog: list popular: %v", err)
		return out
	}
	for _, f := range films {
		out = append(out, dbFilmToEntry(f))
	}
	return out
}

// Best возвращает «лучшие» записи (чарт top_rated IMDb+TMDB) для фильмов (series=false) или сериалов — подборка «Лучшие …».
func (s *catalogService) Best(ctx context.Context, series bool) []catalogEntry {
	var out []catalogEntry
	if s.db == nil {
		return out
	}
	films, err := s.db.ListTopRated(ctx, series)
	if err != nil {
		log.Printf("catalog: list top rated (series=%v): %v", series, err)
		return out
	}
	for _, f := range films {
		out = append(out, dbFilmToEntry(f))
	}
	return out
}

// PopularKind возвращает «популярные» (чарты IMDb+TMDB) для фильмов (series=false) или сериалов — подборки «Популярные …».
func (s *catalogService) PopularKind(ctx context.Context, series bool) []catalogEntry {
	var out []catalogEntry
	if s.db == nil {
		return out
	}
	films, err := s.db.ListPopularKind(ctx, series)
	if err != nil {
		log.Printf("catalog: list popular (series=%v): %v", series, err)
		return out
	}
	for _, f := range films {
		out = append(out, dbFilmToEntry(f))
	}
	return out
}

// SearchExternal ищет фильмы в TMDB и IMDb (on-demand), сохраняет их в БД и возвращает записи каталога
// (используется, когда локальный поиск по БД дал мало результатов).
func (s *catalogService) SearchExternal(ctx context.Context, q string) []catalogEntry {
	// Для внешних API берём чистый запрос (часть до "/" без года) — TMDB/IMDb не нужно «RU / EN 2014».
	eq := cleanSearchQuery(q)
	seen := map[string]bool{}
	var out []catalogEntry

	if s.tm != nil {
		if films, err := s.tm.Search(ctx, eq, 10); err == nil {
			for _, f := range films {
				if f.IMDBID == "" || seen[f.IMDBID] {
					continue
				}
				seen[f.IMDBID] = true
				if s.db != nil {
					if err := s.db.SaveTMDBFilm(ctx, f); err != nil {
						log.Printf("catalog: save tmdb %s: %v", f.IMDBID, err)
					}
				}
				out = append(out, tmdbFilmToEntry(f))
			}
		} else {
			log.Printf("catalog: tmdb search %q: %v", q, err)
		}
	}

	if s.imdb != nil {
		if films, err := s.imdb.Search(ctx, eq); err == nil {
			for _, f := range films {
				if f.IMDBID == "" || seen[f.IMDBID] {
					continue
				}
				seen[f.IMDBID] = true
				if s.db != nil {
					if err := s.db.SaveFilm(ctx, f); err != nil {
						log.Printf("catalog: save imdb %s: %v", f.IMDBID, err)
					}
				}
				out = append(out, dbFilmToEntry(db.FromIMDB(f)))
			}
		} else {
			log.Printf("catalog: imdb search %q: %v", q, err)
		}
	}
	if len(out) > 0 {
		log.Printf("catalog: external search %q -> %d записей", q, len(out))
	}
	return out
}

func mergeEntries(groups ...[]catalogEntry) []catalogEntry {
	seen := map[string]bool{}
	var out []catalogEntry
	for _, g := range groups {
		for _, e := range g {
			if e.item.ID == "" || seen[e.item.ID] {
				continue
			}
			seen[e.item.ID] = true
			out = append(out, e)
		}
	}
	return out
}

// kindsForSection — множество сырых типов для секции; nil — секция "all" (без фильтра по типу).
func kindsForSection(section string) map[string]bool {
	switch section {
	case "movie":
		return map[string]bool{"": true, "feature": true}
	case "series":
		return map[string]bool{"tvSeries": true, "tvMiniSeries": true}
	case "tv_movie":
		return map[string]bool{"tvMovie": true}
	case "short":
		return map[string]bool{"short": true, "tvShort": true}
	case "video":
		return map[string]bool{"video": true}
	case "episode":
		return map[string]bool{"tvEpisode": true}
	default:
		return nil // all / other
	}
}

// FindMagnet ищет магнет-ссылку по id только в локальном каталоге (data/catalog.json):
// магнеты фильмов БД живут в таблице sources (колонка films.magnet удалена как legacy) — в самой БД магнета нет.
func (s *catalogService) FindMagnet(ctx context.Context, id string) (string, bool) {
	if it, ok := s.jsonCat.Get(id); ok && it.Magnet != "" {
		return it.Magnet, true
	}
	return "", false
}

func dbFilmToEntry(f db.Film) catalogEntry {
	source := "imdb"
	if strings.HasPrefix(f.IMDBID, "kp") {
		source = "kinopoisk"
	}
	return catalogEntry{
		item: CatalogItem{
			ID:          f.IMDBID,
			Title:       f.Title,
			TitleRU:     f.TitleRU,
			Kind:        f.Kind,
			Poster:      f.PosterURL,
			Category:    source,
			Size:        f.Size,
			Year:        f.Year,
			ReleaseDate: f.ReleaseDate,
			Rating:      f.Rating,
			Plot:        f.Plot,
			PlotRU:      f.PlotRU,
			Genres:      f.Genres,
			IMDbID:      f.IMDBID,
			TMDBID:      f.TMDBID,
			Seasons:     f.Seasons,
			MovieLength: f.MovieLength,
			Countries:   f.Countries,
			Director:    f.Director,
			Actors:      f.Actors,
			RatingTMDB:  f.RatingTMDB,
			Source:      source,
		},
	}
}

func tmdbFilmToEntry(f tmdb.Film) catalogEntry {
	return catalogEntry{
		item: CatalogItem{
			ID:          f.IMDBID,
			Title:       f.Title,
			TitleRU:     f.TitleRU,
			Kind:        f.Kind,
			Poster:      f.PosterURL,
			Category:    "tmdb",
			Year:        f.Year,
			ReleaseDate: f.ReleaseDate,
			RatingTMDB:  f.Rating,
			Plot:        "",
			PlotRU:      f.OverviewRU,
			Genres:      f.Genres,
			IMDbID:      f.IMDBID,
			TMDBID:      strconv.FormatInt(f.TMDBID, 10),
			Source:      "tmdb",
		},
	}
}
