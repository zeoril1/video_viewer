// Package tmdb — клиент The Movie Database (TMDB); мягкий потолок ~40 req/s.
package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL — базовый адрес TMDB API v3.
const DefaultBaseURL = "https://api.themoviedb.org/3"

const userAgent = "video_viewer/1.0"

// Client — HTTP-клиент TMDB.
type Client struct {
	apiKey      string
	accessToken string
	baseURL     string
	http        *http.Client
	// seasonsCache — кэш структуры сезонов сериалов (меняется редко).
	seasonsCache *seasonsCache
}

// NewClient создаёт клиент TMDB; accessToken (Bearer) приоритетнее apiKey.
func NewClient(apiKey, accessToken, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		apiKey:      strings.TrimSpace(apiKey),
		accessToken: strings.TrimSpace(accessToken),
		baseURL:     strings.TrimRight(baseURL, "/"),
		http:        &http.Client{Timeout: 30 * time.Second},
		seasonsCache: &seasonsCache{
			m: map[int64]seasonEntry{},
		},
	}
}

// ChartKind — тип чарта TMDB.
type ChartKind int

const (
	// ChartTopRated — «лучшие фильмы» (top_rated).
	ChartTopRated ChartKind = iota
	// ChartPopular — «популярные» (movie/popular).
	ChartPopular
)

// String возвращает человекочитаемое имя чарта.
func (k ChartKind) String() string {
	if k == ChartPopular {
		return "popular"
	}
	return "top_rated"
}

// Film — фильм из TMDB (внутреннее представление).
type Film struct {
	TMDBID int64
	// IMDBID может быть пустым (из external_ids).
	IMDBID string
	// Title — оригинальное название, TitleRU — русское (language=ru-RU).
	Title   string
	TitleRU string
	// Kind — feature/tvSeries/...
	Kind string
	Year int
	// ReleaseDate "YYYY-MM-DD"; может быть пустой.
	ReleaseDate string
	// Rating — vote_average, Votes — число голосов.
	Rating float64
	Votes  int64
	// MovieLength — минуты (0 — неизвестно; у сериалов всегда 0).
	MovieLength int
	// OverviewRU — русское описание.
	OverviewRU string
	// Genres — английские имена (в стиле жанров IMDb).
	Genres    []string
	PosterURL string
	// Rank — позиция в чарте (1-based; 0 — обычный поиск).
	Rank int
}

// page — ответ пагинированного списка TMDB.
type page struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
	Results    []struct {
		ID            int64   `json:"id"`
		Title         string  `json:"title"`
		OriginalTitle string  `json:"original_title"`
		Overview      string  `json:"overview"`
		ReleaseDate   string  `json:"release_date"`
		GenreIDs      []int   `json:"genre_ids"`
		VoteAverage   float64 `json:"vote_average"`
		VoteCount     int64   `json:"vote_count"`
		PosterPath    string  `json:"poster_path"`
	} `json:"results"`
}

