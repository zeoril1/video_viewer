package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zeoril1/video_viewer/internal/tmdb"
)

const seriesSchema = `CREATE TABLE IF NOT EXISTS series_seasons (
 tmdb_id BIGINT PRIMARY KEY,
 seasons JSONB NOT NULL DEFAULT '[]',
 refreshed_at TIMESTAMPTZ
)`

// CachedSeriesSeasons never waits for a network refresh or locks its row.
func (r *Repo) CachedSeriesSeasons(ctx context.Context, id int64) ([]tmdb.SeasonInfo, error) {
	var raw []byte
	err := r.conn.QueryRowContext(ctx, `SELECT seasons FROM series_seasons WHERE tmdb_id=$1`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var seasons []tmdb.SeasonInfo
	err = json.Unmarshal(raw, &seasons)
	return seasons, err
}

// SeriesSeasons обновляет метаданные по запросу, не чаще раза в 24 часа: блокировка строки (FOR UPDATE)
// не даёт параллельным запросам карточек обновлять один сериал, а неудачное обновление сохраняет
// последние успешные данные и метку времени.
func (r *Repo) SeriesSeasons(ctx context.Context, id int64, fetch func(context.Context, int64) ([]tmdb.SeasonInfo, error)) ([]tmdb.SeasonInfo, error) {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO series_seasons(tmdb_id) VALUES ($1) ON CONFLICT DO NOTHING`, id); err != nil {
		return nil, err
	}
	var raw []byte
	var refreshed sql.NullTime
	if err = tx.QueryRowContext(ctx, `SELECT seasons, refreshed_at FROM series_seasons WHERE tmdb_id=$1 FOR UPDATE`, id).Scan(&raw, &refreshed); err != nil {
		return nil, err
	}
	var seasons []tmdb.SeasonInfo
	if err = json.Unmarshal(raw, &seasons); err != nil {
		return nil, err
	}
	if refreshed.Valid && time.Since(refreshed.Time) < 24*time.Hour {
		return seasons, tx.Commit()
	}
	updated, err := fetch(ctx, id)
	if err != nil {
		return seasons, err
	}
	if len(updated) == 0 {
		return seasons, fmt.Errorf("tmdb: empty season structure for %d", id)
	}
	raw, err = json.Marshal(updated)
	if err != nil {
		return seasons, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE series_seasons SET seasons=$2, refreshed_at=now() WHERE tmdb_id=$1`, id, string(raw)); err != nil {
		return seasons, err
	}
	if err = tx.Commit(); err != nil {
		return seasons, err
	}
	return updated, nil
}
