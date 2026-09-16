package streamapi

import (
	"encoding/json"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/tmdb"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

// torrentFile — видеофайл торрента с сезоном/серией (для селектора серий сериала).
type torrentFile struct {
	Index   int    `json:"index"` // позиция файла в торренте (для ?file=)
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Season  int    `json:"season"`  // сезон (0 — не определён)
	Episode int    `json:"episode"` // серия (0 — не определена)
}

// Регулярки для определения сезона/серии из имени файла.
var (
	// Разделитель между сезоном и серией бывает любой: «s01e05», «S14.E01»,
	// «S02 E10», «S3-E2» — иначе файлы вида «Realnye.pacany.S14.E01.mkv»
	// оставались без сезона и все попадали в первый сезон.
	epSxxExxRe = regexp.MustCompile(`(?i)[sS](\d{1,2})[\s._-]*[eE](\d{1,3})`)
	epSeasonRe = regexp.MustCompile(`(?i)(?:сезон|season)\s*(\d{1,2})`)
	epNumRe    = regexp.MustCompile(`(?i)(?:серия|эпизод|episode|\bep\b|\be\b)\s*\.?\s*(\d{1,3})`)
	epTrailRe  = regexp.MustCompile(`(?:^|[^0-9])(\d{1,3})\s*$`)
)

// episodeOf извлекает (сезон, серию) из имени файла: явный SxxExx → «сезон N» + номер → номер в конце имени.
func episodeOf(name string) (season, ep int) {
	base := strings.TrimSuffix(filepath.Base(name), path.Ext(name))
	if m := epSxxExxRe.FindStringSubmatch(base); m != nil {
		return atoiOr(m[1], 0), atoiOr(m[2], 0)
	}
	if m := epSeasonRe.FindStringSubmatch(base); m != nil {
		season = atoiOr(m[1], 0)
	}
	if m := epNumRe.FindStringSubmatch(base); m != nil {
		ep = atoiOr(m[1], 0)
	} else if m := epTrailRe.FindStringSubmatch(base); m != nil {
		ep = atoiOr(m[1], 0)
	}
	return season, ep
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// handleTorrentFiles — GET /api/films/{id}/files?magnet=...: видеофайлы торрента
// с сезонами/сериями (селектор серий). title/tmdb позволяют разложить файлы по
// сезонам TMDB — у трекеров своя нарезка сезонов, а сборники нумеруют сквозняком.
func handleTorrentFiles(mgr *torrents.Manager, tmdbClient *tmdb.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		magnet := strings.TrimSpace(r.URL.Query().Get("magnet"))
		if magnet == "" {
			http.Error(w, "missing magnet", http.StatusBadRequest)
			return
		}
		relTitle := strings.TrimSpace(r.URL.Query().Get("title"))
		var structure []tmdb.SeasonInfo
		if tmdbID := int64(atoiOr(r.URL.Query().Get("tmdb"), 0)); tmdbID > 0 && tmdbClient != nil {
			structure = tmdbClient.Structure(r.Context(), tmdbID)
		}

		t, release, err := mgr.Acquire(catalog.Item{ID: id, Magnet: magnet})
		if err != nil {
			http.Error(w, "unable to open torrent: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer release()

		select {
		case <-t.GotInfo():
			log.Printf("files: %s метаданные получены, файлов: %d", id, len(t.Files()))
		case <-time.After(metadataTimeout):
			http.Error(w, "timeout waiting for torrent metadata (no peers?)", http.StatusGatewayTimeout)
			return
		}
		// Открыт только для списка серий: пока серия не выбрана, ничего не качаем
		// (по умолчанию anacrolix хочет все файлы торрента).
		mgr.ApplyDownloadPriorities(catalog.Item{ID: id, Magnet: magnet})

		files := make([]torrentFile, 0, len(t.Files()))
		for i, f := range t.Files() {
			if !isVideo(f) {
				continue
			}
			season, ep := episodeOf(f.DisplayPath())
			files = append(files, torrentFile{
				Index:   i,
				Name:    filepath.Base(f.DisplayPath()),
				Size:    f.Length(),
				Season:  season,
				Episode: ep,
			})
		}
		if len(files) == 0 {
			http.Error(w, "no video files in torrent", http.StatusNotFound)
			return
		}

		// Сортируем по распознанным сезону/серии, а не по порядку файлов в торренте:
		// он бывает произвольным (в паке «S01-02x01-41» первым идёт s01e17), а сквозная
		// нумерация должна идти по эпизодам. Затем — раскладка по сезонам TMDB.
		sort.SliceStable(files, func(a, b int) bool {
			if files[a].Season != files[b].Season {
				return files[a].Season < files[b].Season
			}
			if files[a].Episode != files[b].Episode {
				return files[a].Episode < files[b].Episode
			}
			return files[a].Index < files[b].Index
		})
		files = applySeasonMapping(files, relTitle, structure)
		sort.SliceStable(files, func(a, b int) bool {
			if files[a].Season != files[b].Season {
				return files[a].Season < files[b].Season
			}
			if files[a].Episode != files[b].Episode {
				return files[a].Episode < files[b].Episode
			}
			return files[a].Index < files[b].Index
		})

		// Сводка распознавания — видно, почему селектор серий пуст/частичен.
		seasonsSet := map[int]bool{}
		eps := 0
		for _, f := range files {
			seasonsSet[f.Season] = true
			if f.Episode > 0 {
				eps++
			}
		}
		log.Printf("files: %s: файлов %d, сезонов %d, серий с номером %d", id, len(files), len(seasonsSet), eps)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "files": files})
	}
}