// Search ищет на TMDB фильмы И сериалы (/search/multi) и возвращает до limit
// результатов с IMDb-ссылками (нужны для дедупликации); люди отбрасываются.
func (c *Client) Search(ctx context.Context, q string, limit int) ([]Film, error) {
	qv := url.Values{}
	qv.Set("query", q)
	qv.Set("language", "ru-RU")
	qv.Set("page", "1")
	body, err := c.get(ctx, "/search/multi", qv)
	if err != nil {
		return nil, err
	}
	var p struct {
		Results []multiResult `json:"results"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("tmdb: parse search multi: %w", err)
	}
	items := make([]multiResult, 0, len(p.Results))
	for _, r := range p.Results {
		if (r.MediaType != "movie" && r.MediaType != "tv") || r.ID == 0 {
			continue
		}
		items = append(items, r)
	}
	if len(items) > limit {
		items = items[:limit]
	}
	imdbs := c.externalIDs(ctx, items)
	films := make([]Film, 0, len(items))
	for i, r := range items {
		if r.MediaType == "tv" {
			films = append(films, filmFrom(r.ID, imdbs[i], r.OriginalName, r.Name, r.Overview,
				r.FirstAirDate, r.GenreIDs, r.VoteAverage, r.VoteCount, r.PosterPath, "tvSeries"))
			continue
		}
		films = append(films, filmFrom(r.ID, imdbs[i], r.OriginalTitle, r.Title, r.Overview,
			r.ReleaseDate, r.GenreIDs, r.VoteAverage, r.VoteCount, r.PosterPath, "feature"))
	}
	return films, nil
}

// externalIDs тянет IMDb-ссылки для смешанного списка (по запросу на запись, ≤6 сразу).
func (c *Client) externalIDs(ctx context.Context, items []multiResult) []string {
	out := make([]string, len(items))
	type ext struct {
		i  int
		id string
	}
	ch := make(chan ext, len(items))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, r := range items {
		wg.Add(1)
		go func(i int, media string, tmdbID int64) {
			defer wg.Done()
			sem <- struct{}{}
			id, err := c.externalIDMedia(ctx, media, tmdbID)
			<-sem
			if err != nil {
				id = "" // нет ссылки — не критично
			}
			ch <- ext{i: i, id: id}
		}(i, r.MediaType, r.ID)
	}
	wg.Wait()
	close(ch)
	for e := range ch {
		out[e.i] = e.id
	}
	return out
}

// SearchLite ищет фильмы ОДНИМ запросом, без external_ids на кандидата —
// для сверки по названию/году в фоновой джобе рейтингов.
func (c *Client) SearchLite(ctx context.Context, q string, limit int) ([]Film, error) {
	qv := url.Values{}
	qv.Set("query", q)
	qv.Set("language", "ru-RU")
	qv.Set("page", "1")
	p, err := c.getPage(ctx, "/search/movie", qv)
	if err != nil {
		return nil, err
	}
	items := p.Results
	if len(items) > limit {
		items = items[:limit]
	}
	films := make([]Film, 0, len(items))
	for _, r := range items {
		films = append(films, detailToFilm(movieDetail{
			ID:            r.ID,
			Title:         r.Title,
			OriginalTitle: r.OriginalTitle,
			Overview:      r.Overview,
			ReleaseDate:   r.ReleaseDate,
			VoteAverage:   r.VoteAverage,
			VoteCount:     r.VoteCount,
			PosterPath:    r.PosterPath,
		}))
	}
	return films, nil
}

// Popular возвращает до target «популярных» фильмов TMDB
// (в порядке популярности).
func (c *Client) Popular(ctx context.Context, target int) ([]Film, error) {
	return c.chart(ctx, "/movie/popular", target)
}

// TopRated возвращает до target лучших фильмов TMDB (top_rated).
func (c *Client) TopRated(ctx context.Context, target int) ([]Film, error) {
	return c.chart(ctx, "/movie/top_rated", target)
}

// TVPopular возвращает до target «популярных» сериалов TMDB (/tv/popular).
func (c *Client) TVPopular(ctx context.Context, target int) ([]Film, error) {
	return c.tvChart(ctx, "/tv/popular", target)
}

// TVTopRated возвращает до target лучших сериалов TMDB (/tv/top_rated).
func (c *Client) TVTopRated(ctx context.Context, target int) ([]Film, error) {
	return c.tvChart(ctx, "/tv/top_rated", target)
}

// tvResult — элемент списка сериалов TMDB (у ТВ другие поля: name, first_air_date).
type tvResult struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	OriginalName string  `json:"original_name"`
	Overview     string  `json:"overview"`
	FirstAirDate string  `json:"first_air_date"`
	GenreIDs     []int   `json:"genre_ids"`
	VoteAverage  float64 `json:"vote_average"`
	VoteCount    int64   `json:"vote_count"`
	PosterPath   string  `json:"poster_path"`
}

// multiResult — элемент /search/multi: у фильмов title/release_date, у сериалов name/first_air_date.
type multiResult struct {
	ID            int64   `json:"id"`
	MediaType     string  `json:"media_type"` // movie | tv | person
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Name          string  `json:"name"`
	OriginalName  string  `json:"original_name"`
	Overview      string  `json:"overview"`
	ReleaseDate   string  `json:"release_date"`
	FirstAirDate  string  `json:"first_air_date"`
	GenreIDs      []int   `json:"genre_ids"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int64   `json:"vote_count"`
	PosterPath    string  `json:"poster_path"`
}

type tvPage struct {
	Page       int        `json:"page"`
	TotalPages int        `json:"total_pages"`
	Results    []tvResult `json:"results"`
}

// tvChart тянет пагинированный список сериалов TMDB до target позиций.
func (c *Client) tvChart(ctx context.Context, path string, target int) ([]Film, error) {
	perPage := 20
	if perPage > target {
		perPage = target
	}
	q := url.Values{}
	q.Set("language", "ru-RU")
	q.Set("page", "1")

	var films []Film
	for pageNum := 1; len(films) < target; pageNum++ {
		q.Set("page", fmt.Sprint(pageNum))
		body, err := c.get(ctx, path, q)
		if err != nil {
			return nil, err
		}
		var p tvPage
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		if len(p.Results) == 0 {
			break
		}
		want := target - len(films)
		if want > len(p.Results) {
			want = len(p.Results)
		}
		items := p.Results[:want]
		fs, err := c.tvToFilms(ctx, items, len(films)+1)
		if err != nil {
			return nil, err
		}
		films = append(films, fs...)
		if pageNum >= p.TotalPages {
			break
		}
	}
	return films, nil
}

// tvToFilms преобразует результаты /tv/* в Film (kind=tvSeries) с IMDb-ссылками.
func (c *Client) tvToFilms(ctx context.Context, results []tvResult, startRank int) ([]Film, error) {
	type ext struct {
		i   int
		id  string
		err error
	}
	ch := make(chan ext, len(results))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, r := range results {
		wg.Add(1)
		go func(i int, tmdbID int64) {
			defer wg.Done()
			sem <- struct{}{}
			id, err := c.externalIDMedia(ctx, "tv", tmdbID)
			<-sem
			ch <- ext{i: i, id: id, err: err}
		}(i, r.ID)
	}
	wg.Wait()
	close(ch)

	extIDs := make([]string, len(results))
	for e := range ch {
		extIDs[e.i] = e.id
		if e.err != nil {
			extIDs[e.i] = ""
		}
	}

	films := make([]Film, 0, len(results))
	for i, r := range results {
		f := filmFrom(r.ID, extIDs[i], r.OriginalName, r.Name, r.Overview,
			r.FirstAirDate, r.GenreIDs, r.VoteAverage, r.VoteCount, r.PosterPath, "tvSeries")
		if startRank > 0 {
			f.Rank = startRank + i
		}
		films = append(films, f)
	}
	return films, nil
}

// chart тянет пагинированный список TMDB до target фильмов.
func (c *Client) chart(ctx context.Context, path string, target int) ([]Film, error) {
	perPage := 20
	if perPage > target {
		perPage = target
	}
	q := url.Values{}
	q.Set("language", "ru-RU")
	q.Set("page", "1")

	var films []Film
	for pageNum := 1; len(films) < target; pageNum++ {
		q.Set("page", fmt.Sprint(pageNum))
		p, err := c.getPage(ctx, path, q)
		if err != nil {
			return nil, err
		}
		if len(p.Results) == 0 {
			break
		}
		want := target - len(films)
		if want > len(p.Results) {
			want = len(p.Results)
		}
		items := p.Results[:want]
		fs, err := c.toFilms(ctx, items, len(films)+1)
		if err != nil {
			return nil, err
		}
		films = append(films, fs...)
		if pageNum >= p.TotalPages {
			break
		}
	}
	return films, nil
}

// toFilms преобразует результаты TMDB в Film; startRank — начальная позиция чарта.
func (c *Client) toFilms(ctx context.Context, results []struct {
	ID            int64   `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Overview      string  `json:"overview"`
	ReleaseDate   string  `json:"release_date"`
	GenreIDs      []int   `json:"genre_ids"`
	VoteAverage   float64 `json:"vote_average"`
	VoteCount     int64   `json:"vote_count"`
	PosterPath    string  `json:"poster_path"`
}, startRank int) ([]Film, error) {
	// Параллельно тянем external_ids — по запросу на фильм, ≤6 сразу.
	type ext struct {
		i   int
		id  string
		err error
	}
	ch := make(chan ext, len(results))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, r := range results {
		wg.Add(1)
		go func(i int, tmdbID int64) {
			defer wg.Done()
			sem <- struct{}{}
			id, err := c.externalIDMedia(ctx, "movie", tmdbID)
			<-sem
			ch <- ext{i: i, id: id, err: err}
		}(i, r.ID)
	}
	wg.Wait()
	close(ch)

	extIDs := make([]string, len(results))
	for e := range ch {
		extIDs[e.i] = e.id
		if e.err != nil {
			// нет ссылки — не критично
			extIDs[e.i] = ""
		}
	}

	films := make([]Film, 0, len(results))
	for i, r := range results {
		f := filmFrom(r.ID, extIDs[i], r.OriginalTitle, r.Title, r.Overview,
			r.ReleaseDate, r.GenreIDs, r.VoteAverage, r.VoteCount, r.PosterPath, "feature")
		if startRank > 0 {
			f.Rank = startRank + i
		}
		films = append(films, f)
	}
	return films, nil
}

// filmFrom собирает Film из полей TMDB (год из даты, жанры из genre_ids) — общая часть.
func filmFrom(tmdbID int64, imdbID, origTitle, titleRU, overview, date string, genreIDs []int, rating float64, votes int64, poster, kind string) Film {
	year := 0
	if len(date) >= 4 {
		year = atoi(date[:4])
	}
	genres := make([]string, 0, len(genreIDs))
	for _, id := range genreIDs {
		if g := genreName(id); g != "" {
			genres = append(genres, g)
		}
	}
	return Film{
		TMDBID:      tmdbID,
		IMDBID:      imdbID,
		Title:       strings.TrimSpace(origTitle),
		TitleRU:     strings.TrimSpace(titleRU),
		Kind:        kind,
		Year:        year,
		ReleaseDate: strings.TrimSpace(date),
		Rating:      rating,
		Votes:       votes,
		OverviewRU:  strings.TrimSpace(overview),
		Genres:      genres,
		PosterURL:   posterURL(poster),
	}
}

// SeasonsCount возвращает число сезонов сериала (/tv/{id}). Нужно поиску
// источников: трекеры держат сезон отдельной раздачей, и без этого числа
// запрос к трекеру выходит один общий, со случайными сезонами в выдаче.
func (c *Client) SeasonsCount(ctx context.Context, tmdbID int64) (int, error) {
	qv := url.Values{}
	qv.Set("language", "ru-RU")
	body, err := c.get(ctx, fmt.Sprintf("/tv/%d", tmdbID), qv)
	if err != nil {
		return 0, err
	}
	var d struct {
		NumberOfSeasons int `json:"number_of_seasons"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return 0, fmt.Errorf("tmdb: parse tv %d: %w", tmdbID, err)
	}
	return d.NumberOfSeasons, nil
}

// SeasonInfo — сезон TMDB: номер, число серий и год старта (спецматериалы — сезон 0).
type SeasonInfo struct {
	Number   int    `json:"season_number"`
	Episodes int    `json:"episode_count"`
	Year     int    `json:"year,omitempty"`
	Name     string `json:"name,omitempty"`
	AirDate  string `json:"air_date,omitempty"`
}

// SeasonStructure возвращает структуру сезонов сериала (число серий и год).
// Нужна для раскладки раздач: нарезка трекеров дробнее официальной, а
// сборники нумеруют серии СКВОЗНЯКОМ («001 seriya») — без этой структуры
// такие файлы по сезонам не разложить.
func (c *Client) SeasonStructure(ctx context.Context, tmdbID int64) ([]SeasonInfo, error) {
	qv := url.Values{}
	qv.Set("language", "ru-RU")
	body, err := c.get(ctx, fmt.Sprintf("/tv/%d", tmdbID), qv)
	if err != nil {
		return nil, err
	}
	var d struct {
		Seasons []struct {
			Number   int    `json:"season_number"`
			Episodes int    `json:"episode_count"`
			AirDate  string `json:"air_date"`
			Name     string `json:"name"`
		} `json:"seasons"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("tmdb: parse tv seasons %d: %w", tmdbID, err)
	}
	out := make([]SeasonInfo, 0, len(d.Seasons))
	for _, s := range d.Seasons {
		if s.Number <= 0 || s.Episodes <= 0 {
			continue
		}
		year := 0
		if len(s.AirDate) >= 4 {
			year = atoi(s.AirDate[:4])
		}
		out = append(out, SeasonInfo{Number: s.Number, Episodes: s.Episodes, Year: year, Name: s.Name, AirDate: s.AirDate})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// SeasonAt переводит сквозной номер серии (сборники: «001 seriya») в сезон и
// серию по структуре TMDB; (0, 0) — если номер вне диапазона/структура пуста.
func SeasonAt(structure []SeasonInfo, abs int) (season, episode int) {
	if abs <= 0 {
		return 0, 0
	}
	n := 0
	for _, s := range structure {
		n += s.Episodes
		if abs <= n {
			return s.Number, abs - (n - s.Episodes)
		}
	}
	return 0, 0
}

// SeasonByYear возвращает сезон TMDB по году выпуска: «сезоны» трекеров
// дробнее официальных (трекерный S14 (2023) — это TMDB-сезон 10), поэтому
// год надёжнее номера. 0 — если год неизвестен или подходящего сезона нет.
func SeasonByYear(structure []SeasonInfo, year int) int {
	if year <= 0 {
		return 0
	}
	for _, s := range structure {
		if s.Year == year {
			return s.Number
		}
	}
	// Года нет точного — берём ближайший сезон НЕ ПОЗЖЕ года.
	best := 0
	for _, s := range structure {
		if s.Year > 0 && s.Year <= year && s.Number > best {
			best = s.Number
		}
	}
	return best
}

// Structure возвращает структуру сезонов (кэш на час: спрашивается на каждый
// разбор раздач, а меняется редко). nil — вызывающий работает без сопоставления.
func (c *Client) Structure(ctx context.Context, tmdbID int64) []SeasonInfo {
	if c == nil || tmdbID <= 0 || c.seasonsCache == nil {
		return nil
	}
	c.seasonsCache.mu.Lock()
	if e, ok := c.seasonsCache.m[tmdbID]; ok && time.Since(e.at) < time.Hour {
		c.seasonsCache.mu.Unlock()
		return e.list
	}
	c.seasonsCache.mu.Unlock()
	list, err := c.SeasonStructure(ctx, tmdbID)
	if err != nil || len(list) == 0 {
		return nil
	}
	c.seasonsCache.mu.Lock()
	c.seasonsCache.m[tmdbID] = seasonEntry{list: list, at: time.Now()}
	c.seasonsCache.mu.Unlock()
	return list
}

type seasonEntry struct {
	list []SeasonInfo
	at   time.Time
}

type seasonsCache struct {
	mu sync.Mutex
	m  map[int64]seasonEntry
}

// externalIDMedia возвращает IMDb-ссылку TMDB (пустая — если нет).
func (c *Client) externalIDMedia(ctx context.Context, media string, tmdbID int64) (string, error) {
	body, err := c.get(ctx, fmt.Sprintf("/%s/%d/external_ids", media, tmdbID), url.Values{})
	if err != nil {
		return "", err
	}
	var r struct {
		IMDBID string `json:"imdb_id"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}
	return strings.TrimSpace(r.IMDBID), nil
}

// movieDetail — детали фильма TMDB; у /find нет жанров и длительности, поэтому
// поля GenreIDs/Genres/Runtime заполняются по наличию.
type movieDetail struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Overview      string `json:"overview"`
	ReleaseDate   string `json:"release_date"`
	Runtime       int    `json:"runtime"`
	GenreIDs      []int  `json:"genre_ids"`
	Genres        []struct {
		ID int `json:"id"`
	} `json:"genres"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int64   `json:"vote_count"`
	PosterPath  string  `json:"poster_path"`
}

// tvDetail — результат /find по ТВ-сериалу (tv_results) или /tv/{id}.
type tvDetail struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	Overview     string `json:"overview"`
	FirstAirDate string `json:"first_air_date"`
	GenreIDs     []int  `json:"genre_ids"`
	Genres       []struct {
		ID int `json:"id"`
	} `json:"genres"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int64   `json:"vote_count"`
	PosterPath  string  `json:"poster_path"`
}

