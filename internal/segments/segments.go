// Package segments defines skip ranges in absolute media time (seconds).
package segments

import (
	"math"
	"sort"
)

const (
	Intro   = "intro"
	Recap   = "recap"
	Credits = "credits"
)

// Segment is a half-open range [Start, End). AutoSkip is permitted only for
// ranges tied to this exact file; external releases must remain suggestions.
type Segment struct {
	Type     string  `json:"type"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Source   string  `json:"source"`
	AutoSkip bool    `json:"auto_skip"`
}

// Validate rejects unknown kinds and non-finite, empty or out-of-file ranges.
// Unknown duration cannot safely bound a range, so it is rejected too.
func Validate(s Segment, duration float64) bool {
	return (s.Type == Intro || s.Type == Recap || s.Type == Credits) &&
		finite(duration) && duration > 0 && finite(s.Start) && finite(s.End) &&
		s.Start >= 0 && s.End > s.Start && s.End <= duration
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Normalize returns a sorted, validated copy. Exact duplicates from the same
// source are removed; separate credit blocks are kept to preserve extra scenes.
func Normalize(input []Segment, duration float64) []Segment {
	out := make([]Segment, 0, len(input))
	seen := make(map[Segment]bool)
	for _, s := range input {
		if Validate(s, duration) && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		if out[i].End != out[j].End {
			return out[i].End < out[j].End
		}
		return out[i].Type < out[j].Type
	})
	return out
}
