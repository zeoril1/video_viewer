package iptv

import "testing"

// TestCountryRU — русское название страны по коду из id канала; незнакомый
// код отдаём как есть (он всё равно различает каналы).
func TestCountryRU(t *testing.T) {
	cases := map[string]string{
		"BG":  "Болгария",
		"CZ":  "Чехия",
		"us":  "США",
		" UK": "Великобритания",
		"RU":  "Россия",
		"ZZ":  "ZZ",
		"":    "",
	}
	for code, want := range cases {
		if got := CountryRU(code); got != want {
			t.Errorf("CountryRU(%q) = %q, ждали %q", code, got, want)
		}
	}
}

// TestFeedTag — вариант вещания из @-пометки id: пометки качества (в том
// числе слипшиеся с лентой) вариантом не считаются, сдвиг по времени
// записывается как «+N».
func TestFeedTag(t *testing.T) {
	cases := map[string]string{
		"NationalGeographic.us@East":        "East",
		"NationalGeographic.us@HDEast":      "East",
		"NationalGeographic.us@Panregional": "Panregional",
		"Perviy.ru@Plus4":                   "+4",
		"Perviy.ru@plus12":                  "+12",
		"NationalGeographic.bg@SD":          "",
		"NationalGeographic.nl@HD":          "",
		"LoveNature.ca@4K":                  "",
		"Channel@1080p":                     "",
		"NationalGeographic.ru":             "",
		"MoyaPlaneta.ru@SD":                 "",
	}
	for id, want := range cases {
		if got := FeedTag(id); got != want {
			t.Errorf("FeedTag(%q) = %q, ждали %q", id, got, want)
		}
	}
}

// TestChannelQualifier — уточнение к названию: страна вещания, а если она
// неизвестна или это Россия — вариант вещания (например, сдвиг по времени).
func TestChannelQualifier(t *testing.T) {
	cases := []struct {
		id, name, country, want string
	}{
		{"NationalGeographic.bg@SD", "National Geographic", "BG", "Болгария"},
		{"NationalGeographic.ru@SD", "National Geographic Россия", "RU", ""},
		{"Perviy.ru@Plus4", "Первый канал", "RU", "+4"},
		{"Perviy.ru@Plus4", "Первый канал +4", "RU", ""}, // вариант уже в названии
		{"Perviy.ru@SD", "Первый канал", "RU", ""},
		{"SomeChannel@East", "Канал", "", "East"}, // страны нет — различаем лентой
		{"SomeChannel", "Канал", "", ""},
	}
	for _, c := range cases {
		if got := ChannelQualifier(c.id, c.name, c.country); got != c.want {
			t.Errorf("ChannelQualifier(%q, %q, %q) = %q, ждали %q",
				c.id, c.name, c.country, got, c.want)
		}
	}
}
