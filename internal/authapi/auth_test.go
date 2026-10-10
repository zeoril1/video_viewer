package authapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
)

func TestLoginLimiterLockout(t *testing.T) {
	l := newLoginLimiter()
	key := "192.168.1.10|zeoril"
	if !l.allow(key) {
		t.Fatal("первая попытка должна быть разрешена")
	}
	for i := 0; i < loginMaxAttempts-1; i++ {
		l.fail(key)
	}
	if !l.allow(key) {
		t.Fatal("после loginMaxAttempts-1 неудач доступ ещё разрешён")
	}
	l.fail(key) // достигаем лимита — блокировка
	if l.allow(key) {
		t.Error("после loginMaxAttempts неудач ключ должен быть заблокирован")
	}
	l.success(key)
	if !l.allow(key) {
		t.Error("success должен снять блокировку")
	}
}

func TestLoginLimiterIsolation(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < loginMaxAttempts; i++ {
		l.fail("1.1.1.1|alice")
	}
	if l.allow("1.1.1.1|alice") {
		t.Error("alice с IP 1.1.1.1 должна быть заблокирована")
	}
	if !l.allow("1.1.1.1|bob") {
		t.Error("другой username не должен блокироваться")
	}
	if !l.allow("2.2.2.2|alice") {
		t.Error("тот же username с другого IP не должен блокироваться")
	}
}

func TestLoginLimiterPrune(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < loginStateMax; i++ {
		key := fmt.Sprintf("192.0.2.1|user%d", i)
		if !l.allow(key) {
			t.Fatalf("key %d was unexpectedly rejected", i)
		}
		l.fail(key)
	}
	if l.allow("192.0.2.1|new-user") || l.takeRegistration("192.0.2.2|") {
		t.Fatal("new keys exceeded the limiter capacity")
	}
	if len(l.state) != loginStateMax {
		t.Fatalf("retained %d records, want %d", len(l.state), loginStateMax)
	}
	old := l.state["192.0.2.1|user0"]
	old.window = time.Now().Add(-loginWindow - time.Second)
	pending := l.state["192.0.2.1|user1"]
	pending.window, pending.pending = old.window, 1
	locked := l.state["192.0.2.1|user2"]
	locked.window, locked.locked = old.window, time.Now().Add(loginLockout)
	if !l.allow("192.0.2.1|new-user") {
		t.Fatal("expired records were not reclaimed")
	}
	if l.state["192.0.2.1|user1"] != pending || l.state["192.0.2.1|user2"] != locked {
		t.Fatal("pending attempts or active lockouts were evicted")
	}
	if _, ok := l.state["192.0.2.1|user3"]; !ok {
		t.Fatal("fresh failure record was evicted")
	}
}

func TestLoginLimiterLockoutExpires(t *testing.T) {
	l := newLoginLimiter()
	const key = "192.0.2.1|user"
	for range loginMaxAttempts {
		if !l.allow(key) {
			t.Fatal("initial reservation rejected")
		}
		l.fail(key)
	}
	l.state[key].locked = time.Now().Add(-time.Second)
	if !l.allow(key) {
		t.Fatal("expired lockout still blocked login")
	}
	if s := l.state[key]; s.fails != 0 || s.pending != 1 {
		t.Fatalf("new lockout window has fails=%d, pending=%d", s.fails, s.pending)
	}
}

func TestLoginLimiterCountsPendingAttempts(t *testing.T) {
	l := newLoginLimiter()
	const key = "192.0.2.1|user"
	const parallel = 128
	var admitted atomic.Int32
	var ready, done sync.WaitGroup
	finish := make(chan struct{})
	ready.Add(parallel)
	done.Add(parallel)
	for range parallel {
		go func() {
			defer done.Done()
			ok := l.allow(key)
			if ok {
				admitted.Add(1)
			}
			ready.Done()
			<-finish
			if ok {
				l.fail(key)
			}
		}()
	}
	ready.Wait()
	close(finish)
	done.Wait()
	if admitted.Load() != loginMaxAttempts {
		t.Fatalf("admitted %d simultaneous attempts, want %d", admitted.Load(), loginMaxAttempts)
	}
	if l.allow(key) {
		t.Fatal("completed failures did not block further attempts")
	}
}

