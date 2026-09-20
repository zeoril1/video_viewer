package iptv

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Xtream — доступ к панели Xtream Codes (player_api.php): категории, логотипы,
// epg_channel_id и готовая ссылка на XMLTV (альтернатива M3U-ссылке).
type Xtream struct {
	BaseURL  string // http://host:port (можно с /player_api.php — отрежем)
	Username string
	Password string
	// UA/Referer — необязательные заголовки (часть панелей их требует).
	UA      string
	Referer string
}

// normalizeBase приводит BaseURL к виду «схема://хост[:порт]» без хвостов.
func (x Xtream) normalizeBase() (string, error) {
	raw := strings.TrimSpace(x.BaseURL)
	if raw == "" {
		return "", fmt.Errorf("xtream: пустой адрес панели")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("xtream: некорректный адрес %q", x.BaseURL)
	}
	// Отрезаем /player_api.php, /get.php и query — оставляем только хост.
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

// apiURL собирает вызов player_api.php.
func (x Xtream) apiURL(ctx context.Context, action string) (string, error) {
	base, err := x.normalizeBase()
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("username", x.Username)
	q.Set("password", x.Password)
	if action != "" {
		q.Set("action", action)
	}
	_ = ctx
	return base + "/player_api.php?" + q.Encode(), nil
}

// EPGURL возвращает ссылку на XMLTV панели (её можно подставить в epg_url).
func (x Xtream) EPGURL() (string, error) {
	base, err := x.normalizeBase()
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("username", x.Username)
	q.Set("password", x.Password)
	return base + "/xmltv.php?" + q.Encode(), nil
}

// xtreamCategory / xtreamStream — ответы player_api.php.
type xtreamCategory struct {
	ID   string `json:"category_id"`
	Name string `json:"category_name"`
}

type xtreamStream struct {
	ArchiveDays int    `json:"tv_archive_duration"`
	Archive     int    `json:"tv_archive"`
	Num         int    `json:"num"`
	Name        string `json:"name"`
	StreamID    int    `json:"stream_id"`
	Icon        string `json:"stream_icon"`
	EPGChannel  string `json:"epg_channel_id"`
	CategoryID  string `json:"category_id"`
}

// LiveChannels забирает категории и live-каналы панели.
func (x Xtream) LiveChannels(ctx context.Context, hc *http.Client) ([]Channel, error) {
	base, err := x.normalizeBase()
	if err != nil {
		return nil, err
	}
	var cats []xtreamCategory
	if err := x.getJSON(ctx, hc, "get_live_categories", &cats); err != nil {
		return nil, err
	}
	catName := make(map[string]string, len(cats))
	for _, c := range cats {
		catName[c.ID] = strings.TrimSpace(c.Name)
	}

	var streams []xtreamStream
	if err := x.getJSON(ctx, hc, "get_live_streams", &streams); err != nil {
		return nil, err
	}

	out := make([]Channel, 0, len(streams))
	for _, s := range streams {
		if s.StreamID == 0 {
			continue
		}
		// Отдаём HLS-вариант потока: браузер играет его без перепаковки.
		streamURL := fmt.Sprintf("%s/live/%s/%s/%d.m3u8", base, url.PathEscape(x.Username), url.PathEscape(x.Password), s.StreamID)
		ch := Channel{
			CatchupDays: s.ArchiveDays,
			CatchupMode: "xtream",
			ExtID:       strconv.Itoa(s.StreamID),
			Name:        strings.TrimSpace(s.Name),
			Group:       catName[s.CategoryID],
			Logo:        s.Icon,
			EPGID:       strings.TrimSpace(s.EPGChannel),
			URL:         streamURL,
			UA:          x.UA,
			Referer:     x.Referer,
			IsHLS:       true,
		}
		if s.Archive != 1 {
			ch.CatchupDays = 0
		}
		if ch.ExtID == "" {
			ch.ExtID = ch.Name
		}
		out = append(out, ch)
	}
	return out, nil
}

// getJSON делает запрос к player_api.php и разбирает JSON.
func (x Xtream) getJSON(ctx context.Context, hc *http.Client, action string, dst any) error {
	u, err := x.apiURL(ctx, action)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	setStreamHeaders(req, x.UA, x.Referer)
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("xtream %s: %w", action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("xtream %s: %s", action, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("xtream %s: %w", action, err)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("xtream %s: разбор ответа: %w", action, err)
	}
	return nil
}

// setStreamHeaders проставляет заголовки запроса к потоку/панели.
func setStreamHeaders(req *http.Request, ua, referer string) {
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
}
