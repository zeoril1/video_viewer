// Админ-страница каталога: список записей с пустыми полями, прямое
// редактирование в браузере, кнопка автоматического обновления данных из
// TMDB для каждой записи и лог прогресса операций. Доступ — только для
// пользователей с ролью "admin" (по httpOnly-куке сессии).
package catalogapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	catalogsync "github.com/zeoril1/video_viewer/internal/sync"
)

// sessionCookieName — имя httpOnly-куки с токеном сессии (то же, что
// в auth-сервисе). Сессии хранятся в общей БД, поэтому catalog-сервис
// может проверять их сам.
const sessionCookieName = "video_viewer_session"

// ---- Лог админ-операций (прогресс обновления данных) ----

// adminLogCap — сколько последних строк лога держим в памяти.
const adminLogCap = 500

// adminLogLine — одна строка админ-лога.
type adminLogLine struct {
	Seq  int    `json:"seq"`
	At   string `json:"at"`
	Kind string `json:"kind"` // info | ok | error
	Msg  string `json:"msg"`
}

// adminLogger — кольцевой буфер лога админ-страницы (в памяти процесса).
type adminLogger struct {
	mu    sync.Mutex
	lines []adminLogLine
	seq   int
}

var adminLog = &adminLogger{}

func (l *adminLogger) add(kind, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	l.lines = append(l.lines, adminLogLine{Seq: l.seq, At: time.Now().Format("15:04:05"), Kind: kind, Msg: msg})
	if len(l.lines) > adminLogCap {
		l.lines = l.lines[len(l.lines)-adminLogCap:]
	}
}

// since возвращает строки лога с seq > after.
func (l *adminLogger) since(after int) []adminLogLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return nil
	}
	first := l.lines[0].Seq
	if after < first-1 {
		after = first - 1
	}
	var out []adminLogLine
	for _, ln := range l.lines {
		if ln.Seq > after {
			out = append(out, ln)
		}
	}
	return out
}

// ---- Проверка прав ---- //

// currentUser возвращает пользователя по куке сессии (валидную сессию
// проверяет по общей БД).
func currentUser(cfg Config, r *http.Request) (db.User, bool) {
	if cfg.DB == nil {
		return db.User{}, false
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return db.User{}, false
	}
	u, ok, err := cfg.DB.GetUserBySession(r.Context(), c.Value)
	if err != nil || !ok {
		return db.User{}, false
	}
	return u, true
}

// requireAdmin проверяет, что запрос идёт от пользователя с ролью admin.
func requireAdmin(cfg Config, w http.ResponseWriter, r *http.Request) (db.User, bool) {
	u, ok := currentUser(cfg, r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return db.User{}, false
	}
	if u.Role != "admin" {
		http.Error(w, "forbidden: admin only", http.StatusForbidden)
		return db.User{}, false
	}
	return u, true
}

// writeJSON отдаёт JSON-ответ.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// handleAdminFilmsMissing — GET /api/admin/films/missing?limit=N.
// Возвращает записи с пустыми полями (полный набор колонок для правки).
func handleAdminFilmsMissing(cfg Config, w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(cfg, w, r); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	films, err := cfg.DB.FilmsMissingFull(r.Context(), limit)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"items": films, "total": len(films)})
}

// handleAdminFilmsTMDBNotFound — GET /api/admin/films/notfound?limit=N.
// Записи, помеченные как «не найдено совпадение на TMDB» (отдельная
// таблица на админ-странице; из списка «пустых полей» исключены).
func handleAdminFilmsTMDBNotFound(cfg Config, w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(cfg, w, r); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	films, err := cfg.DB.FilmsTMDBNotFound(r.Context(), limit)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"items": films, "total": len(films)})
}

