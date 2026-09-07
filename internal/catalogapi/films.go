package catalogapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	catalogsync "github.com/zeoril1/video_viewer/internal/sync"
	"github.com/zeoril1/video_viewer/internal/wikidata"
)

// requireDataSources проверяет, что БД и IMDb-клиент сконфигурированы.
func requireDataSources(cfg Config, w http.ResponseWriter) bool {
	if cfg.DB == nil || cfg.IMDB == nil {
		http.Error(w, "database or imdb client not configured", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// handleFilmByID обрабатывает GET /api/films/{imdbID}.
// Если фильма нет в БД, его данные запрашиваются у IMDb и сохраняются.
// ВАЖНО: никаких медленных сетевых вызовов в запросе — недостающие данные
// (описание из IMDb, карточка из Wikidata) дозаполняются В ФОНЕ, чтобы
// WDQS/IMDb (таймауты до минут) не вешали клиента. Фронтенд переспрашивает
// и подхватывает уже сохранённое в БД.
func handleFilmByID(cfg Config, id string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireDataSources(cfg, w) {
			return
		}
		id = strings.TrimSpace(id)
		if id == "" {
			http.NotFound(w, r)
			return
		}

		film, ok, err := cfg.DB.GetByIMDBID(r.Context(), id)
		if err != nil {
			http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if !ok {
			// Фильма нет в БД — получаем данные из IMDb синхронно (они
			// нужны для ответа). В обычном UI-потоке так не бывает: карточки
			// идут из каталога, где фильм уже в БД.
			f, err := cfg.IMDB.GetByID(r.Context(), id)
			if err != nil {
				http.Error(w, "not found on imdb: "+err.Error(), http.StatusNotFound)
				return
			}
			if err := cfg.DB.SaveFilm(r.Context(), f); err != nil {
				log.Printf("films: save %s: %v", id, err)
			}
			film = db.FromIMDB(f)
			log.Printf("films: get %s: нет в БД — загружен из IMDb и сохранён", id)
			// Заполняем недостающие данные из TMDB в фоне.
			maybeRefreshFilmDataBg(cfg, id)
		} else {
			// Фильм есть в БД — дозаполняем недостающее (описание, карточку)
			// и данные из TMDB в фоне, отвечаем сразу.
			maybeFillFilmBg(cfg, id, film)
			maybeRefreshFilmDataBg(cfg, id)
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(film)
	}
}

// fillTotalTimeout — общий бюджет на фоновое дозаполнение одного фильма
// (IMDb-описание + Wikidata-карточка). Больше не ждём — карточка просто
// останется базовой до следующего запроса.
const fillTotalTimeout = 40 * time.Second

// fillRetryAfter — не повторяем дозаполнение фильма чаще этого интервала,
// даже если прошлый раз WDQS/IMDb были недоступны.
const fillRetryAfter = 15 * time.Minute

// fillStateMax — предел записей fillDone. Карта растёт с числом фильмов,
// которые хоть раз пытались дозаполнить; прунинг не даёт ей расти
// бесконечно за долгую сессию.
const fillStateMax = 2048

// pruneFillDoneLocked удаляет старые попытки дозаполнения. Вызывается с
// захваченным fillMu.
func pruneFillDoneLocked() {
	if len(fillDone) <= fillStateMax {
		return
	}
	cutoff := time.Now().Add(-fillRetryAfter * 2)
	for k, t := range fillDone {
		if t.Before(cutoff) {
			delete(fillDone, k)
		}
	}
}

// Защита от дублирующих фоновых дозаполнений одного фильма: фронтенд шлёт
// /api/films/{id} для каждой видимой карточки, и без дедупа параллельные
// запросы дублировали бы сетевые вызовы к IMDb/WDQS.
var (
	fillMu   sync.Mutex
	fillRun  = map[string]bool{}      // id → дозаполнение уже выполняется
	fillDone = map[string]time.Time{} // id → время последней попытки
)

// filmExtrasEmpty — нет ли в фильме ни одного из полей обогащения карточки.
func filmExtrasEmpty(f db.Film) bool {
	return f.MovieLength <= 0 && len(f.Countries) == 0 && f.Director == "" && len(f.Actors) == 0
}

// maybeFillFilmBg запускает фоновое дозаполнение фильма: описание (IMDb),
// если его нет на одном из языков, и карточку из Wikidata (длительность/
// страна/режиссёр/актёры). Не блокирует клиента.
func maybeFillFilmBg(cfg Config, id string, film db.Film) {
	needIMDb := film.Plot == "" || film.PlotRU == ""
	needWiki := filmExtrasEmpty(film)
	if !needIMDb && !needWiki {
		return
	}
	fillMu.Lock()
	pruneFillDoneLocked()
	if fillRun[id] {
		fillMu.Unlock()
		return
	}
	if t, ok := fillDone[id]; ok && time.Since(t) < fillRetryAfter {
		fillMu.Unlock()
		return
	}
	fillRun[id] = true
	fillMu.Unlock()
	log.Printf("films: fill %s (фон): imdb=%v wiki=%v", id, needIMDb, needWiki)

	go func() {
		defer func() {
			fillMu.Lock()
			delete(fillRun, id)
			fillDone[id] = time.Now()
			fillMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), fillTotalTimeout)
		defer cancel()

		if needIMDb {
			if f, err := cfg.IMDB.GetByID(ctx, id); err != nil {
				log.Printf("films: imdb fill %s (фон): %v", id, err)
			} else if err := cfg.DB.SaveFilm(ctx, f); err != nil {
				log.Printf("films: save %s (фон): %v", id, err)
			}
		}
		if needWiki {
			enr, err := wikidata.Enrich(ctx, []string{id})
			if err != nil {
				log.Printf("films: wikidata enrich %s (фон): %v", id, err)
				return
			}
			e, ok := enr[id]
			if !ok || (e.Duration <= 0 && len(e.Countries) == 0 && e.Director == "" && len(e.Actors) == 0) {
				return // в Wikidata данных нет
			}
			if err := cfg.DB.UpdateFilmExtras(ctx, id, e.Duration, e.Countries, e.Director, e.Actors); err != nil {
				log.Printf("films: wikidata enrich %s (фон, save): %v", id, err)
			}
		}
	}()
}

// ratingRefreshMin — не обновляем рейтинг одного фильма чаще этого
// интервала при открытии карточки (защита от частых запросов к TMDB).
const ratingRefreshMin = 60 * time.Second

// ratingStateMax — предел записей ratingDone. Прунинг не даёт карте расти
// бесконечно за долгую сессию.
const ratingStateMax = 2048

// Защита от дублирующих обновлений рейтинга одного фильма: карточки
// открываются часто, а TMDB не должен долбиться на каждый клик.
var (
	ratingMu   sync.Mutex
	ratingRun  = map[string]bool{}      // id → обновление уже выполняется
	ratingDone = map[string]time.Time{} // id → время последнего обновления
)

// pruneRatingDoneLocked удаляет старые отметки обновлений рейтинга.
// Вызывается с захваченным ratingMu.
func pruneRatingDoneLocked() {
	if len(ratingDone) <= ratingStateMax {
		return
	}
	cutoff := time.Now().Add(-ratingRefreshMin * 2)
	for k, t := range ratingDone {
		if t.Before(cutoff) {
			delete(ratingDone, k)
		}
	}
}

// maybeRefreshFilmDataBg запускает фоновое заполнение данных фильма из
// TMDB при открытии карточки (GET /api/films/{id}): рейтинг, жанры,
// описания, русское название, постер и т.д. Не блокирует клиента и не
// долбит API: на один фильм — не чаще раза в ratingRefreshMin.
func maybeRefreshFilmDataBg(cfg Config, id string) {
	if cfg.TMDB == nil {
		return
	}
	ratingMu.Lock()
	pruneRatingDoneLocked()
	if ratingRun[id] {
		ratingMu.Unlock()
		return
	}
	if t, ok := ratingDone[id]; ok && time.Since(t) < ratingRefreshMin {
		ratingMu.Unlock()
		return
	}
	ratingRun[id] = true
	ratingMu.Unlock()

	go func() {
		defer func() {
			ratingMu.Lock()
			delete(ratingRun, id)
			ratingDone[id] = time.Now()
			ratingMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := catalogsync.RefreshFilmData(ctx, cfg.DB, cfg.TMDB, cfg.IMDB, id); err != nil {
			log.Printf("films: refresh data %s (фон): %v", id, err)
			return
		}
		log.Printf("films: refresh data %s (фон): готово", id)
	}()
}
