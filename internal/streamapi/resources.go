package streamapi

import (
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"time"

	"github.com/zeoril1/video_viewer/internal/disklimit"
)

var errResources = errors.New("Сервер занят или достигнут лимит диска. Повторите позже.")

func (m *hlsManager) diskAvailable() bool {
	if m.minFreeBytes <= 0 {
		return true
	}
	free, err := disklimit.Free(m.dataDir)
	return err == nil && free >= uint64(m.minFreeBytes)
}

// FFmpeg writes independently; bound its output with a one-second watchdog.
// The spool budget is enforced before every write; HLS can overshoot between ticks.
func (m *hlsManager) watchResources() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			var size int64
			_ = filepath.WalkDir(m.dataDir, func(_ string, d fs.DirEntry, e error) error {
				if e == nil && !d.IsDir() {
					if i, e := d.Info(); e == nil {
						size += i.Size()
					}
				}
				return nil
			})
			if (m.maxDiskBytes > 0 && size > m.maxDiskBytes) || !m.diskAvailable() {
				m.stopAll()
			}
		}
	}
}

func resourceError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResources) {
		w.Header().Set("Retry-After", "10")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.Error(w, "hls start failed: "+err.Error(), http.StatusInternalServerError)
}
