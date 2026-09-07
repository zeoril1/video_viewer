// Package streamapi — HTTP-сервис стриминга (микросервис stream):
// торрент-клиент (in-memory), стриминг с поддержкой Range, список файлов
// торрента, HLS-транскодинг (ffmpeg). Не владеет БД: магнет-ссылку берёт
// из query-параметра, либо (по id) резолвит через внутренний эндпоинт
// catalog-сервиса.
package streamapi

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"

	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

// MagnetResolver — резолв магнет-ссылки по id записи. В проде — клиент
// catalog-сервиса; может быть nil, тогда /api/stream/{id} без ?magnet=
// возвращает 404.
type MagnetResolver interface {
	FindMagnet(ctx context.Context, id string) (string, bool)
}

// Config — зависимости HTTP-сервиса стриминга.
type Config struct {
	Torrents *torrents.Manager // торрент-клиент
	Addr     string            // адрес прослушивания (для внутреннего URL ffmpeg)
	Resolver MagnetResolver    // опциональный резолв магнета по id (catalog-сервис)
}

// NewServer собирает HTTP-обработчики стриминга в один mux. Возвращает
// обработчик и функцию остановки фоновых ресурсов (ffmpeg-сессий) для
// graceful shutdown.
func NewServer(cfg Config) (http.Handler, func()) {
	mux := http.NewServeMux()

	// HLS-транскодинг (ffmpeg) для воспроизведения звука в браузере
	// и выбора звуковой дорожки.
	hls := newHLSManager(selfBase(cfg.Addr))
	go hls.cleanup()

	// GET /api/stream/{id} — стриминг магнет-видео (с поддержкой Range).
	// Необязательный параметр magnet=... задаёт конкретную магнет-ссылку
	// (выбранный пользователем вариант из /api/films/{id}/sources); без
	// него магнет резолвится по id через catalog-сервис.
	mux.HandleFunc("GET /api/stream/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/stream/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		m := strings.TrimSpace(r.URL.Query().Get("magnet"))
		if m == "" {
			if cfg.Resolver == nil {
				http.NotFound(w, r)
				return
			}
			var ok bool
			m, ok = cfg.Resolver.FindMagnet(r.Context(), id)
			if !ok {
				http.NotFound(w, r)
				return
			}
		}
		handleStream(cfg.Torrents, catalog.Item{ID: id, Magnet: m})(w, r)
	})

	// GET /api/films/{imdbID}/files — список видеофайлов торрента
	// с сезонами/сериями (для селектора серий сериала): ?magnet=...
	mux.HandleFunc("GET /api/films/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		handleTorrentFiles(cfg.Torrents)(w, r)
	})

	// GET /api/films/{imdbID}/tracks — звуковые дорожки торрента (ffprobe).
	mux.HandleFunc("GET /api/films/{id}/tracks", func(w http.ResponseWriter, r *http.Request) {
		handleTracks(hls)(w, r)
	})

	// GET /api/films/{imdbID}/hls.m3u8 — HLS-плейлист (ffmpeg, выбранная дорожка).
	mux.HandleFunc("GET /api/films/{id}/hls.m3u8", func(w http.ResponseWriter, r *http.Request) {
		hls.servePlaylist(w, r)
	})

	// GET /api/films/{imdbID}/hls/pl.m3u8 — media-плейлист (video+audio)
	// сессии; вызывается hls.js из master-плейлиста при включённых субтитрах.
	mux.HandleFunc("GET /api/films/{id}/hls/pl.m3u8", func(w http.ResponseWriter, r *http.Request) {
		hls.serveMediaPlaylist(w, r)
	})

	// GET /api/films/{imdbID}/hls/subs/{idx}.m3u8 — WebVTT-плейлист субтитров
	// сессии (вызывается hls.js из master-плейлиста).
	mux.HandleFunc("GET /api/films/{id}/hls/subs/", func(w http.ResponseWriter, r *http.Request) {
		hls.serveSubPlaylist(w, r)
	})

	// GET /api/films/{imdbID}/hls/segments/{name} — сегмент HLS.
	mux.HandleFunc("GET /api/films/{id}/hls/segments/{name}", func(w http.ResponseWriter, r *http.Request) {
		hls.serveSegment(w, r)
	})

	// GET /api/films/{imdbID}/hls/stop — остановить ffmpeg-сессии фильма
	// (вызывается фронтендом при закрытии плеера; освобождает память).
	mux.HandleFunc("GET /api/films/{id}/hls/stop", func(w http.ResponseWriter, r *http.Request) {
		hls.stopSessions(r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/debug/mem — диагностика памяти стрим-сервиса (открытые
	// торренты, HLS-сессии, heap Go).
	mux.HandleFunc("GET /api/debug/mem", func(w http.ResponseWriter, _ *http.Request) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sys_mb":            ms.Sys >> 20,
			"heap_alloc_mb":     ms.HeapAlloc >> 20,
			"heap_sys_mb":       ms.HeapSys >> 20,
			"heap_idle_mb":      ms.HeapIdle >> 20,
			"heap_released_mb":  ms.HeapReleased >> 20,
			"num_open_torrents": len(cfg.Torrents.Status()),
			"torrents":          cfg.Torrents.Status(),
			"hls_sessions":      hls.status(),
		})
	})

	// GET /api/health — проверка живости.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	return httpx.LogMiddleware(mux), func() {
		// Останавливаем все ffmpeg-сессии (при завершении сервера), чтобы
		// не оставлять осиротевшие процессы и не держать торренты в памяти.
		hls.stopAll()
	}
}

// selfBase строит внутренний базовый URL (127.0.0.1:<порт>) — по нему
// ffmpeg читает сырой видеофайл через наш же эндпоинт /api/stream.
func selfBase(addr string) string {
	if addr == "" {
		addr = ":8080"
	}
	host := "127.0.0.1"
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host += addr[i:]
	}
	return "http://" + host
}
