package imdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// seasonQID — элемент Wikidata «телевизионный сезон» (значение P31),
// которым помечены сезоны сериалов, перечисленные через P527.
const seasonQID = "Q3464665"

// Seasons возвращает число сезонов телесериала по IMDb ID.
// Источник — Wikidata Query Service (SPARQL): у сериалов сезоны
// перечислены свойством P527 («состоит из»), у каждого сезона тип
// P31 = Q3464665 («телевизионный сезон»). Возвращает 0, если число
// определить не удалось (не сериал либо нет данных в Wikidata).
func (c *Client) Seasons(ctx context.Context, imdbID string) (int, error) {
	id := normalizeID(imdbID)
	// IMDb ID состоит из "tt" + цифр — экранируем от спецсимволов,
	// чтобы строка не сломала SPARQL-литерал.
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", r) {
			return 0, fmt.Errorf("imdb: invalid title id %q", id)
		}
	}

	query := `SELECT (COUNT(?season) AS ?n) WHERE {
  ?item wdt:P345 "` + id + `" .
  ?item wdt:P527 ?season .
  ?season wdt:P31 wd:` + seasonQID + ` .
}`
	data, err := c.postSparql(ctx, query)
	if err != nil {
		return 0, err
	}

	var sr struct {
		Results struct {
			Bindings []struct {
				N struct{ Value string } `json:"n"`
			} `json:"bindings"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &sr); err != nil {
		return 0, err
	}
	if len(sr.Results.Bindings) == 0 {
		return 0, nil
	}
	n, err := strconv.Atoi(sr.Results.Bindings[0].N.Value)
	if err != nil {
		return 0, err
	}
	return n, nil
}
