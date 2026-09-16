package catalogapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/imdb"
)

// sourceItem — вариант для просмотра, найденный on-demand на трекере.
// Quality — разрешение (2160/1080/720/480), Audio — ключ озвучки, Season — сезон (0 — не определён);
// поля парсятся из заголовка и используются для ранжирования и группировки «сезон → озвучка → качество».
type sourceItem struct {
	Title   string `json:"title"`
	Size    string `json:"size"`
	Seeds   int    `json:"seeds"`
	Magnet  string `json:"magnet"`
	Quality string `json:"quality,omitempty"`
	Audio   string `json:"audio,omitempty"`
	// Season всегда в JSON (без omitempty): сезон 0 (полный сборник) должен приходить явно,
	// иначе на фронтенде s.season === undefined и проверки вида s.season === 0 ломаются.
	Season int `json:"season"`
	// Provider — трекер, где найден вариант (jackett и т.п.).
	Provider string `json:"provider,omitempty"`
}

// Парсинг качества/озвучки/сезона из заголовка — в internal/magnet/parse.go (общий с фоновым поиском).

// srcAudioScore — приоритет озвучки (выше — лучше; первичный признак).
func srcAudioScore(audio string) int {
	switch audio {
	case "dub":
		return 50
	case "multi":
		return 40
	case "two":
		return 30
	case "original":
		return 25
	case "single":
		return 20
	case "subs":
		return 10
	}
	return 0
}

// srcQualityScore — приоритет разрешения (вторичный признак).
func srcQualityScore(quality string) int {
	switch quality {
	case "2160":
		return 40
	case "1080":
		return 30
	case "720":
		return 20
	case "480":
		return 10
	}
	return 15 // неизвестно — чуть выше 480
}

// srcRank — итоговый рейтинг варианта: озвучка > качество > сиды.
func srcRank(q, audio string, seeds int) int {
	s := seeds
	if s > 100 {
		s = 100
	}
	return srcAudioScore(audio)*10000 + srcQualityScore(q)*100 + s
}

// normSeason нормализует сезон для сортировки: неизвестный (0) уходит в конец.
func normSeason(s int) int {
	if s <= 0 {
		return 1 << 20
	}
	return s
}

// isSeriesKind — является ли тип контента сериалом (есть сезоны/серии).
// Помимо «чистых» типов учитывается legacy-тип "animation": Кинопоиск (удалён) сохранял мультсериалы
// как kind="animation", без этого у них не работали ни сезоны, ни селектор серий на фронтенде.
func isSeriesKind(kind string) bool {
	switch imdb.NormalizeKind(kind) {
	case "tvSeries", "tvMiniSeries", "animation":
		return true
	}
	return false
}

// sourceQuery — поисковый запрос к трекеру; seasonHint > 0 — запрос искал раздачи конкретного сезона (подсказка при нераспознанном сезоне).
type sourceQuery struct {
	q          string
	seasonHint int
}

// maxSeasonQueries — максимум отдельных запросов по сезонам: не спамим трекер (Jackett ограничивает частоту); 16 хватает сериалам с 14 сезонами.
const maxSeasonQueries = 16

// trackerTitles — названия фильма для поиска на трекере, в порядке предпочтения: русское, затем исходное.
//
// ВАЖНО: запрос строится из ОДНОГО названия. Вариант «Русское / English» на трекерах, которые ищут все слова
// запроса (RuTracker, MegaPeer, NoNaMe-club, BigFanGroup), даёт ПУСТУЮ выдачу — раздачи там названы по-русски,
// и оставались только результаты rutor (в UI — лишь свежие сезоны).
func trackerTitles(f db.Film) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, v := range out {
			if strings.EqualFold(v, s) {
				return
			}
		}
		out = append(out, s)
	}
	add(f.TitleRU)
	add(f.Title)
	return out
}

// trackerTitle — основное название для запроса: русское (трекеры называют раздачи по-русски), иначе исходное.
func trackerTitle(f db.Film) string {
	ts := trackerTitles(f)
	if len(ts) == 0 {
		return ""
	}
	return ts[0]
}

