package authapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
	// Много ключей сверх лимита — pruneLocked не должен паниковать и не
	// должен удалять свежие (ещё не истёкшие) записи.
	for i := 0; i < loginStateMax+10; i++ {
		l.fail("ip" + string(rune('a'+i%26)) + "|u")
	}
	l.mu.Lock()
	n := len(l.state)
	l.mu.Unlock()
	if n == 0 {
		t.Error("pruneLocked удалил все записи (включая свежие)")
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
