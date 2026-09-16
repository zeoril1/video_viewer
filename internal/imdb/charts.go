package imdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// MirrorTop250Default — проверенное зеркало IMDb Top 250 (JSON,
// обновляется каждые несколько часов) на случай недоступности IMDb.
const MirrorTop250Default = "https://raw.githubusercontent.com/movie-monk-b0t/top250/master/top250_min.json"

func chartURL(kind ChartKind) string {
	if kind == ChartTop250 {
		return "https://www.imdb.com/chart/top/"
	}
	return "https://www.imdb.com/chart/moviemeter/"
}

func (c *Client) mirrorURL(kind ChartKind) string {
	if kind == ChartTop250 {
		return c.cfg.Top250URL
	}
	return c.cfg.PopularURL
}

// FetchChart возвращает список фильмов чарта (топ-250 или популярные):
// сначала скрапер IMDb (если разрешён), затем JSON-зеркало (если задано).
func (c *Client) FetchChart(ctx context.Context, kind ChartKind) ([]Film, error) {
	var lastErr error

	if c.cfg.Scrape {
		if films, err := c.scrapeChart(ctx, kind); err == nil && len(films) > 0 {
			return films, nil
		} else if err != nil {
			lastErr = err
		}
	}

	if u := c.mirrorURL(kind); u != "" {
		if films, err := c.mirrorChart(ctx, u); err == nil && len(films) > 0 {
			return films, nil
		} else if err != nil {
			lastErr = err
		}
	}

	if lastErr == nil {
		lastErr = errors.New("no chart source configured")
	}
	return nil, fmt.Errorf("imdb: chart %s: %w", kind, lastErr)
}

// ---- Официальный скрапер (JSON-LD) ----

var (
	jsonLDRe  = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)
	imdbIDRe  = regexp.MustCompile(`/title/(tt\d+)/`)
	htmlIDRe  = regexp.MustCompile(`/title/(tt\d{6,9})/`)
	htmlAltRe = regexp.MustCompile(`alt="([^"]{1,200})"`)
	posterRe  = regexp.MustCompile(`src="(https://m\.media-amazon\.com/images/[^"]+)"`)
)

// scrapeChart загружает страницу чарта IMDb и извлекает фильмы.
func (c *Client) scrapeChart(ctx context.Context, kind ChartKind) ([]Film, error) {
	data, err := c.get(ctx, chartURL(kind))
	if err != nil {
		return nil, err
	}

	films := parseChartJSONLD(data)
	if len(films) == 0 {
		films = parseChartHTML(data) // грубый фолбэк HTML-парсингом
	}
	if len(films) == 0 {
		return nil, errors.New("no entries parsed from chart page")
	}
	return films, nil
}

// JSON-LD структуры страниц чартов IMDb.
type ldDocument struct {
	Type            string       `json:"@type"`
	ItemListElement []ldListItem `json:"itemListElement"`
}

type ldListItem struct {
	Item ldItem `json:"item"`
}

type ldItem struct {
	URL             string    `json:"url"`
	Name            string    `json:"name"`
	Image           string    `json:"image"`
	DatePublished   string    `json:"datePublished"`
	Description     string    `json:"description"`
	Genre           []string  `json:"genre"`
	AggregateRating *ldRating `json:"aggregateRating"`
}

type ldRating struct {
	RatingValue float64 `json:"ratingValue"`
	RatingCount int     `json:"ratingCount"`
}

func parseChartJSONLD(data []byte) []Film {
	var films []Film
	for _, m := range jsonLDRe.FindAllSubmatch(data, -1) {
		// В JSON-LD внутри HTML встречаются сущности вида &quot;, &#x27;.
		raw := html.UnescapeString(string(m[1]))

		var doc ldDocument
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			continue
		}
		if doc.Type != "ItemList" {
			continue
		}

		for i, li := range doc.ItemListElement {
			it := li.Item
			if it.Name == "" {
				continue
			}
			idm := imdbIDRe.FindStringSubmatch(it.URL)
			if len(idm) != 2 {
				continue
			}

			f := Film{
				IMDBID:    idm[1],
				Title:     it.Name,
				Plot:      it.Description,
				Genres:    it.Genre,
				PosterURL: it.Image,
				Rank:      i + 1,
			}
			if len(it.DatePublished) >= 4 {
				if y, err := strconv.Atoi(it.DatePublished[:4]); err == nil {
					f.Year = y
				}
			}
			f.ReleaseDate = strings.TrimSpace(it.DatePublished)
			if it.AggregateRating != nil {
				f.Rating = it.AggregateRating.RatingValue
				f.Votes = it.AggregateRating.RatingCount
			}
			films = append(films, f)
		}
	}
	return films
}

// parseChartHTML — грубый фолбэк: попарно собирает ссылки /title/tt.../
// и alt-тексты постеров (в чартах они идут в одном порядке).
func parseChartHTML(data []byte) []Film {
	ids := htmlIDRe.FindAllSubmatch(data, -1)
	alts := htmlAltRe.FindAllSubmatch(data, -1)
	posters := posterRe.FindAllSubmatch(data, -1)

	n := len(ids)
	if len(alts) < n {
		n = len(alts)
	}

	var films []Film
	for i := 0; i < n; i++ {
		title := strings.TrimSpace(string(alts[i][1]))
		if title == "" {
			continue
		}
		f := Film{IMDBID: string(ids[i][1]), Title: title, Rank: i + 1}
		if i < len(posters) {
			f.PosterURL = string(posters[i][1])
		}
		films = append(films, f)
	}
	return films
}

// ---- Зеркало JSON ----

type mirrorEntry struct {
	Name    string   `json:"name"`
	Year    int      `json:"year"`
	Rating  float64  `json:"rating"`
	Desc    string   `json:"desc"`
	Genre   []string `json:"genre"`
	Image   string   `json:"image_url"`
	Thumb   string   `json:"thumb_url"`
	IMDBURL string   `json:"imdb_url"`
	ID      string   `json:"id"`
}

// mirrorChart загружает чарт из JSON-зеркала (name, year, rating, desc,
// genre, image_url, imdb_url).
func (c *Client) mirrorChart(ctx context.Context, u string) ([]Film, error) {
	data, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}

	var entries []mirrorEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("imdb: decode mirror %s: %w", u, err)
	}

	var films []Film
	for i, e := range entries {
		id := e.ID
		if id == "" {
			if m := imdbIDRe.FindStringSubmatch(e.IMDBURL); len(m) == 2 {
				id = m[1]
			}
		}
		if id == "" || e.Name == "" {
			continue
		}
		films = append(films, Film{
			IMDBID:    id,
			Title:     e.Name,
			Kind:      "feature", // в чартах только полнометражные фильмы
			Year:      e.Year,
			Rating:    e.Rating,
			Plot:      e.Desc,
			Genres:    e.Genre,
			PosterURL: e.Image,
			Rank:      i + 1,
		})
	}
	return films, nil
}
