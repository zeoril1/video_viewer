package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// Browse filters TMDB without downloading its complete catalogue.
func (c *Client) Browse(ctx context.Context, media string, q url.Values) ([]Film, int, error) {
	q.Set("language", "ru-RU")
	q.Set("include_adult", "false")
	body, err := c.get(ctx, "/discover/"+media, q)
	if err != nil {
		return nil, 0, err
	}
	return c.decodeFilms(ctx, body, media)
}
func (c *Client) decodeFilms(ctx context.Context, body []byte, media string) ([]Film, int, error) {
	var p struct {
		Results    []multiResult `json:"results"`
		TotalPages int           `json:"total_pages"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, 0, err
	}
	for i := range p.Results {
		p.Results[i].MediaType = media
	}
	ids := c.externalIDs(ctx, p.Results)
	out := []Film{}
	for i, r := range p.Results {
		if media == "tv" {
			out = append(out, filmFrom(r.ID, ids[i], r.OriginalName, r.Name, r.Overview, r.FirstAirDate, r.GenreIDs, r.VoteAverage, r.VoteCount, r.PosterPath, "tvSeries"))
		} else {
			out = append(out, filmFrom(r.ID, ids[i], r.OriginalTitle, r.Title, r.Overview, r.ReleaseDate, r.GenreIDs, r.VoteAverage, r.VoteCount, r.PosterPath, "feature"))
		}
	}
	return out, p.TotalPages, nil
}

type ReleaseEpisode struct {
	Name    string `json:"name"`
	Date    string `json:"air_date"`
	Season  int    `json:"season_number"`
	Episode int    `json:"episode_number"`
}

// EpisodeCalendar loads overlapping seasons, including a season that started before this month.
func (c *Client) EpisodeCalendar(ctx context.Context, id int64, from, to string) ([]ReleaseEpisode, error) {
	body, err := c.get(ctx, fmt.Sprintf("/tv/%d", id), url.Values{"language": {"ru-RU"}})
	if err != nil {
		return nil, err
	}
	var d struct {
		Seasons []struct {
			Number int    `json:"season_number"`
			Date   string `json:"air_date"`
		} `json:"seasons"`
	}
	if err = json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	out := []ReleaseEpisode{}
	for i, s := range d.Seasons {
		if s.Number == 0 || s.Date > to {
			continue
		}
		// Only skip an old season when a subsequent season already started before the window.
		if i+1 < len(d.Seasons) && d.Seasons[i+1].Date != "" && d.Seasons[i+1].Date < from {
			continue
		}
		b, e := c.get(ctx, fmt.Sprintf("/tv/%d/season/%d", id, s.Number), url.Values{"language": {"ru-RU"}})
		if e != nil {
			return nil, e
		}
		var p struct {
			Episodes []ReleaseEpisode `json:"episodes"`
		}
		if e = json.Unmarshal(b, &p); e != nil {
			return nil, e
		}
		for _, ep := range p.Episodes {
			if ep.Date >= from && ep.Date <= to {
				out = append(out, ep)
			}
		}
	}
	return out, nil
}

type Explore struct {
	Similar []Film `json:"-"`
	Cast    []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"cast"`
	Directors []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Job  string `json:"job"`
	} `json:"directors"`
	Trailer string `json:"trailer"`
}

func (c *Client) Explore(ctx context.Context, id int64, media string) (Explore, error) {
	var out Explore
	path := "/" + media + "/" + strconv.FormatInt(id, 10)
	b, err := c.get(ctx, path, url.Values{"language": {"ru-RU"}, "append_to_response": {"recommendations,credits,videos"}})
	if err != nil {
		return out, err
	}
	var raw struct {
		Recommendations json.RawMessage `json:"recommendations"`
		Credits         struct {
			Cast json.RawMessage `json:"cast"`
			Crew json.RawMessage `json:"crew"`
		} `json:"credits"`
		Videos struct {
			Results []struct {
				Key, Site, Type string
				Official        bool
			}
		} `json:"videos"`
	}
	if err = json.Unmarshal(b, &raw); err != nil {
		return out, err
	}
	out.Similar, _, err = c.decodeFilms(ctx, raw.Recommendations, media)
	if err != nil {
		return out, err
	}
	_ = json.Unmarshal(raw.Credits.Cast, &out.Cast)
	_ = json.Unmarshal(raw.Credits.Crew, &out.Directors)
	directors := out.Directors[:0]
	for _, p := range out.Directors {
		if p.Job == "Director" || p.Job == "Series Director" {
			directors = append(directors, p)
		}
	}
	out.Directors = directors
	if len(out.Cast) > 8 {
		out.Cast = out.Cast[:8]
	}
	for _, v := range raw.Videos.Results {
		if v.Site == "YouTube" && v.Type == "Trailer" {
			out.Trailer = "https://www.youtube.com/watch?v=" + url.QueryEscape(v.Key)
			if v.Official {
				break
			}
		}
	}
	return out, nil
}
