// Package wikidata — данные о фильмах из Wikidata (WDQS SPARQL) без ключей API.
package wikidata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// wdqsURL — Wikidata Query Service (SPARQL): пакетные запросы,
// в отличие от www.wikidata.org/w/api.php.
const wdqsURL = "https://query.wikidata.org/sparql"

const userAgent = "video_viewer/1.0"

// Единицы измерения длительности (P2047) в Wikidata.
const (
	unitSecond = "http://www.wikidata.org/entity/Q11574" // секунда
	unitMinute = "http://www.wikidata.org/entity/Q7727"  // минута
)

// Enrichment — расширенные данные карточки фильма из Wikidata.
type Enrichment struct {
	// Duration — длительность в минутах (0 — данных нет).
	Duration int
	// Countries — страны производства (русские названия).
	Countries []string
	Director  string
	// Actors — главные роли (до 6 актёров из P161).
	Actors []string
}

// Enrich запрашивает данные по набору IMDb-ссылок одним SPARQL-запросом:
// карта imdbID → Enrichment (только найденные в Wikidata).
func Enrich(ctx context.Context, imdbIDs []string) (map[string]Enrichment, error) {
	ids := make([]string, 0, len(imdbIDs))
	for _, id := range imdbIDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	vals := make([]string, len(ids))
	for i, id := range ids {
		// IMDb id — "tt"+цифры, безопасен для SPARQL-литерала.
		vals[i] = `"` + id + `"`
	}

	// Один запрос на все id. Возможен декартов обход (item × страна ×
	// режиссёр × актёр) — агрегируем по imdb в Go, актёров ограничиваем.
	query := `SELECT ?imdb ?dur ?unit ?countryLabel ?directorLabel ?actorLabel WHERE {
  VALUES ?imdb { ` + strings.Join(vals, " ") + ` }
  ?item wdt:P345 ?imdb .
  OPTIONAL { ?item wdt:P2047 ?dur }
  OPTIONAL { ?item p:P2047 ?st . ?st wikibase:quantityUnit ?unit }
  OPTIONAL { ?item wdt:P495 ?country }
  OPTIONAL { ?item wdt:P57 ?director }
  OPTIONAL { ?item wdt:P161 ?actor }
  SERVICE wikibase:label { bd:serviceParam wikibase:language "ru,en". }
}`

	data, err := postSparql(ctx, query)
	if err != nil {
		return nil, err
	}

	var sr struct {
		Results struct {
			Bindings []struct {
				IMDB     struct{ Value string } `json:"imdb"`
				Dur      struct{ Value string } `json:"dur"`
				Unit     struct{ Value string } `json:"unit"`
				Country  struct{ Value string } `json:"countryLabel"`
				Director struct{ Value string } `json:"directorLabel"`
				Actor    struct{ Value string } `json:"actorLabel"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &sr); err != nil {
		return nil, err
	}

	const maxActors = 6
	out := make(map[string]Enrichment)
	for _, b := range sr.Results.Bindings {
		id := b.IMDB.Value
		e := out[id]
		if b.Dur.Value != "" {
			if d, err := parseDuration(b.Dur.Value, b.Unit.Value); err == nil && d > 0 {
				e.Duration = d
			}
		}
		if c := strings.TrimSpace(b.Country.Value); c != "" && !contains(e.Countries, c) {
			e.Countries = append(e.Countries, c)
		}
		if d := strings.TrimSpace(b.Director.Value); d != "" && e.Director == "" {
			e.Director = d
		}
		if a := strings.TrimSpace(b.Actor.Value); a != "" && len(e.Actors) < maxActors && !contains(e.Actors, a) {
			e.Actors = append(e.Actors, a)
		}
		out[id] = e
	}
	return out, nil
}

// parseDuration переводит P2047 в минуты: с единицей «секунда» — деление
// на 60; без единицы значение уже в минутах (так у большинства записей).
func parseDuration(value, unit string) (int, error) {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	if unit == unitSecond {
		f /= 60
	}
	m := int(math.Round(f))
	if m <= 0 {
		return 0, fmt.Errorf("bad duration %q", value)
	}
	return m, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// postSparql выполняет SPARQL-запрос к WDQS с повторами при
// транзиентных ошибках (обрыв сети, 429, 5xx).
func postSparql(ctx context.Context, query string) ([]byte, error) {
	form := url.Values{}
	form.Set("query", query)

	client := &http.Client{Timeout: 60 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 800 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, wdqsURL+"?format=json", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/sparql-results+json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("wikidata: WDQS: %d", resp.StatusCode)
			continue
		default:
			return nil, fmt.Errorf("wikidata: WDQS: %d: %s", resp.StatusCode, truncate(body))
		}
	}
	return nil, lastErr
}

func truncate(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