func TestLoginLimiterSuccessKeepsOtherPendingAttempts(t *testing.T) {
	l := newLoginLimiter()
	const key = "192.0.2.1|user"
	for range loginMaxAttempts {
		if !l.allow(key) {
			t.Fatal("initial reservation rejected")
		}
	}
	l.success(key)
	if !l.allow(key) {
		t.Fatal("success did not release its reservation")
	}
	if l.allow(key) {
		t.Fatal("success discarded the other pending reservations")
	}
	for range loginMaxAttempts {
		l.fail(key)
	}
	if l.allow(key) {
		t.Fatal("failures arriving after success were lost")
	}
}

func TestLoginLimiterExpiredWindowKeepsPendingAttempts(t *testing.T) {
	l := newLoginLimiter()
	const key = "192.0.2.1|user"
	for range loginMaxAttempts {
		if !l.allow(key) {
			t.Fatal("initial reservation rejected")
		}
	}
	l.state[key].window = time.Now().Add(-loginWindow - time.Second)
	if l.allow(key) {
		t.Fatal("window rollover discarded pending attempts")
	}
	for range loginMaxAttempts {
		l.cancel(key)
	}
	if len(l.state) != 0 || !l.allow(key) {
		t.Fatal("canceled attempts were not released")
	}
}

type loginTestConnector struct {
	queries chan<- struct{}
	release <-chan struct{}
	err     error
	noRows  bool
}

func (c loginTestConnector) Connect(context.Context) (driver.Conn, error) {
	return loginTestConn{c}, nil
}
func (c loginTestConnector) Driver() driver.Driver { return loginTestDriver{} }

type loginTestDriver struct{}

func (loginTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the test connector")
}

type loginTestConn struct{ loginTestConnector }

func (loginTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not used")
}
func (loginTestConn) Begin() (driver.Tx, error) { return nil, errors.New("not used") }
func (loginTestConn) Close() error              { return nil }
func (c loginTestConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.queries != nil {
		c.queries <- struct{}{}
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	if strings.Contains(query, "FROM sessions") {
		return &sessionTestRows{read: c.noRows}, nil
	}
	return &loginTestRows{}, nil
}

type sessionTestRows struct{ read bool }

func (*sessionTestRows) Columns() []string { return []string{"id", "username", "role", "created_at"} }
func (*sessionTestRows) Close() error      { return nil }
func (r *sessionTestRows) Next(out []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	out[0], out[1], out[2], out[3] = int64(7), "session-user", db.RoleUser, time.Now()
	return nil
}

type loginTestRows struct{ read bool }

func (*loginTestRows) Columns() []string {
	return []string{"id", "username", "role", "password_hash", "created_at"}
}
func (*loginTestRows) Close() error { return nil }
func (r *loginTestRows) Next(out []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	out[0], out[1], out[2] = int64(1), "existing-user", db.RoleUser
	out[3] = "pbkdf2$1$00$0000000000000000000000000000000000000000000000000000000000000000"
	out[4] = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	return nil
}

func TestLoginHandlerLimitsRequestsBeforeDatabaseLookup(t *testing.T) {
	const parallel = 128
	queries := make(chan struct{}, parallel)
	release := make(chan struct{})
	connection := sql.OpenDB(loginTestConnector{queries: queries, release: release})
	t.Cleanup(func() { _ = connection.Close() })
	h := &authHandler{repo: db.NewRepo(connection), limiter: newLoginLimiter()}
	codes := make(chan int, parallel)
	var requests sync.WaitGroup
	requests.Add(parallel)
	for range parallel {
		go func() {
			defer requests.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
				strings.NewReader(`{"username":"existing-user","password":"wrong-password"}`))
			req.RemoteAddr = "192.0.2.10:30000"
			rec := httptest.NewRecorder()
			h.login(rec, req)
			codes <- rec.Code
		}()
	}
	// Requests are released even when the assertions fail, so cleanup cannot hang.
	defer func() { close(release); requests.Wait() }()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for range loginMaxAttempts {
		select {
		case <-queries:
		case <-timeout.C:
			t.Fatal("admitted requests did not reach the database")
		}
	}
	for range parallel - loginMaxAttempts {
		select {
		case code := <-codes:
			if code != http.StatusTooManyRequests {
				t.Fatalf("request returned %d before database release, want 429", code)
			}
		case <-timeout.C:
			t.Fatal("parallel requests were not rejected while password checks were pending")
		}
	}
	if len(queries) != 0 {
		t.Fatal("more than five requests reached the database")
	}
}

