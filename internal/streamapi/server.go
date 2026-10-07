// Package streamapi — HTTP-сервис стриминга (микросервис stream): торрент-клиент,
// стриминг с поддержкой Range, список файлов торрента, HLS-транскодинг (ffmpeg).
// БД не использует: магнет выбранной раздачи берёт из query-параметра.
package streamapi

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"

	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/remoteauth"
	"github.com/zeoril1/video_viewer/internal/tmdb"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

// Config — зависимости HTTP-сервиса стриминга.
type Config struct {
	AuthURL                   string
	Context                   context.Context
	AnalysisStoreURL          string
	DisableSegmentAnalysis    bool
	MaxSessions               int
	MaxHLSBytes, MinFreeBytes int64
	Torrents                  *torrents.Manager
	Addr                      string // адрес прослушивания (для внутреннего URL ffmpeg)
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
	analyzer := newEpisodeAnalyzer(hls, cfg)
	access := remoteauth.New(cfg.AuthURL)
	viewers := newViewingStore()
	mux.HandleFunc("POST /api/stream/viewing", viewers.handle(cfg.Torrents, access))
	mux.HandleFunc("DELETE /api/stream/viewing", viewers.handle(cfg.Torrents, access))
	storage := &storageHandler{mgr: cfg.Torrents, hls: hls, access: access, viewers: viewers}
	mux.HandleFunc("GET /api/admin/storage", storage.list)
	mux.HandleFunc("DELETE /api/admin/storage/{hash}", storage.remove)
	mux.HandleFunc("POST /api/stream/prepare", prepareHandler(cfg.Torrents, analyzer))
	mux.HandleFunc("GET /api/stream/download-status", downloadStatusHandler(cfg.Torrents))

	// GET /api/stream/{id} — стриминг с поддержкой Range; magnet задаёт раздачу.
	mux.HandleFunc("GET /api/stream/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/stream/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		m := strings.TrimSpace(r.URL.Query().Get("magnet"))
		if m == "" {
			http.Error(w, "magnet is required", http.StatusBadRequest)
			return
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
	mux.HandleFunc("POST /api/films/{id}/files", func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			Magnet string `json:"magnet"`
			Title  string `json:"title"`
			TMDB   string `json:"tmdb"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&params); err != nil {
			http.Error(w, "invalid file request", http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		q.Set("magnet", params.Magnet)
		q.Set("title", params.Title)
		q.Set("tmdb", params.TMDB)
		r.URL.RawQuery = q.Encode()
		handleTorrentFiles(cfg.Torrents, cfg.TMDB)(w, r)
	})

	// GET /api/films/{imdbID}/tracks — звуковые дорожки торрента (ffprobe).
	mux.HandleFunc("GET /api/films/{id}/tracks", func(w http.ResponseWriter, r *http.Request) {
		handleTracks(hls, cfg.Torrents)(w, r)
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
		analyzer.cancel()
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
