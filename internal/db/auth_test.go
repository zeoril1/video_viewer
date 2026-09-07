package db

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

func TestHashVerifyPassword(t *testing.T) {
	const pass = "correct horse battery staple"
	h, err := HashPassword(pass)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(pass, h) {
		t.Error("VerifyPassword не принял верный пароль")
	}
	if VerifyPassword("wrong", h) {
		t.Error("VerifyPassword принял неверный пароль")
	}
}

// Хеши с разным числом итераций проверяются корректно (формат хранит iter),
// т.е. старые 120k-хеши продолжают работать после поднятия pbkdf2Iter до 600k.
func TestVerifyPasswordLegacyIter(t *testing.T) {
	const pass = "pasha333"
	const legacyIter = 120_000
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	key, err := pbkdf2.Key(sha256.New, pass, salt, legacyIter, 32)
	if err != nil {
		t.Fatal(err)
	}
	stored := fmt.Sprintf("pbkdf2$%d$%s$%s", legacyIter, hex.EncodeToString(salt), hex.EncodeToString(key))
	if !VerifyPassword(pass, stored) {
		t.Error("VerifyPassword не принял хеш с устаревшим числом итераций (120k)")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	for _, h := range []string{
		"",
		"pbkdf2",
		"pbkdf2$1000",
		"bcrypt$2a$10$...",
		"pbkdf2$x$zz$zz",
		"pbkdf2$100$!$!",
		"plaintext",
	} {
		if VerifyPassword("whatever", h) {
			t.Errorf("VerifyPassword принял некорректный хеш %q", h)
		}
	}
}

// SeedDevUser с пустыми кредами возвращается раньше обращения к БД —
// безопасно вызывается даже с nil-подключением.
func TestSeedDevUserSkipsEmptyCreds(t *testing.T) {
	r := &Repo{}
	if err := r.SeedDevUser(context.Background(), "", ""); err != nil {
		t.Errorf("SeedDevUser('','') = %v, want nil", err)
	}
	if err := r.SeedDevUser(context.Background(), "   ", "x"); err != nil {
		t.Errorf("SeedDevUser(пробелы,'x') = %v, want nil", err)
	}
}
