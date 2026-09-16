package httpx

import (
	"context"
	"log"
	"net/http"
	"time"
)

// Serve запускает HTTP-сервер на addr и блокируется до отмены ctx
// (SIGINT/SIGTERM), после чего корректно завершает сервер и вызывает cleanup.
func Serve(ctx context.Context, addr string, handler http.Handler, cleanup func()) {
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout — простаивающие keep-alive соединения; на долгие стримы
		// (Range/HLS) не влияет.
		IdleTimeout: 120 * time.Second,
	}
	go func() {
		log.Printf("http: listening on http://localhost%s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = server.Shutdown(shutdownCtx)
	cancel()
	if cleanup != nil {
		cleanup()
	}
}