func TestLoginHandlerDatabaseErrorsReleaseReservations(t *testing.T) {
	connection := sql.OpenDB(loginTestConnector{err: errors.New("temporary database error")})
	t.Cleanup(func() { _ = connection.Close() })
	h := &authHandler{repo: db.NewRepo(connection), limiter: newLoginLimiter()}
	for range 2 * loginMaxAttempts {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
			strings.NewReader(`{"username":"existing-user","password":"password"}`))
		req.RemoteAddr = "192.0.2.10:30000"
		rec := httptest.NewRecorder()
		h.login(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("database error returned %d, want 503 without login lockout", rec.Code)
		}
	}
	if len(h.limiter.state) != 0 {
		t.Fatal("database failures left pending attempts or failed-password counters")
	}
}

func TestSessionDistinguishesUnavailableExpiredAndDisabled(t *testing.T) {
	for _, tc := range []struct {
		name             string
		connector        loginTestConnector
		disabled, cookie bool
		status           int
		code             string
	}{
		{name: "valid", cookie: true, status: 200},
		{name: "expired", connector: loginTestConnector{noRows: true}, cookie: true, status: 401},
		{name: "no cookie", connector: loginTestConnector{err: errors.New("database down")}, status: 401},
		{name: "unavailable", connector: loginTestConnector{err: errors.New("database down")}, cookie: true, status: 503, code: "auth_unavailable"},
		{name: "disabled", disabled: true, status: 503, code: "auth_disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &authHandler{}
			if !tc.disabled {
				conn := sql.OpenDB(tc.connector)
				t.Cleanup(func() { _ = conn.Close() })
				h.repo = db.NewRepo(conn)
			}
			r := httptest.NewRequest("GET", "/api/auth/me", nil)
			if tc.cookie {
				r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "valid-token"})
			}
			w := httptest.NewRecorder()
			h.me(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tc.code != "" {
				var body map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["code"] != tc.code {
					t.Fatalf("unexpected error: %s", w.Body.String())
				}
			}
			if w.Header().Get("Set-Cookie") != "" {
				t.Fatal("session was cleared during verification")
			}
		})
	}
}

