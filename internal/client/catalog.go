// Package client — HTTP-клиенты для межсервисного взаимодействия.
package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CatalogClient — клиент catalog-сервиса. Используется stream-сервисом
// для резолва магнет-ссылки по id записи (когда фронтенд не передал
// ?magnet= в /api/stream/{id}).
type CatalogClient struct {
	BaseURL string
	HTTP    *http.Client
}

// NewCatalogClient создаёт клиент. Пустой baseURL даёт nil-клиент,
// который всегда возвращает «не найдено» (резолв магнета выключен).
func NewCatalogClient(baseURL string) *CatalogClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	return &CatalogClient{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

// FindMagnet возвращает магнет-ссылку по id через внутренний эндпоинт
// catalog-сервиса /api/internal/films/{id}/magnet.
func (c *CatalogClient) FindMagnet(ctx context.Context, id string) (string, bool) {
	if c == nil {
		return "", false
	}
	u := c.BaseURL + "/api/internal/films/" + url.PathEscape(id) + "/magnet"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", false
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var out struct {
		Magnet string `json:"magnet"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false
	}
	return out.Magnet, out.Magnet != ""
}
