package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zeoril1/video_viewer/internal/torrentvpn"
)

func main() {
	addr := flag.String("addr", ":8085", "internal control HTTP listen address")
	proxyAddr := flag.String("proxy-addr", ":8888", "internal torrent download HTTP proxy listen address")
	path := flag.String("config", "/app/vpn/wg0.conf", "persistent WireGuard configuration file")
	authURL := os.Getenv("AUTH_URL")
	if authURL == "" {
		authURL = "http://127.0.0.1:8083"
	}
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	manager := torrentvpn.NewManager(*path)
	defer manager.Close()
	control := &http.Server{Addr: *addr, Handler: torrentvpn.NewControlHandler(manager, authURL), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	proxy := &http.Server{Addr: *proxyAddr, Handler: torrentvpn.NewProxyHandler(manager), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	failed := make(chan error, 2)
	for _, server := range []*http.Server{control, proxy} {
		go func(s *http.Server) {
			if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				failed <- err
			}
		}(server)
	}
	log.Printf("torrent-vpn: control=%s proxy=%s state=%s", *addr, *proxyAddr, manager.Status().State)
	select {
	case <-ctx.Done():
	case <-failed:
		log.Print("torrent-vpn: HTTP listener failed")
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	manager.Close()
	_ = proxy.Shutdown(shutdown)
	_ = control.Shutdown(shutdown)
}
