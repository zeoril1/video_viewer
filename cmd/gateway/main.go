// gateway — API-шлюз: раздаёт статику фронтенда (web/) и проксирует
// /api/* на внутренние микросервисы (catalog, stream, auth).
// Это единственная публичная точка входа (порт 8080).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/zeoril1/video_viewer/internal/gatewayapi"
	"github.com/zeoril1/video_viewer/internal/httpx"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	var (
		addr       = flag.String("addr", ":8080", "HTTP listen address")
		webDir     = flag.String("web", "web", "path to static web assets directory")
		catalogURL = flag.String("catalog-url", envOr("CATALOG_URL", "http://127.0.0.1:8081"), "catalog-service base URL")
		streamURL  = flag.String("stream-url", envOr("STREAM_URL", "http://127.0.0.1:8082"), "stream-service base URL")
		authURL    = flag.String("auth-url", envOr("AUTH_URL", "http://127.0.0.1:8083"), "auth-service base URL")
	)
	flag.Parse()

	// Контекст приложения для graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("config: addr=%s catalog=%s stream=%s auth=%s web=%s",
		*addr, *catalogURL, *streamURL, *authURL, *webDir)

	handler := gatewayapi.NewServer(gatewayapi.Config{
		WebDir:     *webDir,
		CatalogURL: *catalogURL,
		StreamURL:  *streamURL,
		AuthURL:    *authURL,
	})

	httpx.Serve(ctx, *addr, handler, nil)
}

// envOr возвращает значение env-переменной key или def, если она пуста.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
