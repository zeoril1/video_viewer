package streamapi

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/zeoril1/video_viewer/internal/disklimit"
	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/remoteauth"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

type storageHandler struct {
	mgr     *torrents.Manager
	hls     *hlsManager
	access  *remoteauth.Client
	viewers *viewingStore
	mu      sync.Mutex
}

type hlsStorage struct {
	UsedBytes      int64  `json:"used_bytes"`
	TotalBytes     uint64 `json:"total_bytes"`
	FreeBytes      uint64 `json:"free_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	Error          string `json:"error,omitempty"`
}

func (m *hlsManager) storageUsage() hlsStorage {
	var result hlsStorage
	space, err := disklimit.Stats(m.dataDir)
	if err != nil {
		result.Error = "Не удалось получить объём диска"
	} else {
		result.TotalBytes, result.FreeBytes, result.AvailableBytes = space.TotalBytes, space.FreeBytes, space.AvailableBytes
	}
	if !strings.HasPrefix(filepath.Base(m.dataDir), "video-viewer-hls") {
		result.Error = "Каталог файлов плеера недоступен"
		return result
	}
	err = filepath.WalkDir(m.dataDir, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		size, e := disklimit.AllocatedBytes(path)
		if e != nil {
			return e
		}
		result.UsedBytes += size
		return nil
	})
	if err != nil {
		result.Error = "Не удалось полностью посчитать файлы плеера"
	}
	return result
}

func (h *storageHandler) list(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.access.RequireAdmin(w, r) {
		return
	}
	if h.mgr == nil {
		http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	storage := h.mgr.StorageStatus()
	viewers := h.viewers.snapshot()
	sort.Slice(viewers, func(i, j int) bool {
		if viewers[i].Username == viewers[j].Username {
			return viewers[i].Session < viewers[j].Session
		}
		return viewers[i].Username < viewers[j].Username
	})
	// A fully transcoded stream can have no torrent readers left. Keep delete
	// controls accurate while viewers or HLS sessions still use its media.
	for i := range storage.Torrents {
		t := &storage.Torrents[i]
		for _, v := range viewers {
			if v.Hash == t.Hash {
				t.Busy = true
			}
		}
		if h.hls.usesTorrent(t.Hash) {
			t.Busy = true
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"storage": storage, "hls": h.hls.storageUsage(), "viewers": viewers})
}

func (m *hlsManager) usesTorrentLocked(hash string) bool {
	for _, s := range m.sessions {
		if key, err := torrentHash(s.magnet); err == nil && key == hash {
			return true
		}
	}
	return false
}

func (m *hlsManager) usesTorrent(hash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usesTorrentLocked(hash)
}

func (h *storageHandler) remove(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.access.RequireAdmin(w, r) || !httpx.CheckOrigin(w, r) {
		return
	}
	if h.mgr == nil {
		http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
		return
	}
	hash := strings.ToLower(r.PathValue("hash"))
	var index *int
	if r.URL.Query().Has("file") {
		values := r.URL.Query()["file"]
		if len(values) != 1 {
			http.Error(w, "invalid file index", http.StatusBadRequest)
			return
		}
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 0 {
			http.Error(w, "invalid file index", http.StatusBadRequest)
			return
		}
		index = &n
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Serialize against new heartbeat registration and HLS session creation;
	// Manager additionally serializes against raw readers and preparation.
	h.viewers.mu.Lock()
	defer h.viewers.mu.Unlock()
	h.viewers.pruneLocked(h.viewers.now())
	for _, v := range h.viewers.items {
		if v.Hash == hash {
			http.Error(w, "Файл сейчас просматривается. Закройте плеер и повторите удаление.", http.StatusConflict)
			return
		}
	}
	h.hls.mu.Lock()
	defer h.hls.mu.Unlock()
	if h.hls.usesTorrentLocked(hash) {
		http.Error(w, "Файлы ещё используются плеером. Закройте просмотр и повторите позже.", http.StatusConflict)
		return
	}
	err := h.mgr.RemoveCached(hash, index)
	if err != nil {
		status, message := http.StatusInternalServerError, "Не удалось удалить файл"
		switch {
		case errors.Is(err, torrents.ErrInvalidCacheKey):
			status, message = http.StatusBadRequest, "Неверный файл"
		case errors.Is(err, torrents.ErrCacheNotFound):
			status, message = http.StatusNotFound, "Файл уже удалён"
		case errors.Is(err, torrents.ErrCacheBusy):
			status, message = http.StatusConflict, "Файл используется загрузкой или подготовкой серии"
		case errors.Is(err, torrents.ErrCacheUnsupported):
			status, message = http.StatusNotImplemented, "В режиме RAM доступна очистка всей раздачи"
		}
		http.Error(w, message, status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
