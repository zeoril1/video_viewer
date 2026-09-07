// Package magnet — поиск магнет-ссылок для фильмов на торрент-трекерах.
package magnet

import "context"

// userAgent — браузерный User-Agent для запросов к трекеру/Jackett.
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// Result — найденная раздача на трекере.
type Result struct {
	Title  string
	Magnet string
	Size   string
	Seeds  int
}

// Provider — поисковик магнет-ссылок (например, через Jackett Torznab).
type Provider interface {
	Name() string
	Search(ctx context.Context, q string, limit int) ([]Result, error)
}
