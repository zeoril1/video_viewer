package sync

import (
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

func TestNormTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Одержимость / Whiplash", "одержимость whiplash"},
		{"  Звёздные войны: Эпизод  4 ", "звёздные войны эпизод 4"},
		{"T2 Trainspotting (1996)", "t2 trainspotting 1996"},
		{"", ""},
		{"...", ""},
	}
	for _, c := range cases {
		if got := normTitle(c.in); got != c.want {
			t.Errorf("normTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFilmMatchScore(t *testing.T) {
	f := db.Film{Title: "Whiplash", TitleRU: "Одержимость", Year: 2014}

	// Точное совпадение русского названия и года.
	if s := filmMatchScore(f, tmdb.Film{Title: "Whiplash", TitleRU: "Одержимость", Year: 2014}); s == 0 {
		t.Error("полное совпадение не распознано")
	}
	// Не тот год (разница > 1 года) — не совпадает.
	if s := filmMatchScore(f, tmdb.Film{Title: "Whiplash", TitleRU: "Одержимость", Year: 2017}); s != 0 {
		t.Errorf("разница в год >1 не должна совпадать, score=%d", s)
	}
	// Разница в год (±1) при точном названии — допустима (разные источники
	// дат премьеры).
	if s := filmMatchScore(f, tmdb.Film{Title: "Whiplash", TitleRU: "Одержимость", Year: 2015}); s == 0 {
		t.Error("разница в 1 год при точном названии должна допускаться")
	}
	// Другое название — не совпадает.
	if s := filmMatchScore(f, tmdb.Film{Title: "Interstellar", TitleRU: "Интерстеллар", Year: 2014}); s != 0 {
		t.Errorf("другое название не должно совпадать, score=%d", s)
	}
	// Совпадение по английскому названию (русского у записи может не быть).
	g := db.Film{Title: "Whiplash", Year: 2014}
	if s := filmMatchScore(g, tmdb.Film{Title: "Whiplash", TitleRU: "Одержимость", Year: 2014}); s == 0 {
		t.Error("совпадение по английскому названию не распознано")
	}
	// Вложенное совпадение (разные регистры/знаки) и год.
	h := db.Film{Title: "Звёздные войны: Эпизод 4", TitleRU: "Звёздные войны: Эпизод 4", Year: 1977}
	if s := filmMatchScore(h, tmdb.Film{Title: "Star Wars", TitleRU: "Звёздные войны. Эпизод 4: Новая надежда", Year: 1977}); s == 0 {
		t.Error("вложенное совпадение названия с тем же годом не распознано")
	}
	// Без года — достаточно совпадения названия.
	k := db.Film{Title: "Whiplash", Year: 0}
	if s := filmMatchScore(k, tmdb.Film{Title: "Whiplash", TitleRU: "Одержимость", Year: 2014}); s == 0 {
		t.Error("без года совпадение по названию не распознано")
	}
}

func TestCreditsAgree(t *testing.T) {
	f := db.Film{Director: "Дэмьен Шазелл", Actors: []string{"Майлз Теллер", "Дж.К. Симмонс"}}

	// Режиссёр/актёры в разных алфавитах (русская транслитерация из КП
	// против английских имён в TMDB) — сравнить нельзя, не спорим.
	if !creditsAgree(f, "Damien Chazelle", []string{"Miles Teller"}) {
		t.Error("разные алфавиты не должны отклоняться")
	}
	// У TMDB нет данных — не спорим.
	if !creditsAgree(f, "", nil) {
		t.Error("пустые данные TMDB не должны отклонять совпадение")
	}
	// У записи нет режиссёра/актёров — согласны без проверки.
	if !creditsAgree(db.Film{}, "Whatever", []string{"X"}) {
		t.Error("запись без режиссёра/актёров не требует сверки")
	}

	// Тот же алфавит: совпадение имени — согласие, несовпадение — отказ.
	en := db.Film{Director: "Damien Chazelle", Actors: []string{"Miles Teller", "J.K. Simmons"}}
	if !creditsAgree(en, "Damien Chazelle", []string{"Miles Teller"}) {
		t.Error("совпавший режиссёр (латиница) должен давать согласие")
	}
	if creditsAgree(en, "Christopher Nolan", []string{"Leonardo DiCaprio"}) {
		t.Error("полное несовпадение в одном алфавите должно отклоняться")
	}
}
