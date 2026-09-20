package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

const personalSchema = `
CREATE TABLE IF NOT EXISTS personal_items (
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 kind TEXT NOT NULL, item_key TEXT NOT NULL, data JSONB NOT NULL DEFAULT '{}',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(user_id,kind,item_key)
);
CREATE TABLE IF NOT EXISTS device_links (
 device_hash TEXT PRIMARY KEY, user_code TEXT UNIQUE NOT NULL,
 user_id BIGINT REFERENCES users(id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS device_links_expiry ON device_links(expires_at);
`

type PersonalItem struct {
	Kind      string          `json:"kind"`
	Key       string          `json:"key"`
	Data      json.RawMessage `json:"data"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func (r *Repo) ListPersonal(ctx context.Context, uid int64) ([]PersonalItem, error) {
	rows, err := r.conn.QueryContext(ctx, `SELECT kind,item_key,data,updated_at FROM personal_items WHERE user_id=$1 ORDER BY updated_at DESC`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PersonalItem{}
	for rows.Next() {
		var p PersonalItem
		if err = rows.Scan(&p.Kind, &p.Key, &p.Data, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (r *Repo) SavePersonal(ctx context.Context, uid int64, p PersonalItem) error {
	_, err := r.conn.ExecContext(ctx, `INSERT INTO personal_items(user_id,kind,item_key,data) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,kind,item_key) DO UPDATE SET data=EXCLUDED.data,updated_at=now()`, uid, p.Kind, p.Key, string(p.Data))
	return err
}
func (r *Repo) DeletePersonal(ctx context.Context, uid int64, kind, key string) error {
	_, err := r.conn.ExecContext(ctx, `DELETE FROM personal_items WHERE user_id=$1 AND kind=$2 AND item_key=$3`, uid, kind, key)
	return err
}
func (r *Repo) CreateDeviceLink(ctx context.Context, hash, code string) error {
	_, err := r.conn.ExecContext(ctx, `DELETE FROM device_links WHERE expires_at<now()`)
	if err != nil {
		return err
	}
	_, err = r.conn.ExecContext(ctx, `INSERT INTO device_links(device_hash,user_code,expires_at) VALUES($1,$2,now()+interval '10 minutes')`, hash, code)
	return err
}
func (r *Repo) ApproveDeviceLink(ctx context.Context, code string, uid int64) (bool, error) {
	res, err := r.conn.ExecContext(ctx, `UPDATE device_links SET user_id=$2 WHERE user_code=$1 AND expires_at>now() AND user_id IS NULL`, code, uid)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// The lock makes claiming a code and issuing its session one atomic operation.
func (r *Repo) ClaimDeviceLink(ctx context.Context, hash, token string, ttl time.Duration) (string, error) {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var uid sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM device_links WHERE device_hash=$1 AND expires_at>now() FOR UPDATE`, hash).Scan(&uid)
	if err == sql.ErrNoRows {
		return "expired", nil
	}
	if err != nil {
		return "", err
	}
	if !uid.Valid {
		return "pending", nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(token,user_id,expires_at) VALUES($1,$2,$3)`, token, uid.Int64, time.Now().Add(ttl)); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM device_links WHERE device_hash=$1`, hash); err != nil {
		return "", err
	}
	return "approved", tx.Commit()
}
