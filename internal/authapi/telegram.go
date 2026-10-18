package authapi

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

type TelegramProfile interface {
	Username(context.Context) (string, error)
}

var telegramUsername = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)

func (h *authHandler) telegram(bot TelegramProfile) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		u, ok := h.requireUser(w, r)
		if !ok {
			return
		}
		if r.Method != http.MethodGet && !checkOrigin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			binding, linked, err := h.repo.TelegramBinding(r.Context(), u.ID)
			if err != nil {
				http.Error(w, "database unavailable", 503)
				return
			}
			username := ""
			if cached, ok := bot.(interface{ CachedUsername() string }); ok {
				username = cached.CachedUsername()
			}
			writePersonalJSON(w, map[string]any{"configured": bot != nil, "linked": linked,
				"enabled": linked && binding.Enabled, "bot_username": username, "username": binding.Username, "check_interval_hours": 6})
		case http.MethodDelete:
			if err := h.repo.UnlinkTelegram(r.Context(), u.ID); err != nil {
				http.Error(w, "database unavailable", 503)
				return
			}
			w.WriteHeader(204)
		case http.MethodPut:
			var body struct {
				Enabled *bool `json:"enabled"`
			}
			if !readSmallJSON(w, r, &body) {
				return
			}
			if body.Enabled == nil {
				http.Error(w, "enabled must be boolean", 400)
				return
			}
			if bot == nil && *body.Enabled {
				http.Error(w, "telegram is not configured", 503)
				return
			}
			_, linked, err := h.repo.TelegramBinding(r.Context(), u.ID)
			if err != nil {
				http.Error(w, "database unavailable", 503)
				return
			}
			if !linked {
				http.Error(w, "telegram is not linked", 409)
				return
			}
			if err := h.repo.SetTelegramEnabled(r.Context(), u.ID, *body.Enabled); err != nil {
				http.Error(w, "database unavailable", 503)
				return
			}
			w.WriteHeader(204)
		}
	}
}

func (h *authHandler) telegramLink(bot TelegramProfile) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		u, ok := h.requireUser(w, r)
		if !ok {
			return
		}
		if !checkOrigin(w, r) {
			return
		}
		if bot == nil {
			http.Error(w, "telegram is not configured", 503)
			return
		}
		if !h.limiter.takeRegistration(clientKey(r, "telegram-link")) {
			http.Error(w, "try later", 429)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		username, err := bot.Username(ctx)
		if err != nil || !telegramUsername.MatchString(username) {
			http.Error(w, "telegram is temporarily unavailable", 503)
			return
		}
		// Telegram deep-link payloads allow up to 64 characters: 5+48 here.
		token := randomHex(24)
		if err := h.repo.CreateTelegramLink(ctx, u.ID, deviceHash(token), time.Now().Add(10*time.Minute)); err != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		writePersonalJSON(w, map[string]any{"url": "https://t.me/" + username + "?start=" + url.QueryEscape("link_"+token), "expires_in": 600})
	}
}
