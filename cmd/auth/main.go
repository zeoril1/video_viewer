// auth — микросервис авторизации и истории просмотра: регистрация/вход/выход,
// сессии (httpOnly-кука), история с позицией. Требует PostgreSQL (без БД — 503).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/zeoril1/video_viewer/internal/authapi"
	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/httpx"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	var (
		addr         = flag.String("addr", ":8083", "HTTP listen address")
		dsn          = flag.String("dsn", os.Getenv("DATABASE_URL"), "PostgreSQL DSN (или env DATABASE_URL)")
		cookieSecure = flag.Bool("cookie-secure", envBool("COOKIE_SECURE", false), "set Secure flag on the session cookie (HTTPS)")
	)
	flag.Parse()

	// Контекст приложения для graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var repo *db.Repo
	if *dsn != "" {
		// Ретраи: при одновременном старте стека БД может быть ещё не готова.
		conn, err := db.OpenRetry(ctx, *dsn, 12, 5*time.Second)
		if err != nil {
			log.Fatalf("connect to db: %v", err)
		}
		repo = db.NewRepo(conn)
		defer repo.Close()

		initCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err = repo.EnsureSchema(initCtx)
		cancel()
		if err != nil {
			log.Fatalf("ensure schema: %v", err)
		}
		log.Printf("postgres connected, schema ready")
		if err := repo.SeedDevUser(context.Background(), os.Getenv("DEV_USER"), os.Getenv("DEV_PASSWORD")); err != nil {
			log.Printf("warn: seed dev user: %v", err)
		}
	} else {
		log.Printf("warn: DATABASE_URL не задан — авторизация и история отключены (503)")
	}

	log.Printf("config: addr=%s db=%v", *addr, repo != nil)

	handler := authapi.NewServer(authapi.Config{DB: repo, SecureCookies: *cookieSecure})

	httpx.Serve(ctx, *addr, handler, nil)
}

// envBool возвращает значение env-переменной key как bool или def.
func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
