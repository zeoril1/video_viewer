package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProxyURL(t *testing.T) {
	// Прокси с учётными данными.
	u := proxyURL("vi3y34tr:q2w44fnqt5ua@80.66.72.103:53542")
	if u.Scheme != "http" || u.Host != "80.66.72.103:53542" {
		t.Fatalf("unexpected url: %+v", u)
	}
	if u.User == nil || u.User.Username() != "vi3y34tr" {
		t.Fatalf("unexpected user: %+v", u.User)
	}
	if pw, ok := u.User.Password(); !ok || pw != "q2w44fnqt5ua" {
		t.Fatalf("unexpected password: %q ok=%v", pw, ok)
	}
	// Прокси без учётных данных.
	u2 := proxyURL("10.0.0.5:8080")
	if u2.Host != "10.0.0.5:8080" || u2.User != nil {
		t.Fatalf("unexpected url: %+v", u2)
	}
	// redact не должен выдавать пароль.
	if got := redact("vi3y34tr:q2w44fnqt5ua@80.66.72.103:53542"); got != "80.66.72.103:53542" {
		t.Fatalf("redact = %q, want host only", got)
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4:8080":        "1.2.3.4:8080",
		"http://1.2.3.4:80":   "1.2.3.4:80",
		" https://x.y:3128/ ": "x.y:3128",
		"":                    "",
		"   ":                 "",
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStaticRoundRobin — прокси ходят по кругу; после MarkBroken обоих Next
// возвращает "" (запросы идут напрямую).
func TestStaticRoundRobin(t *testing.T) {
	p := NewPool(Config{Static: []string{"s1:3128", "s2:8080"}, Timeout: time.Second})
	if got := p.Next(); got != "s1:3128" {
		t.Fatalf("Next = %q, want s1:3128", got)
	}
	if got := p.Next(); got != "s2:8080" {
		t.Fatalf("Next = %q, want s2:8080", got)
	}
	if got := p.Next(); got != "s1:3128" {
		t.Fatalf("Next = %q, want s1:3128 (round-robin)", got)
	}
	// Оба упали -> пул пуст -> Next пустая строка (прямое соединение).
	p.MarkBroken("s1:3128")
	p.MarkBroken("s2:8080")
	if got := p.Next(); got != "" {
		t.Fatalf("Next = %q, want empty after all broken", got)
	}
}

// TestFailover — при сбое первого прокси запрос идёт через следующий, сломанный удаляется.
func TestFailover(t *testing.T) {
	// Рабочий «прокси» — HTTP-сервер, отвечающий на любой запрос.
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "proxied")
	}))
	defer good.Close()

	broken := "127.0.0.1:1" // закрытый порт -> connection refused

	p := NewPool(Config{Static: []string{broken, good.Listener.Addr().String()}, Timeout: 500 * time.Millisecond})
	if p.Size() != 2 {
		t.Fatalf("pool size = %d, want 2", p.Size())
	}

	rt := p.RoundTripper(3)
	req, err := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "proxied" {
		t.Fatalf("unexpected body: %q", string(b))
	}
	if got := p.Size(); got != 1 {
		t.Fatalf("pool size after failover = %d, want 1", got)
	}
}

// TestRoundTripperDirectFallback — при пустом пуле запрос идёт напрямую.
func TestRoundTripperDirectFallback(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "direct")
	}))
	defer target.Close()

	p := NewPool(Config{Timeout: time.Second})
	rt := p.RoundTripper(3)
	req, _ := http.NewRequest(http.MethodGet, target.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "direct" {
		t.Fatalf("unexpected body: %q", string(b))
	}
}