// ByID возвращает фильм TMDB по ID (детали) — только фильмы (/movie).
func (c *Client) ByID(ctx context.Context, tmdbID int64) (Film, error) {
	qv := url.Values{}
	qv.Set("language", "ru-RU")
	body, err := c.get(ctx, fmt.Sprintf("/movie/%d", tmdbID), qv)
	if err != nil {
		return Film{}, err
	}
	var d movieDetail
	if err := json.Unmarshal(body, &d); err != nil {
		return Film{}, fmt.Errorf("tmdb: parse by id %d: %w", tmdbID, err)
	}
	f := detailToFilm(d)
	f.TMDBID = tmdbID
	return f, nil
}

// ByIDKind выбирает эндпоинт по типу: tvSeries/tvMiniSeries → /tv/{id}.
// Для сериала /movie вернул бы ДРУГОЙ фильм с тем же числовым id.
func (c *Client) ByIDKind(ctx context.Context, tmdbID int64, kind string) (Film, error) {
	if kind == "tvSeries" || kind == "tvMiniSeries" {
		qv := url.Values{}
		qv.Set("language", "ru-RU")
		body, err := c.get(ctx, fmt.Sprintf("/tv/%d", tmdbID), qv)
		if err != nil {
			return Film{}, err
		}
		var d tvDetail
		if err := json.Unmarshal(body, &d); err != nil {
			return Film{}, fmt.Errorf("tmdb: parse tv %d: %w", tmdbID, err)
		}
		f := tvToFilm(d)
		f.TMDBID = tmdbID
		return f, nil
	}
	return c.ByID(ctx, tmdbID)
}

