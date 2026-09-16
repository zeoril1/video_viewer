package main

// iptv — микросервис IPTV: плейлисты (M3U/M3U8, Xtream Codes), каналы,
// телепрограмма (XMLTV) и live-воспроизведение: HLS проксируется, MPEG-TS
// перепаковывается в HLS через ffmpeg; адреса и логины провайдера скрыты.

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/iptvapi"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	var (
		addr = flag.String("addr", ":8084", "HTTP listen address")
		dsn  = flag.String("dsn", os.Getenv("DATABASE_URL"), "PostgreSQL DSN (или env DATABASE_URL)")
		// dataDir — каталог временных live-сессий ffmpeg (в compose монтируется
		// томом, чтобы кэш не рос в слое контейнера).
		dataDir = flag.String("data-dir", envOr("IPTV_DATA_DIR", ""), "каталог для временных HLS-сессий (пусто — системный temp)")
		ffmpeg  = flag.String("ffmpeg", envOr("IPTV_FFMPEG", "ffmpeg"), "путь к ffmpeg")
		// maxSessions — сколько каналов смотреть одновременно (каждый TS-канал — свой ffmpeg).
		maxSessions = flag.Int("max-sessions", 8, "максимум одновременных live-сессий ffmpeg")
		sessionIdle = flag.Duration("session-idle", 90*time.Second, "гасить live-сессию после N секунд без запросов")
		// syncInterval/epgInterval — фоновое обновление каналов и программы.
		syncInterval = flag.Duration("sync-interval", 6*time.Hour, "период обновления плейлистов (0 — только вручную)")
		epgInterval  = flag.Duration("epg-interval", 3*time.Hour, "период обновления телепрограммы (0 — только вручную)")
		// refURL — справочник каналов iptv-org (русские названия каналов).
		refURL = flag.String("ref-url", envOr("IPTV_REF_URL", ""), "справочник каналов iptv-org (пусто — стандартный)")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *dsn == "" {
		log.Fatal("DATABASE_URL не задан: сервису IPTV нужна общая БД (таблицы iptv_*)")
	}
	conn, err := db.OpenRetry(ctx, *dsn, 12, 5*time.Second)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	repo := db.NewRepo(conn)
	defer repo.Close()
	if err := repo.EnsureSchema(ctx); err != nil {
		log.Fatalf("ensure schema: %v", err)
	}
	log.Printf("postgres connected, schema ready")

	dir := *dataDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "video-viewer-iptv")
	}

	handler, shutdown := iptvapi.NewServer(iptvapi.Config{
		DB:           repo,
		DataDir:      dir,
		MaxSessions:  *maxSessions,
		SessionIdle:  *sessionIdle,
		FFmpegPath:   *ffmpeg,
		SyncInterval: *syncInterval,
		EPGInterval:  *epgInterval,
		RefURL:       *refURL,
		Context:      ctx,
	})
	log.Printf("config: addr=%s data_dir=%s sessions=%d sync=%s epg=%s", *addr, dir, *maxSessions, *syncInterval, *epgInterval)

	httpx.Serve(ctx, *addr, handler, shutdown)
}

// envOr возвращает значение env-переменной key или def, если она пуста.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
