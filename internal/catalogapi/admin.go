// Админ-страница каталога: записи с пустыми полями, правка в браузере, обновление данных из
// TMDB и лог прогресса. Доступ — только для роли "admin" (по httpOnly-куке сессии).
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
)

// sessionCookieName — имя httpOnly-куки с токеном сессии (то же, что в auth-сервисе);
// сессии в общей БД, поэтому catalog-сервис может проверять их сам.
const sessionCookieName = "video_viewer_session"

// ---- Лог админ-операций (прогресс обновления данных) ----

// adminLogCap — сколько последних строк лога держим в памяти.
const adminLogCap = 500

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

// currentUser возвращает пользователя по куке сессии (валидную сессию проверяет по общей БД).
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// handleAdminFilmsMissing returns all eligible records, without a page limit.
func handleAdminFilmsMissing(cfg Config, w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(cfg, w, r); !ok {
		return
	}
	films, err := cfg.DB.AdminFilmsMissing(r.Context())
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"items": films, "total": len(films)})
}

// handleAdminFilmsTMDBNotFound returns failed refreshes during their weekly cooldown.
func handleAdminFilmsTMDBNotFound(cfg Config, w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(cfg, w, r); !ok {
		return
	}
	films, err := cfg.DB.AdminFilmsFailed(r.Context())
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"items": films, "total": len(films)})
}

// handleAdminFilmSetNotFound — POST /api/admin/films/{id}/notfound c телом {"not_found": true|false}:
// поставить/снять отметку «не найден на TMDB».
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
	reason := ""
	if d.NotFound {
		reason = "Не найдено на TMDB"
	}
	if err := cfg.DB.SetAdminRefreshResult(r.Context(), id, reason); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d.NotFound {
		adminLog.add("ok", "фильм "+id+" помечен как не найден на TMDB")
	} else {
		adminLog.add("ok", "с фильма "+id+" снята отметка «не найден на TMDB»")
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// handleAdminFilmUpdate — PUT /api/admin/films/{id}: перезапись редактируемых полей фильма (админ правит в браузере).
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

// handleAdminFilmRefresh — POST /api/admin/films/{id}/refresh: обновление данных из TMDB в фоне,
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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		refreshAdminFilm(ctx, cfg, id)
	}()
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

// handleAdminRefreshAll snapshots the full eligible queue and processes it with bounded workers.
func handleAdminRefreshAll(cfg Config, w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(cfg, w, r); !ok {
		return
	}
	if cfg.TMDB == nil {
		http.Error(w, "tmdb not configured", http.StatusServiceUnavailable)
		return
	}
	if !adminBatchRunning.CompareAndSwap(false, true) {
		http.Error(w, "Обновление уже выполняется", http.StatusConflict)
		return
	}
	films, err := cfg.DB.AdminFilmsMissing(r.Context())
	if err != nil {
		adminBatchRunning.Store(false)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	adminLog.add("info", "массовое обновление: начало, записей "+strconv.Itoa(len(films))+", потоков 4")
	go func() {
		defer adminBatchRunning.Store(false)
		parent := cfg.Context
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, 90*time.Minute)
		defer cancel()
		runAdminWorkers(ctx, films, func(ctx context.Context, id string) { refreshAdminFilm(ctx, cfg, id) })
		if ctx.Err() != nil {
			adminLog.add("error", "массовое обновление: прервано")
		} else {
			adminLog.add("info", "массовое обновление: завершено")
		}
	}()
	writeJSON(w, map[string]any{"ok": true})
}

// handleAdminLogs — GET /api/admin/logs?after=<seq> — новые строки админ-лога (для опроса страницей).
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
	writeJSON(w, map[string]any{"lines": lines, "last_seq": last, "running": adminBatchRunning.Load()})
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
	log.Printf("admin: маршруты /api/admin/* включены (проверка роли по куке сессии)")
}