// FindByIMDB находит фильм или сериал TMDB по IMDb-ссылке (movie_results,
// затем tv_results) — резерв для записей без tmdb_id.
func (c *Client) FindByIMDB(ctx context.Context, imdbID string) (Film, error) {
	qv := url.Values{}
	qv.Set("external_source", "imdb_id")
	qv.Set("language", "ru-RU")
	body, err := c.get(ctx, "/find/"+url.PathEscape(imdbID), qv)
	if err != nil {
		return Film{}, err
	}
	var r struct {
		MovieResults []movieDetail `json:"movie_results"`
		TVResults    []tvDetail    `json:"tv_results"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Film{}, fmt.Errorf("tmdb: parse find %s: %w", imdbID, err)
	}
	if len(r.MovieResults) > 0 {
		f := detailToFilm(r.MovieResults[0])
		f.IMDBID = imdbID
		return f, nil
	}
	if len(r.TVResults) > 0 {
		f := tvToFilm(r.TVResults[0])
		f.IMDBID = imdbID
		return f, nil
	}
	return Film{}, fmt.Errorf("tmdb: no movie or tv for %s", imdbID)
}

// detailToFilm преобразует movieDetail в Film.
func detailToFilm(d movieDetail) Film {
	year := 0
	if len(d.ReleaseDate) >= 4 {
		year = atoi(d.ReleaseDate[:4])
	}
	return Film{
		TMDBID:      d.ID,
		Title:       strings.TrimSpace(d.OriginalTitle),
		TitleRU:     strings.TrimSpace(d.Title),
		Kind:        "feature",
		Year:        year,
		ReleaseDate: strings.TrimSpace(d.ReleaseDate),
		Rating:      d.VoteAverage,
		Votes:       d.VoteCount,
		MovieLength: d.Runtime,
		OverviewRU:  strings.TrimSpace(d.Overview),
		Genres:      detailGenres(d.GenreIDs, d.Genres),
		PosterURL:   posterURL(d.PosterPath),
	}
}

// tvToFilm преобразует tvDetail в Film (тип tvSeries).
func tvToFilm(d tvDetail) Film {
	year := 0
	if len(d.FirstAirDate) >= 4 {
		year = atoi(d.FirstAirDate[:4])
	}
	return Film{
		TMDBID:      d.ID,
		Title:       strings.TrimSpace(d.OriginalName),
		TitleRU:     strings.TrimSpace(d.Name),
		Kind:        "tvSeries",
		Year:        year,
		ReleaseDate: strings.TrimSpace(d.FirstAirDate),
		Rating:      d.VoteAverage,
		Votes:       d.VoteCount,
		OverviewRU:  strings.TrimSpace(d.Overview),
		Genres:      detailGenres(d.GenreIDs, d.Genres),
		PosterURL:   posterURL(d.PosterPath),
	}
}

// detailGenres собирает жанры: по genre_ids, иначе по genres; имена — как в IMDb.
func detailGenres(ids []int, genres []struct {
	ID int `json:"id"`
}) []string {
	if len(ids) == 0 {
		for _, g := range genres {
			ids = append(ids, g.ID)
		}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if n := genreName(id); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// Overview возвращает описание фильма или сериала на указанном языке.
func (c *Client) Overview(ctx context.Context, tmdbID int64, kind, lang string) string {
	path := fmt.Sprintf("/movie/%d", tmdbID)
	if kind == "tvSeries" || kind == "tvMiniSeries" {
		path = fmt.Sprintf("/tv/%d", tmdbID)
	}
	qv := url.Values{}
	qv.Set("language", lang)
	body, err := c.get(ctx, path, qv)
	if err != nil {
		return ""
	}
	var d struct {
		Overview string `json:"overview"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return ""
	}
	return strings.TrimSpace(d.Overview)
}

