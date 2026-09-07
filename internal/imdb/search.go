package imdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// suggestionResponse — ответ эндпоинта автодополнения IMDb.
type suggestionResponse struct {
	D []suggestionItem `json:"d"`
}

type suggestionItem struct {
	ID string `json:"id"` // tt..., nm... и т.п.
	L  string `json:"l"`  // название
	Y  int    `json:"y"`  // год
	Q  string `json:"q"`  // тип: feature, tvSeries, ...
	I  *struct {
		ImageURL string `json:"imageUrl"`
	} `json:"i"`
}

// Search ищет фильмы и сериалы по запросу через собственный
// JSON-эндпоинт автодополнения IMDb (без API-ключа).
func (c *Client) Search(ctx context.Context, q string) ([]Film, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, fmt.Errorf("imdb: empty search query")
	}

	// Путь эндпоинта: /suggestion/{первая буква}/{запрос}.json
	letter := "x"
	if r, _ := utf8.DecodeRuneInString(strings.ToLower(q)); r != utf8.RuneError {
		letter = string(r)
	}
	encoded := url.PathEscape(q)
	u := fmt.Sprintf(suggestionAPI, letter, encoded)

	data, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}

	var resp suggestionResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("imdb: decode suggestion: %w", err)
	}

	var films []Film
	for _, it := range resp.D {
		if !strings.HasPrefix(it.ID, "tt") || it.L == "" {
			continue
		}
		f := Film{IMDBID: it.ID, Title: it.L, Year: it.Y, Kind: NormalizeKind(it.Q)}
		if it.I != nil {
			f.PosterURL = it.I.ImageURL
		}
		films = append(films, f)
	}
	return films, nil
}

// normalizeID приводит IMDb ID к виду "ttNNNN..." (убирает /title/ и /).
func normalizeID(id string) string {
	id = strings.TrimPrefix(id, "/title/")
	id = strings.TrimSuffix(id, "/")
	return id
}

// getBasic возвращает базовые данные фильма (id, название, год, постер)
// через suggestion API IMDb. Если точного совпадения нет — ошибка.
func (c *Client) getBasic(ctx context.Context, id string) (Film, error) {
	id = normalizeID(id)
	if !strings.HasPrefix(id, "tt") {
		return Film{}, fmt.Errorf("imdb: invalid title id %q", id)
	}

	films, err := c.Search(ctx, id)
	if err != nil {
		return Film{}, err
	}
	for _, f := range films {
		if f.IMDBID == id {
			return f, nil
		}
	}
	return Film{}, fmt.Errorf("imdb: title %s not found", id)
}
