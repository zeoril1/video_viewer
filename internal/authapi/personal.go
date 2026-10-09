package authapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/zeoril1/video_viewer/internal/authn"
	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/httpx"
)

var personalKinds = map[string]bool{"watchlist": true, "watched": true, "follow": true, "preferences": true, "iptv_favorite": true, "iptv_recent": true}
var personalKey = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)

func readSmallJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 32768)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		http.Error(w, "invalid json", 400)
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid json", 400)
		return false
	}
	return true
}
func (h *authHandler) personal(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	u, ok := h.currentUser(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.Method == "GET" {
		items, err := h.repo.ListPersonal(r.Context(), u.ID)
		if err != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		writePersonalJSON(w, map[string]any{"items": items})
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	kind, key := r.PathValue("kind"), r.PathValue("key")
	if !personalKinds[kind] || !personalKey.MatchString(key) {
		http.Error(w, "invalid item", 400)
		return
	}
	if !canWritePersonal(u, kind, key) {
		http.Error(w, "forbidden: moderator or admin required to edit skip marks", http.StatusForbidden)
		return
	}
	if r.Method == "DELETE" {
		if err := h.repo.DeletePersonal(r.Context(), u.ID, kind, key); err != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
	} else {
		var data map[string]any
		if !readSmallJSON(w, r, &data) {
			return
		}
		if data == nil {
			http.Error(w, "object required", 400)
			return
		}
		raw, _ := json.Marshal(data)
		if err := h.repo.SavePersonal(r.Context(), u.ID, db.PersonalItem{Kind: kind, Key: key, Data: raw}); err != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
	}
	w.WriteHeader(204)
}

func canWritePersonal(u db.User, kind, key string) bool {
	return kind != "preferences" || !strings.HasPrefix(key, "segments.") || authn.CanEditSegments(u)
}
func writePersonalJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func deviceHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (h *authHandler) deviceStart(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	if !h.limiter.takeRegistration(clientKey(r, "device-start")) {
		http.Error(w, "try later", 429)
		return
	}
	token, code := randomHex(32), strings.ToUpper(randomHex(4))
	if err := h.repo.CreateDeviceLink(r.Context(), deviceHash(token), code); err != nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writePersonalJSON(w, map[string]any{"device_code": token, "user_code": code, "expires_in": 600, "interval": 5, "verification_uri": httpx.PublicBase(r) + "/device.html?mode=approve"})
}
func (h *authHandler) deviceApprove(w http.ResponseWriter, r *http.Request) {
	if !checkOrigin(w, r) {
		return
	}
	u, ok := h.currentUser(r)
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}
	if !h.limiter.takeRegistration(clientKey(r, "device-approve")) {
		http.Error(w, "try later", 429)
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !readSmallJSON(w, r, &body) {
		return
	}
	code := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(body.Code), "-", ""))
	ok, err := h.repo.ApproveDeviceLink(r.Context(), code, u.ID)
	if err != nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	if !ok {
		http.Error(w, "code expired or already used", 400)
		return
	}
	w.WriteHeader(204)
}
func (h *authHandler) devicePoll(w http.ResponseWriter, r *http.Request) {
	if h.repo == nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	if !checkOrigin(w, r) {
		return
	}
	var body struct {
		Code string `json:"device_code"`
	}
	if !readSmallJSON(w, r, &body) {
		return
	}
	if len(body.Code) != 64 {
		http.Error(w, "invalid code", 400)
		return
	}
	token := randomHex(32)
	status, err := h.repo.ClaimDeviceLink(r.Context(), deviceHash(body.Code), token, sessionTTL)
	if err != nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	if status == "approved" {
		setSessionCookie(w, token, h.cookieSecure(r))
	}
	w.Header().Set("Cache-Control", "no-store")
	writePersonalJSON(w, map[string]string{"status": status})
}
