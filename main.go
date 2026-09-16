// Пакет main — диспетчер микросервисов video_viewer.
//
// Монолит разбит на микросервисы (см. cmd/):
//
//	go run . gateway   — API-шлюз (web + прокси /api/* на сервисы)
//	go run . catalog   — каталог (IMDb/TMDB/Jackett, PostgreSQL)
//	go run . stream    — стриминг (торрент-клиент, HLS/ffmpeg)
//	go run . auth      — авторизация и история просмотра
//	go run . iptv      — IPTV (плейлисты M3U/Xtream, каналы, EPG, live-HLS)
//
// Диспетчер просто запускает нужный сервис через go run ./cmd/<svc>,
// чтобы сохранить привычный вход через go run .
package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	services := map[string]string{
		"gateway": "cmd/gateway",
		"catalog": "cmd/catalog",
		"stream":  "cmd/stream",
		"auth":    "cmd/auth",
		"iptv":    "cmd/iptv",
	}

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run . [gateway|catalog|stream|auth|iptv] [flags...]")
		os.Exit(2)
	}
	dir, ok := services[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown service %q (use: gateway, catalog, stream, auth, iptv)\n", os.Args[1])
		os.Exit(2)
	}

	args := append([]string{"run", "./" + dir}, os.Args[2:]...)
	cmd := exec.Command("go", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}
