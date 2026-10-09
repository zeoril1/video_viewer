// people_test.go — имена режиссёра/актёров в выдаче карточки: перевод подставляется, если он есть;
// частичный перевод не скрывает остальных; признак people_pending ставится только там, где
// перевод реально ожидается (имена в латинице + известен tmdb_id).
package catalogapi

import (
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
)

func TestApplyPeopleNamesFullTranslation(t *testing.T) {
	f := db.Film{IMDBID: "tt1", TMDBID: "977942", Director: "Paul Greengrass",
		Actors: []string{"Andrew Garfield", "Jamie Bell"}}
	applyPeopleNames(&f, map[string]string{
		"paul greengrass": "Пол Гринграсс",
		"andrew garfield": "Эндрю Гарфилд",
		"jamie bell":      "Джейми Белл",
	})
	if f.DirectorRU != "Пол Гринграсс" {
		t.Errorf("director_ru = %q", f.DirectorRU)
	}
	if len(f.ActorsRU) != 2 || f.ActorsRU[0] != "Эндрю Гарфилд" || f.ActorsRU[1] != "Джейми Белл" {
		t.Errorf("actors_ru = %#v", f.ActorsRU)
	}
	if f.PeoplePending {
		t.Error("переводы есть — people_pending не нужен")
	}
}

func TestApplyPeopleNamesPartialTranslation(t *testing.T) {
	f := db.Film{IMDBID: "tt1", TMDBID: "977942", Director: "Paul Greengrass",
		Actors: []string{"Andrew Garfield", "Jamie Bell"}}
	applyPeopleNames(&f, map[string]string{
		"paul greengrass": "Пол Гринграсс",
		"andrew garfield": "Эндрю Гарфилд",
	})
	// Непереведённый актёр остаётся в исходном написании — список не «теряет» людей.
	if len(f.ActorsRU) != 2 || f.ActorsRU[1] != "Jamie Bell" {
		t.Errorf("actors_ru = %#v", f.ActorsRU)
	}
	if !f.PeoplePending {
		t.Error("часть имён не переведена — ожидался people_pending")
	}
}

func TestApplyPeopleNamesRussianSourceData(t *testing.T) {
	// Имена из Wikidata уже русские — переводить нечего, ждать нечего.
	f := db.Film{IMDBID: "tt1", TMDBID: "977942", Director: "Пол Гринграсс",
		Actors: []string{"Эндрю Гарфилд"}}
	applyPeopleNames(&f, map[string]string{})
	if f.DirectorRU != "" || len(f.ActorsRU) != 0 {
		t.Errorf("русские имена не требуют перевода: %+v %#v", f.DirectorRU, f.ActorsRU)
	}
	if f.PeoplePending {
		t.Error("кириллица — people_pending не ставим")
	}
}

func TestApplyPeopleNamesPendingNeedsTMDBID(t *testing.T) {
	// Без tmdb_id перевести нечем — обещать клиенту «перевод в пути» нельзя.
	f := db.Film{IMDBID: "tt1", Director: "Paul Greengrass", Actors: []string{"Andrew Garfield"}}
	applyPeopleNames(&f, map[string]string{})
	if f.PeoplePending {
		t.Error("нет tmdb_id — перевод невозможен, people_pending не ставим")
	}
	if f.DirectorRU != "" || len(f.ActorsRU) != 0 {
		t.Errorf("переводов нет — поля должны остаться пустыми: %+v %#v", f.DirectorRU, f.ActorsRU)
	}
}

func TestRuNameIgnoresSameSpelling(t *testing.T) {
	tr := map[string]string{"stephen dillane": "Stephen Dillane"}
	if got := ruName(tr, "Stephen Dillane"); got != "" {
		t.Errorf("одинаковое написание не считается переводом: %q", got)
	}
	if got := ruName(tr, ""); got != "" {
		t.Errorf("пустое имя: %q", got)
	}
}
