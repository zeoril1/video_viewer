// stream — микросервис стриминга: торрент-клиент (in-memory), стриминг
// с поддержкой Range, список файлов торрента, HLS-транскодинг (ffmpeg).
// Без БД: магнет-ссылку берёт из query-параметра выбранной раздачи.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/streamapi"
	"github.com/zeoril1/video_viewer/internal/tmdb"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	var (
		maxSessions = flag.Int("max-sessions", 4, "maximum concurrent HLS processes")
		maxCache    = flag.Int64("cache-bytes", 40<<30, "maximum logical spool size in bytes")
		maxHLS      = flag.Int64("hls-bytes", 8<<30, "maximum HLS output bytes (one-second watchdog)")
		minFree     = flag.Int64("min-free-bytes", 2<<30, "minimum free disk space")
		addr        = flag.String("addr", ":8082", "HTTP listen address")
		port        = flag.Int("port", 0, "torrent client listen port (0 = random)")
		// spoolDir — каталог дискового спула скачанных кусков (по файлу на
		// серию; файлы удаляются после просмотра). Пусто — данные в RAM.
		spoolDir = flag.String("spool-dir", envOr("STREAM_DATA_DIR", ""), "каталог для временного дискового спула, по файлу на серию (пусто — данные в RAM)")
		// readahead — объём упреждающего скачивания вперёд от позиции чтения.
		readahead = flag.Int64("readahead", 0, "объём упреждающего скачивания в байтах (0 — по умолчанию)")
		// cacheTTL — время жизни «тёплого» кеша (торрент, просмотренный больше
		// чем на 5%, отдаётся другим клиентам той же раздачи без перекачивания).
		cacheTTL = flag.Duration("cache-ttl", 24*time.Hour, "TTL тёплого кеша после просмотра >5% (0 — 24ч)")
		// cacheMax — максимум «тёплых» торрентов; при переполнении выгружается самый старый.
		cacheMax = flag.Int("cache-max", 16, "максимум тёплых торрентов в кеше")
		// tmdbURL — базовый адрес TMDB (ключи — TMDB_API_KEY/TMDB_ACCESS_TOKEN).
		// Без ключей раскладка файлов сериалов по сезонам TMDB отключена.
		tmdbURL = flag.String("tmdb-url", tmdb.DefaultBaseURL, "TMDB API v3 base URL")
	)
	flag.Parse()
	if *maxSessions < 1 || *maxCache < 1 || *maxHLS < 1 || *minFree < 0 {
		log.Fatal("invalid resource limits")
	}

	// Контекст приложения для graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mgr, err := torrents.NewManager(torrents.Config{
		MaxCacheBytes: *maxCache,
		MinFreeBytes:  *minFree,
		ListenPort:    *port,
		SpoolDir:      *spoolDir,
		CacheTTL:      *cacheTTL,
		MaxCached:     *cacheMax,
	})
	if err != nil {
		log.Fatalf("create torrent manager: %v", err)
	}
	defer mgr.Close()

	// TMDB опционален: нужен, чтобы раскладывать файлы сериалов по сезонам
	// TMDB (у трекеров своя нарезка, сборники нумеруют серии сквозняком);
	// без ключей сезон берётся из имени файла.
	var tmdbClient *tmdb.Client
	tmKey := os.Getenv("TMDB_API_KEY")
	tmToken := os.Getenv("TMDB_ACCESS_TOKEN")
	if tmKey != "" || tmToken != "" {
		tmdbClient = tmdb.NewClient(tmKey, tmToken, *tmdbURL)
		log.Printf("tmdb client ready (сезоны сериалов)")
	} else {
		log.Printf("warn: TMDB_API_KEY / TMDB_ACCESS_TOKEN не заданы — раскладка серий по сезонам TMDB отключена")
	}

	handler, stopHLS := streamapi.NewServer(streamapi.Config{
		AuthURL:                envOr("AUTH_URL", "http://127.0.0.1:8083"),
		Context:                ctx,
		AnalysisStoreURL:       envOr("SEGMENTS_CATALOG_URL", "http://127.0.0.1:8081"),
		DisableSegmentAnalysis: os.Getenv("SEGMENTS_ANALYSIS_ENABLED") == "false",
		MaxSessions:            *maxSessions,
		MaxHLSBytes:            *maxHLS,
		MinFreeBytes:           *minFree,
		Torrents:               mgr,
		Addr:                   *addr,
		Readahead:              *readahead,
		TMDB:                   tmdbClient,
	})

	// При shutdown останавливаем ffmpeg-сессии (иначе процессы осиротеют).
	httpx.Serve(ctx, *addr, handler, func() {
		stopHLS()
	})
}

// envOr возвращает значение env-переменной key или def, если она пуста.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
