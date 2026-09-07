package streamapi

import "testing"

func TestParseRange(t *testing.T) {
	cases := []struct {
		rng        string
		size       int64
		start, end int64
		ok         bool
	}{
		{"bytes=0-99", 1000, 0, 99, true},
		{"bytes=500-", 1000, 500, 999, true},
		{"bytes=-100", 1000, 900, 999, true},
		{"bytes=0-0", 1, 0, 0, true},
		{"bytes=-150", 100, 0, 99, true}, // N больше файла — весь файл
		{"bytes=5-2", 100, 0, 0, false},  // end < start
		{"bytes=abc", 100, 0, 0, false},  // нет дефиса
		{"", 100, 0, 0, false},
		{"bytes=100-200", 50, 0, 0, false}, // start >= size
		{"bytes=-0", 100, 0, 0, false},     // N = 0
		{"bytes=1-2-3", 100, 0, 0, false},  // лишний дефис
	}
	for _, c := range cases {
		start, end, ok := parseRange(c.rng, c.size)
		if ok != c.ok || (ok && (start != c.start || end != c.end)) {
			t.Errorf("parseRange(%q, %d) = (%d, %d, %v), want (%d, %d, %v)",
				c.rng, c.size, start, end, ok, c.start, c.end, c.ok)
		}
	}
}
