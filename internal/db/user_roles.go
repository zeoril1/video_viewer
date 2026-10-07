package db

import (
	"context"
	"database/sql"
	"errors"
)

const (
	RoleUser      = "user"
	RoleModerator = "moderator"
	RoleAdmin     = "admin"
)

var (
	ErrInvalidRole   = errors.New("invalid user role")
	ErrUserNotFound  = errors.New("user not found")
	ErrLastAdmin     = errors.New("cannot remove the last administrator")
	ErrAdminRequired = errors.New("admin role required")
)

func ValidRole(role string) bool {
	return role == RoleUser || role == RoleModerator || role == RoleAdmin
}

func (r *Repo) ensureUserRoles(ctx context.Context) error {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Multiple services share this schema. Serialize role migration and role
	// changes so a concurrent startup cannot replace a later admin decision.
	if _, err = tx.ExecContext(ctx, `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE users SET role = 'user' WHERE role NOT IN ('user', 'moderator', 'admin');
		DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_role_valid' AND conrelid = 'users'::regclass) THEN
				ALTER TABLE users ADD CONSTRAINT users_role_valid CHECK (role IN ('user', 'moderator', 'admin'));
			END IF;
		END $$;
		CREATE TABLE IF NOT EXISTS auth_role_migrations (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		WITH applied AS (
			INSERT INTO auth_role_migrations (name) VALUES ('20261007_zeoril_admin')
			ON CONFLICT DO NOTHING RETURNING name
		)
		UPDATE users SET role = 'admin'
		WHERE username = 'zeoril' AND EXISTS (SELECT 1 FROM applied);
	`); err != nil {
		return err
	}
	// The migration is recorded even when the account does not exist. A later
	// public registration with this username must not acquire admin access.
	return tx.Commit()
}

func (r *Repo) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := r.conn.QueryContext(ctx, `SELECT id, username, role, created_at FROM users ORDER BY lower(username), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]User, 0)
	for rows.Next() {
		var u User
		if err = rows.Scan(&u.ID, &u.Username, &u.Role, &u.Created); err != nil {
			return nil, err
		}
		items = append(items, u)
	}
	return items, rows.Err()
}

// ChangeUserRole checks the actor again within the transaction and retains at
// least one admin, including when two administrators demote one another.
func (r *Repo) ChangeUserRole(ctx context.Context, actorID, userID int64, role string) (User, error) {
	if !ValidRole(role) {
		return User{}, ErrInvalidRole
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return User{}, err
	}
	var actorRole string
	if err = tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id = $1`, actorID).Scan(&actorRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrAdminRequired
		}
		return User{}, err
	}
	if actorRole != RoleAdmin {
		return User{}, ErrAdminRequired
	}
	var u User
	if err = tx.QueryRowContext(ctx, `SELECT id, username, role, created_at FROM users WHERE id = $1`, userID).Scan(&u.ID, &u.Username, &u.Role, &u.Created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, err
	}
	if u.Role == RoleAdmin && role != RoleAdmin {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin'`).Scan(&count); err != nil {
			return User{}, err
		}
		if count <= 1 {
			return User{}, ErrLastAdmin
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET role = $2 WHERE id = $1`, userID, role); err != nil {
		return User{}, err
	}
	u.Role = role
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}
