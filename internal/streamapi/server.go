// Package streamapi — HTTP-сервис стриминга (микросервис stream): торрент-клиент,
// стриминг с поддержкой Range, список файлов торрента, HLS-транскодинг (ffmpeg).
// БД не использует: магнет берёт из query-параметра либо резолвит по id через
// catalog-сервис.
package streamapi

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"

	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/tmdb"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

// MagnetResolver — резолв магнет-ссылки по id: в проде клиент catalog-сервиса,
// nil — /api/stream/{id} без ?magnet= отдаёт 404.
type MagnetResolver interface {
	FindMagnet(ctx context.Context, id string) (string, bool)
}

// Config — зависимости HTTP-сервиса стриминга.
type Config struct {
	MaxSessions               int
	MaxHLSBytes, MinFreeBytes int64
	Torrents                  *torrents.Manager
	Addr                      string         // адрес прослушивания (для внутреннего URL ffmpeg)
	Resolver                  MagnetResolver // опциональный резолв магнета по id (catalog-сервис)
	// TMDB — опциональный клиент TMDB (nil — файлы раскладываются только по именам):
	// нужен, чтобы приводить сезоны трекера к TMDB (сборники нумеруют серии сквозняком).
	TMDB *tmdb.Client
	// Readahead — упреждающее скачивание вперёд (байт; данные ложатся в спул/на диск,
	// а не в RAM). 0 — defaultReadahead.
	Readahead int64
}

// NewServer собирает HTTP-обработчики стриминга в один mux. Возвращает обработчик
// и функцию остановки фоновых ресурсов (ffmpeg) для graceful shutdown.
func NewServer(cfg Config) (http.Handler, func()) {
	mux := http.NewServeMux()

	// HLS-транскодинг (ffmpeg): звук в браузере и выбор звуковой дорожки.
	hls := newHLSManager(selfBase(cfg.Addr))
	if cfg.MaxSessions > 0 {
		hls.slots = make(chan struct{}, cfg.MaxSessions)
	}
	hls.maxDiskBytes = cfg.MaxHLSBytes
	hls.minFreeBytes = cfg.MinFreeBytes
	go hls.cleanup()
	go hls.watchResources()
	mux.HandleFunc("POST /api/stream/prepare", prepareHandler(cfg.Torrents))

	// GET /api/stream/{id} — стриминг с поддержкой Range; необязательный magnet=...
	// задаёт конкретную раздачу, иначе магнет резолвится по id через catalog-сервис.
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
		handleStream(cfg.Torrents, catalog.Item{ID: id, Magnet: m}, cfg.Readahead)(w, r)
	})

	// POST /api/stream/keep — «тёплый» кеш на 24 ч для того же infohash (фронтенд зовёт
	// после >5% просмотра): скачанное остаётся доступно другим без повторного скачивания.
	mux.HandleFunc("POST /api/stream/keep", func(w http.ResponseWriter, r *http.Request) {
		if cfg.Torrents != nil {
			cfg.Torrents.Keep(r.URL.Query().Get("magnet"), 0)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/films/{id}/files — видеофайлы торрента с сезонами/сериями (селектор серий): ?magnet=...
	mux.HandleFunc("GET /api/films/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		handleTorrentFiles(cfg.Torrents, cfg.TMDB)(w, r)
	})

	// GET /api/films/{imdbID}/tracks — звуковые дорожки торрента (ffprobe).
	mux.HandleFunc("GET /api/films/{id}/tracks", func(w http.ResponseWriter, r *http.Request) {
		handleTracks(hls)(w, r)
	})

	// GET /api/films/{imdbID}/hls.m3u8 — HLS-плейлист (ffmpeg, выбранная дорожка).
	mux.HandleFunc("GET /api/films/{id}/hls.m3u8", func(w http.ResponseWriter, r *http.Request) {
		hls.servePlaylist(w, r)
	})

	// GET /api/films/{id}/hls/pl.m3u8 — media-плейлист сессии (зовёт hls.js).
	mux.HandleFunc("GET /api/films/{id}/hls/pl.m3u8", func(w http.ResponseWriter, r *http.Request) {
		hls.serveMediaPlaylist(w, r)
	})

	// GET /api/films/{id}/hls/subs/{idx}.m3u8 — WebVTT-плейлист субтитров сессии.
	mux.HandleFunc("GET /api/films/{id}/hls/subs/", func(w http.ResponseWriter, r *http.Request) {
		hls.serveSubPlaylist(w, r)
	})

	// GET /api/films/{imdbID}/hls/segments/{name} — сегмент HLS.
	mux.HandleFunc("GET /api/films/{id}/hls/segments/{name}", func(w http.ResponseWriter, r *http.Request) {
		hls.serveSegment(w, r)
	})

	// GET /api/films/{id}/hls/stop — остановить ffmpeg-сессии фильма (закрытие плеера).
	mux.HandleFunc("GET /api/films/{id}/hls/stop", func(w http.ResponseWriter, r *http.Request) {
		hls.stopSessions(hlsSessionKey(r.PathValue("id"), r.URL.Query().Get("session")))
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/debug/mem — диагностика памяти (торренты, HLS-сессии, heap).
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
		// Останавливаем все ffmpeg-сессии, чтобы не оставить осиротевшие процессы.
		close(hls.done)
		hls.stopAll()
	}
}

// selfBase — внутренний базовый URL (127.0.0.1:<порт>): по нему ffmpeg читает сырой файл через наш /api/stream.
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
