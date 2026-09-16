package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zeoril1/video_viewer/internal/imdb"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

const schema = `
CREATE TABLE IF NOT EXISTS films (
	imdb_id      TEXT PRIMARY KEY,
	title        TEXT NOT NULL,
	title_ru     TEXT,
	kind         TEXT,
	rating       DOUBLE PRECISION,
	votes        BIGINT,
	plot         TEXT,
	plot_ru      TEXT,
	ruwiki_title TEXT,
	genres       TEXT,
	poster_url   TEXT,
	rank_top250  INT,
	rank_popular INT,
	tmdb_id        TEXT,
	rating_tmdb    DOUBLE PRECISION,
	votes_tmdb     BIGINT,
	rank_tmdb_top250 INT,
	rank_tmdb_popular INT,
	size         TEXT,
	seasons      INT,
	movie_length INT,
	countries    TEXT,
	director     TEXT,
	actors       TEXT,
	release_date TEXT,
	rating_updated_at TIMESTAMPTZ,
	tmdb_not_found BOOLEAN NOT NULL DEFAULT false,
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// yearExpr — год фильма из release_date ("YYYY-MM-DD": первые 4 символа). Колонка year удалена;
// release_date — единственный источник года (сортировка, поиск, сопоставление с TMDB).
const yearExpr = `COALESCE(NULLIF(SUBSTRING(release_date FROM 1 FOR 4), '')::int, 0)`

// releaseDateUpsert — обновление release_date в ON CONFLICT: точная дата ("2024-05-10") не затирается
// «фолбэком по году» ("2024-01-01") из источника без даты; фолбэк заменяется точной датой, когда она приходит.
const releaseDateUpsert = `release_date = CASE
		WHEN NULLIF(EXCLUDED.release_date, '') IS NULL THEN films.release_date
		WHEN films.release_date = SUBSTRING(EXCLUDED.release_date FROM 1 FOR 4) || '-01-01' THEN EXCLUDED.release_date
		WHEN EXCLUDED.release_date = SUBSTRING(films.release_date FROM 1 FOR 4) || '-01-01' THEN films.release_date
		ELSE COALESCE(NULLIF(EXCLUDED.release_date, ''), films.release_date)
	END`

// releaseDateVal возвращает дату выпуска для записи в БД: полную дату, фолбэк по году "YYYY-01-01" (год берётся из release_date) или "".
func releaseDateVal(releaseDate string, year int) string {
	if strings.TrimSpace(releaseDate) != "" {
		return releaseDate
	}
	if year > 0 {
		return fmt.Sprintf("%04d-01-01", year)
	}
	return ""
}

// filmCols — колонки для SELECT (везде COALESCE, чтобы сканировать без NULL).
// genres хранится как JSON-строка: pgx stdlib не сканирует text[] в []string.
const filmCols = `imdb_id, title, COALESCE(title_ru, ''), COALESCE(kind, ''), ` + yearExpr + `,
	COALESCE(rating, 0), COALESCE(votes, 0), COALESCE(plot, ''),
	COALESCE(plot_ru, ''), COALESCE(genres, '[]'), COALESCE(poster_url, ''),
	rank_top250, rank_popular,
	COALESCE(tmdb_id, ''), COALESCE(rating_tmdb, 0), COALESCE(votes_tmdb, 0), rank_tmdb_top250, rank_tmdb_popular,
	COALESCE(size, ''), COALESCE(seasons, 0),
	COALESCE(movie_length, 0), COALESCE(countries, '[]'), COALESCE(director, ''), COALESCE(actors, '[]'),
	COALESCE(release_date, ''),
	COALESCE(tmdb_not_found, false)`

// filmColsLite — лёгкий набор колонок для списков каталога: без тяжёлых описаний plot/plot_ru (догружаются при открытии фильма).
const filmColsLite = `imdb_id, title, COALESCE(title_ru, ''), COALESCE(kind, ''), ` + yearExpr + `,
	COALESCE(rating, 0), COALESCE(votes, 0),
	COALESCE(genres, '[]'), COALESCE(poster_url, ''),
	rank_top250, rank_popular,
	COALESCE(tmdb_id, ''), COALESCE(rating_tmdb, 0), COALESCE(votes_tmdb, 0), rank_tmdb_top250, rank_tmdb_popular,
	COALESCE(size, ''), COALESCE(seasons, 0),
	COALESCE(movie_length, 0), COALESCE(countries, '[]'), COALESCE(director, ''), COALESCE(actors, '[]'),
	COALESCE(release_date, '')`

// Film — фильм из таблицы films (JSON-форма для API).
type Film struct {
	IMDBID          string   `json:"imdb_id"`
	Title           string   `json:"title"`
	TitleRU         string   `json:"title_ru"`
	Kind            string   `json:"kind,omitempty"`
	Year            int      `json:"year"`
	ReleaseDate     string   `json:"release_date,omitempty"` // "YYYY-MM-DD"
	Rating          float64  `json:"rating"`
	Votes           int64    `json:"votes"`
	Plot            string   `json:"plot"`
	PlotRU          string   `json:"plot_ru"`
	Genres          []string `json:"genres"`
	PosterURL       string   `json:"poster_url"`
	RankTop250      *int     `json:"rank_top250,omitempty"`
	RankPopular     *int     `json:"rank_popular,omitempty"`
	TMDBID          string   `json:"tmdb_id,omitempty"`
	RatingTMDB      float64  `json:"rating_tmdb,omitempty"`
	VotesTMDB       int64    `json:"votes_tmdb,omitempty"`
	RankTMDBTop250  *int     `json:"rank_tmdb_top250,omitempty"`
	RankTMDBPopular *int     `json:"rank_tmdb_popular,omitempty"`
	Size            string   `json:"size,omitempty"`
	Seasons         int      `json:"seasons,omitempty"`
	MovieLength     int      `json:"movie_length,omitempty"`
	Countries       []string `json:"countries,omitempty"`
	Director        string   `json:"director,omitempty"`
	Actors          []string `json:"actors,omitempty"`
	// RatingUpdatedAt — когда последний раз обновлялся рейтинг (заполняется только джобой обновления рейтингов; в списках — нулевое).
	RatingUpdatedAt time.Time `json:"-"`
	// TMDBNotFound — совпадение на TMDB не найдено (отдельная таблица на админ-странице, из «пустых полей» исключается).
	TMDBNotFound bool `json:"tmdb_not_found,omitempty"`
}

// EnsureSchema создаёт таблицу films, если её нет, и применяет миграции (новые колонки локализации) к существующим.
func (r *Repo) EnsureSchema(ctx context.Context) error {
	if _, err := r.conn.ExecContext(ctx, seriesSchema); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS title_ru TEXT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS plot_ru TEXT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS ruwiki_title TEXT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS kind TEXT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS seasons INT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS movie_length INT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS countries TEXT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS director TEXT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS actors TEXT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS release_date TEXT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS rating_updated_at TIMESTAMPTZ"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS tmdb_not_found BOOLEAN NOT NULL DEFAULT false"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS tmdb_id TEXT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS rating_tmdb DOUBLE PRECISION"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS votes_tmdb BIGINT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS rank_tmdb_top250 INT"); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE films ADD COLUMN IF NOT EXISTS rank_tmdb_popular INT"); err != nil {
		return err
	}
	if err := r.ensureSourcesSchema(ctx); err != nil {
		return err
	}
	// Таблицы авторизации (users/sessions) и истории просмотра.
	if err := r.ensureAuthSchema(ctx); err != nil {
		return err
	}
	if err := r.ensureHistorySchema(ctx); err != nil {
		return err
	}
	// Таблицы IPTV (плейлисты, каналы, телепрограмма).
	return r.ensureIPTVSchema(ctx)
}

// UpsertFilms сохраняет фильмы чарта IMDb: новые вставляет, существующие обновляет (метаданные и позиция в чарте).
// Возвращает число вставленных записей.
func (r *Repo) UpsertFilms(ctx context.Context, films []imdb.Film, chart imdb.ChartKind) (int, error) {
	if len(films) == 0 {
		return 0, nil
	}

	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO films (imdb_id, title, title_ru, kind, release_date, rating, votes, plot, plot_ru, genres, poster_url, seasons, rank_top250, rank_popular)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (imdb_id) DO UPDATE SET
			title        = EXCLUDED.title,
			title_ru     = COALESCE(NULLIF(EXCLUDED.title_ru, ''),  films.title_ru),
			kind         = COALESCE(NULLIF(EXCLUDED.kind, ''),  films.kind),
			`+releaseDateUpsert+`,
			rating       = EXCLUDED.rating,
			votes        = EXCLUDED.votes,
			plot         = COALESCE(NULLIF(EXCLUDED.plot, ''),  films.plot),
			plot_ru      = COALESCE(NULLIF(EXCLUDED.plot_ru, ''), films.plot_ru),
			genres       = EXCLUDED.genres,
			poster_url   = EXCLUDED.poster_url,
			seasons      = COALESCE(EXCLUDED.seasons, films.seasons),
			rank_top250  = COALESCE(EXCLUDED.rank_top250,  films.rank_top250),
			rank_popular = COALESCE(EXCLUDED.rank_popular, films.rank_popular),
			updated_at   = now()
		RETURNING (xmax = 0) AS is_insert
	`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmt.Close() }()

	var (
		topRank, popRank sql.NullInt64
		inserted         int
	)
	for _, f := range films {
		topRank.Valid, popRank.Valid = false, false
		switch chart {
		case imdb.ChartTop250:
			topRank = sql.NullInt64{Int64: int64(f.Rank), Valid: f.Rank > 0}
		case imdb.ChartPopular:
			popRank = sql.NullInt64{Int64: int64(f.Rank), Valid: f.Rank > 0}
		}

		var isInsert bool
		err := stmt.QueryRowContext(ctx,
			f.IMDBID, f.Title, nullStr(f.TitleRU), nullStr(f.Kind), releaseDateVal(f.ReleaseDate, f.Year),
			nullFloat(f.Rating), nullInt(f.Votes),
			nullStr(f.Plot), nullStr(f.PlotRU), genresJSON(f.Genres), f.PosterURL, nullInt(f.Seasons), topRank, popRank,
		).Scan(&isInsert)
		if err != nil {
			return inserted, fmt.Errorf("upsert %s: %w", f.IMDBID, err)
		}
		if isInsert {
			inserted++
		}
	}

	if err := tx.Commit(); err != nil {
		return inserted, err
	}
	return inserted, nil
}

