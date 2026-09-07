package db

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// User — учётная запись пользователя сайта (для истории просмотра).
// Role: "user" (по умолчанию) или "admin" (админ-страница каталога).
type User struct {
	ID       int64     `json:"id"`
	Username string    `json:"username"`
	Role     string    `json:"role"`
	Created  time.Time `json:"created_at,omitempty"`
}

const authSchema = `
CREATE TABLE IF NOT EXISTS users (
	id BIGSERIAL PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'user',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sessions (
	token TEXT PRIMARY KEY,
	user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);
`

// ensureAuthSchema создаёт таблицы пользователей и сессий, применяет
// миграции (роль) и чистит истёкшие сессии при старте.
func (r *Repo) ensureAuthSchema(ctx context.Context) error {
	if _, err := r.conn.ExecContext(ctx, authSchema); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE users ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'user'"); err != nil {
		return err
	}
	_, _ = r.conn.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return nil
}

// CreateUser создаёт пользователя и возвращает его id.
func (r *Repo) CreateUser(ctx context.Context, username, passwordHash string) (int64, error) {
	var id int64
	err := r.conn.QueryRowContext(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id`,
		username, passwordHash,
	).Scan(&id)
	return id, err
}

// GetUserByUsername возвращает пользователя и его хеш пароля.
func (r *Repo) GetUserByUsername(ctx context.Context, username string) (User, string, bool, error) {
	var (
		u  User
		ph string
	)
	err := r.conn.QueryRowContext(ctx,
		`SELECT id, username, role, password_hash, created_at FROM users WHERE username = $1`, username,
	).Scan(&u.ID, &u.Username, &u.Role, &ph, &u.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, "", false, nil
	}
	if err != nil {
		return User{}, "", false, err
	}
	return u, ph, true, nil
}

// GetUserByID возвращает пользователя по id.
func (r *Repo) GetUserByID(ctx context.Context, id int64) (User, bool, error) {
	var u User
	err := r.conn.QueryRowContext(ctx,
		`SELECT id, username, role, created_at FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Username, &u.Role, &u.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return u, true, nil
}

// CreateSession создаёт сессию пользователя с временем жизни ttl
// и возвращает случайный токен (хранится в httpOnly-куке).
func (r *Repo) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	_, err = r.conn.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES ($1, $2, now() + $3 * interval '1 second')`,
		token, userID, int(ttl.Seconds()),
	)
	if err != nil {
		return "", err
	}
	return token, nil
}

// GetUserBySession возвращает пользователя по валидному токену сессии.
func (r *Repo) GetUserBySession(ctx context.Context, token string) (User, bool, error) {
	var u User
	err := r.conn.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.role, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = $1 AND s.expires_at > now()
	`, token).Scan(&u.ID, &u.Username, &u.Role, &u.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return u, true, nil
}

// DeleteSession удаляет сессию (выход из аккаунта).
func (r *Repo) DeleteSession(ctx context.Context, token string) error {
	_, err := r.conn.ExecContext(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	return err
}

// SetUserRole устанавливает роль пользователя ("user" или "admin").
// Используется для выдачи админ-доступа (например, dev-пользователю).
func (r *Repo) SetUserRole(ctx context.Context, userID int64, role string) error {
	_, err := r.conn.ExecContext(ctx,
		`UPDATE users SET role = $2 WHERE id = $1`, userID, role)
	return err
}

// randomToken генерирует криптостойкий токен из n случайных байт (hex).
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// pbkdf2Iter — число итераций PBKDF2 при хешировании пароля. 600k —
// современная рекомендация OWASP для PBKDF2-SHA256. Формат хеша хранит
// число итераций, поэтому старые хеши (120k) продолжают проверяться.
const pbkdf2Iter = 600_000

// HashPassword хеширует пароль PBKDF2 (crypto/pbkdf2 из stdlib Go 1.24+)
// со случайной солью. Формат: "pbkdf2$<iter>$<salt_hex>$<hash_hex>".
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2$%d$%s$%s", pbkdf2Iter, hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

// VerifyPassword проверяет пароль против сохранённого хеша.
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// SeedDevUser создаёт разработческого пользователя (логин/пароль берутся
// из env DEV_USER/DEV_PASSWORD), если его ещё нет. Существующего
// пользователя и его пароль НЕ трогает. Если хотя бы одно из значений
// пусто — пользователь не создаётся (в проде dev-доступ не нужен).
func (r *Repo) SeedDevUser(ctx context.Context, login, password string) error {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return nil
	}
	u, _, exists, err := r.GetUserByUsername(ctx, login)
	if err != nil {
		return err
	}
	if exists {
		// Существующего dev-пользователя делаем админом (доступ к админке).
		if u.Role != "admin" {
			if err := r.SetUserRole(ctx, u.ID, "admin"); err != nil {
				return err
			}
			log.Printf("dev user %q promoted to admin", login)
		}
		return nil
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	userID, err := r.CreateUser(ctx, login, hash)
	if err != nil {
		return err
	}
	// Dev-пользователь получает роль админа — доступ к админ-странице.
	if err := r.SetUserRole(ctx, userID, "admin"); err != nil {
		return err
	}
	log.Printf("dev user %q created (admin)", login)
	return nil
}
