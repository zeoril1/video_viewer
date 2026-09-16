package iptv

// basekey_test.go — проверки упрощённого ключа сопоставления с программой.

import "testing"

func TestBaseKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// реальные варианты одного канала из трёх источников
		{"MatchTV.ru@SD", "matchtv"},
		{"Матч ТВ.ru", "матчтв"},
		{"Матч ТВ (1080p)", "матчтв"},
		{"2x2.ru@SD", "2x2"},
		{"Channel5.ru@SD", "channel5"},
		{"5 Канал", "5канал"},
		{"RUTV.ru@SD", "rutv"},
		// «.TV» — не домен: дальше идут пробел и скобки
		{"RU.TV (1080p)", "rutv"},
		{"Россия 1 HD", "россия1"},
		// отброс: слишком короткий ключ даёт ложные совпадения
		{".ru", ""},
		{"", ""},
		{"ТВ", ""},
	}
	for _, c := range cases {
		if got := BaseKey(c.in); got != c.want {
			t.Errorf("BaseKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
