// Package tmdb — клиент The Movie Database (TMDB). Свободный API-ключ
// без жёстких суточных лимитов (мягкий потолок ~40 req/s). Заменяет
// Кинопоиск (токен с ограничениями) для «популярных», «топ-250» и
// внешнего поиска.
package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
}

// NewClient создаёт клиент TMDB. Доступны два способа авторизации
// (оба из env): accessToken — «API Read Access Token» (рекомендуемый,
// идёт в Authorization: Bearer), apiKey — «API Key» (v3, запасной).
// Если accessToken задан — используется он, иначе apiKey.
func NewClient(apiKey, accessToken, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		apiKey:      strings.TrimSpace(apiKey),
		accessToken: strings.TrimSpace(accessToken),
		baseURL:     strings.TrimRight(baseURL, "/"),
		http:        &http.Client{Timeout: 30 * time.Second},
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
	// TMDBID — идентификатор фильма на TMDB.
	TMDBID int64
	// IMDBID — IMDb-ссылка (из /movie/{id}/external_ids; может быть пустой).
	IMDBID string
	// Title — оригинальное название.
	Title string
	// TitleRU — русское название (title при language=ru-RU).
	TitleRU string
	// Kind — нормализованный тип (feature/tvSeries/...).
	Kind string
	Year int
	// ReleaseDate — дата выпуска "YYYY-MM-DD" (может быть пустой).
	ReleaseDate string
	// Rating — рейтинг TMDB (vote_average), Votes — число голосов.
	Rating float64
	Votes  int64
	// MovieLength — длительность фильма в минутах (0 — неизвестно; для
	// сериалов — 0).
	MovieLength int
	// OverviewRU — русское описание.
	OverviewRU string
	// Genres — жанры (английские имена, в стиле жанров IMDb).
	Genres []string
	// PosterURL — постер.
	PosterURL string
	// Rank — позиция в чарте (1-based; 0 для обычного поиска).
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

// Search ищет фильмы на TMDB по запросу q и возвращает до limit
// результатов (с IMDb-ссылками для дедупликации каталога).
func (c *Client) Search(ctx context.Context, q string, limit int) ([]Film, error) {
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
	return c.toFilms(ctx, items, 0)
}

// SearchLite ищет фильмы по названию ОДНИМ запросом (без дозаполнения
// IMDb-ссылок через external_ids — то есть без лишних запросов на
// кандидата). Используется фоновой джобой обновления рейтингов для
// сверки кандидатов по названию/году, где IMDb-ссылки не нужны.
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

// tvToFilms преобразует результаты /tv/* в Film (kind=tvSeries), дозаполняя
// IMDb-ссылки через /tv/{id}/external_ids.
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
		year := 0
		if len(r.FirstAirDate) >= 4 {
			year = atoi(r.FirstAirDate[:4])
		}
		genres := make([]string, 0, len(r.GenreIDs))
		for _, id := range r.GenreIDs {
			if g := genreName(id); g != "" {
				genres = append(genres, g)
			}
		}
		f := Film{
			TMDBID:      r.ID,
			IMDBID:      extIDs[i],
			Title:       strings.TrimSpace(r.OriginalName),
			TitleRU:     strings.TrimSpace(r.Name),
			Kind:        "tvSeries",
			Year:        year,
			ReleaseDate: strings.TrimSpace(r.FirstAirDate),
			Rating:      r.VoteAverage,
			Votes:       r.VoteCount,
			OverviewRU:  strings.TrimSpace(r.Overview),
			Genres:      genres,
			PosterURL:   posterURL(r.PosterPath),
		}
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

// toFilms преобразует результаты TMDB в Film, дозаполняя IMDb-ссылки
// через /movie/{id}/external_ids (startRank — начальная позиция чарта).
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
	// Параллельно запрашиваем IMDb-ссылки (external_ids) — по одному
	// запросу на фильм, с ограничением параллельности.
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
			// Нет ссылки — не критично; запись останется без IMDb-дубля.
			extIDs[e.i] = ""
		}
	}

	films := make([]Film, 0, len(results))
	for i, r := range results {
		year := 0
		if len(r.ReleaseDate) >= 4 {
			year = atoi(r.ReleaseDate[:4])
		}
		genres := make([]string, 0, len(r.GenreIDs))
		for _, id := range r.GenreIDs {
			if g := genreName(id); g != "" {
				genres = append(genres, g)
			}
		}
		f := Film{
			TMDBID:      r.ID,
			IMDBID:      extIDs[i],
			Title:       strings.TrimSpace(r.OriginalTitle),
			TitleRU:     strings.TrimSpace(r.Title),
			Kind:        "feature",
			Year:        year,
			ReleaseDate: strings.TrimSpace(r.ReleaseDate),
			Rating:      r.VoteAverage,
			Votes:       r.VoteCount,
			OverviewRU:  strings.TrimSpace(r.Overview),
			Genres:      genres,
			PosterURL:   posterURL(r.PosterPath),
		}
		if startRank > 0 {
			f.Rank = startRank + i
		}
		films = append(films, f)
	}
	return films, nil
}

