package db

import (
	"context"
	"time"
)

// WatchProgress — позиция просмотра для сохранения в историю.
type WatchProgress struct {
	FilmID   string  // imdb_id / id фильма
	Magnet   string  // магнет-ссылка выбранного источника
	File     int     // индекс файла (серии) в торренте; -1 — авто
	Season   int     // сезон (0 — фильм/не определён)
	Episode  int     // серия (0 — не определена)
	Position float64 // секунды с начала эпизода/фильма
	Duration float64 // полная длительность (сек)
}

// HistoryEntry — запись истории просмотра с данными фильма (для карточек
// «Продолжить просмотр» на главной странице).
type HistoryEntry struct {
	FilmID    string    `json:"film_id"`
	Title     string    `json:"title"`
	TitleRU   string    `json:"title_ru"`
	Kind      string    `json:"kind,omitempty"`
	Year      int       `json:"year"`
	PosterURL string    `json:"poster_url"`
	Magnet    string    `json:"magnet,omitempty"`
	File      int       `json:"file"`
	Season    int       `json:"season"`
	Episode   int       `json:"episode"`
	Position  float64   `json:"position"`
	Duration  float64   `json:"duration"`
	UpdatedAt time.Time `json:"updated_at"`
}

const historySchema = `
CREATE TABLE IF NOT EXISTS watch_history (
	id BIGSERIAL PRIMARY KEY,
	user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	film_id TEXT NOT NULL,
	magnet TEXT NOT NULL DEFAULT '',
	file INT NOT NULL DEFAULT -1,
	season INT NOT NULL DEFAULT 0,
	episode INT NOT NULL DEFAULT 0,
	position_sec DOUBLE PRECISION NOT NULL DEFAULT 0,
	duration_sec DOUBLE PRECISION NOT NULL DEFAULT 0,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (user_id, film_id, magnet, file)
);
CREATE INDEX IF NOT EXISTS idx_watch_history_user ON watch_history (user_id, updated_at DESC);
`

// ensureHistorySchema создаёт таблицу истории просмотра.
func (r *Repo) ensureHistorySchema(ctx context.Context) error {
	_, err := r.conn.ExecContext(ctx, historySchema)
	return err
}

// SaveWatchProgress сохраняет/обновляет позицию просмотра (upsert по
// user+film+magnet+file — один эпизод одного источника = одна запись,
// чтобы при повторном поиске источников не плодились дубли).
func (r *Repo) SaveWatchProgress(ctx context.Context, userID int64, p WatchProgress) error {
	_, err := r.conn.ExecContext(ctx, `
		INSERT INTO watch_history (user_id, film_id, magnet, file, season, episode, position_sec, duration_sec)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (user_id, film_id, magnet, file) DO UPDATE SET
			season       = EXCLUDED.season,
			episode      = EXCLUDED.episode,
			position_sec = EXCLUDED.position_sec,
			duration_sec = EXCLUDED.duration_sec,
			updated_at   = now()
	`, userID, p.FilmID, p.Magnet, p.File, p.Season, p.Episode, p.Position, p.Duration)
	return err
}

// ListWatchHistory возвращает историю просмотра пользователя, отсортированную
// по времени последнего просмотра (свежие сверху), с данными фильма.
// Для каждого фильма (film_id) возвращается ТОЛЬКО самая свежая запись
// (последняя просмотренная серия у сериалов) — DISTINCT ON по film_id
// с сортировкой по updated_at DESC, затем общий порядок по свежести.
func (r *Repo) ListWatchHistory(ctx context.Context, userID int64, limit int) ([]HistoryEntry, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT film_id, title, title_ru, kind, year, poster_url,
		       magnet, file, season, episode, position_sec, duration_sec, updated_at
		FROM (
			SELECT DISTINCT ON (wh.film_id)
			       wh.film_id,
			       COALESCE(f.title, wh.film_id)          AS title,
			       COALESCE(f.title_ru, '')               AS title_ru,
			       COALESCE(f.kind, '')                   AS kind,
			       COALESCE(NULLIF(SUBSTRING(f.release_date FROM 1 FOR 4), '')::int, 0) AS year,
			       COALESCE(f.poster_url, '')             AS poster_url,
			       wh.magnet, wh.file, wh.season, wh.episode,
			       wh.position_sec, wh.duration_sec, wh.updated_at
			FROM watch_history wh
			LEFT JOIN films f ON f.imdb_id = wh.film_id
			WHERE wh.user_id = $1
			ORDER BY wh.film_id, wh.updated_at DESC
		) sub
		ORDER BY updated_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]HistoryEntry, 0, 16)
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.FilmID, &e.Title, &e.TitleRU, &e.Kind, &e.Year, &e.PosterURL,
			&e.Magnet, &e.File, &e.Season, &e.Episode, &e.Position, &e.Duration, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteWatchHistory удаляет одну запись истории (по film+magnet+file)
// или все записи фильма, если magnet пустой.
func (r *Repo) DeleteWatchHistory(ctx context.Context, userID int64, filmID, magnet string, file int) error {
	if magnet != "" {
		_, err := r.conn.ExecContext(ctx, `
			DELETE FROM watch_history WHERE user_id = $1 AND film_id = $2 AND magnet = $3 AND file = $4
		`, userID, filmID, magnet, file)
		return err
	}
	_, err := r.conn.ExecContext(ctx, `
		DELETE FROM watch_history WHERE user_id = $1 AND film_id = $2
	`, userID, filmID)
	return err
}

// ClearWatchHistory удаляет всю историю пользователя.
func (r *Repo) ClearWatchHistory(ctx context.Context, userID int64) error {
	_, err := r.conn.ExecContext(ctx, `DELETE FROM watch_history WHERE user_id = $1`, userID)
	return err
}
