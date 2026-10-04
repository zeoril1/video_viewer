package db

import (
	"context"
	"strings"
	"time"
)

const adminRefreshSchema = `CREATE TABLE IF NOT EXISTS admin_refresh_failures (
 imdb_id TEXT PRIMARY KEY REFERENCES films(imdb_id) ON DELETE CASCADE,
 reason TEXT NOT NULL,
 failed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 retry_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '7 days'
);`

// IMDb rating is optional. A future release does not need any rating yet.
const adminMissingPredicate = `(COALESCE(kind, '') = ''
 OR COALESCE(title_ru, '') = '' OR COALESCE(plot_ru, '') = ''
 OR COALESCE(genres, '[]') = '[]' OR COALESCE(poster_url, '') = ''
 OR COALESCE(tmdb_id, '') = ''
 OR (COALESCE(rating_tmdb, 0) <= 0 AND NOT (
 CASE WHEN COALESCE(release_date, '') ~ '^\d{4}-\d{2}-\d{2}$'
 THEN release_date > to_char(now() AT TIME ZONE 'Europe/Moscow', 'YYYY-MM-DD')
 WHEN COALESCE(release_date, '') ~ '^\d{4}$'
 THEN release_date > to_char(now() AT TIME ZONE 'Europe/Moscow', 'YYYY')
 ELSE false END)))`

func (r *Repo) AdminFilmsMissing(ctx context.Context) ([]Film, error) {
	return r.adminFilms(ctx, adminMissingPredicate+` AND NOT EXISTS (
 SELECT 1 FROM admin_refresh_failures a WHERE a.imdb_id = films.imdb_id AND a.retry_at > now())`)
}

func (r *Repo) adminFilms(ctx context.Context, predicate string) ([]Film, error) {
	rows, err := r.conn.QueryContext(ctx, `SELECT `+filmCols+` FROM films WHERE `+predicate+` ORDER BY created_at, imdb_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	films := make([]Film, 0)
	for rows.Next() {
		f, err := scanFilm(rows)
		if err != nil {
			return nil, err
		}
		films = append(films, f)
	}
	return films, rows.Err()
}

type AdminFailure struct {
	Film
	Reason   string    `json:"refresh_error"`
	FailedAt time.Time `json:"failed_at"`
	RetryAt  time.Time `json:"retry_at"`
}

func (r *Repo) AdminFilmsFailed(ctx context.Context) ([]AdminFailure, error) {
	rows, err := r.conn.QueryContext(ctx, `SELECT imdb_id, reason, failed_at, retry_at FROM admin_refresh_failures WHERE retry_at > now()`)
	if err != nil {
		return nil, err
	}
	meta := map[string]AdminFailure{}
	for rows.Next() {
		var f AdminFailure
		if err := rows.Scan(&f.IMDBID, &f.Reason, &f.FailedAt, &f.RetryAt); err != nil {
			rows.Close()
			return nil, err
		}
		meta[f.IMDBID] = f
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	films, err := r.adminFilms(ctx, `EXISTS (SELECT 1 FROM admin_refresh_failures a WHERE a.imdb_id=films.imdb_id AND a.retry_at > now())`)
	if err != nil {
		return nil, err
	}
	out := make([]AdminFailure, 0, len(films))
	for _, film := range films {
		f, ok := meta[film.IMDBID]
		if !ok {
			continue
		}
		f.Film = film
		out = append(out, f)
	}
	return out, nil
}

func (r *Repo) SetAdminRefreshResult(ctx context.Context, id, reason string) error {
	if reason == "" {
		_, err := r.conn.ExecContext(ctx, `DELETE FROM admin_refresh_failures WHERE imdb_id=$1`, id)
		return err
	}
	_, err := r.conn.ExecContext(ctx, `INSERT INTO admin_refresh_failures (imdb_id, reason) VALUES ($1,$2)
 ON CONFLICT (imdb_id) DO UPDATE SET reason=EXCLUDED.reason, failed_at=now(), retry_at=now()+interval '7 days'`, id, reason)
	return err
}

func (r *Repo) AdminFilmIncomplete(ctx context.Context, id string) (bool, error) {
	var missing bool
	err := r.conn.QueryRowContext(ctx, `SELECT `+adminMissingPredicate+` FROM films WHERE imdb_id=$1`, id).Scan(&missing)
	return missing, err
}

// MissingAdminFields mirrors the admin selection for readable failure messages.
func MissingAdminFields(f Film, now time.Time) []string {
	var fields []string
	for _, x := range []struct{ name, value string }{
		{"тип", f.Kind}, {"название RU", f.TitleRU}, {"описание RU", f.PlotRU}, {"постер", f.PosterURL}, {"TMDB ID", f.TMDBID},
	} {
		if strings.TrimSpace(x.value) == "" {
			fields = append(fields, x.name)
		}
	}
	if len(f.Genres) == 0 {
		fields = append(fields, "жанры")
	}
	future := false
	if date, err := time.Parse("2006-01-02", f.ReleaseDate); err == nil {
		future = date.Format("2006-01-02") > now.Format("2006-01-02")
	} else if year, err := time.Parse("2006", f.ReleaseDate); err == nil {
		future = year.Year() > now.Year()
	}
	if !future && f.RatingTMDB <= 0 {
		fields = append(fields, "рейтинг TMDB")
	}
	return fields
}
