// backfill — одноразовый инструмент заполнения пустых полей каталога из TMDB,
// запускается вручную и отдельно от сервиса (не фоновая джоба):
//
//	go run ./cmd/backfill -dsn "postgres://video_viewer:video_viewer@localhost:5432/video_viewer?sslmode=disable"
//
// Требует DATABASE_URL (или -dsn) и TMDB_API_KEY / TMDB_ACCESS_TOKEN; заполняет
// пустые поля (рейтинг, описания, жанры, постер, длительность, tmdb_id), уже
// заполненные не затирает.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/sync"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

func main() {
	var (
		dsn   = flag.String("dsn", os.Getenv("DATABASE_URL"), "PostgreSQL DSN (или env DATABASE_URL)")
		batch = flag.Int("batch", 100, "сколько фильмов за один запрос к БД")
		pace  = flag.Duration("pace", 300*time.Millisecond, "пауза между запросами к TMDB")
		limit = flag.Int("limit", 0, "максимум фильмов за весь прогон (0 — все)")
	)
	flag.Parse()

	if *dsn == "" {
		log.Fatal("DATABASE_URL не задан (или -dsn)")
	}
	tmKey := os.Getenv("TMDB_API_KEY")
	tmToken := os.Getenv("TMDB_ACCESS_TOKEN")
	if tmKey == "" && tmToken == "" {
		log.Fatal("TMDB_API_KEY / TMDB_ACCESS_TOKEN не заданы")
	}

	ctx := context.Background()

	conn, err := db.Open(*dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	repo := db.NewRepo(conn)
	defer repo.Close()

	if err := repo.EnsureSchema(ctx); err != nil {
		log.Printf("warn: ensure schema: %v", err)
	}

	tm := tmdb.NewClient(tmKey, tmToken, tmdb.DefaultBaseURL)

	total, err := repo.FilmsMissingDataCount(ctx)
	if err != nil {
		log.Fatalf("count: %v", err)
	}
	if total == 0 {
		log.Println("пустых полей нет — бэкфилл не нужен")
		return
	}
	log.Printf("записей с пустыми полями: %d", total)

	// seen — уже пытавшиеся в этом прогоне: ненайденные на TMDB записи остаются
	// в выборке, и без счётчика прогон зациклился бы.
	seen := map[string]bool{}
	attempted, filled := 0, 0
	for {
		if *limit > 0 && attempted >= *limit {
			break
		}
		want := *batch
		if *limit > 0 && *limit-attempted < want {
			want = *limit - attempted
		}

		films, err := repo.FilmsMissingData(ctx, want)
		if err != nil {
			log.Fatalf("list: %v", err)
		}

		// Отбираем ещё не пытавшиеся записи.
		queue := films[:0:0]
		for _, f := range films {
			if !seen[f.IMDBID] {
				seen[f.IMDBID] = true
				queue = append(queue, f)
			}
		}
		if len(queue) == 0 {
			break // все записи из выборки уже попробованы — всё, что можно, заполнено
		}

		for _, f := range queue {
			if err := sync.RefreshFilmData(ctx, repo, tm, nil, f.IMDBID); err != nil {
				log.Printf("fill %s: %v", f.IMDBID, err)
			} else {
				filled++
			}
			attempted++
			time.Sleep(*pace)
		}
		log.Printf("прогресс: попыток %d, заполнено %d (осталось ~%d)", attempted, filled, total-attempted)
	}
	log.Printf("готово: попыток %d, заполнено %d", attempted, filled)
}
