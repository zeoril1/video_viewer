package authapi

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/zeoril1/video_viewer/internal/db"
)

// historyHandler — история просмотра пользователя (без БД — 503, без сессии — 401).
type historyHandler struct {
	repo *db.Repo
}

// historyLimit — сколько записей истории отдавать в списке.
const historyLimit = 50

// requireUser — текущий пользователь из куки; пишет ошибку и возвращает
// ok=false, если БД нет или пользователь не авторизован.
func (h *historyHandler) requireUser(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	if h.repo == nil {
		http.Error(w, "auth disabled (no database)", http.StatusServiceUnavailable)
		return db.User{}, false
	}
	// CSRF: мутирующие запросы (save/remove/clear) — с того же origin;
	// GET-список пропускается.
	if r.Method != http.MethodGet && !sameOrigin(r) {
		http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
		return db.User{}, false
	}
	auth := &authHandler{repo: h.repo}
	u, ok := auth.currentUser(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return db.User{}, false
	}
	return u, true
}

// list — GET /api/history: история просмотра пользователя.
func (h *historyHandler) list(w http.ResponseWriter, r *http.Request) {
	u, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var items []db.HistoryEntry
	var err error
	if filmID := strings.TrimSpace(r.URL.Query().Get("film_id")); filmID != "" {
		items, err = h.repo.ListEpisodeHistory(r.Context(), u.ID, filmID)
	} else {
		items, err = h.repo.ListWatchHistory(r.Context(), u.ID, historyLimit)
	}
	if err != nil {
		log.Printf("history: list: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []db.HistoryEntry{}
	}
	log.Printf("history: list %s -> %d записей", u.Username, len(items))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

// save — POST /api/history/progress: сохранить позицию просмотра.
func (h *historyHandler) save(w http.ResponseWriter, r *http.Request) {
	u, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Voice    string  `json:"voice"`
		FilmID   string  `json:"film_id"`
		Magnet   string  `json:"magnet"`
		File     int     `json:"file"`
		Season   int     `json:"season"`
		Episode  int     `json:"episode"`
		Position float64 `json:"position"`
		Duration float64 `json:"duration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	body.FilmID = strings.TrimSpace(body.FilmID)
	if body.FilmID == "" {
		http.Error(w, "film_id required", http.StatusBadRequest)
		return
	}
	if body.File < -1 {
		body.File = -1
	}
	if body.Position < 0 {
		body.Position = 0
	}
	if err := h.repo.SaveWatchProgress(r.Context(), u.ID, db.WatchProgress{
		Voice:    body.Voice,
		FilmID:   body.FilmID,
		Magnet:   body.Magnet,
		File:     body.File,
		Season:   body.Season,
		Episode:  body.Episode,
		Position: body.Position,
		Duration: body.Duration,
	}); err != nil {
		log.Printf("history: save: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// remove — DELETE /api/history/{film_id}?magnet=&file=: удалить запись
// истории (без magnet — все записи фильма).
func (h *historyHandler) remove(w http.ResponseWriter, r *http.Request) {
	u, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	filmID := strings.TrimSpace(r.PathValue("film_id"))
	if filmID == "" {
		http.Error(w, "film_id required", http.StatusBadRequest)
		return
	}
	magnet := r.URL.Query().Get("magnet")
	file := -1
	if s := r.URL.Query().Get("file"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			file = n
		}
	}
	if err := h.repo.DeleteWatchHistory(r.Context(), u.ID, filmID, magnet, file); err != nil {
		log.Printf("history: remove: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	log.Printf("history: remove %s magnet=%q file=%d", filmID, magnet, file)
	w.WriteHeader(http.StatusNoContent)
}

// clear — DELETE /api/history: очистить всю историю пользователя.
func (h *historyHandler) clear(w http.ResponseWriter, r *http.Request) {
	u, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	if err := h.repo.ClearWatchHistory(r.Context(), u.ID); err != nil {
		log.Printf("history: clear: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	log.Printf("history: clear %s", u.Username)
	w.WriteHeader(http.StatusNoContent)
}
