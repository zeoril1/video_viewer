package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zeoril1/video_viewer/internal/tmdb"
)

const SeriesMetadataFreshFor = 24 * time.Hour

const seriesSchema = `CREATE TABLE IF NOT EXISTS series_seasons (
 tmdb_id BIGINT PRIMARY KEY,
 seasons JSONB NOT NULL DEFAULT '[]',
 refreshed_at TIMESTAMPTZ,
 refresh_started_at TIMESTAMPTZ
);
ALTER TABLE series_seasons ADD COLUMN IF NOT EXISTS refresh_started_at TIMESTAMPTZ;
CREATE TABLE IF NOT EXISTS series_episodes (
 tmdb_id BIGINT NOT NULL,
 season INTEGER NOT NULL,
 episodes JSONB NOT NULL DEFAULT '[]',
 refreshed_at TIMESTAMPTZ,
 refresh_started_at TIMESTAMPTZ,
 PRIMARY KEY (tmdb_id, season)
)`

// SeriesSeasonCache reads persisted metadata immediately, including its freshness.
func (r *Repo) SeriesSeasonCache(ctx context.Context, id int64) ([]tmdb.SeasonInfo, time.Time, error) {
	var raw []byte
	var refreshed sql.NullTime
	err := r.conn.QueryRowContext(ctx, `SELECT seasons, refreshed_at FROM series_seasons WHERE tmdb_id=$1`, id).Scan(&raw, &refreshed)
	if err == sql.ErrNoRows {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	var seasons []tmdb.SeasonInfo
	err = json.Unmarshal(raw, &seasons)
	return seasons, refreshed.Time, err
}

// CachedSeriesSeasons never waits for TMDB or holds a transaction open.
func (r *Repo) CachedSeriesSeasons(ctx context.Context, id int64) ([]tmdb.SeasonInfo, error) {
	seasons, _, err := r.SeriesSeasonCache(ctx, id)
	return seasons, err
}

// SeriesSeasons refreshes at most daily. A short persisted lease deduplicates workers,
// including workers in another catalog instance; TMDB runs outside any transaction.
// A failed refresh leaves the previous metadata and successful timestamp intact.
func (r *Repo) SeriesSeasons(ctx context.Context, id int64, fetch func(context.Context, int64) ([]tmdb.SeasonInfo, error)) ([]tmdb.SeasonInfo, error) {
	seasons, refreshed, err := r.SeriesSeasonCache(ctx, id)
	if err != nil || !refreshed.IsZero() && time.Since(refreshed) < SeriesMetadataFreshFor {
		return seasons, err
	}
	var claimed int64
	err = r.conn.QueryRowContext(ctx, `INSERT INTO series_seasons(tmdb_id, refresh_started_at) VALUES ($1, now())
 ON CONFLICT (tmdb_id) DO UPDATE SET refresh_started_at=now()
 WHERE (series_seasons.refreshed_at IS NULL OR series_seasons.refreshed_at < now()-interval '24 hours')
 AND (series_seasons.refresh_started_at IS NULL OR series_seasons.refresh_started_at < now()-interval '2 minutes')
 RETURNING tmdb_id`, id).Scan(&claimed)
	if err == sql.ErrNoRows {
		return seasons, nil
	}
	if err != nil {
		return seasons, err
	}
	updated, err := fetch(ctx, id)
	if err == nil && len(updated) == 0 {
		err = fmt.Errorf("tmdb: empty season structure for %d", id)
	}
	if err != nil {
		r.releaseSeriesLease(ctx, id, -1)
		return seasons, err
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		return seasons, err
	}
	_, err = r.conn.ExecContext(ctx, `UPDATE series_seasons SET seasons=$2, refreshed_at=now(), refresh_started_at=NULL WHERE tmdb_id=$1`, id, string(raw))
	if err != nil {
		return seasons, err
	}
	return updated, nil
}

// SeriesEpisodeCache stores the selected season separately, so opening one season
// never requires downloading details of all the others.
func (r *Repo) SeriesEpisodeCache(ctx context.Context, id int64, season int) ([]tmdb.EpisodeInfo, time.Time, error) {
	var raw []byte
	var refreshed sql.NullTime
	err := r.conn.QueryRowContext(ctx, `SELECT episodes, refreshed_at FROM series_episodes WHERE tmdb_id=$1 AND season=$2`, id, season).Scan(&raw, &refreshed)
	if err == sql.ErrNoRows {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	var episodes []tmdb.EpisodeInfo
	err = json.Unmarshal(raw, &episodes)
	return episodes, refreshed.Time, err
}

func (r *Repo) SeriesEpisodes(ctx context.Context, id int64, season int, fetch func(context.Context, int64, int) ([]tmdb.EpisodeInfo, error)) ([]tmdb.EpisodeInfo, error) {
	episodes, refreshed, err := r.SeriesEpisodeCache(ctx, id, season)
	if err != nil || !refreshed.IsZero() && time.Since(refreshed) < SeriesMetadataFreshFor {
		return episodes, err
	}
	var claimed int64
	err = r.conn.QueryRowContext(ctx, `INSERT INTO series_episodes(tmdb_id, season, refresh_started_at) VALUES ($1, $2, now())
 ON CONFLICT (tmdb_id, season) DO UPDATE SET refresh_started_at=now()
 WHERE (series_episodes.refreshed_at IS NULL OR series_episodes.refreshed_at < now()-interval '24 hours')
 AND (series_episodes.refresh_started_at IS NULL OR series_episodes.refresh_started_at < now()-interval '2 minutes')
 RETURNING tmdb_id`, id, season).Scan(&claimed)
	if err == sql.ErrNoRows {
		return episodes, nil
	}
	if err != nil {
		return episodes, err
	}
	updated, err := fetch(ctx, id, season)
	if err != nil {
		r.releaseSeriesLease(ctx, id, season)
		return episodes, err
	}
	if updated == nil {
		updated = []tmdb.EpisodeInfo{}
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		return episodes, err
	}
	_, err = r.conn.ExecContext(ctx, `UPDATE series_episodes SET episodes=$3, refreshed_at=now(), refresh_started_at=NULL WHERE tmdb_id=$1 AND season=$2`, id, season, string(raw))
	if err != nil {
		return episodes, err
	}
	return updated, nil
}

// Keep a 30-second retry delay after errors, while allowing crashed jobs to expire.
func (r *Repo) releaseSeriesLease(ctx context.Context, id int64, season int) {
	if season < 0 {
		_, _ = r.conn.ExecContext(ctx, `UPDATE series_seasons SET refresh_started_at=now()-interval '90 seconds' WHERE tmdb_id=$1`, id)
		return
	}
	_, _ = r.conn.ExecContext(ctx, `UPDATE series_episodes SET refresh_started_at=now()-interval '90 seconds' WHERE tmdb_id=$1 AND season=$2`, id, season)
}
