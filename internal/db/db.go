// Package db — работа с PostgreSQL: подключение, схема и репозиторий фильмов, собранных из IMDb.
package db

import (
	"context"
	"database/sql"
	"log"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // драйвер PostgreSQL
)

// Open подключается к PostgreSQL по DSN вида postgres://user:pass@host:port/db.
func Open(dsn string) (*sql.DB, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// OpenRetry подключается к PostgreSQL с повторными попытками: при одновременном старте всего стека
// (docker compose up после перезагрузки хоста) БД может быть ещё не готова, и разовый сбой подключения
// не должен оставлять сервис без БД (иначе каталог стартует пустым). До attempts попыток с паузой delay;
// при отмене ctx возвращает ctx.Err(); nil — если все попытки исчерпаны.
func OpenRetry(ctx context.Context, dsn string, attempts int, delay time.Duration) (*sql.DB, error) {
	var lastErr error
	for i := 1; i <= attempts; i++ {
		conn, err := Open(dsn)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		log.Printf("db: connect attempt %d/%d failed: %v", i, attempts, err)
		if i < attempts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return nil, lastErr
}

// Repo — репозиторий поверх *sql.DB.
type Repo struct {
	conn *sql.DB
}

// NewRepo создаёт репозиторий.
func NewRepo(conn *sql.DB) *Repo { return &Repo{conn: conn} }

// Close закрывает соединение с БД.
func (r *Repo) Close() error { return r.conn.Close() }
