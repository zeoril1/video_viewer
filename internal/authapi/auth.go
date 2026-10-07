package authapi

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/httpx"
)

const (
	// sessionCookie — имя httpOnly-куки с токеном сессии.
	sessionCookie = "video_viewer_session"
	// sessionTTL — время жизни сессии (30 дней).
	sessionTTL = 30 * 24 * time.Hour
)

// usernameRe — допустимый логин: 3-32 символа, буквы/цифры/_.-
var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`)

// authHandler — обработчики регистрации/входа/выхода (без БД отдают 503).
type authHandler struct {
	repo          *db.Repo
	secureCookies bool          // Secure-флаг на куке сессии (HTTPS)
	limiter       *loginLimiter // ограничение попыток входа/регистрации
}

// cookieSecure решает, ставить ли флаг Secure на куку сессии: COOKIE_SECURE=1
// включает принудительно, иначе флаг берётся из запроса (HTTPS напрямую или
// через прокси). Так один сервис обслуживает и локальный HTTP (иначе вход
// не работал бы), и публичный HTTPS.
func (h *authHandler) cookieSecure(r *http.Request) bool {
	return h.secureCookies || httpx.IsSecureRequest(r)
}

// setSessionCookie записывает httpOnly-куку с токеном сессии (secure —
// ставить флаг Secure, то есть отдавать куку только по HTTPS).
func setSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// clearSessionCookie удаляет куку сессии (выход).
func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// ---- CSRF: мутирующие запросы должны приходить с того же origin ----

// sameOrigin проверяет, что Origin/Referer совпадает с Host. Браузеры шлют
// Origin на всех POST/DELETE; без заголовков (curl, внутренние клиенты) — пропускаем.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
		if origin == "" {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// checkOrigin пишет 403, если запрос кросс-ориджиновый.
func checkOrigin(w http.ResponseWriter, r *http.Request) bool {
	if !sameOrigin(r) {
		http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
		return false
	}
	return true
}

// ---- ограничение попыток входа/регистрации (защита от брутфорса) ----

const (
	loginMaxAttempts = 5                // неудачных попыток до блокировки
	loginWindow      = 10 * time.Minute // окно учёта попыток
	loginLockout     = 5 * time.Minute  // блокировка после превышения
	loginStateMax    = 4096             // предел записей в карте (прунинг)
)

// loginState — счётчик неудачных попыток по ключу (IP|username).
type loginState struct {
	fails  int
	window time.Time
	locked time.Time
}

// loginLimiter — простой in-memory throttle попыток входа/регистрации.
type loginLimiter struct {
	mu    sync.Mutex
	state map[string]*loginState
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{state: make(map[string]*loginState)}
}

// allow разрешает попытку входа для ключа (false — ключ заблокирован).
func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state[key]
	if s == nil {
		return true
	}
	now := time.Now()
	if now.Before(s.locked) {
		return false
	}
	// Окно истекло — сбрасываем счётчик.
	if now.Sub(s.window) > loginWindow {
		delete(l.state, key)
	}
	return true
}

// fail фиксирует неудачную попытку; при превышении лимита блокирует ключ.
func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	s := l.state[key]
	if s == nil {
		s = &loginState{window: now}
		l.state[key] = s
	}
	if now.Sub(s.window) > loginWindow {
		s.fails = 0
		s.window = now
	}
	s.fails++
	if s.fails >= loginMaxAttempts {
		s.locked = now.Add(loginLockout)
	}
	l.pruneLocked(now)
}

// success сбрасывает счётчик после успешного входа.
func (l *loginLimiter) success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.state, key)
}

// pruneLocked удаляет устаревшие записи, чтобы карта не росла бесконечно.
func (l *loginLimiter) pruneLocked(now time.Time) {
	if len(l.state) <= loginStateMax {
		return
	}
	for k, s := range l.state {
		if now.After(s.locked) && now.Sub(s.window) > loginWindow*2 {
			delete(l.state, k)
		}
	}
}

// clientKey строит ключ лимитера из IP клиента и имени пользователя.
func clientKey(r *http.Request, username string) string {
	ip := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ip = h
	}
	// This header is overwritten by gateway; auth is an internal service.
	if forwarded := net.ParseIP(r.Header.Get("X-Video-Viewer-Client-IP")); forwarded != nil {
		ip = forwarded.String()
	}
	return ip + "|" + strings.ToLower(strings.TrimSpace(username))
}

// currentUser возвращает пользователя по куке сессии (если валидна).
func (h *authHandler) currentUser(r *http.Request) (db.User, bool) {
	if h.repo == nil {
		return db.User{}, false
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return db.User{}, false
	}
	u, ok, err := h.repo.GetUserBySession(r.Context(), c.Value)
	if err != nil {
		log.Printf("auth: get user by session: %v", err)
		return db.User{}, false
	}
	return u, ok
}

// register — POST /api/auth/register: создание аккаунта.
func (h *authHandler) register(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "auth disabled (no database)", http.StatusServiceUnavailable)
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	// Регистрация ограничивается по IP (имени ещё нет).
	if !h.limiter.takeRegistration(clientKey(r, "")) {
		http.Error(w, "too many attempts, try again later", http.StatusTooManyRequests)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if !usernameRe.MatchString(body.Username) {
		http.Error(w, "username must be 3-32 chars (letters, digits, _ . -)", http.StatusBadRequest)
		return
	}
	if len(body.Password) < 6 {
		http.Error(w, "password too short (min 6)", http.StatusBadRequest)
		return
	}
	if _, _, exists, err := h.repo.GetUserByUsername(r.Context(), body.Username); err != nil {
		log.Printf("auth: register lookup: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	} else if exists {
		http.Error(w, "username already taken", http.StatusConflict)
		return
	}
	hash, err := db.HashPassword(body.Password)
	if err != nil {
		log.Printf("auth: hash password: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	userID, err := h.repo.CreateUser(r.Context(), body.Username, hash)
	if err != nil {
		log.Printf("auth: create user: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	token, err := h.repo.CreateSession(r.Context(), userID, sessionTTL)
	if err != nil {
		log.Printf("auth: create session: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, token, h.cookieSecure(r))
	log.Printf("auth: register %s (id=%d)", body.Username, userID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"user": db.User{ID: userID, Username: body.Username, Role: db.RoleUser}})
}

// login — POST /api/auth/login: вход по логину/паролю.
func (h *authHandler) login(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "auth disabled (no database)", http.StatusServiceUnavailable)
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(body.Username)
	key := clientKey(r, username)
	if !h.limiter.allow(key) {
		http.Error(w, "too many failed attempts, try again later", http.StatusTooManyRequests)
		return
	}
	u, hash, ok, err := h.repo.GetUserByUsername(r.Context(), username)
	if err != nil {
		log.Printf("auth: login lookup: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if !ok || !db.VerifyPassword(body.Password, hash) {
		h.limiter.fail(key)
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	h.limiter.success(key)
	token, err := h.repo.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		log.Printf("auth: create session: %v", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, token, h.cookieSecure(r))
	log.Printf("auth: login %s (id=%d)", u.Username, u.ID)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"user": u})
}

// logout — POST /api/auth/logout: удаляет сессию и куку.
func (h *authHandler) logout(w http.ResponseWriter, r *http.Request) {
	if !checkOrigin(w, r) {
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" && h.repo != nil {
		_ = h.repo.DeleteSession(r.Context(), c.Value)
	}
	clearSessionCookie(w, h.cookieSecure(r))
	log.Printf("auth: logout")
	w.WriteHeader(http.StatusNoContent)
}

// me — GET /api/auth/me: текущий пользователь (по куке сессии).
func (h *authHandler) me(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.repo == nil {
		http.Error(w, "auth disabled", http.StatusServiceUnavailable)
		return
	}
	u, ok := h.currentUser(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"user": u})
}

// takeRegistration атомарно резервирует попытку ДО разбора тела и хеширования
// пароля (ключ регистрации — только IP и не пересекается с попытками входа).
func (l *loginLimiter) takeRegistration(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	s := l.state[key]
	if s == nil || now.Sub(s.window) >= loginWindow {
		s = &loginState{window: now}
		l.state[key] = s
	}
	if s.fails >= loginMaxAttempts {
		return false
	}
	s.fails++
	l.pruneLocked(now)
	return true
}