// trackerTitleAlt — запасное название (пусто, если совпадает с основным); нужен второй проход
// для зарубежных сериалов, где поиск по русскому названию ничего не находит.
func trackerTitleAlt(f db.Film) string {
	ts := trackerTitles(f)
	if len(ts) < 2 {
		return ""
	}
	return ts[1]
}

func sourceQueries(f db.Film) []sourceQuery {
	return sourceQueriesFor(f, trackerTitle(f))
}

// sourceQueriesFor формирует запросы для указанного названия.
//
// У сериала с несколькими сезонами — ОДИН общий запрос по названию: разбивать его на «… N сезон» заранее вредно
// (RuTracker на такие запросы даёт мусор, а каждый лишний запрос — отдельный обход всех индексаров Jackett).
// Недостающие сезоны добирает missingSeasonQueries — точечно и по факту пробела.
func sourceQueriesFor(f db.Film, title string) []sourceQuery {
	if strings.TrimSpace(title) == "" {
		return nil
	}
	if isSeriesKind(f.Kind) && f.Seasons > 1 {
		return []sourceQuery{{q: title}}
	}
	q := title
	if f.Year > 0 {
		// Один точный запрос «название год» — не цепляет одноимённые фильмы (корейская «Одержимость», не Whiplash).
		q = fmt.Sprintf("%s %d", q, f.Year)
	}
	return []sourceQuery{{q: q}}
}

// handleFilmSources обрабатывает GET /api/films/{id}/sources: отдаёт варианты из кэша (БД) сразу,
// а поиск на трекере запускает в фоне (пока идёт — status="searching").
func handleFilmSources(cfg Config, mgr *sourcesManager, id string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.DB == nil || cfg.Magnet == nil {
			http.Error(w, "tracker search not configured", http.StatusServiceUnavailable)
			return
		}
		id = strings.TrimSpace(id)
		if id == "" {
			http.NotFound(w, r)
			return
		}

		// Запрос только читает кэш и ставит фоновый поиск — отвечает быстро, большой таймаут не нужен.
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		var film db.Film
		if f, ok, err := cfg.DB.GetByIMDBID(ctx, id); err == nil && ok {
			film = f
		}
		seasons := seasonEpisodes(ctx, cfg, film)
		srcs, ready := mgr.sourcesForView(ctx, id)
		items := sourceItemsFromDB(srcs, film)

		status := "ready"
		if !ready {
			status = "searching"
		}
		log.Printf("sources: GET %s -> %d вариантов (status=%s)", id, len(items), status)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "items": items, "status": status,
			"seasons": seasons,
		})
	}
}

// seasonEpisode — число серий сезона TMDB: каноническая сетка серий сериала.
type seasonEpisode struct {
	Season   int `json:"season"`
	Episodes int `json:"episodes"`
}

// seasonEpisodes отдаёт структуру сезонов из TMDB (nil — нет клиента, это не сериал или нет tmdb_id).
// Нужна для сетки серий: раздачи трекеров покрывают сезон ЧАСТИЧНО (паки «[S01-02x01-41]» и «[01-21]»
// дают 40 и 21 серию из 50), иначе в сетке было бы столько серий, сколько отдала первая раздача.
func seasonEpisodes(ctx context.Context, cfg Config, film db.Film) []seasonEpisode {
	if cfg.TMDB == nil || !isSeriesKind(film.Kind) {
		return nil
	}
	tmdbID, err := strconv.ParseInt(strings.TrimSpace(film.TMDBID), 10, 64)
	if err != nil || tmdbID <= 0 {
		return nil
	}
	st, refreshErr := cfg.DB.SeriesSeasons(ctx, tmdbID, cfg.TMDB.SeasonStructure)
	if refreshErr != nil {
		log.Printf("seasons: refresh %d: %v", tmdbID, refreshErr)
	}
	if len(st) == 0 {
		return nil
	}
	out := make([]seasonEpisode, 0, len(st))
	for _, s := range st {
		if s.Number > 0 && s.Episodes > 0 {
			out = append(out, seasonEpisode{Season: s.Number, Episodes: s.Episodes})
		}
	}
	return out
}
