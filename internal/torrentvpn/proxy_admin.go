package torrentvpn

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/proxycheck"
	"github.com/zeoril1/video_viewer/internal/remoteauth"
)

type ProxyControlOptions struct {
	Pool   *proxycheck.Manager
	Router interface{ VPNStatus() (bool, time.Time) }
}

type proxyAdminStatus struct {
	proxycheck.Status
	ActiveRoute   string     `json:"active_route"`
	VPNConfigured bool       `json:"vpn_configured"`
	VPNRetryAt    *time.Time `json:"vpn_retry_at,omitempty"`
}

func (o ProxyControlOptions) status(manager *Manager) proxyAdminStatus {
	status := proxyAdminStatus{Status: o.Pool.Status(), ActiveRoute: "unavailable"}
	status.VPNConfigured = manager.Status().Configured
	var retryAt time.Time
	if o.Router != nil {
		status.VPNConfigured, retryAt = o.Router.VPNStatus()
	}
	if status.VPNConfigured && !time.Now().Before(retryAt) {
		status.ActiveRoute = "vpn"
	} else if status.Config.Enabled && status.Available > 0 {
		status.ActiveRoute = "proxy"
	}
	if time.Now().Before(retryAt) {
		status.VPNRetryAt = &retryAt
	}
	return status
}

func registerProxyAdmin(mux *http.ServeMux, auth *remoteauth.Client, manager *Manager, options ProxyControlOptions) {
	allowed := func(w http.ResponseWriter, r *http.Request, write bool) bool {
		w.Header().Set("Cache-Control", "no-store")
		if !auth.RequireAdmin(w, r) {
			return false
		}
		if write && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "Запрос с другого сайта запрещён"})
			return false
		}
		if write && !httpx.CheckOrigin(w, r) {
			return false
		}
		if options.Pool == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Сервис проверки прокси не настроен"})
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/admin/proxies", func(w http.ResponseWriter, r *http.Request) {
		if allowed(w, r, false) {
			writeJSON(w, http.StatusOK, options.status(manager))
		}
	})
	mux.HandleFunc("PUT /api/admin/proxies", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(w, r, true) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var config proxycheck.Config
		if err := decoder.Decode(&config); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Некорректные настройки прокси"})
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Ожидается один объект настроек"})
			return
		}
		if _, err := options.Pool.Replace(config); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, options.status(manager))
	})
	mux.HandleFunc("POST /api/admin/proxies/refresh", func(w http.ResponseWriter, r *http.Request) {
		if !allowed(w, r, true) {
			return
		}
		if !options.Pool.Status().Config.Enabled {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "Сначала включите резервные прокси"})
			return
		}
		options.Pool.Refresh()
		writeJSON(w, http.StatusAccepted, options.status(manager))
	})
}
