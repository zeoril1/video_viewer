package gatewayapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesPostBodyProxied(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		var body map[string]string
		if r.Method != http.MethodPost || r.URL.RawQuery != "" || json.NewDecoder(r.Body).Decode(&body) != nil || body["magnet"] != "magnet:test" {
			t.Errorf("file request body was not preserved")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	h := NewServer(Config{WebDir: t.TempDir(), StreamURL: upstream.URL})
	r := httptest.NewRequest(http.MethodPost, "/api/films/tt1/files", strings.NewReader(`{"magnet":"magnet:test","tmdb":"123"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !called || w.Code != http.StatusOK {
		t.Fatalf("files POST: called=%v status=%d", called, w.Code)
	}
}

// Статика фронтенда отдаётся с Cache-Control: no-cache — иначе браузер
// (и WebView Android-приставки) кэширует app.js эвристически по
// Last-Modified и продолжает работать на старой версии после обновления.
func TestStaticFilesNoCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("// test"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewServer(Config{WebDir: dir})

	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app.js -> %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
}

// /api/debug/mem НЕ должен проксироваться наружу: маршрут закрыт, запрос
// уходит на FileServer (404), а stream-сервис не вызывается.
func TestDebugMemNotPublic(t *testing.T) {
	called := false
	streamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer streamSrv.Close()

	cfg := Config{WebDir: t.TempDir(), StreamURL: streamSrv.URL}
	h := NewServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/debug/mem", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if called {
		t.Error("/api/debug/mem проксируется наружу — маршрут должен быть закрыт")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/debug/mem -> %d, want 404 (FileServer)", rec.Code)
	}
}

// HLS-эндпоинты субтитров (/hls/pl.m3u8 и /hls/subs/) должны проксироваться
// на stream-сервис (вызываются hls.js из master-плейлиста).
func TestHLSSubtitleRoutesProxied(t *testing.T) {
	for _, path := range []string{
		"/api/films/tt123/hls/pl.m3u8?magnet=m&track=1",
		"/api/films/tt123/hls/subs/0.m3u8?magnet=m&track=1",
	} {
		called := false
		streamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))
		cfg := Config{WebDir: t.TempDir(), StreamURL: streamSrv.URL}
		h := NewServer(cfg)

		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		streamSrv.Close()

		if !called {
			t.Errorf("GET %s не проксирован на stream-сервис", path)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s -> %d, want 200", path, rec.Code)
		}
	}
}

func TestHealthAllOK(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()

	cfg := Config{WebDir: t.TempDir(), CatalogURL: okSrv.URL, StreamURL: okSrv.URL, AuthURL: okSrv.URL, IPTVURL: okSrv.URL}
	h := NewServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/health -> %d, want 200", rec.Code)
	}
}

func TestHealthAggregatesStatuses(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	cfg := Config{WebDir: t.TempDir(), CatalogURL: okSrv.URL, StreamURL: errSrv.URL, AuthURL: "", IPTVURL: okSrv.URL}
	h := NewServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /api/health -> %d, want 503", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", out["status"])
	}
	services, _ := out["services"].(map[string]any)
	if services["catalog"] != "ok" || services["stream"] != "error" || services["auth"] != "not_configured" {
		t.Errorf("services = %v, want catalog=ok stream=error auth=not_configured", services)
	}
}