// ListFilmsLite возвращает фильмы каталога лёгким набором колонок (без описаний plot/plot_ru) — для страниц и метаданных.
// Порядок: топ-250, популярные, затем остальные.
func (r *Repo) ListFilmsLite(ctx context.Context) ([]Film, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT `+filmColsLite+`
		FROM films
		ORDER BY
			CASE WHEN rank_top250  IS NOT NULL THEN 0
			     WHEN rank_popular IS NOT NULL THEN 1
			     ELSE 2 END,
			COALESCE(rank_top250, rank_popular, 2147483647),
			created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var films []Film
	for rows.Next() {
		f, err := scanFilmLite(rows)
		if err != nil {
			return nil, err
		}
		films = append(films, f)
	}
	return films, rows.Err()
}

// GetByIMDBID возвращает фильм по IMDb ID.
func (r *Repo) GetByIMDBID(ctx context.Context, id string) (Film, bool, error) {
	row := r.conn.QueryRowContext(ctx,
		`SELECT `+filmCols+` FROM films WHERE imdb_id = $1`, id)
	f, err := scanFilm(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Film{}, false, nil
	}
	if err != nil {
		return Film{}, false, err
	}
	return f, true, nil
}

// FilmsMissingTitleRU возвращает IMDb ID фильмов без русского названия (для пакетной локализации названий).
func (r *Repo) FilmsMissingTitleRU(ctx context.Context, limit int) ([]string, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT imdb_id FROM films
		WHERE title_ru IS NULL OR title_ru = ''
		ORDER BY created_at
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetTitleLocalization сохраняет русское название и заголовок статьи в русской Википедии для фильма.
func (r *Repo) SetTitleLocalization(ctx context.Context, imdbID, titleRU, ruWiki string) error {
	_, err := r.conn.ExecContext(ctx, `
		UPDATE films SET
			title_ru     = COALESCE(NULLIF($2, ''), title_ru),
			ruwiki_title = COALESCE(NULLIF($3, ''), ruwiki_title),
			updated_at   = now()
		WHERE imdb_id = $1
	`, imdbID, titleRU, ruWiki)
	return err
}

// RuWikiFilm — фильм с известным заголовком статьи в русской Википедии.
type RuWikiFilm struct {
	IMDBID      string
	RuWikiTitle string
}

// FilmsWithRuWikiNoPlot возвращает фильмы со статьёй в ru-wiki, но без русского описания (для фоновой загрузки описаний).
func (r *Repo) FilmsWithRuWikiNoPlot(ctx context.Context, limit int) ([]RuWikiFilm, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT imdb_id, ruwiki_title FROM films
		WHERE ruwiki_title IS NOT NULL AND ruwiki_title <> ''
		  AND (plot_ru IS NULL OR plot_ru = '')
		ORDER BY created_at
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var films []RuWikiFilm
	for rows.Next() {
		var f RuWikiFilm
		if err := rows.Scan(&f.IMDBID, &f.RuWikiTitle); err != nil {
			return nil, err
		}
		films = append(films, f)
	}
	return films, rows.Err()
}

// SetPlotRU сохраняет русское описание фильма.
func (r *Repo) SetPlotRU(ctx context.Context, imdbID, plotRU string) error {
	_, err := r.conn.ExecContext(ctx, `
		UPDATE films SET plot_ru = COALESCE(NULLIF($2, ''), plot_ru), updated_at = now()
		WHERE imdb_id = $1
	`, imdbID, plotRU)
	return err
}

// SaveFilm сохраняет (или обновляет) одиночный фильм из IMDb (on-demand загрузка, когда фильма нет в БД).
func (r *Repo) SaveFilm(ctx context.Context, f imdb.Film) error {
	_, err := r.conn.ExecContext(ctx, `
		INSERT INTO films (imdb_id, title, title_ru, kind, release_date, rating, votes, plot, plot_ru, genres, poster_url, seasons)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (imdb_id) DO UPDATE SET
			title        = EXCLUDED.title,
			title_ru     = COALESCE(NULLIF(EXCLUDED.title_ru, ''),  films.title_ru),
			kind         = COALESCE(NULLIF(EXCLUDED.kind, ''),  films.kind),
			`+releaseDateUpsert+`,
			rating       = EXCLUDED.rating,
			votes        = EXCLUDED.votes,
			plot         = COALESCE(NULLIF(EXCLUDED.plot, ''),  films.plot),
			plot_ru      = COALESCE(NULLIF(EXCLUDED.plot_ru, ''), films.plot_ru),
			genres       = EXCLUDED.genres,
			poster_url   = EXCLUDED.poster_url,
			seasons      = COALESCE(EXCLUDED.seasons, films.seasons),
			updated_at   = now()
	`, f.IMDBID, f.Title, nullStr(f.TitleRU), nullStr(f.Kind), releaseDateVal(f.ReleaseDate, f.Year),
		nullFloat(f.Rating), nullInt(f.Votes),
		nullStr(f.Plot), nullStr(f.PlotRU), genresJSON(f.Genres), f.PosterURL, nullInt(f.Seasons))
	return err
}

// SaveTMDBFilm сохраняет (или обновляет) одиночный фильм из TMDB (on-demand поиск/открытие).
// Рейтинг TMDB пишется в rating_tmdb, поле rating (IMDb) не затирается.
func (r *Repo) SaveTMDBFilm(ctx context.Context, f tmdb.Film) error {
	_, err := r.conn.ExecContext(ctx, `
		INSERT INTO films (imdb_id, title, title_ru, kind, release_date, plot_ru, genres, poster_url, tmdb_id, rating_tmdb, votes_tmdb)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (imdb_id) DO UPDATE SET
			title        = COALESCE(NULLIF(EXCLUDED.title, ''), films.title),
			title_ru     = COALESCE(NULLIF(EXCLUDED.title_ru, ''), films.title_ru),
			kind         = COALESCE(NULLIF(EXCLUDED.kind, ''), films.kind),
			`+releaseDateUpsert+`,
			plot_ru      = COALESCE(NULLIF(EXCLUDED.plot_ru, ''), films.plot_ru),
			genres       = CASE WHEN EXCLUDED.genres = '[]' THEN films.genres ELSE EXCLUDED.genres END,
			poster_url   = COALESCE(NULLIF(EXCLUDED.poster_url, ''), films.poster_url),
			tmdb_id      = COALESCE(NULLIF(EXCLUDED.tmdb_id, ''), films.tmdb_id),
			rating_tmdb  = COALESCE(EXCLUDED.rating_tmdb, films.rating_tmdb),
			votes_tmdb   = COALESCE(EXCLUDED.votes_tmdb, films.votes_tmdb),
			updated_at   = now()
	`, f.IMDBID, nullStr(f.Title), nullStr(f.TitleRU), nullStr(f.Kind), releaseDateVal(f.ReleaseDate, f.Year),
		nullStr(f.OverviewRU), genresJSON(f.Genres), f.PosterURL, itoa(f.TMDBID), nullFloat(f.Rating), nullInt64(f.Votes))
	return err
}

// UpdateFilmExtras сохраняет расширенные данные карточки (длительность, страна, режиссёр, главные роли)
// из Wikidata. Пустые значения не затирают уже сохранённые.
func (r *Repo) UpdateFilmExtras(ctx context.Context, imdbID string, movieLength int, countries []string, director string, actors []string) error {
	_, err := r.conn.ExecContext(ctx, `
		UPDATE films SET
			movie_length = CASE WHEN COALESCE($2, 0) > 0 THEN $2 ELSE movie_length END,
			countries    = CASE WHEN $3 = '[]' THEN countries ELSE $3 END,
			director     = COALESCE(NULLIF($4, ''), director),
			actors       = CASE WHEN $5 = '[]' THEN actors ELSE $5 END,
			updated_at   = now()
		WHERE imdb_id = $1
	`, imdbID, nullInt(movieLength), genresJSON(countries), director, genresJSON(actors))
	return err
}

// UpdateSeasons сохраняет число сезонов сериала (дозаполнение для записей, сохранённых по источнику без сезонов).
func (r *Repo) UpdateSeasons(ctx context.Context, imdbID string, seasons int) error {
	_, err := r.conn.ExecContext(ctx, `
		UPDATE films SET seasons = $2, updated_at = now() WHERE imdb_id = $1
	`, imdbID, seasons)
	return err
}

// FromIMDB преобразует фильм IMDb в запись БД.
func FromIMDB(f imdb.Film) Film {
	return Film{
		IMDBID:      f.IMDBID,
		Title:       f.Title,
		TitleRU:     f.TitleRU,
		Kind:        f.Kind,
		Year:        f.Year,
		ReleaseDate: f.ReleaseDate,
		Rating:      f.Rating,
		Votes:       int64(f.Votes),
		Plot:        f.Plot,
		PlotRU:      f.PlotRU,
		Genres:      f.Genres,
		PosterURL:   f.PosterURL,
		Seasons:     f.Seasons,
	}
}

func itoa(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

// UpsertTMDBFilms сохраняет фильмы чарта TMDB (top_rated или популярные) в БД. Идентификация — по imdb_id
// (из /movie/{id}/external_ids); записи без IMDb не вставляем (без IMDb-дубля они не попадают в каталог).
// Жанры объединяются с уже сохранёнными. Возвращает число вставленных записей.
func (r *Repo) UpsertTMDBFilms(ctx context.Context, films []tmdb.Film, chart tmdb.ChartKind) (int, error) {
	if len(films) == 0 {
		return 0, nil
	}

	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO films (imdb_id, title, title_ru, kind, release_date, plot_ru, genres, poster_url, tmdb_id, rating_tmdb, votes_tmdb, rank_tmdb_top250, rank_tmdb_popular)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (imdb_id) DO UPDATE SET
			title            = COALESCE(NULLIF(EXCLUDED.title, ''), films.title),
			title_ru         = COALESCE(NULLIF(EXCLUDED.title_ru, ''), films.title_ru),
			kind             = COALESCE(NULLIF(EXCLUDED.kind, ''), films.kind),
			`+releaseDateUpsert+`,
			plot_ru          = COALESCE(NULLIF(EXCLUDED.plot_ru, ''), films.plot_ru),
			genres           = COALESCE(
				(SELECT json_agg(x)::text FROM (
					SELECT DISTINCT g AS x FROM (
						SELECT jsonb_array_elements_text(EXCLUDED.genres::jsonb) AS g
						UNION ALL
						SELECT jsonb_array_elements_text(COALESCE(films.genres, '[]')::jsonb)
					) s
				) t),
				'[]'),
			poster_url        = COALESCE(NULLIF(EXCLUDED.poster_url, ''), films.poster_url),
			tmdb_id           = COALESCE(NULLIF(EXCLUDED.tmdb_id, ''), films.tmdb_id),
			rating_tmdb       = COALESCE(EXCLUDED.rating_tmdb, films.rating_tmdb),
			votes_tmdb        = COALESCE(EXCLUDED.votes_tmdb, films.votes_tmdb),
			rank_tmdb_top250  = COALESCE(EXCLUDED.rank_tmdb_top250, films.rank_tmdb_top250),
			rank_tmdb_popular = COALESCE(EXCLUDED.rank_tmdb_popular, films.rank_tmdb_popular),
			updated_at        = now()
		RETURNING (xmax = 0) AS is_insert
	`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = stmt.Close() }()

	var (
		tmTop, tmPop sql.NullInt64
		inserted     int
	)
	for _, f := range films {
		if f.IMDBID == "" {
			continue // без IMDb-ссылки запись не ключуем
		}
		tmTop.Valid, tmPop.Valid = false, false
		switch chart {
		case tmdb.ChartTopRated:
			tmTop = sql.NullInt64{Int64: int64(f.Rank), Valid: f.Rank > 0}
		case tmdb.ChartPopular:
			tmPop = sql.NullInt64{Int64: int64(f.Rank), Valid: f.Rank > 0}
		}

		var isInsert bool
		err := stmt.QueryRowContext(ctx,
			f.IMDBID, nullStr(f.Title), nullStr(f.TitleRU), nullStr(f.Kind), releaseDateVal(f.ReleaseDate, f.Year),
			nullStr(f.OverviewRU), genresJSON(f.Genres), f.PosterURL, itoa(f.TMDBID), nullFloat(f.Rating),
			nullInt64(f.Votes), tmTop, tmPop,
		).Scan(&isInsert)
		if err != nil {
			return inserted, fmt.Errorf("tmdb upsert %s: %w", f.IMDBID, err)
		}
		if isInsert {
			inserted++
		}
	}

	if err := tx.Commit(); err != nil {
		return inserted, err
	}
	return inserted, nil
}

// chartKindSQL возвращает SQL-выражение набора kind для чарта: «фильмы» (feature + legacy пустой kind)
// или «сериалы» (tvSeries/tvMiniSeries). Чарты фильмов и сериалов делят одни колонки рангов
// (rank_tmdb_top250/rank_tmdb_popular), поэтому чистка идёт ТОЛЬКО по нужному типу — иначе цикл фильмов затирал бы ранги сериалов.
func chartKindSQL(series bool) string {
	if series {
		return "kind IN ('tvSeries','tvMiniSeries')"
	}
	return "kind IN ('feature', '')"
}

// ClearTMDBRank сбрасывает ранги чарта TMDB (top_rated или популярные) для указанного типа контента
// перед загрузкой свежего списка, чтобы сошедшие с чарта записи не сохраняли устаревшие позиции.
func (r *Repo) ClearTMDBRank(ctx context.Context, chart tmdb.ChartKind, series bool) error {
	col := "rank_tmdb_top250"
	if chart == tmdb.ChartPopular {
		col = "rank_tmdb_popular"
	}
	_, err := r.conn.ExecContext(ctx, "UPDATE films SET "+col+" = NULL, updated_at = now() WHERE "+chartKindSQL(series))
	return err
}

// ListPopular — «популярные» одним списком: чарт IMDb (moviemeter, rank_popular) + TMDB (rank_tmdb_popular), без дублей.
func (r *Repo) ListPopular(ctx context.Context) ([]Film, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT `+filmCols+`
		FROM films
		WHERE rank_popular IS NOT NULL OR rank_tmdb_popular IS NOT NULL
		ORDER BY
			CASE WHEN rank_popular IS NOT NULL THEN 0 ELSE 1 END,
			COALESCE(rank_popular, 2147483647),
			COALESCE(rank_tmdb_popular, 2147483647),
			COALESCE(votes, 0) DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var films []Film
	for rows.Next() {
		f, err := scanFilm(rows)
		if err != nil {
			return nil, err
		}
		films = append(films, f)
	}
	return films, rows.Err()
}

// ListTopRated — «лучшие» (rank_top250 и/или rank_tmdb_top250) в порядке чарта, для фильмов (series=false)
// или сериалов (series=true) — подборки «Лучшие фильмы»/«Лучшие сериалы».
func (r *Repo) ListTopRated(ctx context.Context, series bool) ([]Film, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT `+filmCols+`
		FROM films
		WHERE `+chartKindSQL(series)+`
		  AND (rank_top250 IS NOT NULL OR rank_tmdb_top250 IS NOT NULL)
		ORDER BY
			COALESCE(rank_top250, rank_tmdb_top250),
			COALESCE(rank_tmdb_top250, 2147483647),
			imdb_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var films []Film
	for rows.Next() {
		f, err := scanFilm(rows)
		if err != nil {
			return nil, err
		}
		films = append(films, f)
	}
	return films, rows.Err()
}

// ListPopularKind — «популярные» (rank_popular и/или rank_tmdb_popular) в порядке чарта, для фильмов (series=false)
// или сериалов (series=true) — подборки «Популярные фильмы/сериалы».
func (r *Repo) ListPopularKind(ctx context.Context, series bool) ([]Film, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT `+filmCols+`
		FROM films
		WHERE `+chartKindSQL(series)+`
		  AND (rank_popular IS NOT NULL OR rank_tmdb_popular IS NOT NULL)
		ORDER BY
			CASE WHEN rank_popular IS NOT NULL THEN 0 ELSE 1 END,
			COALESCE(rank_popular, 2147483647),
			COALESCE(rank_tmdb_popular, 2147483647),
			COALESCE(votes, 0) DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var films []Film
	for rows.Next() {
		f, err := scanFilm(rows)
		if err != nil {
			return nil, err
		}
		films = append(films, f)
	}
	return films, rows.Err()
}

// FilmsNeedingRatingRefresh возвращает до limit «популярных» фильмов с самым старым (или отсутствующим)
// временем обновления рейтинга — кандидаты для фоновой джобы обновления рейтингов (никогда не обновлявшиеся — первыми,
// порядок детерминирован по imdb_id). Записи без tmdb_id включаются: джоба ищет их по названию с проверкой совпадения.
func (r *Repo) FilmsNeedingRatingRefresh(ctx context.Context, limit int) ([]Film, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT imdb_id, COALESCE(tmdb_id, ''), COALESCE(title, ''), COALESCE(title_ru, ''),
		       `+yearExpr+`, COALESCE(director, ''), COALESCE(actors, '[]'),
		       COALESCE(release_date, ''), COALESCE(rating_updated_at, TIMESTAMPTZ 'epoch')
		FROM films
		WHERE rank_popular IS NOT NULL OR rank_tmdb_popular IS NOT NULL
		ORDER BY rating_updated_at ASC NULLS FIRST, imdb_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var films []Film
	for rows.Next() {
		var (
			f         Film
			actorsStr string
		)
		if err := rows.Scan(&f.IMDBID, &f.TMDBID, &f.Title, &f.TitleRU,
			&f.Year, &f.Director, &actorsStr, &f.ReleaseDate, &f.RatingUpdatedAt); err != nil {
			return nil, err
		}
		f.Actors = parseGenres(actorsStr)
		films = append(films, f)
	}
	return films, rows.Err()
}

// UpdateRating обновляет рейтинг TMDB (и дату выпуска, если пришла), привязывает tmdb_id
// и помечает rating_updated_at, чтобы джоба не переспрашивала слишком часто.
// Рейтинг 0 и пустой tmdb_id не затирают уже сохранённые.
func (r *Repo) UpdateRating(ctx context.Context, imdbID string, ratingTmdb float64, votesTmdb int64, releaseDate, tmdbID string) error {
	_, err := r.conn.ExecContext(ctx, `
		UPDATE films SET
			rating_tmdb       = CASE WHEN $2 > 0 THEN $2 ELSE rating_tmdb END,
			votes_tmdb        = CASE WHEN $3 > 0 THEN $3 ELSE votes_tmdb END,
			release_date      = COALESCE(NULLIF($4, ''), release_date),
			tmdb_id           = COALESCE(NULLIF($5, ''), tmdb_id),
			rating_updated_at = now(),
			updated_at        = now()
		WHERE imdb_id = $1
	`, imdbID, nullFloat(ratingTmdb), nullInt64(votesTmdb), releaseDate, tmdbID)
	return err
}

// FilmsMissingData возвращает до limit записей с пустыми полями, заполняемыми из TMDB
// (kind/title_ru/plot_ru/genres/poster/rating_tmdb/tmdb_id) — для инструмента бэкфилла cmd/backfill, отдельно от джобы.
// Порядок — по времени создания (старые сначала).
func (r *Repo) FilmsMissingData(ctx context.Context, limit int) ([]Film, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT imdb_id, COALESCE(tmdb_id, ''), COALESCE(title, ''), COALESCE(title_ru, ''),
		       `+yearExpr+`, COALESCE(director, ''), COALESCE(actors, '[]'),
		       COALESCE(release_date, ''), COALESCE(rating_updated_at, TIMESTAMPTZ 'epoch')
		FROM films
		WHERE (COALESCE(kind, '') = ''
		   OR COALESCE(title_ru, '') = ''
		   OR COALESCE(plot_ru, '') = ''
		   OR COALESCE(genres, '[]') = '[]'
		   OR COALESCE(poster_url, '') = ''
		   OR COALESCE(rating_tmdb, 0) <= 0
		   OR COALESCE(tmdb_id, '') = '')
		   AND COALESCE(tmdb_not_found, false) = false
		ORDER BY created_at, imdb_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var films []Film
	for rows.Next() {
		var (
			f         Film
			actorsStr string
		)
		if err := rows.Scan(&f.IMDBID, &f.TMDBID, &f.Title, &f.TitleRU,
			&f.Year, &f.Director, &actorsStr, &f.ReleaseDate, &f.RatingUpdatedAt); err != nil {
			return nil, err
		}
		f.Actors = parseGenres(actorsStr)
		films = append(films, f)
	}
	return films, rows.Err()
}

// FilmsMissingDataCount возвращает число записей с пустыми полями (оценка объёма бэкфилла).
func (r *Repo) FilmsMissingDataCount(ctx context.Context) (int, error) {
	var n int
	err := r.conn.QueryRowContext(ctx, `
		SELECT count(*) FROM films
		WHERE (COALESCE(kind, '') = ''
		   OR COALESCE(title_ru, '') = ''
		   OR COALESCE(plot_ru, '') = ''
		   OR COALESCE(genres, '[]') = '[]'
		   OR COALESCE(poster_url, '') = ''
		   OR COALESCE(rating_tmdb, 0) <= 0
		   OR COALESCE(tmdb_id, '') = '')
		   AND COALESCE(tmdb_not_found, false) = false
	`).Scan(&n)
	return n, err
}

// FilmsMissingFull возвращает до limit записей с пустыми полями в полном наборе колонок (для админ-страницы).
// Порядок — по времени создания (старые сначала).
func (r *Repo) FilmsMissingFull(ctx context.Context, limit int) ([]Film, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT `+filmCols+`
		FROM films
		WHERE (COALESCE(kind, '') = ''
		   OR COALESCE(title_ru, '') = ''
		   OR COALESCE(plot_ru, '') = ''
		   OR COALESCE(genres, '[]') = '[]'
		   OR COALESCE(poster_url, '') = ''
		   OR COALESCE(rating_tmdb, 0) <= 0
		   OR COALESCE(tmdb_id, '') = '')
		   AND COALESCE(tmdb_not_found, false) = false
		ORDER BY created_at, imdb_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var films []Film
	for rows.Next() {
		f, err := scanFilm(rows)
		if err != nil {
			return nil, err
		}
		films = append(films, f)
	}
	return films, rows.Err()
}

// FilmsTMDBNotFound возвращает до limit записей, помеченных «не найдено на TMDB».
// Полный набор колонок — для показа и ручного снятия отметки.
func (r *Repo) FilmsTMDBNotFound(ctx context.Context, limit int) ([]Film, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT `+filmCols+`
		FROM films
		WHERE COALESCE(tmdb_not_found, false) = true
		ORDER BY created_at, imdb_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var films []Film
	for rows.Next() {
		f, err := scanFilm(rows)
		if err != nil {
			return nil, err
		}
		films = append(films, f)
	}
	return films, rows.Err()
}

// SetTMDBNotFound ставит/снимает отметку «не найдено совпадение на TMDB»: true — запись исключается
// из списков «пустых полей» (показывается отдельной таблицей), false — возвращается в общий список.
func (r *Repo) SetTMDBNotFound(ctx context.Context, id string, notFound bool) error {
	_, err := r.conn.ExecContext(ctx, `
		UPDATE films SET tmdb_not_found = $2, updated_at = now() WHERE imdb_id = $1
	`, id, notFound)
	return err
}

// UpdateFilmAdmin перезаписывает редактируемые поля значениями с админ-страницы (пустые строки → NULL, списки — JSON).
// Это админское редактирование: значения задаются явно, а не «заполнить если пусто».
func (r *Repo) UpdateFilmAdmin(ctx context.Context, id string, d Film) error {
	_, err := r.conn.ExecContext(ctx, `
		UPDATE films SET
			title = CASE WHEN NULLIF($2, '') IS NULL THEN title ELSE $2 END,
			title_ru = $3, kind = $4,
			plot = $5, plot_ru = $6, genres = $7, poster_url = $8,
			rating = $9, votes = $10, rating_tmdb = $11, votes_tmdb = $12,
			tmdb_id = $13, release_date = $14, movie_length = $15,
			director = $16, actors = $17, seasons = $18,
			updated_at = now()
		WHERE imdb_id = $1
	`, id,
		nullStr(d.Title), nullStr(d.TitleRU), nullStr(d.Kind),
		nullStr(d.Plot), nullStr(d.PlotRU), genresJSON(d.Genres), nullStr(d.PosterURL),
		nullFloat(d.Rating), nullInt64(d.Votes), nullFloat(d.RatingTMDB), nullInt64(d.VotesTMDB),
		nullStr(d.TMDBID), nullStr(d.ReleaseDate), nullInt(d.MovieLength),
		nullStr(d.Director), genresJSON(d.Actors), nullInt(d.Seasons))
	return err
}

// UpdateFilmData заполняет недостающие поля фильма из TMDB, не затирая заполненные (kind, названия, описания,
// жанры, постер, tmdb_id, длительность, режиссёр, актёры). Рейтинг и голоса пишутся всегда, если ненулевые;
// дата выпуска уточняется (фолбэк "YYYY-01-01" заменяется точной датой).
func (r *Repo) UpdateFilmData(ctx context.Context, id string, d Film) error {
	_, err := r.conn.ExecContext(ctx, `
		UPDATE films SET
			title            = CASE WHEN COALESCE(title, '') = '' THEN $2 ELSE title END,
			title_ru         = CASE WHEN COALESCE(title_ru, '') = '' THEN $3 ELSE title_ru END,
			kind             = CASE WHEN COALESCE(kind, '') = '' THEN $4 ELSE kind END,
			plot             = CASE WHEN COALESCE(plot, '') = '' THEN $5 ELSE plot END,
			plot_ru          = CASE WHEN COALESCE(plot_ru, '') = '' THEN $6 ELSE plot_ru END,
			genres           = CASE WHEN COALESCE(genres, '[]') = '[]' THEN $7 ELSE genres END,
			poster_url       = CASE WHEN COALESCE(poster_url, '') = '' THEN $8 ELSE poster_url END,
			tmdb_id          = CASE WHEN COALESCE(tmdb_id, '') = '' THEN $9 ELSE tmdb_id END,
			rating_tmdb      = CASE WHEN $10 > 0 THEN $10 ELSE rating_tmdb END,
			votes_tmdb       = CASE WHEN $11 > 0 THEN $11 ELSE votes_tmdb END,
			release_date     = CASE
				WHEN NULLIF($12, '') IS NULL THEN release_date
				WHEN release_date = SUBSTRING($12 FROM 1 FOR 4) || '-01-01' THEN $12
				WHEN $12 = SUBSTRING(release_date FROM 1 FOR 4) || '-01-01' THEN release_date
				WHEN COALESCE(release_date, '') = '' THEN $12
				ELSE release_date
			END,
			movie_length     = CASE WHEN COALESCE(movie_length, 0) <= 0 THEN $13 ELSE movie_length END,
			director         = CASE WHEN COALESCE(director, '') = '' THEN $14 ELSE director END,
			actors           = CASE WHEN COALESCE(actors, '[]') = '[]' THEN $15 ELSE actors END,
			rating           = CASE WHEN $16 > 0 THEN $16 ELSE rating END,
			votes            = CASE WHEN $17 > 0 THEN $17 ELSE votes END,
			tmdb_not_found   = false,
			rating_updated_at = now(),
			updated_at       = now()
		WHERE imdb_id = $1
	`, id, d.Title, d.TitleRU, d.Kind, d.Plot, d.PlotRU, genresJSON(d.Genres), d.PosterURL,
		d.TMDBID, nullFloat(d.RatingTMDB), nullInt64(d.VotesTMDB), d.ReleaseDate,
		nullInt(d.MovieLength), d.Director, genresJSON(d.Actors), nullFloat(d.Rating), nullInt64(d.Votes))
	return err
}

// ---- хелперы сканирования ----

type scanner interface{ Scan(dest ...any) error }

func scanFilm(s scanner) (Film, error) {
	var (
		f                Film
		top, popul       sql.NullInt64
		tmdbTop, tmdbPop sql.NullInt64
		seasons          sql.NullInt64
		movieLen         sql.NullInt64
		genresStr        string
		countriesStr     string
		actorsStr        string
	)
	err := s.Scan(&f.IMDBID, &f.Title, &f.TitleRU, &f.Kind, &f.Year, &f.Rating, &f.Votes,
		&f.Plot, &f.PlotRU, &genresStr, &f.PosterURL, &top, &popul,
		&f.TMDBID, &f.RatingTMDB, &f.VotesTMDB, &tmdbTop, &tmdbPop, &f.Size, &seasons,
		&movieLen, &countriesStr, &f.Director, &actorsStr, &f.ReleaseDate, &f.TMDBNotFound)
	if err != nil {
		return Film{}, err
	}
	f.Seasons = int(seasons.Int64)
	if !seasons.Valid {
		f.Seasons = 0
	}
	f.MovieLength = int(movieLen.Int64)
	if !movieLen.Valid {
		f.MovieLength = 0
	}
	f.Genres = parseGenres(genresStr)
	f.Countries = parseGenres(countriesStr)
	f.Actors = parseGenres(actorsStr)
	f.RankTop250 = toPtr(top)
	f.RankPopular = toPtr(popul)
	f.RankTMDBTop250 = toPtr(tmdbTop)
	f.RankTMDBPopular = toPtr(tmdbPop)
	return f, nil
}

// scanFilmLite — сканирование лёгкого набора колонок (filmColsLite): без описаний plot/plot_ru, остальное как в scanFilm.
func scanFilmLite(s scanner) (Film, error) {
	var (
		f                Film
		top, popul       sql.NullInt64
		tmdbTop, tmdbPop sql.NullInt64
		seasons          sql.NullInt64
		movieLen         sql.NullInt64
		genresStr        string
		countriesStr     string
		actorsStr        string
	)
	err := s.Scan(&f.IMDBID, &f.Title, &f.TitleRU, &f.Kind, &f.Year, &f.Rating, &f.Votes,
		&genresStr, &f.PosterURL, &top, &popul,
		&f.TMDBID, &f.RatingTMDB, &f.VotesTMDB, &tmdbTop, &tmdbPop, &f.Size, &seasons,
		&movieLen, &countriesStr, &f.Director, &actorsStr, &f.ReleaseDate)
	if err != nil {
		return Film{}, err
	}
	f.Seasons = int(seasons.Int64)
	if !seasons.Valid {
		f.Seasons = 0
	}
	f.MovieLength = int(movieLen.Int64)
	if !movieLen.Valid {
		f.MovieLength = 0
	}
	f.Genres = parseGenres(genresStr)
	f.Countries = parseGenres(countriesStr)
	f.Actors = parseGenres(actorsStr)
	f.RankTop250 = toPtr(top)
	f.RankPopular = toPtr(popul)
	f.RankTMDBTop250 = toPtr(tmdbTop)
	f.RankTMDBPopular = toPtr(tmdbPop)
	return f, nil
}

// genresJSON сериализует список жанров в JSON-строку для хранения в TEXT.
func genresJSON(genres []string) string {
	if genres == nil {
		genres = []string{}
	}
	b, err := json.Marshal(genres)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func parseGenres(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func toPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func nullInt(v int) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(v), Valid: true}
}

func nullInt64(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullFloat(v float64) sql.NullFloat64 {
	if v == 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: v, Valid: true}
}