// Person — человек из титров TMDB: ID (ключ таблицы переводов имён) и имя на языке запроса.
type Person struct {
	ID   int64
	Name string
}

// PersonName — имена одного человека: оригинал (исходное написание TMDB) и русский перевод
// (пустой, если TMDB русского имени не знает).
type PersonName struct {
	ID     int64
	Name   string
	NameRU string
}

// Credits — титры фильма: режиссёр и до 6 главных ролей в исходном написании плюс пары имён
// «оригинал → русский» для таблицы переводов.
type Credits struct {
	Director string
	Actors   []string
	Names    []PersonName
}

// creditsResult — титры одного языка с id людей.
type creditsResult struct {
	Director Person
	Actors   []Person
}

// maxCreditsActors — сколько главных ролей берём из титров (для карточки хватает).
const maxCreditsActors = 6

// Credits возвращает режиссёра (первый с job "Director") и до 6 актёров в исходном написании TMDB:
// используется для сверки при поиске по названию и заполнения карточки.
func (c *Client) Credits(ctx context.Context, tmdbID int64, kind string) (director string, actors []string, err error) {
	cr, err := c.credits(ctx, tmdbID, kind, "")
	if err != nil {
		return "", nil, err
	}
	return cr.Director.Name, personNames(cr.Actors), nil
}

