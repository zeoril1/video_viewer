package torrentvpn

import (
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/remoteauth"
)

func NewControlHandler(manager *Manager, authURL string, options ...ProxyControlOptions) http.Handler {
	auth := remoteauth.New(authURL)
	// Control-plane authentication always uses the ordinary container network.
	auth.HTTP.Transport = &http.Transport{Proxy: nil}
	mux := http.NewServeMux()
	var proxyOptions ProxyControlOptions
	if len(options) > 0 {
		proxyOptions = options[0]
	}
	registerProxyAdmin(mux, auth, manager, proxyOptions)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/admin/vpn", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !auth.RequireAdmin(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, manager.Status())
	})
	mux.HandleFunc("PUT /api/admin/vpn", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !auth.RequireAdmin(w, r) || !httpx.CheckOrigin(w, r) {
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "Запрос с другого сайта запрещён"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxConfigBytes+(16<<10))
		reader, err := r.MultipartReader()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Выберите WireGuard-файл .conf"})
			return
		}
		name, data, err := readConfigPart(reader)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		status, err := manager.Replace(name, data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
	return mux
}

func readConfigPart(reader *multipart.Reader) (string, []byte, error) {
	part, err := reader.NextPart()
	if err != nil {
		return "", nil, errors.New("Выберите один WireGuard-файл .conf")
	}
	defer part.Close()
	if part.FormName() != "config" || part.FileName() == "" {
		return "", nil, errors.New("Ожидается файл в поле config")
	}
	data, err := io.ReadAll(io.LimitReader(part, MaxConfigBytes+1))
	if err != nil || len(data) > MaxConfigBytes {
		return "", nil, errors.New("Размер конфига не должен превышать 64 КиБ")
	}
	if _, err := reader.NextPart(); err != io.EOF {
		return "", nil, errors.New("Загрузите только один файл конфигурации")
	}
	return part.FileName(), data, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
