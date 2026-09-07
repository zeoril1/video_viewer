package imdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// wdqsURL — Wikidata Query Service (SPARQL). Отдельный сервис от
// www.wikidata.org/w/api.php, позволяет получать данные пакетно.
const wdqsURL = "https://query.wikidata.org/sparql"

// postSparql выполняет SPARQL-запрос к WDQS с повторами при
// транзиентных ошибках (обрыв сети, 429, 5xx).
func (c *Client) postSparql(ctx context.Context, query string) ([]byte, error) {
	form := url.Values{}
	form.Set("query", query)

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * 800 * time.Millisecond
			if ra, ok := rateLimitDelay(lastErr); ok && ra > delay {
				delay = ra
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, wdqsURL+"?format=json", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/sparql-results+json")

		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = err // сетевая ошибка — повторяем
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = &statusError{
				msg:        "imdb: WDQS: 429 too many requests",
				retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			}
			continue
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("imdb: WDQS: %s", resp.Status)
			continue // транзиентная ошибка сервера — повторяем
		case resp.StatusCode == http.StatusOK && readErr == nil:
			return body, nil
		default:
			return nil, fmt.Errorf("imdb: WDQS: %s", resp.Status)
		}
	}
	return nil, lastErr
}

// LocalizedTitle — русское название фильма и заголовок его статьи
// в русской Википедии (для последующего получения описания).
type LocalizedTitle struct {
	TitleRU     string
	RuWikiTitle string
}

// LocalizeTitles пакетно получает русские названия (и заголовки статей
// ru-wiki) для списка IMDb ID. Один SPARQL-запрос заменяет сотни
// поштучных обращений к API Wikidata.
func (c *Client) LocalizeTitles(ctx context.Context, imdbIDs []string) (map[string]LocalizedTitle, error) {
	out := make(map[string]LocalizedTitle)
	const chunk = 100
	for i := 0; i < len(imdbIDs); i += chunk {
		end := min(i+chunk, len(imdbIDs))
		res, err := c.localizeTitlesBatch(ctx, imdbIDs[i:end])
		if err != nil {
			return out, err
		}
		for k, v := range res {
			out[k] = v
		}
	}
	return out, nil
}

func (c *Client) localizeTitlesBatch(ctx context.Context, imdbIDs []string) (map[string]LocalizedTitle, error) {
	values := make([]string, 0, len(imdbIDs))
	for _, id := range imdbIDs {
		values = append(values, `"`+id+`"`)
	}
	query := `SELECT ?imdb ?ruLabel ?ruTitle WHERE {
  VALUES ?imdb { ` + strings.Join(values, " ") + ` }
  ?item wdt:P345 ?imdb .
  OPTIONAL { ?item rdfs:label ?ruLabel . FILTER(LANG(?ruLabel) = "ru") }
  OPTIONAL { ?ruArticle schema:about ?item ; schema:isPartOf <https://ru.wikipedia.org/> ; schema:name ?ruTitle . }
}`

	data, err := c.postSparql(ctx, query)
	if err != nil {
		return nil, err
	}

	var sr struct {
		Results struct {
			Bindings []struct {
				IMDB    struct{ Value string } `json:"imdb"`
				RuLabel struct{ Value string } `json:"ruLabel"`
				RuTitle struct{ Value string } `json:"ruTitle"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &sr); err != nil {
		return nil, err
	}

	out := make(map[string]LocalizedTitle, len(sr.Results.Bindings))
	for _, b := range sr.Results.Bindings {
		if b.IMDB.Value == "" {
			continue
		}
		out[b.IMDB.Value] = LocalizedTitle{TitleRU: b.RuLabel.Value, RuWikiTitle: b.RuTitle.Value}
	}
	return out, nil
}

// PlotFromRuWiki возвращает вступительный абзац статьи русской Википедии
// (по заголовку, полученному из WDQS). Используется фоновым локализатором.
func (c *Client) PlotFromRuWiki(ctx context.Context, ruWikiTitle string) (string, error) {
	return c.wikiExtract(ctx, wikipediaRUAPI, ruWikiTitle)
}
