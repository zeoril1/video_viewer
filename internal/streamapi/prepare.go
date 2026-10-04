package streamapi

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

// Download the selected next episode only. Foreground readers retain their
// higher piece priorities; cancellation removes this request's file demand.
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
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
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
		w.WriteHeader(204)
	}
}
