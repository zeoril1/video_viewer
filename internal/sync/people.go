// Добор переводов имён людей (режиссёр, актёры) из TMDB: карточка показывает их на языке сайта,
// для чего пары «оригинал → перевод» складываются в таблицу person_names.
package sync

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/tmdb"
)

// needCredits — нужно ли запрашивать титры TMDB: в БД нет режиссёра/актёров либо имена записаны
// латиницей и ещё не проходили через таблицу переводов (тогда нужен русский вариант имён).
// Имена из Wikidata (кириллица) не трогаем — они уже на русском.
func needCredits(ctx context.Context, repo *db.Repo, f db.Film) bool {
	if f.Director == "" || len(f.Actors) == 0 {
		return true
	}
	names := f.PeopleNames()
	if len(names) == 0 {
		return true
	}
	tr, err := repo.LocalizePeople(ctx, "ru", names)
	if err != nil {
		log.Printf("refresh %s: people lookup: %v", f.IMDBID, err)
		return false
	}
	return db.PeopleNeedTranslation(names, tr)
}

// personPairs переводит пары имён TMDB в записи таблицы переводов.
func personPairs(names []tmdb.PersonName) []db.PersonName {
	out := make([]db.PersonName, 0, len(names))
	for _, n := range names {
		out = append(out, db.PersonName{PersonID: n.ID, Name: n.Name, NameRU: n.NameRU})
	}
	return out
}

// BackfillPeople — одноразовый добор переводов имён для всего каталога: у каждого фильма с
// tmdb_id, у которого имена хранятся латиницей, титры запрашиваются в оригинале и по-русски,
// пары кладутся в person_names. Запускается вручную (cmd/backfill -people), не фоновой джобой.
// Возвращает число попыток и число фильмов, для которых пары сохранены.
func BackfillPeople(ctx context.Context, repo *db.Repo, tm *tmdb.Client, batch int, pace time.Duration, limit int) (attempted, updated int, err error) {
	if batch <= 0 {
		batch = 100
	}
	if pace <= 0 {
		pace = 300 * time.Millisecond
	}
	// seen — уже попытанные в этом прогоне: записи, для которых TMDB не даёт перевода, остаются
	// в выборке, и без этого счётчика прогон зациклился бы.
	seen := map[string]bool{}
	for {
		if limit > 0 && attempted >= limit {
			break
		}
		want := batch
		if limit > 0 && limit-attempted < want {
			want = limit - attempted
		}

		films, lerr := repo.FilmsNeedingPeopleNames(ctx, want)
		if lerr != nil {
			return attempted, updated, lerr
		}
		var queue []db.Film
		for _, f := range films {
			if !seen[f.IMDBID] {
				seen[f.IMDBID] = true
				queue = append(queue, f)
			}
		}
		if len(queue) == 0 {
			break // всё, что можно перевести, уже переведено
		}

		for _, f := range queue {
			if cerr := ctx.Err(); cerr != nil {
				return attempted, updated, cerr
			}
			id, perr := strconv.ParseInt(f.TMDBID, 10, 64)
			if perr != nil || id <= 0 {
				continue
			}
			attempted++
			cr, cerr := tm.CreditsLocalized(ctx, id, f.Kind)
			if cerr != nil {
				log.Printf("people: %s: %v", f.IMDBID, cerr)
			} else if cerr := repo.UpsertPersonNames(ctx, personPairs(cr.Names)); cerr != nil {
				log.Printf("people: save %s: %v", f.IMDBID, cerr)
			} else {
				updated++
			}
			time.Sleep(pace)
		}
		log.Printf("people: прогресс: попыток %d, с переводом %d", attempted, updated)
	}
	return attempted, updated, nil
}