// CreditsLocalized запрашивает титры дважды: в исходном написании и по-русски (TMDB отдаёт имена
// на языке запроса). Русские имена сопоставляются по id человека — из пар «оригинал → перевод»
// карточка показывает режиссёра/актёров на языке сайта.
func (c *Client) CreditsLocalized(ctx context.Context, tmdbID int64, kind string) (Credits, error) {
	en, err := c.credits(ctx, tmdbID, kind, "")
	if err != nil {
		return Credits{}, err
	}
	ru, err := c.credits(ctx, tmdbID, kind, "ru-RU")
	if err != nil {
		// Без русского варианта пары неполные — не сохраняем ничего, задача повторит позже.
		return Credits{}, err
	}
	ruByID := make(map[int64]string, len(ru.Actors)+1)
	for _, p := range append([]Person{ru.Director}, ru.Actors...) {
		if p.ID > 0 && p.Name != "" {
			ruByID[p.ID] = p.Name
		}
	}
	out := Credits{Director: en.Director.Name, Actors: personNames(en.Actors)}
	for _, p := range append([]Person{en.Director}, en.Actors...) {
		if p.ID <= 0 || p.Name == "" {
			continue
		}
		nameRU := ruByID[p.ID]
		if nameRU == p.Name {
			nameRU = "" // TMDB отдал то же написание — перевода нет
		}
		out.Names = append(out.Names, PersonName{ID: p.ID, Name: p.Name, NameRU: nameRU})
	}
	return out, nil
}

