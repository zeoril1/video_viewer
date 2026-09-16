// Имена людей (режиссёр, актёры) на языке сайта: переводы лежат в отдельной таблице
// person_names, её наполняет TMDB (internal/sync). films.director/actors хранят исходное
// написание, а в API уходят дополнительно director_ru/actors_ru — клиент выбирает по языку.
package catalogapi

import (
	"context"
	"log"

	"github.com/zeoril1/video_viewer/internal/db"
)

// peopleLang — язык, на который переводятся имена в выдаче (сайт по умолчанию русский;
// интерфейс на английском показывает исходное написание из films.director/actors).
const peopleLang = "ru"

// localizeFilmPeople заполняет director_ru/actors_ru/people_pending у фильмов ОДНИМ запросом
// к person_names (каталог зовёт это на весь список сразу). Изменяет элементы переданного среза.
func localizeFilmPeople(ctx context.Context, repo *db.Repo, films []db.Film) {
	if repo == nil || len(films) == 0 {
		return
	}
	names := make([]string, 0, len(films)*4)
	for _, f := range films {
		names = append(names, f.PeopleNames()...)
	}
	if len(names) == 0 {
		return
	}
	tr, err := repo.LocalizePeople(ctx, peopleLang, names)
	if err != nil {
		log.Printf("catalog: people names: %v", err)
		return
	}
	for i := range films {
		applyPeopleNames(&films[i], tr)
	}
}

// applyPeopleNames расставляет русские имена (где перевод есть) и признак «перевод в пути»:
// имена записаны латиницей и известен tmdb_id — фоновая задача добудет пары из TMDB, а клиенту
// стоит переспросить карточку через пару секунд (people_pending).
func applyPeopleNames(f *db.Film, tr map[string]string) {
	if ru := ruName(tr, f.Director); ru != "" {
		f.DirectorRU = ru
	}
	if len(f.Actors) > 0 {
		actors := make([]string, 0, len(f.Actors))
		changed := false
		for _, a := range f.Actors {
			ru := db.TranslatedName(tr, a)
			if ru != a {
				changed = true
			}
			actors = append(actors, ru)
		}
		if changed {
			f.ActorsRU = actors
		}
	}
	f.PeoplePending = f.TMDBID != "" && db.PeopleNeedTranslation(f.PeopleNames(), tr)
}

// ruName — русское написание имени; пусто, если перевода нет или он совпадает с оригиналом.
func ruName(tr map[string]string, name string) string {
	if name == "" {
		return ""
	}
	if ru := db.TranslatedName(tr, name); ru != name {
		return ru
	}
	return ""
}
