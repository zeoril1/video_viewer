package db

import (
	"context"
	"time"
)

const watchOrderSchema = `CREATE TABLE IF NOT EXISTS watch_orders (
 cache_key TEXT PRIMARY KEY, payload JSONB NOT NULL, refreshed_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

func (r *Repo) WatchOrder(ctx context.Context, key string) ([]byte, time.Time, error) {
	var data []byte
	var at time.Time
	err := r.conn.QueryRowContext(ctx, `SELECT payload, refreshed_at FROM watch_orders WHERE cache_key=$1`, key).Scan(&data, &at)
	return data, at, err
}
func (r *Repo) SaveWatchOrder(ctx context.Context, key string, data []byte) error {
	_, err := r.conn.ExecContext(ctx, `INSERT INTO watch_orders(cache_key,payload) VALUES($1,$2)
 ON CONFLICT(cache_key) DO UPDATE SET payload=EXCLUDED.payload,refreshed_at=now()`, key, string(data))
	return err
}