// credits — титры на одном языке (lang пустой — исходное написание): режиссёр и до 6 актёров.
func (c *Client) credits(ctx context.Context, tmdbID int64, kind, lang string) (creditsResult, error) {
	path := fmt.Sprintf("/movie/%d/credits", tmdbID)
	if kind == "tvSeries" || kind == "tvMiniSeries" {
		path = fmt.Sprintf("/tv/%d/credits", tmdbID)
	}
	q := url.Values{}
	if lang != "" {
		q.Set("language", lang)
	}
	body, err := c.get(ctx, path, q)
	if err != nil {
		return creditsResult{}, err
	}
	var r struct {
		Crew []struct {
			ID   int64  `json:"id"`
			Job  string `json:"job"`
			Name string `json:"name"`
		} `json:"crew"`
		Cast []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"cast"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return creditsResult{}, err
	}
	var out creditsResult
	for _, m := range r.Crew {
		if m.Job == "Director" && strings.TrimSpace(m.Name) != "" {
			out.Director = Person{ID: m.ID, Name: strings.TrimSpace(m.Name)}
			break
		}
	}
	for _, m := range r.Cast {
		if len(out.Actors) >= maxCreditsActors {
			break
		}
		if n := strings.TrimSpace(m.Name); n != "" {
			out.Actors = append(out.Actors, Person{ID: m.ID, Name: n})
		}
	}
	return out, nil
}

