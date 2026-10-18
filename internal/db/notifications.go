package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

const notificationSchema = `
CREATE TABLE IF NOT EXISTS telegram_bindings (
 user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 chat_id BIGINT NOT NULL UNIQUE, username TEXT NOT NULL DEFAULT '',
 enabled BOOLEAN NOT NULL DEFAULT true, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS telegram_links (
 token_hash TEXT PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS telegram_links_user ON telegram_links(user_id);
CREATE TABLE IF NOT EXISTS notification_scan_state (
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE, film_id TEXT NOT NULL,
 data JSONB NOT NULL, checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,film_id)
);
CREATE TABLE IF NOT EXISTS notification_outbox (
 id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL, film_id TEXT NOT NULL,
 event_key TEXT NOT NULL, message_text TEXT NOT NULL, chat_id BIGINT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), sent_at TIMESTAMPTZ, cancelled_at TIMESTAMPTZ,
 FOREIGN KEY(user_id,film_id) REFERENCES notification_scan_state(user_id,film_id) ON DELETE CASCADE,
 UNIQUE(user_id,film_id,event_key)
);
CREATE INDEX IF NOT EXISTS notification_outbox_pending ON notification_outbox(next_attempt_at,id)
 WHERE sent_at IS NULL AND cancelled_at IS NULL;
CREATE TABLE IF NOT EXISTS notification_cursors (
 cursor_key TEXT PRIMARY KEY, cursor_value TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// All services initialize the shared database. Serialize this migration because
// concurrent CREATE TABLE IF NOT EXISTS can still race in PostgreSQL's catalog.
func (r *Repo) ensureNotificationSchema(ctx context.Context) error {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(20261018,2)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, notificationSchema); err != nil {
		return err
	}
	return tx.Commit()
}

type NotificationSubscription struct {
	UserID int64
	FilmID string
	Data   json.RawMessage
	ChatID int64
}

type TelegramBinding struct {
	UserID   int64  `json:"user_id"`
	ChatID   int64  `json:"chat_id"`
	Username string `json:"username"`
	Enabled  bool   `json:"enabled"`
}

type NotificationMessage struct {
	ID       int64
	UserID   int64
	FilmID   string
	EventKey string
	Text     string
	ChatID   int64
	Attempts int
}

func (r *Repo) ListNotificationSubscriptions(ctx context.Context) ([]NotificationSubscription, error) {
	rows, err := r.conn.QueryContext(ctx, `SELECT p.user_id,p.item_key,p.data,b.chat_id
	 FROM personal_items p JOIN telegram_bindings b ON b.user_id=p.user_id
	 WHERE p.kind='follow' AND b.enabled ORDER BY p.item_key,p.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationSubscription{}
	for rows.Next() {
		var sub NotificationSubscription
		if err = rows.Scan(&sub.UserID, &sub.FilmID, &sub.Data, &sub.ChatID); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (r *Repo) NotificationState(ctx context.Context, uid int64, filmID string) (json.RawMessage, error) {
	var state json.RawMessage
	err := r.conn.QueryRowContext(ctx, `SELECT data FROM notification_scan_state WHERE user_id=$1 AND film_id=$2`, uid, filmID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return state, err
}

// The snapshot and its outbox messages commit together. A failed Telegram send
// cannot lose an event, and a repeated scan cannot enqueue it twice.
func (r *Repo) SaveNotificationScan(ctx context.Context, sub NotificationSubscription, state json.RawMessage, messages []NotificationMessage) error {
	if !json.Valid(state) {
		return errors.New("invalid notification state")
	}
	for _, message := range messages {
		if message.EventKey == "" || message.Text == "" {
			return errors.New("notification event key and text are required")
		}
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT true FROM telegram_bindings b JOIN personal_items p ON p.user_id=b.user_id
	 WHERE b.user_id=$1 AND b.chat_id=$3 AND b.enabled AND p.kind='follow' AND p.item_key=$2
	 FOR UPDATE OF b,p`, sub.UserID, sub.FilmID, sub.ChatID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // The user unsubscribed, disabled notifications or relinked while scanning.
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO notification_scan_state(user_id,film_id,data) VALUES($1,$2,$3)
	 ON CONFLICT(user_id,film_id) DO UPDATE SET data=EXCLUDED.data,checked_at=now()`, sub.UserID, sub.FilmID, string(state)); err != nil {
		return err
	}
	for _, message := range messages {
		if _, err = tx.ExecContext(ctx, `INSERT INTO notification_outbox(user_id,film_id,event_key,message_text,chat_id)
		 VALUES($1,$2,$3,$4,$5) ON CONFLICT(user_id,film_id,event_key) DO NOTHING`, sub.UserID, sub.FilmID, message.EventKey, message.Text, sub.ChatID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repo) PendingNotifications(ctx context.Context, limit int) ([]NotificationMessage, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.conn.QueryContext(ctx, `SELECT n.id,n.user_id,n.film_id,n.event_key,n.message_text,n.chat_id,n.attempts
	 FROM notification_outbox n JOIN telegram_bindings b ON b.user_id=n.user_id AND b.chat_id=n.chat_id
	 JOIN personal_items p ON p.user_id=n.user_id AND p.kind='follow' AND p.item_key=n.film_id
	 WHERE b.enabled AND n.sent_at IS NULL AND n.cancelled_at IS NULL AND n.next_attempt_at<=now()
	 ORDER BY n.id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationMessage{}
	for rows.Next() {
		var message NotificationMessage
		if err = rows.Scan(&message.ID, &message.UserID, &message.FilmID, &message.EventKey, &message.Text, &message.ChatID, &message.Attempts); err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

func (r *Repo) MarkNotificationSent(ctx context.Context, id int64) error {
	_, err := r.conn.ExecContext(ctx, `UPDATE notification_outbox SET sent_at=now() WHERE id=$1 AND sent_at IS NULL AND cancelled_at IS NULL`, id)
	return err
}

func (r *Repo) RetryNotification(ctx context.Context, id int64, delay time.Duration) error {
	if delay < time.Second {
		delay = time.Second
	}
	_, err := r.conn.ExecContext(ctx, `UPDATE notification_outbox SET attempts=attempts+1,next_attempt_at=now()+$2*interval '1 second'
	 WHERE id=$1 AND sent_at IS NULL AND cancelled_at IS NULL`, id, delay.Seconds())
	return err
}

func (r *Repo) TelegramBinding(ctx context.Context, uid int64) (TelegramBinding, bool, error) {
	var binding TelegramBinding
	err := r.conn.QueryRowContext(ctx, `SELECT user_id,chat_id,username,enabled FROM telegram_bindings WHERE user_id=$1`, uid).
		Scan(&binding.UserID, &binding.ChatID, &binding.Username, &binding.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return TelegramBinding{}, false, nil
	}
	return binding, err == nil, err
}

func (r *Repo) CreateTelegramLink(ctx context.Context, uid int64, hash string, expires time.Time) error {
	if hash == "" || !expires.After(time.Now()) {
		return errors.New("invalid telegram link")
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&userID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM telegram_links WHERE user_id=$1 OR expires_at<=now()`, uid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO telegram_links(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, hash, uid, expires); err != nil {
		return err
	}
	return tx.Commit()
}

// Tokens are hashes of short-lived random link secrets. Claiming one never
// transfers a Telegram chat already connected to another account.
func (r *Repo) ClaimTelegramLink(ctx context.Context, hash string, chatID int64, username string) (bool, error) {
	if hash == "" || chatID <= 0 {
		return false, nil
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var uid int64
	if err = tx.QueryRowContext(ctx, `SELECT user_id FROM telegram_links WHERE token_hash=$1 AND expires_at>now()`, hash).Scan(&uid); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	// Use the same user-before-link lock order as CreateTelegramLink/UnlinkTelegram.
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&uid); err != nil {
		return false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT user_id FROM telegram_links WHERE token_hash=$1 AND user_id=$2 AND expires_at>now() FOR UPDATE`, hash, uid).Scan(&uid); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	// A chat may receive simultaneous claims from different accounts.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, chatID); err != nil {
		return false, err
	}
	var other bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_bindings WHERE chat_id=$1 AND user_id<>$2)`, chatID, uid).Scan(&other); err != nil || other {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO telegram_bindings(user_id,chat_id,username,enabled) VALUES($1,$2,$3,true)
	 ON CONFLICT(user_id) DO UPDATE SET chat_id=EXCLUDED.chat_id,username=EXCLUDED.username,enabled=true,updated_at=now()`, uid, chatID, username); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification_outbox SET cancelled_at=now() WHERE user_id=$1 AND chat_id<>$2 AND sent_at IS NULL AND cancelled_at IS NULL`, uid, chatID); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM telegram_links WHERE user_id=$1`, uid); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (r *Repo) SetTelegramEnabled(ctx context.Context, uid int64, enabled bool) error {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE telegram_bindings SET enabled=$2,updated_at=now() WHERE user_id=$1`, uid, enabled); err != nil {
		return err
	}
	if !enabled {
		if _, err = tx.ExecContext(ctx, `UPDATE notification_outbox SET cancelled_at=now() WHERE user_id=$1 AND sent_at IS NULL AND cancelled_at IS NULL`, uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repo) UnlinkTelegram(ctx context.Context, uid int64) error {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&userID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM telegram_bindings WHERE user_id=$1`, uid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM telegram_links WHERE user_id=$1`, uid); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification_outbox SET cancelled_at=now() WHERE user_id=$1 AND sent_at IS NULL AND cancelled_at IS NULL`, uid); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repo) StopTelegramChat(ctx context.Context, chatID int64) error {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE telegram_bindings SET enabled=false,updated_at=now() WHERE chat_id=$1`, chatID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification_outbox SET cancelled_at=now() WHERE chat_id=$1 AND sent_at IS NULL AND cancelled_at IS NULL`, chatID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repo) NotificationCursor(ctx context.Context, key string) (string, error) {
	var value string
	err := r.conn.QueryRowContext(ctx, `SELECT cursor_value FROM notification_cursors WHERE cursor_key=$1`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (r *Repo) SaveNotificationCursor(ctx context.Context, key, value string) error {
	_, err := r.conn.ExecContext(ctx, `INSERT INTO notification_cursors(cursor_key,cursor_value) VALUES($1,$2)
	 ON CONFLICT(cursor_key) DO UPDATE SET cursor_value=EXCLUDED.cursor_value,updated_at=now()`, key, value)
	return err
}

// Keep a dedicated connection for the session lock: returning it to the pool
// while locked would make another process's leadership checks unreliable.
func (r *Repo) AcquireNotificationLeader(ctx context.Context) (release func(), acquired bool, err error) {
	conn, err := r.conn.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(20261018,1)`).Scan(&acquired); err != nil || !acquired {
		conn.Close()
		return nil, acquired, err
	}
	r.notificationLeaderMu.Lock()
	r.notificationLeaderConn = conn
	r.notificationLeaderMu.Unlock()
	var once sync.Once
	release = func() {
		once.Do(func() {
			r.notificationLeaderMu.Lock()
			if r.notificationLeaderConn == conn {
				r.notificationLeaderConn = nil
			}
			r.notificationLeaderMu.Unlock()
			unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var unlocked bool
			if unlockErr := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock(20261018,1)`).Scan(&unlocked); unlockErr != nil || !unlocked {
				// Do not put a possibly still locked connection back into the pool.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
			_ = conn.Close()
		})
	}
	return release, true, nil
}

// Check the dedicated lock connection, not a pooled query: the database may
// still be reachable after PostgreSQL has disconnected the old leader session.
func (r *Repo) NotificationLeaderAlive(ctx context.Context) (bool, error) {
	r.notificationLeaderMu.Lock()
	defer r.notificationLeaderMu.Unlock()
	if r.notificationLeaderConn == nil {
		return false, nil
	}
	var alive bool
	err := r.notificationLeaderConn.QueryRowContext(ctx, `SELECT EXISTS(
	 SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid()
	 AND classid=20261018 AND objid=1 AND objsubid=2 AND granted)`).Scan(&alive)
	return alive, err
}
