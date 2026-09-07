package catalogapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/imdb"
)

// sourceItem — вариант для просмотра, найденный on-demand на трекере.
// Quality — разрешение (2160/1080/720/480, может быть пустым); Audio —
// ключ озвучки (dub/multi/two/single/original/subs); Season — сезон
// (для сериалов; 0 — не определён). Поля парсятся из заголовка раздачи
// и используются для ранжирования и группировки «сезон → озвучка → качество».
type sourceItem struct {
	Title   string `json:"title"`
	Size    string `json:"size"`
	Seeds   int    `json:"seeds"`
	Magnet  string `json:"magnet"`
	Quality string `json:"quality,omitempty"`
	Audio   string `json:"audio,omitempty"`
	// Season всегда в JSON (без omitempty): сезон 0 (полный сборник) должен
	// приходить явно, иначе на фронтенде s.season === undefined и проверки
	// вида s.season === 0 (полный сборник покрывает все сезоны) ломаются.
	Season int `json:"season"`
	// Provider — трекер, где найден вариант (jackett и т.п.).
	Provider string `json:"provider,omitempty"`
}

// Парсинг качества/озвучки/сезона из заголовка — в internal/magnet/parse.go
// (общий с фоновым поиском магнетов, чтобы метаданные заполнялись одинаково).

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
// Помимо «чистых» типов сериалов учитывается legacy-тип "animation":
// Кинопоиск (удалён) сохранял в БД мультсериалы (Рик и Морти, Симпсоны и
// т.п.) как kind="animation", и без этого у них не работали ни сезоны
// (поиск по сезонам/число сезонов), ни селектор серий на фронтенде.
// Для анимационных фильмов вреда нет: Seasons<=0 → обычный поиск по
// названию, а селектор серий не показывается для торрента с одним файлом.
func isSeriesKind(kind string) bool {
	switch imdb.NormalizeKind(kind) {
	case "tvSeries", "tvMiniSeries", "animation":
		return true
	}
	return false
}

// sourceQuery — поисковый запрос к трекеру. seasonHint > 0 означает, что
// запрос искал раздачи конкретного сезона (для сериалов): если сезон не
// распознался из названия раздачи, он считается искомым.
type sourceQuery struct {
	q          string
	seasonHint int
}

// maxSeasonQueries — максимум отдельных запросов по сезонам (чтобы не
// спамить трекер на длинных сериалах; Jackett тоже ограничивает частоту).
const maxSeasonQueries = 8

// trackerTitle строит заголовок для поиска на трекере: «Русское / English»,
// если есть оба названия (раздачи на трекерах названы так же —
// «Одержимость / Whiplash (2014)»). Если перевода нет — берём одно
// доступное название.
func trackerTitle(f db.Film) string {
	ru := strings.TrimSpace(f.TitleRU)
	en := strings.TrimSpace(f.Title)
	if ru != "" && en != "" && !strings.EqualFold(ru, en) {
		return ru + " / " + en
	}
	if ru != "" {
		return ru
	}
	return en
}

// sourceQueries формирует поисковые запросы к трекеру для фильма.
// Обычный фильм / сериал с одним сезоном — «название + год» (как раньше,
// но название с переводом: «Одержимость / Whiplash 2014»).
// Сериал с несколькими сезонами — по одному запросу на сезон, иначе трекер
// отдаёт раздачи только 1-го сезона (год в запросе привязывает поиск к
// нему), плюс один запрос по названию для полных сборников (все сезоны
// в одной раздаче).
func sourceQueries(f db.Film) []sourceQuery {
	title := trackerTitle(f)
	if isSeriesKind(f.Kind) && f.Seasons > 1 {
		n := f.Seasons
		if n > maxSeasonQueries {
			n = maxSeasonQueries
		}
		qs := make([]sourceQuery, 0, n+1)
		for s := 1; s <= n; s++ {
			// Первый сезон ищем по ГОДУ сериала (film.Year) — так трекер
			// находит сборки сезона с несколькими озвучками («[S1] … S1E18 …
			// LostFilm+NewStudio+BaibaKo+Eng»), которые запрос «… 1 сезон»
			// не отдаёт. Для остальных сезонов годы точно не знаем — как
			// раньше, по «N сезон».
			if s == 1 && f.Year > 0 {
				qs = append(qs, sourceQuery{q: fmt.Sprintf("%s %d", title, f.Year), seasonHint: s})
			} else {
				qs = append(qs, sourceQuery{q: fmt.Sprintf("%s %d сезон", title, s), seasonHint: s})
			}
		}
		// Полный сборник / все сезоны (сезон 0).
		qs = append(qs, sourceQuery{q: title})
		return qs
	}
	q := title
	if f.Year > 0 {
		q = fmt.Sprintf("%s %d", q, f.Year)
	}
	// Один точный запрос «Русское / English год»: он уникально определяет
	// фильм и не цепляет одноимённые (напр. корейский «Одержимость /
	// Inganjungdok (2014)» — не Whiplash). Запасные запросы по одному
	// названию добавляли бы чужие фильмы.
	return []sourceQuery{{q: q}}
}

// handleFilmSources обрабатывает GET /api/films/{id}/sources.
// Отдаёт доступные варианты из кэша (БД) сразу, а поиск на трекере
// (через Jackett) запускает в фоне — пользователь не ждёт. Пока идёт
// фоновый поиск, статус = "searching" (фронтенд опрашивает повторно).
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

		// Запрос отдаёт кэш из БД и запускает фоновый поиск — выполняется
		// быстро, таймаут не нужен большой.
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		srcs, ready := mgr.sourcesForView(ctx, id)

		// Тип фильма (сериал/фильм) — для ранжирования сезонов.
		var film db.Film
		if f, ok, err := cfg.DB.GetByIMDBID(ctx, id); err == nil && ok {
			film = f
		}
		items := sourceItemsFromDB(srcs, film)

		status := "ready"
		if !ready {
			status = "searching"
		}
		log.Printf("sources: GET %s -> %d вариантов (status=%s)", id, len(items), status)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "items": items, "status": status})
	}
}
