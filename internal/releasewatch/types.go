// Package releasewatch discovers available releases independently of premiere dates.
package releasewatch

import (
	"context"
	"encoding/json"
	"fmt"
)

type Subscription struct {
	FilmID string
	Data   json.RawMessage
}

// Variant identifies content rather than a tracker upload or torrent hash.
// Voice describes the translation declared by the release, not a probed track.
type Variant struct {
	Season     int    `json:"season"`
	Episode    int    `json:"episode"`
	Format     string `json:"format"`
	Resolution int    `json:"resolution"`
	Voice      string `json:"voice"`
}

func (v Variant) Key() string {
	return fmt.Sprintf("%d:%d:%s:%d:%s", v.Season, v.Episode, normalizeFormat(v.Format), v.Resolution, normalizeVoice(v.Voice))
}

type Discovery interface {
	Scan(context.Context, Subscription) ([]Variant, error)
}
