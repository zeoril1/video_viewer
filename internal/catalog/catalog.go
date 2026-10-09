// Package catalog описывает торрент-запись для стриминга через веб-интерфейс.
package catalog

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
