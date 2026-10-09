package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
)

// EpisodeInfo is canonical metadata, independent of a particular release or torrent.
type EpisodeInfo struct {
	Episode  int    `json:"episode"`
	Name     string `json:"name,omitempty"`
	AirDate  string `json:"air_date,omitempty"`
	Overview string `json:"overview,omitempty"`
	Runtime  int    `json:"runtime,omitempty"`
}

// SeasonEpisodes fetches only the selected season, rather than every season of a series.
func (c *Client) SeasonEpisodes(ctx context.Context, id int64, season int) ([]EpisodeInfo, error) {
	if id <= 0 || season < 0 {
		return nil, fmt.Errorf("tmdb: invalid season query")
	}
	body, err := c.get(ctx, fmt.Sprintf("/tv/%d/season/%d", id, season), url.Values{"language": {"ru-RU"}})
	if err != nil {
		return nil, err
	}
	var result struct {
		Episodes []struct {
			Episode  int    `json:"episode_number"`
			Name     string `json:"name"`
			AirDate  string `json:"air_date"`
			Overview string `json:"overview"`
			Runtime  int    `json:"runtime"`
		} `json:"episodes"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("tmdb: parse episodes %d/%d: %w", id, season, err)
	}
	out := make([]EpisodeInfo, 0, len(result.Episodes))
	seen := make(map[int]bool, len(result.Episodes))
	for _, ep := range result.Episodes {
		if ep.Episode <= 0 || seen[ep.Episode] {
			continue
		}
		seen[ep.Episode] = true
		out = append(out, EpisodeInfo{Episode: ep.Episode, Name: ep.Name, AirDate: ep.AirDate, Overview: ep.Overview, Runtime: ep.Runtime})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Episode < out[j].Episode })
	return out, nil
}
