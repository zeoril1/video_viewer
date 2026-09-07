// catalog — микросервис каталога: объединённый каталог (IMDb + TMDB +
// локальные магнеты), детали фильмов, поиск источников (раздач) через
// Jackett. Фоново синхронизирует чарты IMDb/TMDB и локализует каталог.
// Владеет PostgreSQL (таблицы films/sources).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/catalogapi"
	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/imdb"
	"github.com/zeoril1/video_viewer/internal/magnet"
	"github.com/zeoril1/video_viewer/internal/proxy"
	"github.com/zeoril1/video_viewer/internal/sync"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	var (
		addr            = flag.String("addr", ":8081", "HTTP listen address")
		catalogPath     = flag.String("catalog", "data/catalog.json", "path to the catalog JSON file")
		dsn             = flag.String("dsn", os.Getenv("DATABASE_URL"), "PostgreSQL DSN (или env DATABASE_URL)")
		imdbInterval    = flag.Duration("imdb-interval", sync.DefaultInterval, "IMDb charts sync interval")
		imdbScrape      = flag.Bool("imdb-scrape", true, "parse official IMDb chart pages")
		imdbTop250URL   = flag.String("imdb-top250-url", imdb.MirrorTop250Default, "JSON mirror URL for Top 250 (fallback)")
		imdbPopularURL  = flag.String("imdb-popular-url", "", "JSON mirror URL for popular (fallback, optional)")
		localizeInt     = flag.Duration("localize-interval", 10*time.Minute, "background RU localization retry interval")
		localizeWork    = flag.Int("localize-workers", 3, "parallel requests for RU plot localization")
		jackettURL      = flag.String("jackett-url", envOr("JACKETT_URL", ""), "Jackett base URL for Torznab search (e.g. http://127.0.0.1:9117; empty = disabled)")
		jackettAPIKey   = flag.String("jackett-api-key", envOr("JACKETT_API_KEY", ""), "Jackett API key")
		jackettIndexer  = flag.String("jackett-indexer", envOr("JACKETT_INDEXER", "rutracker-ru"), "Jackett indexer IDs: single, comma-separated, or 'all'")
		tmdbURL         = flag.String("tmdb-url", tmdb.DefaultBaseURL, "TMDB API v3 base URL")
		ratingsInterval = flag.Duration("ratings-interval", sync.DefaultRatingsInterval, "background ratings refresh interval")
		ratingsBatch    = flag.Int("ratings-batch", 200, "films per ratings refresh pass")
		ratingsPace     = flag.Duration("ratings-pace", 400*time.Millisecond, "delay between TMDB requests in ratings refresh")
		proxyStatic     = flag.String("proxy-static", envOr("PROXY_STATIC", ""), "comma-separated static proxies host:port")
		proxyTimeout    = flag.Duration("proxy-timeout", envDur("PROXY_TIMEOUT", 10*time.Second), "proxy connect timeout")
		proxyMax        = flag.Int("proxy-max-attempts", envInt("PROXY_MAX_ATTEMPTS", 3), "proxy failover attempts per request")
	)
	flag.Parse()

	// Контекст приложения для фоновых задач и graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Прокси для запросов к клиенту IMDb — только статические (PROXY_STATIC).
	// Динамические списки (PROXY_SOURCES) и их периодическое обновление
	// не используются.
	proxyPool := proxy.NewPool(proxy.Config{
		Static:  splitCSV(*proxyStatic),
		Timeout: *proxyTimeout,
	})
	if n := proxyPool.Size(); n > 0 {
		log.Printf("proxy: %d static proxies configured", n)
	}

	cat, err := catalog.Load(*catalogPath)
	if err != nil {
		log.Fatalf("load catalog: %v", err)
	}
	log.Printf("catalog loaded: %d items", len(cat.Items))

	// PostgreSQL (нужен для IMDb-каталога; без него сервис работает
	// только с локальными магнет-ссылками). Подключаемся с ретраями: при
	// одновременном старте стека БД может быть ещё не готова, и разовый
	// сбой не должен оставлять каталог пустым.
	var repo *db.Repo
	if *dsn != "" {
		conn, err := db.OpenRetry(ctx, *dsn, 12, 5*time.Second)
		if err != nil {
			log.Printf("warn: cannot connect to db (%v); IMDb-синхронизация отключена", err)
		} else {
			repo = db.NewRepo(conn)
			defer repo.Close()

			initCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := repo.EnsureSchema(initCtx)
			cancel()
			if err != nil {
				log.Printf("warn: ensure schema: %v", err)
			} else {
				log.Printf("postgres connected, schema ready")
				if err := repo.SeedDevUser(context.Background(), os.Getenv("DEV_USER"), os.Getenv("DEV_PASSWORD")); err != nil {
					log.Printf("warn: seed dev user: %v", err)
				}
			}
		}
	} else {
		log.Printf("warn: DATABASE_URL не задан — фильмы IMDb недоступны")
	}

	imdbClient := imdb.NewClient(imdb.Config{
		Scrape:     *imdbScrape,
		Top250URL:  *imdbTop250URL,
		PopularURL: *imdbPopularURL,
		Timeout:    30 * time.Second,
		Transport:  proxyPool.RoundTripper(*proxyMax),
	})

	// TMDB (The Movie Database). Требует ключи в env TMDB_API_KEY и/или
	// TMDB_ACCESS_TOKEN.
	var tmdbClient *tmdb.Client
	tmKey := os.Getenv("TMDB_API_KEY")
	tmToken := os.Getenv("TMDB_ACCESS_TOKEN")
	if tmKey != "" || tmToken != "" {
		tmdbClient = tmdb.NewClient(tmKey, tmToken, *tmdbURL)
		log.Printf("tmdb client ready (v3)")
	} else {
		log.Printf("warn: TMDB_API_KEY / TMDB_ACCESS_TOKEN не заданы — TMDB (чарты/поиск) отключён")
	}

	// Фоновая синхронизация чартов IMDb (сразу при старте и далее раз в 24 часа).
	if repo != nil {
		syncer := sync.New(repo, imdbClient, *imdbInterval)
		go syncer.Run(ctx)

		// Фоновая синхронизация чартов TMDB.
		if tmdbClient != nil {
			tmSyncer := sync.NewTMDBChart(repo, tmdbClient, *imdbInterval)
			go tmSyncer.Run(ctx)

			// Фоновая джоба обновления рейтингов фильмов из TMDB
			// (редко и с паузой между запросами, чтобы не заблокировали).
			ratings := sync.NewRatingsRefresher(repo, tmdbClient, *ratingsBatch, *ratingsPace, *ratingsInterval)
			go ratings.Run(ctx)
		}

		// Фоновая локализация каталога (русские названия и описания).
		localizer := sync.NewLocalizer(repo, imdbClient, 200, 40, *localizeWork, *localizeInt)
		go localizer.Run(ctx)
	}

	// Поиск источников идёт ТОЛЬКО через Jackett (Torznab API).
	var jackettProv magnet.Provider
	if *jackettURL != "" {
		if *jackettAPIKey == "" {
			log.Printf("warn: JACKETT_URL задан, но JACKETT_API_KEY пуст — поиск источников не будет работать")
		}
		jackettProv = magnet.NewJackett(*jackettURL, *jackettAPIKey, *jackettIndexer, nil)
		log.Printf("jackett: %s indexer=%s", *jackettURL, *jackettIndexer)
	} else {
		log.Printf("warn: JACKETT_URL не задан — поиск источников (раздач) отключён")
	}

	log.Printf("config: addr=%s db=%v imdb=%v tmdb=%v jackett=%v indexer=%q",
		*addr, repo != nil, imdbClient != nil, tmdbClient != nil, jackettProv != nil, *jackettIndexer)

	handler := catalogapi.NewServer(catalogapi.Config{
		Catalog: cat,
		DB:      repo,
		IMDB:    imdbClient,
		TMDB:    tmdbClient,
		Magnet:  jackettProv,
		Context: ctx, // родитель фоновых задач — останавливаются при shutdown
	})

	httpx.Serve(ctx, *addr, handler, nil)
}

// splitCSV разбивает строку вида "a,b,c" на список строк (без пустых).
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envOr возвращает значение env-переменной key или def, если она пуста.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envDur возвращает значение env-переменной key как длительность или def.
func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// envInt возвращает значение env-переменной key как целое или def.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
