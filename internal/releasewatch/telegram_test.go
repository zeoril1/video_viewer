package releasewatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTelegramClientUsesBotAPIAndCachesVerifiedIdentity(t *testing.T) {
	meCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("invalid request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/bottest-secret/getMe":
			meCalls++
			io.WriteString(w, `{"ok":true,"result":{"is_bot":true,"username":"kinoteka_bot"}}`)
		case "/bottest-secret/sendMessage":
			if body["chat_id"] != float64(123) || body["text"] != "Test <plain> & text" || body["parse_mode"] != nil {
				t.Errorf("bad message: %+v", body)
			}
			io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
		case "/bottest-secret/getUpdates":
			if body["offset"] != float64(42) || body["timeout"] != float64(25) {
				t.Errorf("bad polling: %+v", body)
			}
			io.WriteString(w, `{"ok":true,"result":[{"update_id":43}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	bot := &TelegramClient{Token: "test-secret", BaseURL: server.URL, Client: server.Client()}
	for range 2 {
		name, err := bot.Username(context.Background())
		if err != nil || name != "kinoteka_bot" {
			t.Fatal(name, err)
		}
	}
	if meCalls != 1 || bot.CachedUsername() != "kinoteka_bot" {
		t.Fatal("identity not cached")
	}
	if err := bot.Send(context.Background(), 123, "Test <plain> & text"); err != nil {
		t.Fatal(err)
	}
	updates, err := bot.Updates(context.Background(), 42)
	if err != nil || len(updates) != 1 || updates[0].ID != 43 {
		t.Fatal(updates, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramTransportErrorsNeverExposeToken(t *testing.T) {
	bot := &TelegramClient{Token: "never-log-this-token", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "POST", URL: r.URL.String(), Err: errors.New("connection refused")}
	})}}
	err := bot.Send(context.Background(), 1, "test")
	if err == nil || strings.Contains(err.Error(), bot.Token) || strings.Contains(err.Error(), "api.telegram.org") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestTelegramRateLimitCarriesRetryAfter(t *testing.T) {
	bot := &TelegramClient{Token: "x", Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":429,"parameters":{"retry_after":180}}`)), Header: make(http.Header)}, nil
	})}}
	var api *APIError
	if err := bot.Send(context.Background(), 1, "test"); !errors.As(err, &api) || api.Code != 429 || api.RetryAfter != 3*time.Minute {
		t.Fatalf("retry information lost: %v", err)
	}
}