// personNames — имена людей из титров (в порядке следования).
func personNames(people []Person) []string {
	out := make([]string, 0, len(people))
	for _, p := range people {
		if p.Name != "" {
			out = append(out, p.Name)
		}
	}
	return out
}

// getPage выполняет GET и декодирует пагинированный ответ.
func (c *Client) getPage(ctx context.Context, path string, q url.Values) (page, error) {
	var p page
	body, err := c.get(ctx, path, q)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, fmt.Errorf("tmdb: parse: %w", err)
	}
	return p, nil
}

// get выполняет GET с ретраями на 429/5xx.
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 700 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.bearerToken())
		req.Header.Set("User-Agent", userAgent)

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		_ = resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("tmdb: %d: %s", resp.StatusCode, truncate(string(body)))
			continue
		default:
			return nil, fmt.Errorf("tmdb: %d: %s", resp.StatusCode, truncate(string(body)))
		}
	}
	return nil, lastErr
}

// genreName возвращает английское имя жанра TMDB (как в IMDb, для словаря
// фронтенда); "" — жанр неизвестен.
func genreName(id int) string {
	switch id {
	case 28:
		return "Action"
	case 12:
		return "Adventure"
	case 16:
		return "Animation"
	case 35:
		return "Comedy"
	case 80:
		return "Crime"
	case 99:
		return "Documentary"
	case 18:
		return "Drama"
	case 10751:
		return "Family"
	case 14:
		return "Fantasy"
	case 36:
		return "History"
	case 27:
		return "Horror"
	case 10402:
		return "Music"
	case 9648:
		return "Mystery"
	case 10749:
		return "Romance"
	case 878:
		return "Sci-Fi"
	case 10770:
		return "TV Movie"
	case 53:
		return "Thriller"
	case 10752:
		return "War"
	case 37:
		return "Western"
	case 10759:
		return "Action"
	case 10762:
		return "Kids"
	case 10764:
		return "Reality-TV"
	case 10765:
		return "Sci-Fi"
	case 10766:
		return "Soap"
	case 10767:
		return "Talk-Show"
	case 10768:
		return "War"
	}
	return ""
}

func posterURL(path string) string {
	if path == "" {
		return ""
	}
	return "https://image.tmdb.org/t/p/w500" + path
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// bearerToken — токен для Authorization: Bearer (Read Access Token, иначе API Key).
func (c *Client) bearerToken() string {
	if c.accessToken != "" {
		return c.accessToken
	}
	return c.apiKey
}

func truncate(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
