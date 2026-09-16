// Package catalog описывает каталог торрент-записей для стриминга через веб-интерфейс.
package catalog

import (
	"encoding/json"
	"os"
)

// Item описывает одну запись каталога.
type Item struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Magnet   string `json:"magnet"`
	Poster   string `json:"poster,omitempty"`
	Category string `json:"category,omitempty"`
	Size     string `json:"size,omitempty"` // человекочитаемая подсказка размера
	AddedAt  string `json:"added_at,omitempty"`
}

// Catalog — список записей.
type Catalog struct {
	Items []Item `json:"items"`
}

// Load читает каталог из JSON-файла.
func Load(path string) (*Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var c Catalog
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Get возвращает запись по ID.
func (c *Catalog) Get(id string) (Item, bool) {
	for _, it := range c.Items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}
