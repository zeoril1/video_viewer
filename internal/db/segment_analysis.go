package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/zeoril1/video_viewer/internal/segments"
)

const segmentAnalysisSchema = `
CREATE TABLE IF NOT EXISTS segment_analysis (
	media_key TEXT PRIMARY KEY,
	version INT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('ready', 'not_found')),
	record JSONB NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

func (r *Repo) GetSegmentAnalysis(ctx context.Context, key string) (segments.AnalysisRecord, bool, error) {
	var record segments.AnalysisRecord
	if !segments.ValidMediaKey(key) {
		return record, false, errors.New("invalid media key")
	}
	var data []byte
	var updatedAt time.Time
	err := r.conn.QueryRowContext(ctx, `SELECT record, updated_at FROM segment_analysis WHERE media_key=$1`, key).Scan(&data, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return record, false, nil
	}
	if err != nil {
		return record, false, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, false, err
	}
	record.UpdatedAt = updatedAt
	if record.MediaKey != key {
		return segments.AnalysisRecord{}, false, errors.New("stored analysis media mismatch")
	}
	if err := segments.ValidateAnalysis(record); err != nil {
		return segments.AnalysisRecord{}, false, err
	}
	return record, true, nil
}

func (r *Repo) SaveSegmentAnalysis(ctx context.Context, record segments.AnalysisRecord) error {
	if err := segments.ValidateAnalysis(record); err != nil {
		return err
	}
	record.UpdatedAt = time.Now().UTC()
	record.Segments = segments.Normalize(record.Segments, record.Duration)
	if record.Fingerprint == nil {
		record.Fingerprint = []uint32{}
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > segments.AnalysisMaxBytes {
		return errors.New("analysis exceeds size limit")
	}
	// Versions never move backward. A later negative match must not erase valid
	// timestamps from the same detector version (e.g. an unavailable neighbour).
	_, err = r.conn.ExecContext(ctx, `INSERT INTO segment_analysis(media_key,version,status,record,updated_at)
		VALUES($1,$2,$3,$4::jsonb,$5) ON CONFLICT(media_key) DO UPDATE SET
		version=EXCLUDED.version,status=EXCLUDED.status,record=EXCLUDED.record,updated_at=EXCLUDED.updated_at
		WHERE segment_analysis.version < EXCLUDED.version OR
		(segment_analysis.version=EXCLUDED.version AND NOT
		(segment_analysis.status='ready' AND EXCLUDED.status='not_found'))`,
		record.MediaKey, record.Version, record.Status, string(data), record.UpdatedAt)
	return err
}
