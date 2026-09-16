package imdb

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"strconv"
	"strings"
)

const (
	wikidataAPI    = "https://www.wikidata.org/w/api.php"
	wikipediaAPI   = "https://en.wikipedia.org/w/api.php"
	wikipediaRUAPI = "https://ru.wikipedia.org/w/api.php"
)

// GetByID возвращает фильм по IMDb ID с данными на двух языках.
// Порядок источников:
//  1. страница IMDb (JSON-LD) — английские данные;
//  2. suggestion API IMDb — база, когда IMDb блокирует;
//  3. Wikidata → Wikipedia — русское название и описания.
func (c *Client) GetByID(ctx context.Context, id string) (Film, error) {
	id = normalizeID(id)
	if !strings.HasPrefix(id, "tt") {
		return Film{}, errors.New("imdb: invalid title id " + id)
	}

	f := Film{IMDBID: id}

	if ld, err := c.titleJSONLD(ctx, id); err == nil {
		f = ld
	} else {
		base, err := c.getBasic(ctx, id)
		if err != nil {
			return Film{}, err
		}
		f.Title, f.Year, f.PosterURL = base.Title, base.Year, base.PosterURL
	}

	// Русские название и описание (Wikidata → Википедия).
	c.localize(ctx, &f)

	return f, nil
}

// localize заполняет русские название и описание, а также английское
// описание, если оно пустое; при отсутствии данных поля остаются пустыми.
func (c *Client) localize(ctx context.Context, f *Film) {
	qid, err := c.wikidataItem(ctx, f.IMDBID)
	if err != nil {
		return
	}
	ruTitle, enWiki, ruWiki, err := c.wikidataMeta(ctx, qid)
	if err != nil {
		return
	}

	if f.TitleRU == "" && ruTitle != "" {
		f.TitleRU = ruTitle
	}
	if f.Plot == "" && enWiki != "" {
		if plot, err := c.wikiExtract(ctx, wikipediaAPI, enWiki); err == nil {
			f.Plot = plot
		}
	}
	if f.PlotRU == "" && ruWiki != "" {
		if plot, err := c.wikiExtract(ctx, wikipediaRUAPI, ruWiki); err == nil {
			f.PlotRU = plot
		}
	}
}

// wikidataItem находит QID элемента Wikidata по IMDb ID (P345).
func (c *Client) wikidataItem(ctx context.Context, imdbID string) (string, error) {
	u := wikidataAPI + "?action=query&list=search&srnamespace=0&format=json&srlimit=1&srsearch=" +
		url.QueryEscape("haswbstatement:P345="+imdbID)
	data, err := c.get(ctx, u)
	if err != nil {
		return "", err
	}
	var sr struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(data, &sr); err != nil {
		return "", err
	}
	if len(sr.Query.Search) == 0 {
		return "", errors.New("wikidata: no item for " + imdbID)
	}
	return sr.Query.Search[0].Title, nil
}

// wikidataMeta возвращает русскую метку и заголовки статей en/ru-вики.
func (c *Client) wikidataMeta(ctx context.Context, qid string) (ruTitle, enWiki, ruWiki string, err error) {
	u := wikidataAPI + "?action=wbgetentities&props=sitelinks|labels&sitefilter=enwiki|ruwiki&languages=ru&format=json&ids=" +
		qid
	data, err := c.get(ctx, u)
	if err != nil {
		return "", "", "", err
	}
	var ent struct {
		Entities map[string]struct {
			Labels map[string]struct {
				Value string `json:"value"`
			} `json:"labels"`
			Sitelinks map[string]struct {
				Title string `json:"title"`
			} `json:"sitelinks"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(data, &ent); err != nil {
		return "", "", "", err
	}
	e, ok := ent.Entities[qid]
	if !ok {
		return "", "", "", errors.New("wikidata: entity " + qid + " not found")
	}
	if l, ok := e.Labels["ru"]; ok {
		ruTitle = l.Value
	}
	if l, ok := e.Sitelinks["enwiki"]; ok {
		enWiki = l.Title
	}
	if l, ok := e.Sitelinks["ruwiki"]; ok {
		ruWiki = l.Title
	}
	return ruTitle, enWiki, ruWiki, nil
}

// wikiExtract возвращает вступительный абзац статьи Википедии.
func (c *Client) wikiExtract(ctx context.Context, apiBase, title string) (string, error) {
	u := apiBase + "?action=query&prop=extracts&exintro&explaintext&format=json&titles=" +
		url.QueryEscape(title)
	data, err := c.get(ctx, u)
	if err != nil {
		return "", err
	}
	var ext struct {
		Query struct {
			Pages map[string]struct {
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(data, &ext); err != nil {
		return "", err
	}
	for _, p := range ext.Query.Pages {
		if strings.TrimSpace(p.Extract) != "" {
			// В экстракте встречаются сущности вида &apos;, &quot;.
			return html.UnescapeString(strings.TrimSpace(p.Extract)), nil
		}
	}
	return "", errors.New("wikipedia: no extract for " + title)
}

// titleJSONLD извлекает данные фильма из JSON-LD на странице IMDb.
func (c *Client) titleJSONLD(ctx context.Context, id string) (Film, error) {
	data, err := c.get(ctx, "https://www.imdb.com/title/"+id+"/")
	if err != nil {
		return Film{}, err
	}

	var doc struct {
		Type            string   `json:"@type"`
		Name            string   `json:"name"`
		URL             string   `json:"url"`
		Image           string   `json:"image"`
		Description     string   `json:"description"`
		Genre           []string `json:"genre"`
		DatePublished   string   `json:"datePublished"`
		NumberOfSeasons int      `json:"numberOfSeasons"`
		AggregateRating *struct {
			RatingValue float64 `json:"ratingValue"`
			RatingCount int     `json:"ratingCount"`
		} `json:"aggregateRating"`
	}

	m := jsonLDRe.FindSubmatch(data)
	if len(m) != 2 {
		return Film{}, errors.New("imdb: no json-ld on title page")
	}
	// В JSON-LD внутри HTML встречаются сущности вида &quot;, &#x27;.
	raw := html.UnescapeString(string(m[1]))
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return Film{}, err
	}
	if doc.Name == "" {
		return Film{}, errors.New("imdb: empty title in json-ld")
	}

	f := Film{
		IMDBID:    id,
		Title:     doc.Name,
		PosterURL: doc.Image,
		Plot:      doc.Description,
		Genres:    doc.Genre,
		Seasons:   doc.NumberOfSeasons,
	}
	if len(doc.DatePublished) >= 4 {
		if y, err := strconv.Atoi(doc.DatePublished[:4]); err == nil {
			f.Year = y
		}
	}
	f.ReleaseDate = strings.TrimSpace(doc.DatePublished)
	if doc.AggregateRating != nil {
		f.Rating = doc.AggregateRating.RatingValue
		f.Votes = doc.AggregateRating.RatingCount
	}
	return f, nil
}
