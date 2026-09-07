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
	"github.com/zeoril1/video_viewer/internal/torrents"
)

// torrentFile — видеофайл торрента с определённым сезоном/серией.
// Используется для выбора серии в сериалах.
type torrentFile struct {
	Index   int    `json:"index"`   // позиция файла в торренте (для ?file=)
	Name    string `json:"name"`    // имя файла
	Size    int64  `json:"size"`    // размер в байтах
	Season  int    `json:"season"`  // сезон (0 — не определён)
	Episode int    `json:"episode"` // серия (0 — не определена)
}

// Регулярки для определения сезона/серии из имени файла.
var (
	epSxxExxRe = regexp.MustCompile(`(?i)[sS](\d{1,2})[eE](\d{1,3})`)
	epSeasonRe = regexp.MustCompile(`(?i)(?:сезон|season)\s*(\d{1,2})`)
	epNumRe    = regexp.MustCompile(`(?i)(?:серия|эпизод|episode|\bep\b|\be\b)\s*\.?\s*(\d{1,3})`)
	epTrailRe  = regexp.MustCompile(`(?:^|[^0-9])(\d{1,3})\s*$`)
)

// episodeOf извлекает (сезон, серия) из имени видеофайла.
// Сначала ищет явный паттерн SxxExx, затем «сезон N» + номер серии,
// затем номер в конце имени (напр. "... - 03.mkv").
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

// handleTorrentFiles — GET /api/films/{id}/files?magnet=...
// Открывает торрент и возвращает список видеофайлов с определёнными
// сезоном/серией — фронтенд показывает селектор серий сериала.
func handleTorrentFiles(mgr *torrents.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		magnet := strings.TrimSpace(r.URL.Query().Get("magnet"))
		if magnet == "" {
			http.Error(w, "missing magnet", http.StatusBadRequest)
			return
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

		files := make([]torrentFile, 0, len(t.Files()))
		hasExplicitSeason := false
		for i, f := range t.Files() {
			if !isVideo(f) {
				continue
			}
			season, ep := episodeOf(f.DisplayPath())
			if season > 0 {
				hasExplicitSeason = true
			}
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

		// Сезон не указан ни у одного файла — считаем весь торрент одним
		// сезоном (1), а серии нумеруем по порядку.
		if !hasExplicitSeason {
			for i := range files {
				files[i].Season = 1
			}
		}
		// У файлов без номера серии — нумеруем по порядку внутри сезона.
		ordinal := map[int]int{}
		needRenumber := false
		for i := range files {
			if files[i].Season <= 0 {
				files[i].Season = 1
			}
			if files[i].Episode <= 0 {
				needRenumber = true
			}
		}
		sort.SliceStable(files, func(a, b int) bool {
			if files[a].Season != files[b].Season {
				return files[a].Season < files[b].Season
			}
			return files[a].Index < files[b].Index
		})
		if needRenumber {
			for i := range files {
				ordinal[files[i].Season]++
				files[i].Episode = ordinal[files[i].Season]
			}
		} else {
			sort.SliceStable(files, func(a, b int) bool {
				if files[a].Season != files[b].Season {
					return files[a].Season < files[b].Season
				}
				if files[a].Episode != files[b].Episode {
					return files[a].Episode < files[b].Episode
				}
				return files[a].Index < files[b].Index
			})
		}

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
