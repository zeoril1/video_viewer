// Package imdb — клиент для получения данных о фильмах с IMDb:
// списки «топ-250» и «популярные сейчас», поиск и получение по IMDb ID.
package imdb

import "strings"

// NormalizeKind приводит тип контента IMDb к нормализованному виду
// (feature, tvSeries, tvMiniSeries, tvMovie, short, tvShort, video,
// tvEpisode, tvSpecial). Эндпоинт поиска отдаёт человекочитаемые
// строки вроде "TV series" или "Short".
func NormalizeKind(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "", "feature", "movie", "film":
		return "feature"
	case "tv series", "tvseries", "series":
		return "tvSeries"
	case "tv mini series", "tv mini-series", "tvminiseries", "tv-miniseries", "mini series", "mini-series", "miniseries":
		return "tvMiniSeries"
	case "tv movie", "tvmovie":
		return "tvMovie"
	case "short", "short film":
		return "short"
	case "tv short", "tvshort":
		return "tvShort"
	case "video":
		return "video"
	case "tv episode", "tvepisode", "episode":
		return "tvEpisode"
	case "tv special", "tvspecial":
		return "tvSpecial"
	default:
		return strings.ToLower(strings.TrimSpace(k))
	}
}

// Film — данные фильма из IMDb.
type Film struct {
	IMDBID      string   `json:"imdb_id"`
	Title       string   `json:"title"`              // английское название
	TitleRU     string   `json:"title_ru,omitempty"` // русское название (если есть)
	Kind        string   `json:"kind,omitempty"`     // тип: feature, tvSeries, tvMovie, short, ...
	Year        int      `json:"year,omitempty"`
	ReleaseDate string   `json:"release_date,omitempty"` // "YYYY-MM-DD" (если известна)
	Rating      float64  `json:"rating,omitempty"`
	Votes       int      `json:"votes,omitempty"`
	Plot        string   `json:"plot,omitempty"`    // английское описание
	PlotRU      string   `json:"plot_ru,omitempty"` // русское описание (если есть)
	Genres      []string `json:"genres,omitempty"`
	PosterURL   string   `json:"poster_url,omitempty"`
	// Rank — позиция в чарте (1-based). 0 для обычного поиска.
	Rank int `json:"rank,omitempty"`
	// Seasons — число сезонов (для сериалов; 0 — не определено/фильм).
	Seasons int `json:"seasons,omitempty"`
}

// ChartKind — тип чарта IMDb.
type ChartKind int

const (
	// ChartTop250 — «IMDb Top 250» (https://www.imdb.com/chart/top/).
	ChartTop250 ChartKind = iota
	// ChartPopular — «Most Popular» (https://www.imdb.com/chart/moviemeter/).
	ChartPopular
)

// String возвращает человекочитаемое имя чарта.
func (k ChartKind) String() string {
	switch k {
	case ChartTop250:
		return "top250"
	case ChartPopular:
		return "popular"
	}
	return "unknown"
}