func TestProtectedAuthActionsFailClosedDuringDatabaseOutage(t *testing.T) {
	conn := sql.OpenDB(loginTestConnector{err: errors.New("database down")})
	t.Cleanup(func() { _ = conn.Close() })
	h := NewServer(Config{DB: db.NewRepo(conn)})
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/history", ""}, {"POST", "/api/history/progress", `{"film_id":"tt1","position":10}`},
		{"DELETE", "/api/history", ""}, {"GET", "/api/personal", ""}, {"PUT", "/api/personal/watchlist/tt1", `{}`},
		{"POST", "/api/auth/device/approve", `{"code":"1234"}`}, {"GET", "/api/admin/users", ""},
		{"PUT", "/api/admin/users/7/role", `{"role":"admin"}`}, {"POST", "/api/rooms", `{"id":"tt1","magnet":"magnet:?x=1","file":0}`},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "valid-token"})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 503 || !strings.Contains(w.Body.String(), `"auth_unavailable"`) {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	// A genuinely anonymous viewer can still create an ephemeral room.
	r := httptest.NewRequest("POST", "/api/rooms", strings.NewReader(`{"id":"tt1","magnet":"magnet:?x=1","file":0}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("anonymous room failed during outage: %d %s", w.Code, w.Body.String())
	}
}

func TestClientKey(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "192.168.1.10:54321"
	if got := clientKey(r, "ZeOril"); got != "192.168.1.10|zeoril" {
		t.Errorf("clientKey = %q, want 192.168.1.10|zeoril", got)
	}
	r.RemoteAddr = "10.0.0.5"
	if got := clientKey(r, "  bob "); got != "10.0.0.5|bob" {
		t.Errorf("clientKey(без порта) = %q, want 10.0.0.5|bob", got)
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		name                  string
		origin, referer, host string
		want                  bool
	}{
		{"Origin совпадает", "http://localhost:8080", "", "localhost:8080", true},
		{"Origin чужой", "http://evil.example", "", "localhost:8080", false},
		{"другая схема, тот же host", "https://localhost:8080", "", "localhost:8080", true},
		{"нет Origin/Referer — пропуск", "", "", "localhost:8080", true},
		{"фолбэк на Referer", "", "http://localhost:8080/app", "localhost:8080", true},
		{"Referer чужой", "", "http://evil.example/x", "localhost:8080", false},
		{"битый Origin", "://bad", "", "localhost:8080", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.referer != "" {
			r.Header.Set("Referer", c.referer)
		}
		if got := sameOrigin(r); got != c.want {
			t.Errorf("%s: sameOrigin = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCheckOriginHandler(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !checkOrigin(w, r) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// Чужой Origin — 403.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Host = "localhost:8080"
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("чужой Origin -> %d, want 403", rec.Code)
	}
	// Свой Origin — проходит.
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.Host = "localhost:8080"
	req.Header.Set("Origin", "http://localhost:8080")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("свой Origin -> %d, want 204", rec.Code)
	}
}

// TestCookieSecureFlag проверяет, что Secure-флаг куки сессии выбирается по
// внешней схеме запроса: локальный HTTP — без флага (иначе вход по
// http://192.168.x.x:8080 не работал бы), публичный HTTPS (в том числе
// терминация TLS на обратном прокси) — с флагом.
func TestCookieSecureFlag(t *testing.T) {
	httpsReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	httpsReq.Header.Set("X-Forwarded-Proto", "https")
	plainReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	spoofed := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	spoofed.Header.Set("X-Forwarded-Proto", "https, http")

	cases := []struct {
		name       string
		req        *http.Request
		force      bool
		wantSecure bool
	}{
		{"локальный http", plainReq, false, false},
		{"за https-прокси", httpsReq, false, true},
		{"список схем от прокси", spoofed, false, true},
		{"COOKIE_SECURE=1 форсирует", plainReq, true, true},
	}
	for _, c := range cases {
		h := &authHandler{secureCookies: c.force}
		if got := h.cookieSecure(c.req); got != c.wantSecure {
			t.Errorf("%s: cookieSecure=%v, ожидалось %v", c.name, got, c.wantSecure)
		}
		rec := httptest.NewRecorder()
		setSessionCookie(rec, "tok", h.cookieSecure(c.req))
		got := strings.Contains(rec.Header().Get("Set-Cookie"), "Secure")
		if got != c.wantSecure {
			t.Errorf("%s: Set-Cookie=%q (Secure=%v), ожидалось %v",
				c.name, rec.Header().Get("Set-Cookie"), got, c.wantSecure)
		}
	}
}
