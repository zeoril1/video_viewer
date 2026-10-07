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

func TestPosterProxiedToCatalog(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/poster" || r.URL.Query().Get("url") != "https://image.tmdb.org/t/p/w500/a.jpg" {
			t.Error(r.URL)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write([]byte("poster"))
	}))
	defer upstream.Close()
	w := httptest.NewRecorder()
	NewServer(Config{WebDir: t.TempDir(), CatalogURL: upstream.URL}).ServeHTTP(w, httptest.NewRequest("GET", "/api/poster?url=https%3A%2F%2Fimage.tmdb.org%2Ft%2Fp%2Fw500%2Fa.jpg", nil))
	if w.Code != 200 || w.Body.String() != "poster" || w.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestSegmentsProxiedToCatalog(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/films/tt1234567/segments" || r.URL.Query().Get("episode") != "2" || r.URL.Query().Get("duration") != "2100" {
			t.Error(r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"segments":[],"status":"not_found"}`))
	}))
	defer upstream.Close()
	w := httptest.NewRecorder()
	NewServer(Config{WebDir: t.TempDir(), CatalogURL: upstream.URL}).ServeHTTP(w, httptest.NewRequest("GET", "/api/films/tt1234567/segments?season=1&episode=2&duration=2100", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"not_found"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestSeriesMetadataProxiedToCatalog(t *testing.T) {
	for _, path := range []string{
		"/api/films/tt1234567/seasons",
		"/api/films/tmdb-tv-42/seasons/2",
	} {
		t.Run(path, func(t *testing.T) {
			called := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.Method != http.MethodGet || r.URL.Path != path {
					t.Error(r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"status":"ready"}`))
			}))
			defer upstream.Close()
			w := httptest.NewRecorder()
			NewServer(Config{WebDir: t.TempDir(), CatalogURL: upstream.URL}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if !called || w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ready"`) {
				t.Fatal(called, w.Code, w.Body.String())
			}
		})
	}
}

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

func TestDownloadStatusProxiedAndAnalysisWritesRemainInternal(t *testing.T) {
	called := false
	stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/api/stream/download-status" || r.URL.Query().Get("file") != "2" {
			t.Error(r.URL)
		}
		w.Write([]byte(`{"complete":true,"downloaded":100,"total":100}`))
	}))
	defer stream.Close()
	h := NewServer(Config{WebDir: t.TempDir(), StreamURL: stream.URL, CatalogURL: stream.URL})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stream/download-status?magnet=m&file=2", nil))
	if !called || w.Code != 200 || !strings.Contains(w.Body.String(), `"complete":true`) {
		t.Fatal(called, w.Code, w.Body.String())
	}
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/internal/segment-analysis/segments.0123456789abcdef0123456789abcdef01234567.2"},
		{http.MethodPost, "/internal/segment-analysis"},
		{http.MethodPost, "/api/films/tt1234567/segments"},
	} {
		called = false
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(request.method, request.path, nil))
		if called || w.Code == http.StatusOK || w.Code == http.StatusNoContent {
			t.Fatal("analysis write/protected data exposed", request, w.Code)
		}
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
