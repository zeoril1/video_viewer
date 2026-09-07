package imdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	// suggestionAPI — собственный JSON-эндпоинт автодополнения IMDb,
	// работает без ключа и используется сайтом для поиска.
	suggestionAPI = "https://v2.sg.media-imdb.com/suggestion/%s/%s.json"

	// userAgent — IMDb отдаёт страницы не-браузерным клиентам редко,
	// поэтому представляемся обычным браузером.
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

	// maxBody — ограничение на размер загружаемого тела (8 МБ).
	maxBody = 8 << 20
)

// Config — параметры клиента IMDb.
type Config struct {
	// Scrape — разрешает парсинг официальных страниц чартов IMDb.
	// Работает там, где IMDb не блокирует автоматические запросы.
	Scrape bool

	// Top250URL / PopularURL — URL зеркал чартов (JSON-массивы),
	// используемые как фолбэк, если скрапер недоступен или пуст.
	// Пустая строка означает «не использовать зеркало».
	Top250URL  string
	PopularURL string

	// Timeout — таймаут HTTP-запросов.
	Timeout time.Duration

	// Transport — необязательный http.RoundTripper (например, пул прокси
	// из internal/proxy). Если nil — используется стандартный транспорт.
	Transport http.RoundTripper
}

// Client — HTTP-клиент IMDb.
type Client struct {
	hc  *http.Client
	cfg Config
}

// NewClient создаёт клиент IMDb.
func NewClient(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	hc := &http.Client{Timeout: cfg.Timeout}
	if cfg.Transport != nil {
		hc.Transport = cfg.Transport
	}
	return &Client{
		hc:  hc,
		cfg: cfg,
	}
}

// get выполняет GET-запрос с повторами при транзиентных сетевых ошибках
// (обрывы соединения, rate-limit 429). Ошибки со стабильными статусами
// (202, 404 и т.п.) не повторяются.
func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * 500 * time.Millisecond
			if ra, ok := rateLimitDelay(lastErr); ok && ra > delay {
				delay = ra
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		data, retryable, err := c.getOnce(ctx, u)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !retryable {
			break
		}
	}
	return nil, lastErr
}

func (c *Client) getOnce(ctx context.Context, u string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, true, err // сетевая ошибка — можно повторить
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		// Rate limit: повторяем с паузой, уважая Retry-After.
		return nil, true, &statusError{
			msg:        fmt.Sprintf("imdb: GET %s: 429 too many requests", u),
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("imdb: GET %s: %s", u, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, true, err // обрыв тела — можно повторить
	}
	return data, false, nil
}

// statusError — ошибка HTTP с необязательной задержкой повторного запроса.
type statusError struct {
	msg        string
	retryAfter time.Duration
}

func (e *statusError) Error() string { return e.msg }

// rateLimitDelay возвращает задержку из ошибки rate-limit, если она есть.
func rateLimitDelay(err error) (time.Duration, bool) {
	var se *statusError
	if errors.As(err, &se) && se.retryAfter > 0 {
		return se.retryAfter, true
	}
	return 0, false
}

// parseRetryAfter разбирает заголовок Retry-After (секунды).
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}
