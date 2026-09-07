// stream — микросервис стриминга: торрент-клиент (in-memory), стриминг
// с поддержкой Range, список файлов торрента, HLS-транскодинг (ffmpeg).
// Не владеет БД: магнет-ссылку берёт из query-параметра, либо (по id)
// резолвит через catalog-сервис (CATALOG_URL).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/zeoril1/video_viewer/internal/client"
	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/streamapi"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	var (
		addr       = flag.String("addr", ":8082", "HTTP listen address")
		port       = flag.Int("port", 0, "torrent client listen port (0 = random)")
		catalogURL = flag.String("catalog-url", envOr("CATALOG_URL", "http://127.0.0.1:8081"), "catalog-service base URL (для резолва магнета по id)")
	)
	flag.Parse()

	// Контекст приложения для graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mgr, err := torrents.NewManager(torrents.Config{ListenPort: *port})
	if err != nil {
		log.Fatalf("create torrent manager: %v", err)
	}
	defer mgr.Close()

	// Резолв магнет-ссылки по id через catalog-сервис (nil — отключено,
	// тогда /api/stream/{id} без ?magnet= возвращает 404).
	resolver := client.NewCatalogClient(*catalogURL)
	if resolver == nil {
		log.Printf("warn: CATALOG_URL не задан — резолв магнета по id отключён (нужен явный ?magnet=)")
	} else {
		log.Printf("catalog client: %s", *catalogURL)
	}

	log.Printf("config: addr=%s torrent_port=%d catalog=%v", *addr, *port, resolver != nil)

	handler, stopHLS := streamapi.NewServer(streamapi.Config{
		Torrents: mgr,
		Addr:     *addr,
		Resolver: resolver,
	})

	// Останавливаем ffmpeg-сессии при shutdown (иначе осиротевшие процессы
	// продолжили бы работать).
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
