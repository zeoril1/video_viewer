package iptvapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestFetchStreamGzip — поток разжимается по магическим байтам, а не по
// Content-Type: так отдают плейлисты и телепрограмму многие провайдеры.
// Короткое тело (меньше двух байт) тоже не должно ломать загрузку.
func TestFetchStreamGzip(t *testing.T) {
	body := []byte("<tv>программа передач</tv>")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gz":
			// Content-Type намеренно «неправильный»: ориентируемся на байты.
			w.Header().Set("Content-Type", "audio/x-mpegurl")
			_, _ = w.Write(buf.Bytes())
		case "/plain":
			_, _ = w.Write(body)
		case "/tiny":
			_, _ = w.Write([]byte("x"))
		default:
			http.Error(w, "нет такого", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	hc := &http.Client{Timeout: 5 * time.Second}
	ctx := context.Background()

	for _, path := range []string{"/gz", "/plain", "/tiny"} {
		rc, err := fetchStream(ctx, hc, srv.URL+path, "", "")
		if err != nil {
			t.Fatalf("fetchStream(%s): %v", path, err)
		}
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("чтение %s: %v", path, err)
		}
		if err := rc.Close(); err != nil {
			t.Errorf("закрытие %s: %v", path, err)
		}
		if path == "/tiny" {
			continue
		}
		if string(got) != string(body) {
			t.Errorf("fetchStream(%s) = %q, ждали %q", path, got, body)
		}
	}

	// Плейлисты читаются тем же путём — им тоже нужен разжатый gzip.
	data, err := fetchBytes(ctx, hc, srv.URL+"/gz", "", "")
	if err != nil {
		t.Fatalf("fetchBytes: %v", err)
	}
	if string(data) != string(body) {
		t.Errorf("fetchBytes = %q, ждали %q", data, body)
	}
	// Не-200 — это ошибка, а не пустой ответ.
	if _, err := fetchBytes(ctx, hc, srv.URL+"/none", "", ""); err == nil {
		t.Error("на 404 ждали ошибку")
	}
}
