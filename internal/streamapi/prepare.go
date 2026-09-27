package streamapi

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

// Read only the beginning without WantFile (which requests the entire file).
// Reader priorities disappear on cancellation; the shared torrent cache remains.
func prepareHandler(mgr *torrents.Manager) http.HandlerFunc {
	slots := make(chan struct{}, 2)
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil || mgr.DiskPressure() {
			http.Error(w, "Подготовка отложена: сервер занят.", 503)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Подготовка отложена: сервер занят.", 503)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
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
		mgr.ApplyDownloadPriorities(item)
		reader := files[index].NewReader()
		defer reader.Close()
		reader.SetContext(ctx)
		reader.SetReadahead(4 << 20)
		_, err = io.CopyN(io.Discard, reader, min(files[index].Length(), 8<<20))
		if err != nil {
			http.Error(w, "preparation interrupted", 504)
			return
		}
		mgr.Keep(item.Magnet, 5*time.Minute)
		w.WriteHeader(204)
	}
}
