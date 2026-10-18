package releasewatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		Text string `json:"text"`
		From struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"from"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	} `json:"message"`
}

type Bot interface {
	Username(context.Context) (string, error)
	Updates(context.Context, int64) ([]Update, error)
	Send(context.Context, int64, string) error
}

type APIError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return fmt.Sprintf("telegram API error %d", e.Code) }

// The token only enters the outgoing HTTPS path. It never reaches errors,
// frontend responses or logs, including when net/http reports a URL error.
type TelegramClient struct {
	Token    string
	BaseURL  string // Test server override; production leaves this empty.
	Client   *http.Client
	mu       sync.Mutex
	username string
}

var botUsername = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)

func (b *TelegramClient) call(ctx context.Context, method string, params any, result any) error {
	base := strings.TrimRight(b.BaseURL, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	data, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram: invalid request")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/bot"+b.Token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("telegram: invalid endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 40 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("telegram %s request failed", method)
	}
	defer response.Body.Close()
	var envelope struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		Code       int             `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("telegram %s returned invalid JSON (HTTP %d)", method, response.StatusCode)
	}
	if response.StatusCode != http.StatusOK || !envelope.OK {
		code := envelope.Code
		if code == 0 {
			code = response.StatusCode
		}
		return &APIError{Code: code, RetryAfter: time.Duration(max(0, envelope.Parameters.RetryAfter)) * time.Second}
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("telegram %s returned invalid result", method)
		}
	}
	return nil
}

func (b *TelegramClient) CachedUsername() string { b.mu.Lock(); defer b.mu.Unlock(); return b.username }
func (b *TelegramClient) Username(ctx context.Context) (string, error) {
	if username := b.CachedUsername(); username != "" {
		return username, nil
	}
	var me struct {
		Username string `json:"username"`
		IsBot    bool   `json:"is_bot"`
	}
	if err := b.call(ctx, "getMe", struct{}{}, &me); err != nil {
		return "", err
	}
	if !me.IsBot || !botUsername.MatchString(me.Username) {
		return "", fmt.Errorf("telegram: invalid bot identity")
	}
	b.mu.Lock()
	b.username = me.Username
	b.mu.Unlock()
	return me.Username, nil
}
func (b *TelegramClient) Updates(ctx context.Context, offset int64) ([]Update, error) {
	var updates []Update
	err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "limit": 100, "allowed_updates": []string{"message"}}, &updates)
	return updates, err
}
func (b *TelegramClient) Send(ctx context.Context, chatID int64, text string) error {
	return b.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text, "link_preview_options": map[string]any{"is_disabled": true}}, nil)
}
