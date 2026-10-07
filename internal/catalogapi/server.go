// Package catalogapi — HTTP-сервис каталога (микросервис catalog): объединённый каталог (IMDb + TMDB), детали фильмов, поиск источников (раздач) через Jackett. Владеет PostgreSQL
// (films/sources).
package catalogapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/imdb"
	"github.com/zeoril1/video_viewer/internal/magnet"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

// Config — зависимости HTTP-сервиса каталога.
type Config struct {
	DB     *db.Repo        // PostgreSQL; может быть nil (режим без БД)
	IMDB   *imdb.Client    // может быть nil
	TMDB   *tmdb.Client    // может быть nil
	Magnet magnet.Provider // поиск источников (Jackett Torznab); может быть nil
	// Context — родительский контекст приложения для фоновых задач (поиск источников): при отмене задания останавливаются.
	Context context.Context
}

// NewServer собирает HTTP-обработчики каталога в один mux.
func NewServer(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/poster", newPosterProxy())
	svc := newCatalogService(cfg.DB, cfg.IMDB, cfg.TMDB)
	// Фоновый поиск источников (раздач): медленный Jackett не блокирует HTTP-запросы —
	// результаты отдаются из кэша и обновляются в фоне.
	sourcesMgr := newSourcesManager(cfg)

	// GET /api/catalog — объединённый каталог (IMDb + TMDB).
	// Параметры: q (поиск), section (movie/series/...), genre, sort
	// (year|rating|title; по умолчанию year; не применяется для popular),
	// released (1 — только вышедшие, по умолчанию 0 — все),
	// page (с 1), per_page (по умолчанию 30).
	mux.HandleFunc("GET /api/catalog", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		section := r.URL.Query().Get("section")
		genre := r.URL.Query().Get("genre")
		sortBy := r.URL.Query().Get("sort")
		collection := r.URL.Query().Get("collection")
		released := r.URL.Query().Get("released")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		if perPage <= 0 {
			perPage = 30
		}
		if page <= 0 {
			page = 1
		}
		// «Только вышедшие»: флаг включается любым непустым значением, кроме "0" (фронтенд шлёт released=1/0).
		onlyReleased := released != "" && released != "0"

		entries, total := svc.SearchPage(r.Context(), q, section, genre, sortBy, collection, onlyReleased, page, perPage)

		// Поиск «не только в БД»: если локальных совпадений мало (меньше страницы), ищем также в IMDb и TMDB (on-demand),
		// сохраняем и объединяем без дублей. В подборках (collection) внешние записи не добавляем — они не входят в чарт.
		if q != "" && collection == "" && page == 1 && total < perPage && (cfg.IMDB != nil || cfg.TMDB != nil) {
			if ext := svc.SearchExternal(r.Context(), q); len(ext) > 0 {
				filtered := ext[:0:0]
				for _, e := range ext {
					if !matchesSection(e.item, section) {
						continue
					}
					if genre != "" && !hasGenre(e.item, genre) {
						continue
					}
					if onlyReleased && !isReleased(e.item) {
						continue
					}
					filtered = append(filtered, e)
				}
				merged := mergeEntries(entries, filtered)
				// Внешние результаты добавляются после страницы — применяем ту же сортировку для согласованного порядка.
				if section != "popular" && collection == "" {
					if sortBy == "" {
						sortBy = "year"
					}
					sortCatalogEntries(merged, sortBy)
				}
				entries = merged
				total = len(merged)
			}
		}

		totalPages := 0
		if total > 0 {
			totalPages = (total + perPage - 1) / perPage
		}
		items := make([]CatalogItem, 0, len(entries))
		for _, e := range entries {
			items = append(items, e.item)
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"items":       items,
			"page":        page,
			"per_page":    perPage,
			"total":       total,
			"total_pages": totalPages,
		}); err != nil {
			log.Printf("encode catalog: %v", err)
		}
	})

	// GET /api/catalog/meta — статистика каталога (секции с количеством записей и список жанров).
	// Параметры q и genre пересчитывают счётчики секций под активные фильтры.
	mux.HandleFunc("GET /api/catalog/meta", func(w http.ResponseWriter, r *http.Request) {
		released := r.URL.Query().Get("released")
		kinds, genres := svc.Meta(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("genre"), released != "" && released != "0")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"sections": kinds,
			"genres":   genres,
		}); err != nil {
			log.Printf("encode catalog meta: %v", err)
		}
	})

	// GET /api/films/{imdbID} — фильм по IMDb ID (нет в БД — запрашиваем у IMDb и сохраняем).
	mux.HandleFunc("GET /api/films/{id}", func(w http.ResponseWriter, r *http.Request) {
		handleFilmByID(cfg, r.PathValue("id"))(w, r)
	})

	// GET /api/films/{imdbID}/sources — доступные варианты для просмотра (живой поиск на трекере в фоне).
	mux.HandleFunc("GET /api/films/{id}/sources", func(w http.ResponseWriter, r *http.Request) {
		handleFilmSources(cfg, sourcesMgr, r.PathValue("id"))(w, r)
	})

	// GET /api/health — проверка живости.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	// Админ-эндпоинты (список записей с пустыми полями, редактирование, обновление из TMDB, лог прогресса) — только для роли admin.
	registerAdminRoutes(mux, cfg)
	registerDiscover(mux, cfg)
	registerWatchOrder(mux, cfg, svc)
	registerSegments(mux, cfg)

	return httpx.LogMiddleware(mux)
}