// externalIDMedia возвращает IMDb-ссылку TMDB (пустая — если нет) для
// фильма (/movie) или сериала (/tv).
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

// movieDetail — ответ деталей фильма TMDB (/movie/{id} или результат /find).
// У /find нет жанров и длительности в полной форме (только genre_ids),
// поэтому поля GenreIDs/Genres/Runtime заполняются по наличию.
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

// ByID возвращает фильм TMDB по его ID (детали, включая свежий рейтинг
// и дату выпуска). Только для фильмов (/movie). Используется фоновой
// джобой обновления рейтингов.
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

// ByIDKind возвращает фильм или сериал TMDB по id, выбирая эндпоинт по
// типу: kind "tvSeries"/"tvMiniSeries" → /tv/{id}, иначе /movie/{id}.
// Нужно, чтобы для сериалов с привязанным tmdb_id не дёргать /movie —
// это вернуло бы ДРУГОЙ фильм с тем же числовым id.
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

// FindByIMDB находит фильм (или ТВ-сериал) TMDB по IMDb-ссылке
// (external_source=imdb_id). Сначала ищет в movie_results, затем — в
// tv_results (для сериалов/мини-сериалов). Резерв для записей без tmdb_id.
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

// detailGenres собирает жанры фильма/сериала: по genre_ids (если есть),
// иначе по массиву genres. Имена приводятся к английскому виду (в стиле
// жанров IMDb) через genreName.
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

// Overview возвращает описание (overview) фильма или сериала TMDB на
// указанном языке. Используется для заполнения английского описания.
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

// Credits возвращает режиссёра (первый с job "Director") и до 6 актёров
// фильма или сериала TMDB. Используется для сверки при поиске по названию,
// чтобы убедиться, что найден именно нужный фильм, и для заполнения
// режиссёра/актёров. kind — "tvSeries"/"tvMiniSeries" для сериалов.
func (c *Client) Credits(ctx context.Context, tmdbID int64, kind string) (director string, actors []string, err error) {
	path := fmt.Sprintf("/movie/%d/credits", tmdbID)
	if kind == "tvSeries" || kind == "tvMiniSeries" {
		path = fmt.Sprintf("/tv/%d/credits", tmdbID)
	}
	body, err := c.get(ctx, path, url.Values{})
	if err != nil {
		return "", nil, err
	}
	var r struct {
		Crew []struct {
			Job  string `json:"job"`
			Name string `json:"name"`
		} `json:"crew"`
		Cast []struct {
			Name string `json:"name"`
		} `json:"cast"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", nil, err
	}
	for _, m := range r.Crew {
		if m.Job == "Director" && strings.TrimSpace(m.Name) != "" {
			director = strings.TrimSpace(m.Name)
			break
		}
	}
	for i, m := range r.Cast {
		if i >= 6 {
			break
		}
		if n := strings.TrimSpace(m.Name); n != "" {
			actors = append(actors, n)
		}
	}
	return director, actors, nil
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

// genreName возвращает английское имя жанра TMDB (в стиле жанров IMDb,
// чтобы совпадать со словарём фронтенда). "" — жанр неизвестен.
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

// bearerToken возвращает токен для Authorization: Bearer — приоритет у
// Read Access Token, запасной вариант — API Key.
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
