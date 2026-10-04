// people_test.go — чистые функции таблицы переводов имён (без БД): какие имена ещё нужно
// перевести, как подставляется перевод и как собираются имена карточки.
package db

import "testing"

func TestPeopleNeedTranslation(t *testing.T) {
	tr := map[string]string{
		"paul greengrass": "Пол Гринграсс",
		"andrew garfield": "Эндрю Гарфилд",
	}
	cases := []struct {
		name  string
		names []string
		want  bool
	}{
		{"оба имени известны", []string{"Paul Greengrass", "Andrew Garfield"}, false},
		{"одно имя новое", []string{"Paul Greengrass", "Jamie Bell"}, true},
		{"имена уже по-русски (Wikidata)", []string{"Пол Гринграсс", "Эндрю Гарфилд"}, false},
		{"смешанный алфавит — не трогаем", []string{"Пол Гринграсс", "Andrew Garfield"}, false},
		{"имён нет", nil, false},
		{"имя с лишними пробелами считается известным", []string{"  Paul Greengrass "}, false},
	}
	for _, c := range cases {
		if got := PeopleNeedTranslation(c.names, tr); got != c.want {
			t.Errorf("%s: PeopleNeedTranslation(%v) = %v, ожидалось %v", c.name, c.names, got, c.want)
		}
	}
}

func TestTranslatedName(t *testing.T) {
	// LocalizePeople возвращает карту с ключами в том написании, которое передали (в каталоге
	// это films.director/actors) — перевод находится и по оригиналу, и по русскому имени.
	if got := TranslatedName(map[string]string{"paul greengrass": "Пол Гринграсс"}, "Paul Greengrass"); got != "Пол Гринграсс" {
		t.Errorf("перевод по оригиналу: %q", got)
	}
	if got := TranslatedName(map[string]string{"пол гринграсс": "Пол Гринграсс"}, "Пол Гринграсс"); got != "Пол Гринграсс" {
		t.Errorf("русское написание остаётся собой: %q", got)
	}
	if got := TranslatedName(map[string]string{"paul greengrass": "Пол Гринграсс"}, "Неизвестный"); got != "Неизвестный" {
		t.Errorf("без перевода возвращается исходное имя: %q", got)
	}
}

func TestFilmPeopleNames(t *testing.T) {
	f := Film{Director: " Пол Гринграсс ", Actors: []string{"Эндрю Гарфилд", "", "  "}}
	got := f.PeopleNames()
	if len(got) != 2 || got[0] != "Пол Гринграсс" || got[1] != "Эндрю Гарфилд" {
		t.Errorf("PeopleNames = %#v", got)
	}
	if names := (Film{}).PeopleNames(); len(names) != 0 {
		t.Errorf("пустая карточка: %#v", names)
	}
}

func TestUniqueNames(t *testing.T) {
	got := uniqueNames([]string{"Paul", " paul ", "", "Пол", "Пол"})
	if len(got) != 2 || got[0] != "Paul" || got[1] != "Пол" {
		t.Errorf("uniqueNames = %#v", got)
	}
}
