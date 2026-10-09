package streamapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

// Download the selected next episode only. Foreground readers retain their
// higher piece priorities; cancellation removes this request's file demand.
func prepareHandler(mgr *torrents.Manager, analyzers ...*episodeAnalyzer) http.HandlerFunc {
	slots := make(chan struct{}, 2)
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil || mgr.DiskPressure() {
			http.Error(w, "Подготовка отложена: сервер занят.", 503)
			return
		}
		analysisRequest, analyse, err := parsePrepareAnalysis(r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if analyse {
			// Hold the downloaded reference through preparation and analysis.
			// The server also refuses to demand the next file before completion.
			previous := catalog.Item{ID: analysisRequest.previous.id, Magnet: analysisRequest.previous.magnet}
			t, release, err := mgr.Acquire(previous)
			if err != nil {
				http.Error(w, "invalid previous source", 400)
				return
			}
			defer release()
			select {
			case <-t.GotInfo():
			default:
				w.Header().Set("Retry-After", "5")
				http.Error(w, "current episode is not downloaded", 503)
				return
			}
			files := t.Files()
			index := analysisRequest.previous.file
			if index >= len(files) || !isVideo(files[index]) {
				http.Error(w, "invalid previous file", 400)
				return
			}
			if files[index].Length() <= 0 || files[index].BytesCompleted() < files[index].Length() {
				w.Header().Set("Retry-After", "5")
				http.Error(w, "current episode is not downloaded", 503)
				return
			}
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Подготовка отложена: сервер занят.", 503)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
		defer cancel()
		item := catalog.Item{ID: "prepare", Magnet: r.URL.Query().Get("magnet")}
		index := fileParam(r)
		if index < 0 {
			http.Error(w, "file required", 400)
			return
		}
		torrent, release, err := mgr.Acquire(item)
		if err != nil {
			http.Error(w, "invalid source", 400)
			return
		}
		defer release()
		select {
		case <-torrent.GotInfo():
		case <-ctx.Done():
			http.Error(w, "preparation timeout", 504)
			return
		}
		files := torrent.Files()
		if index >= len(files) || !isVideo(files[index]) {
			http.Error(w, "invalid file", 400)
			return
		}
		unwant := mgr.WantFile(item, index)
		defer unwant()
		mgr.Keep(item.Magnet, 10*time.Minute)
		reader := files[index].NewReader()
		reader.SetContext(ctx)
		reader.SetReadahead(4 << 20)
		_, err = io.CopyN(io.Discard, reader, min(files[index].Length(), 8<<20))
		reader.Close()
		if err != nil {
			http.Error(w, "preparation interrupted", 504)
			return
		}
		// Warm the beginning first, then leave the whole file at normal priority.
		// Do not hold an urgent reader at the head while the foreground is playing.
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for files[index].BytesCompleted() < files[index].Length() {
			select {
			case <-ctx.Done():
				http.Error(w, "preparation interrupted", 504)
				return
			case <-ticker.C:
				if mgr.DiskPressure() {
					http.Error(w, "preparation deferred: disk pressure", 503)
					return
				}
			}
		}
		mgr.Keep(item.Magnet, 10*time.Minute)
		if !analyse {
			w.WriteHeader(204)
			return
		}
		var analyzer *episodeAnalyzer
		if len(analyzers) > 0 {
			analyzer = analyzers[0]
		}
		result := analyzer.analyse(r.Context(), analysisRequest)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// Status concerns the original torrent file, not buffered HLS segments. Polling
// does not add file demand or download the other episodes of a season pack.
func downloadStatusHandler(mgr *torrents.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			http.Error(w, "stream unavailable", 503)
			return
		}
		index, err := strconv.Atoi(r.URL.Query().Get("file"))
		magnet := r.URL.Query().Get("magnet")
		if err != nil || index < 0 || segmentMediaKey(magnet, index) == "" {
			http.Error(w, "invalid file", 400)
			return
		}
		item := catalog.Item{ID: "download-status", Magnet: magnet}
		t, release, err := mgr.Acquire(item)
		if err != nil {
			http.Error(w, "invalid source", 400)
			return
		}
		defer release()
		result := map[string]any{"complete": false, "downloaded": int64(0), "total": int64(0), "metadata_ready": false}
		select {
		case <-t.GotInfo():
			files := t.Files()
			if index >= len(files) || !isVideo(files[index]) {
				http.Error(w, "invalid file", 400)
				return
			}
			mgr.ApplyDownloadPriorities(item)
			downloaded, total := files[index].BytesCompleted(), files[index].Length()
			result["complete"] = total > 0 && downloaded >= total
			result["downloaded"] = downloaded
			result["total"] = total
			result["metadata_ready"] = true
		default:
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(result)
	}
}
