package authapi

import (
	"encoding/json"
	"errors"
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
	loginStateMax    = 4096             // строгий предел записей в карте
)

// loginState — счётчик неудачных попыток по ключу (IP|username).
type loginState struct {
	fails   int
	pending int // попытки, для которых ещё проверяются учётные данные
	window  time.Time
	locked  time.Time
}

// loginLimiter — простой in-memory throttle попыток входа/регистрации.
type loginLimiter struct {
	mu    sync.Mutex
	state map[string]*loginState
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{state: make(map[string]*loginState)}
}

// allow атомарно резервирует попытку до обращения к БД и проверки пароля.
// Каждая разрешённая попытка завершается через fail, success или cancel.
func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	s := l.stateLocked(key, now)
	if s == nil {
		return false
	}
	if now.Before(s.locked) {
		return false
	}
	if now.Sub(s.window) >= loginWindow || !s.locked.IsZero() {
		s.fails = 0
		s.locked = time.Time{}
		s.window = now
	}
	if s.fails+s.pending >= loginMaxAttempts {
		return false
	}
	s.pending++
	return true
}

// fail фиксирует неудачную попытку; при превышении лимита блокирует ключ.
func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	s := l.stateLocked(key, now)
	if s == nil {
		return
	}
	if s.pending > 0 {
		s.pending--
	}
	if now.Sub(s.window) >= loginWindow {
		s.fails = 0
		s.window = now
	}
	s.fails++
	if s.fails >= loginMaxAttempts {
		s.locked = now.Add(loginLockout)
	}
}

// success сбрасывает счётчик после успешного входа.
func (l *loginLimiter) success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.state[key]; s != nil {
		if s.pending > 0 {
			s.pending--
		}
		s.fails = 0
		s.locked = time.Time{}
		s.window = time.Now()
		if s.pending == 0 {
			delete(l.state, key)
		}
	}
}

// cancel освобождает попытку при ошибке БД, не считая её неверным паролем.
func (l *loginLimiter) cancel(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.state[key]; s != nil {
		if s.pending > 0 {
			s.pending--
		}
		if s.pending == 0 && s.fails == 0 && s.locked.IsZero() {
			delete(l.state, key)
		}
	}
}

// stateLocked не вытесняет свежие блокировки ради новых ключей.
func (l *loginLimiter) stateLocked(key string, now time.Time) *loginState {
	if s := l.state[key]; s != nil {
		return s
	}
	if len(l.state) >= loginStateMax {
		l.pruneLocked(now)
		if len(l.state) >= loginStateMax {
			return nil
		}
	}
	s := &loginState{window: now}
	l.state[key] = s
	return s
}

// pruneLocked удаляет только истёкшие записи без выполняющихся запросов.
func (l *loginLimiter) pruneLocked(now time.Time) {
	for k, s := range l.state {
		if s.pending == 0 && !now.Before(s.locked) && now.Sub(s.window) >= loginWindow {
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

var errAuthDisabled = errors.New("auth disabled (no database)")

// currentUser отличает недействительную сессию от сбоя проверки прав.
func (h *authHandler) currentUser(r *http.Request) (db.User, bool, error) {
	if h.repo == nil {
		return db.User{}, false, errAuthDisabled
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return db.User{}, false, nil
	}
	u, ok, err := h.repo.GetUserBySession(r.Context(), c.Value)
	if err != nil {
		log.Printf("auth: get user by session: %v", err)
		return db.User{}, false, err
	}
	return u, ok, nil
}

// Коды стабильны: frontend не принимает временный сбой за отключённый сервис.
func writeAuthServiceError(w http.ResponseWriter, err error) {
	code, message := "auth_unavailable", "Authentication is temporarily unavailable. Try again shortly."
	if errors.Is(err, errAuthDisabled) {
		code, message = "auth_disabled", "Authentication is disabled (no database)."
	} else {
		w.Header().Set("Retry-After", "2")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "error": message})
}

func (h *authHandler) requireUser(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	u, ok, err := h.currentUser(r)
	if err != nil {
		writeAuthServiceError(w, err)
		return db.User{}, false
	}
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return db.User{}, false
	}
	return u, true
}

// register — POST /api/auth/register: создание аккаунта.
func (h *authHandler) register(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		writeAuthServiceError(w, errAuthDisabled)
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
		writeAuthServiceError(w, err)
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
		writeAuthServiceError(w, err)
		return
	}
	token, err := h.repo.CreateSession(r.Context(), userID, sessionTTL)
	if err != nil {
		log.Printf("auth: create session: %v", err)
		writeAuthServiceError(w, err)
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
		writeAuthServiceError(w, errAuthDisabled)
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
	credentialsChecked, credentialsValid := false, false
	defer func() {
		switch {
		case credentialsValid:
			h.limiter.success(key)
		case credentialsChecked:
			h.limiter.fail(key)
		default:
			h.limiter.cancel(key)
		}
	}()
	u, hash, ok, err := h.repo.GetUserByUsername(r.Context(), username)
	if err != nil {
		log.Printf("auth: login lookup: %v", err)
		writeAuthServiceError(w, err)
		return
	}
	credentialsChecked = true
	if !ok || !db.VerifyPassword(body.Password, hash) {
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	credentialsValid = true
	token, err := h.repo.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		log.Printf("auth: create session: %v", err)
		writeAuthServiceError(w, err)
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
	u, ok := h.requireUser(w, r)
	if !ok {
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
	s := l.stateLocked(key, now)
	if s == nil {
		return false
	}
	if now.Sub(s.window) >= loginWindow {
		s.fails = 0
		s.window = now
	}
	if s.fails >= loginMaxAttempts {
		return false
	}
	s.fails++
	return true
}
