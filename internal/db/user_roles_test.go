package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestRoleValidation(t *testing.T) {
	for _, role := range []string{RoleUser, RoleModerator, RoleAdmin} {
		if !ValidRole(role) {
			t.Errorf("valid role rejected: %s", role)
		}
	}
	for _, role := range []string{"", "ADMIN", " admin", "owner"} {
		if ValidRole(role) {
			t.Errorf("invalid role accepted: %q", role)
		}
		r := &Repo{}
		if err := r.SetUserRole(context.Background(), 1, role); !errors.Is(err, ErrInvalidRole) {
			t.Fatalf("SetUserRole: %v", err)
		}
		if _, err := r.ChangeUserRole(context.Background(), 1, 1, role); !errors.Is(err, ErrInvalidRole) {
			t.Fatalf("ChangeUserRole: %v", err)
		}
	}
}

func TestZeorilRoleMigration(t *testing.T) {
	dsn := os.Getenv("FEATURE_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("FEATURE_TEST_DATABASE_URL or TEST_DATABASE_URL required")
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	ctx := context.Background()
	schema := fmt.Sprintf("role_test_%d", time.Now().UnixNano())
	if _, err = conn.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	if _, err = conn.ExecContext(ctx, `SET search_path TO `+schema); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, authSchema); err != nil {
		t.Fatal(err)
	}
	repo := NewRepo(conn)
	zeorilID, err := repo.CreateUser(ctx, "zeoril", "unchanged-password-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.ensureAuthSchema(ctx); err != nil {
		t.Fatal(err)
	}
	u, passwordHash, exists, err := repo.GetUserByUsername(ctx, "zeoril")
	if err != nil || !exists || u.Role != RoleAdmin || passwordHash != "unchanged-password-hash" {
		t.Fatalf("bootstrap: %+v %q %v %v", u, passwordHash, exists, err)
	}
	secondID, err := repo.CreateUser(ctx, "second-admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.SetUserRole(ctx, secondID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ChangeUserRole(ctx, secondID, zeorilID, RoleModerator); err != nil {
		t.Fatal(err)
	}
	if err = repo.ensureAuthSchema(ctx); err != nil {
		t.Fatal(err)
	}
	u, _, _, err = repo.GetUserByUsername(ctx, "zeoril")
	if err != nil || u.Role != RoleModerator {
		t.Fatal("bootstrap replaced a later admin decision", err, u.Role)
	}
	if _, err = conn.ExecContext(ctx, `UPDATE users SET role = 'owner' WHERE id = $1`, zeorilID); err == nil {
		t.Fatal("database accepts unknown roles")
	}
	if _, err = repo.ChangeUserRole(ctx, zeorilID, secondID, RoleUser); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("moderator changed role: %v", err)
	}
	if _, err = repo.ChangeUserRole(ctx, secondID, secondID, RoleUser); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last admin removed: %v", err)
	}
	// A clean installation has no existing named account to promote. Marking
	// the migration prevents public signup from receiving this privilege later.
	if _, err = conn.ExecContext(ctx, `DROP TABLE sessions, users, auth_role_migrations CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err = repo.ensureAuthSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.CreateUser(ctx, "zeoril", "new-hash"); err != nil {
		t.Fatal(err)
	}
	if err = repo.ensureAuthSchema(ctx); err != nil {
		t.Fatal(err)
	}
	u, _, _, err = repo.GetUserByUsername(ctx, "zeoril")
	if err != nil || u.Role != RoleUser {
		t.Fatal("new public account received bootstrap privilege", err, u.Role)
	}
}