// handleAdminFilmSetNotFound — POST /api/admin/films/{id}/notfound
// c телом {"not_found": true|false}: поставить/снять отметку «не найден
// на TMDB» (админ правит отдельную таблицу).
func handleAdminFilmSetNotFound(cfg Config, w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(cfg, w, r); !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}
	var d struct {
		NotFound bool `json:"not_found"`
	}
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := cfg.DB.SetTMDBNotFound(r.Context(), id, d.NotFound); err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if d.NotFound {
		adminLog.add("ok", "фильм "+id+" помечен как не найден на TMDB")
	} else {
		adminLog.add("ok", "с фильма "+id+" снята отметка «не найден на TMDB»")
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// Перезаписывает редактируемые поля фильма (админ правит в браузере).
func handleAdminFilmUpdate(cfg Config, w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(cfg, w, r); !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}
	var d db.Film
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	d.IMDBID = id
	if err := cfg.DB.UpdateFilmAdmin(r.Context(), id, d); err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	adminLog.add("ok", "фильм "+id+" сохранён")
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// handleAdminFilmRefresh — POST /api/admin/films/{id}/refresh.
// Запускает в фоне автоматическое обновление данных фильма из TMDB;
// прогресс пишется в админ-лог (GET /api/admin/logs).
func handleAdminFilmRefresh(cfg Config, w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(cfg, w, r); !ok {
		return
	}
	if cfg.TMDB == nil {
		http.Error(w, "tmdb not configured", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}
	adminLog.add("info", "обновление "+id+": начало")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := catalogsync.RefreshFilmData(ctx, cfg.DB, cfg.TMDB, cfg.IMDB, id); err != nil {
			adminLog.add("error", "обновление "+id+": "+err.Error())
			return
		}
		adminLog.add("ok", "обновление "+id+": готово")
	}()
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// handleAdminRefreshAll — POST /api/admin/refresh-all.
// Обновляет в фоне ВСЕ записи с пустыми полями (партиями), прогресс —
// в админ-лог. Используется тем же инструментом, что и cmd/backfill.
func handleAdminRefreshAll(cfg Config, w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(cfg, w, r); !ok {
		return
	}
	if cfg.TMDB == nil {
		http.Error(w, "tmdb not configured", http.StatusServiceUnavailable)
		return
	}
	adminLog.add("info", "массовое обновление: начало")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
		defer cancel()
		seen := map[string]bool{}
		filled, tried := 0, 0
		// process — обновляет партию записей из TMDB; false, если контекст
		// отменён (надо завершать).
		process := func(queue []db.Film) bool {
			for _, f := range queue {
				select {
				case <-ctx.Done():
					return false
				default:
				}
				adminLog.add("info", "обновление "+f.IMDBID+": ...")
				if err := catalogsync.RefreshFilmData(ctx, cfg.DB, cfg.TMDB, cfg.IMDB, f.IMDBID); err != nil {
					adminLog.add("error", "обновление "+f.IMDBID+": "+err.Error())
				} else {
					filled++
					adminLog.add("ok", "обновление "+f.IMDBID+": готово")
				}
				tried++
				time.Sleep(300 * time.Millisecond)
			}
			return true
		}
		// Фаза 1: записи с пустыми полями (кроме помеченных «не найден»).
		for {
			films, err := cfg.DB.FilmsMissingData(ctx, 100)
			if err != nil {
				adminLog.add("error", "массовое обновление: "+err.Error())
				return
			}
			var queue []db.Film
			for _, f := range films {
				if !seen[f.IMDBID] {
					seen[f.IMDBID] = true
					queue = append(queue, f)
				}
			}
			if len(queue) == 0 {
				break
			}
			if !process(queue) {
				adminLog.add("info", "массовое обновление: прервано")
				return
			}
			adminLog.add("info", "массовое обновление: пустых полей — попыток "+strconv.Itoa(tried)+", готово "+strconv.Itoa(filled))
		}
		// Фаза 2: помеченные «не найдены на TMDB» — повторная попытка
		// (при успехе RefreshFilmData снимет отметку, и запись уйдёт
		// из отдельной таблицы).
		for {
			films, err := cfg.DB.FilmsTMDBNotFound(ctx, 100)
			if err != nil {
				adminLog.add("error", "массовое обновление: "+err.Error())
				return
			}
			var queue []db.Film
			for _, f := range films {
				if !seen[f.IMDBID] {
					seen[f.IMDBID] = true
					queue = append(queue, f)
				}
			}
			if len(queue) == 0 {
				break
			}
			if !process(queue) {
				adminLog.add("info", "массовое обновление: прервано")
				return
			}
			adminLog.add("info", "массовое обновление: не найденных — попыток "+strconv.Itoa(tried)+", готово "+strconv.Itoa(filled))
		}
		adminLog.add("info", "массовое обновление: завершено (попыток "+strconv.Itoa(tried)+", готово "+strconv.Itoa(filled)+")")
	}()
	writeJSON(w, map[string]any{"ok": true})
}

// handleAdminLogs — GET /api/admin/logs?after=<seq>.
// Возвращает новые строки админ-лога (для опроса страницей).
func handleAdminLogs(cfg Config, w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(cfg, w, r); !ok {
		return
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	lines := adminLog.since(after)
	last := 0
	if len(lines) > 0 {
		last = lines[len(lines)-1].Seq
	}
	writeJSON(w, map[string]any{"lines": lines, "last_seq": last})
}

// registerAdminRoutes подключает админ-эндпоинты к mux каталога.
func registerAdminRoutes(mux *http.ServeMux, cfg Config) {
	mux.HandleFunc("GET /api/admin/films/missing", func(w http.ResponseWriter, r *http.Request) {
		handleAdminFilmsMissing(cfg, w, r)
	})
	mux.HandleFunc("GET /api/admin/films/notfound", func(w http.ResponseWriter, r *http.Request) {
		handleAdminFilmsTMDBNotFound(cfg, w, r)
	})
	mux.HandleFunc("PUT /api/admin/films/{id}", func(w http.ResponseWriter, r *http.Request) {
		handleAdminFilmUpdate(cfg, w, r)
	})
	mux.HandleFunc("POST /api/admin/films/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		handleAdminFilmRefresh(cfg, w, r)
	})
	mux.HandleFunc("POST /api/admin/films/{id}/notfound", func(w http.ResponseWriter, r *http.Request) {
		handleAdminFilmSetNotFound(cfg, w, r)
	})
	mux.HandleFunc("POST /api/admin/refresh-all", func(w http.ResponseWriter, r *http.Request) {
		handleAdminRefreshAll(cfg, w, r)
	})
	mux.HandleFunc("GET /api/admin/logs", func(w http.ResponseWriter, r *http.Request) {
		handleAdminLogs(cfg, w, r)
	})
	// Подсказка админу в логах сервиса при старте.
	log.Printf("admin: маршруты /api/admin/* включены (проверка роли по куке сессии)")
}
