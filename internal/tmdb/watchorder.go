package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// UniverseOrder uses an explicit provider keyword, not title similarity or studio.
// The same keyword joins movies and TV series in the MCU.
func (c *Client) UniverseOrder(ctx context.Context, id int64, media string) (int64, []Film, bool, error) {
	body, err := c.get(ctx, fmt.Sprintf("/%s/%d/keywords", media, id), nil)
	if err != nil {
		return 0, nil, false, err
	}
	type keyword struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	var detail struct {
		Keywords []keyword `json:"keywords"`
		Results  []keyword `json:"results"`
	}
	if err = json.Unmarshal(body, &detail); err != nil {
		return 0, nil, false, err
	}
	var key int64
	for _, k := range append(detail.Keywords, detail.Results...) {
		if strings.ToLower(k.Name) == "marvel cinematic universe (mcu)" {
			key = k.ID
			break
		}
	}
	if key == 0 {
		return 0, nil, false, nil
	}
	films := []Film{}
	partial := false
	for _, kind := range []string{"movie", "tv"} {
		for page := 1; page <= 5; page++ {
			q := url.Values{"with_keywords": {strconv.FormatInt(key, 10)}, "language": {"ru-RU"}, "include_adult": {"false"}, "page": {strconv.Itoa(page)}}
			body, err = c.get(ctx, "/discover/"+kind, q)
			if err != nil {
				return 0, nil, false, err
			}
			var data struct {
				Results    []multiResult `json:"results"`
				TotalPages int           `json:"total_pages"`
			}
			if err = json.Unmarshal(body, &data); err != nil {
				return 0, nil, false, err
			}
			for _, p := range data.Results {
				if kind == "tv" {
					films = append(films, filmFrom(p.ID, "", p.OriginalName, p.Name, p.Overview, p.FirstAirDate, p.GenreIDs, p.VoteAverage, p.VoteCount, p.PosterPath, "tvSeries"))
				} else {
					films = append(films, filmFrom(p.ID, "", p.OriginalTitle, p.Title, p.Overview, p.ReleaseDate, p.GenreIDs, p.VoteAverage, p.VoteCount, p.PosterPath, "feature"))
				}
			}
			if page >= data.TotalPages {
				break
			}
			if page == 5 {
				partial = true
			}
		}
	}
	sort.SliceStable(films, func(i, j int) bool {
		a, b := films[i].ReleaseDate, films[j].ReleaseDate
		if a == b {
			return films[i].TMDBID < films[j].TMDBID
		}
		if a == "" {
			return false
		}
		if b == "" {
			return true
		}
		return a < b
	})
	return key, films, partial, nil
}

// CollectionOrder returns the provider's collection, ordered by release date.
// A collection is not necessarily an entire cinematic universe.
func (c *Client) CollectionOrder(ctx context.Context, id int64) (string, int64, []Film, error) {
	body, err := c.get(ctx, fmt.Sprintf("/movie/%d", id), url.Values{"language": {"ru-RU"}})
	if err != nil {
		return "", 0, nil, err
	}
	var detail struct {
		Collection *struct {
			ID int64 `json:"id"`
		} `json:"belongs_to_collection"`
	}
	if err = json.Unmarshal(body, &detail); err != nil {
		return "", 0, nil, err
	}
	if detail.Collection == nil || detail.Collection.ID <= 0 {
		return "", 0, []Film{}, nil
	}
	body, err = c.get(ctx, fmt.Sprintf("/collection/%d", detail.Collection.ID), url.Values{"language": {"ru-RU"}})
	if err != nil {
		return "", 0, nil, err
	}
	var collection struct {
		Name  string        `json:"name"`
		Parts []multiResult `json:"parts"`
	}
	if err = json.Unmarshal(body, &collection); err != nil {
		return "", 0, nil, err
	}
	films := []Film{}
	seen := map[int64]bool{}
	for _, p := range collection.Parts {
		if p.ID <= 0 || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		films = append(films, filmFrom(p.ID, "", p.OriginalTitle, p.Title, p.Overview, p.ReleaseDate, p.GenreIDs, p.VoteAverage, p.VoteCount, p.PosterPath, "feature"))
	}
	sort.SliceStable(films, func(i, j int) bool {
		a, b := films[i].ReleaseDate, films[j].ReleaseDate
		if a == b {
			return films[i].TMDBID < films[j].TMDBID
		}
		if a == "" {
			return false
		}
		if b == "" {
			return true
		}
		return a < b
	})
	return collection.Name, detail.Collection.ID, films, nil
}
