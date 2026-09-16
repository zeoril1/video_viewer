package iptvapi

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxPlaylistBytes — предохранитель на размер плейлиста (100 МиБ — с запасом
// для сборных списков на десятки тысяч каналов).
const maxPlaylistBytes = 100 << 20

// fetchBytes скачивает URL с нужными заголовками и прозрачно разжимает gzip
// (плейлисты часто отдаются как .gz/Content-Encoding: gzip).
func fetchBytes(ctx context.Context, hc *http.Client, url, ua, referer string) ([]byte, error) {
	rc, err := fetchStream(ctx, hc, url, ua, referer)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// Предел — на распакованные данные (в архиве плейлист может развернуться
	// в десятки раз).
	data, err := io.ReadAll(io.LimitReader(rc, maxPlaylistBytes))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// fetchStream скачивает URL ПОТОКОМ и прозрачно разжимает gzip — по магии
// ческим байтам, так как не все провайдеры ставят корректный
// Content-Type/Encoding. Нужен телепрограмме: ~560 МиБ распакованного XMLTV
// целиком в память не влезут. Поток обязательно закрывать (закрывает и тело).
func fetchStream(ctx context.Context, hc *http.Client, url, ua, referer string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("http %s", resp.Status)
	}
	br := bufio.NewReaderSize(resp.Body, 8192)
	magic, err := br.Peek(2)
	if err != nil && err != io.EOF {
		resp.Body.Close()
		return nil, err
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("gzip: %w", err)
		}
		return &readCloser{Reader: zr, closers: []io.Closer{zr, resp.Body}}, nil
	}
	return &readCloser{Reader: br, closers: []io.Closer{resp.Body}}, nil
}

// readCloser — поток с несколькими закрываемыми ресурсами (распаковщик и тело
// ответа): закрывать их нужно вместе.
type readCloser struct {
	io.Reader
	closers []io.Closer
}

func (r *readCloser) Close() error {
	var first error
	for _, c := range r.closers {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// decodeJSON читает тело запроса как JSON (с ограничением размера).
func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// atoiOr — int из строки или значение по умолчанию.
func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

// atoi64 — int64 из строки (0 при ошибке).
func atoi64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// defaultUA — User-Agent по умолчанию для запросов к IPTV-провайдерам:
// часть серверов отвечает только «браузерным» клиентам.
const defaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// channelHeaders дополняет заголовки канала значениями по умолчанию.
func channelHeaders(ua, referer, plUA, plReferer string) (string, string) {
	if ua == "" {
		ua = plUA
	}
	if referer == "" {
		referer = plReferer
	}
	if ua == "" {
		ua = defaultUA
	}
	return ua, referer
}

// waitFile ждёт появления файла (ffmpeg пишет плейлист не сразу).
func waitFile(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if fileExists(path) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("таймаут ожидания %s", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
