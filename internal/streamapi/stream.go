package streamapi

import (
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

const (
	// defaultReadahead — сколько данных качать вперёд при чтении, чтобы плеер не буферизовался.
	defaultReadahead = 64 << 20 // 64 MiB

	// metadataTimeout — максимум на метаданные торрента: нет пиров → 504, а не вечное ожидание.
	metadataTimeout = 60 * time.Second
)

// videoExts — расширения, которые может играть браузерный плеер (выбор файла в многофайловых торрентах).
var videoExts = map[string]bool{
	".mp4":  true,
	".m4v":  true,
	".mkv":  true,
	".webm": true,
	".avi":  true,
	".mov":  true,
	".ts":   true,
	".flv":  true,
	".wmv":  true,
	".mpg":  true,
	".mpeg": true,
	".3gp":  true,
	".ogv":  true,
}

// handleStream открывает торрент по записи каталога и отдаёт выбранный видеофайл с
// поддержкой Range; readahead — сколько байт качать вперёд (0 — по умолчанию).
func handleStream(mgr *torrents.Manager, item catalog.Item, readahead int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, release, err := mgr.Acquire(item)
		if err != nil {
			http.Error(w, "unable to open torrent: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Читатель закрыт (клиент отключился или досмотрел) — освобождаем торрент.
		defer release()

		log.Printf("stream: open %s", item.ID)
		// Ждём метаданные (info); при недоступных пирах не висим вечно, а отдаём 504.
		select {
		case <-r.Context().Done():
			return
		case <-t.GotInfo():
			log.Printf("stream: %s метаданные получены, файлов: %d", item.ID, len(t.Files()))
		case <-time.After(metadataTimeout):
			log.Printf("stream: %s таймаут метаданных (нет пиров?)", item.ID)
			http.Error(w, "timeout waiting for torrent metadata (no peers?)", http.StatusGatewayTimeout)
			return
		}

		// file= — индекс конкретного файла (серии) в торренте; без него — самый крупный видеофайл.
		file, err := selectVideoFile(t, fileParam(r))
		if err != nil {
			log.Printf("stream: %s нет видеофайла: %v", item.ID, err)
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		size := file.Length()
		log.Printf("stream: %s файл %s (%d байт)", item.ID, file.DisplayPath(), size)

		// Помечаем серию востребованной, чтобы из сезон-пака не качался весь торрент
		// (остальным файлам — DoNotDownload; рефкаунт — для параллельных зрителей).
		releaseFile := func() {}
		for i, f := range t.Files() {
			if f == file {
				releaseFile = mgr.WantFile(item, i)
				break
			}
		}
		defer releaseFile()

		reader := file.NewReader()
		defer reader.Close()
		reader.SetContext(r.Context()) // прерываем чтение при отключении клиента

		// Readahead — не буфер в RAM: при спуле данные ложатся на диск, поэтому окно
		// можно делать большим (~минута воспроизведения) без роста памяти.
		ra := readahead
		if ra <= 0 {
			ra = defaultReadahead
		}
		reader.SetReadahead(ra)

		// В URL стрима нет расширения — ставим Content-Type по расширению файла.
		if ct := mime.TypeByExtension(strings.ToLower(path.Ext(file.DisplayPath()))); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Accept-Ranges", "bytes")

		if rng := r.Header.Get("Range"); rng != "" {
			start, end, ok := parseRange(rng, size)
			if !ok {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
				http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if _, err := reader.Seek(start, io.SeekStart); err != nil {
				log.Printf("stream %s: seek: %v", item.ID, err)
				http.Error(w, "seek error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			log.Printf("stream %s: range %d-%d/%d", item.ID, start, end, size)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.CopyN(w, reader, end-start+1)
			return
		}

		if _, err := reader.Seek(0, io.SeekStart); err != nil {
			log.Printf("stream %s: seek: %v", item.ID, err)
			http.Error(w, "seek error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		_, _ = io.Copy(w, reader)
	}
}

// fileParam разбирает параметр file (индекс файла в торренте; -1 — нет).
func fileParam(r *http.Request) int {
	if s := r.URL.Query().Get("file"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return -1
}

// selectVideoFile: валидный file (серия сериала) → этот файл, иначе — самый крупный видеофайл.
func selectVideoFile(t *torrent.Torrent, file int) (*torrent.File, error) {
	files := t.Files()
	if file >= 0 && file < len(files) && isVideo(files[file]) {
		return files[file], nil
	}
	return pickVideoFile(t)
}

// pickVideoFile: приоритет — крупнейший видеофайл, если таких нет — просто крупнейший.
func pickVideoFile(t *torrent.Torrent) (*torrent.File, error) {
	files := t.Files()
	if len(files) == 0 {
		return nil, errors.New("torrent has no files")
	}

	var largestVideo, largestFile *torrent.File
	for _, f := range files {
		if largestFile == nil || f.Length() > largestFile.Length() {
			largestFile = f
		}
		if isVideo(f) && (largestVideo == nil || f.Length() > largestVideo.Length()) {
			largestVideo = f
		}
	}
	if largestVideo != nil {
		return largestVideo, nil
	}
	return largestFile, nil
}

func isVideo(f *torrent.File) bool {
	return videoExts[strings.ToLower(path.Ext(f.DisplayPath()))]
}

// parseRange разбирает "bytes=start-end" (в т.ч. суффиксную "bytes=-N") в рамках [0, size-1].
func parseRange(rng string, size int64) (start, end int64, ok bool) {
	rng = strings.TrimPrefix(rng, "bytes=")
	parts := strings.SplitN(rng, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	startStr := strings.TrimSpace(parts[0])
	endStr := strings.TrimSpace(parts[1])

	var err error
	if startStr == "" {
		// "bytes=-N": последние N байт.
		n, e := strconv.ParseInt(endStr, 10, 64)
		if e != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		start = size - n
		end = size - 1
	} else {
		start, err = strconv.ParseInt(startStr, 10, 64)
		if err != nil || start < 0 || start >= size {
			return 0, 0, false
		}
		if endStr == "" {
			end = size - 1
		} else {
			end, err = strconv.ParseInt(endStr, 10, 64)
			if err != nil || end < start {
				return 0, 0, false
			}
			if end >= size {
				end = size - 1
			}
		}
	}
	return start, end, true
}
