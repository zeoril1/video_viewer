// Локализация имён людей в карточке (режиссёр, актёры): TMDB отдаёт имена на языке запроса,
// поэтому пары «оригинал → перевод» складываются в отдельную таблицу person_names, а выдача
// подставляет имя на языке сайта (films.director/actors хранят исходное, как пришло от источника).
package db

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"
)

// Языки таблицы person_names: оригинал (английское написание TMDB) и русский перевод.
const (
	langEN = "en"
	langRU = "ru"
)

// peopleSchema — таблица переводов имён: по строке на человека и язык (person_id — id человека
// в TMDB). Оригинал хранится тоже: по нему находится перевод, и он же — признак «человека уже
// забирали из TMDB», чтобы не запрашивать его повторно.
const peopleSchema = `
CREATE TABLE IF NOT EXISTS person_names (
	person_id  BIGINT NOT NULL,
	lang       TEXT NOT NULL,
	name       TEXT NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (person_id, lang)
);
CREATE INDEX IF NOT EXISTS idx_person_names_name ON person_names (lower(name));
`

// ensurePeopleSchema создаёт таблицу переводов имён (если её нет).
func (r *Repo) ensurePeopleSchema(ctx context.Context) error {
	_, err := r.conn.ExecContext(ctx, peopleSchema)
	return err
}

// PersonName — имена человека из TMDB: оригинал (то, что хранится в films.director/actors)
// и русский перевод. Перевод может быть пустым — тогда TMDB русского имени не знает;
// оригинал в этом случае всё равно сохраняется.
type PersonName struct {
	PersonID int64
	Name     string
	NameRU   string
}

// UpsertPersonNames сохраняет пары имён одним транзакционным проходом.
func (r *Repo) UpsertPersonNames(ctx context.Context, pairs []PersonName) error {
	if len(pairs) == 0 {
		return nil
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	const q = `INSERT INTO person_names (person_id, lang, name) VALUES ($1, $2, $3)
		ON CONFLICT (person_id, lang) DO UPDATE SET name = EXCLUDED.name, updated_at = now()`
	saved := 0
	for _, p := range pairs {
		if p.PersonID <= 0 {
			continue
		}
		for lang, name := range map[string]string{langEN: p.Name, langRU: p.NameRU} {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, q, p.PersonID, lang, name); err != nil {
				return err
			}
			saved++
		}
	}
	if saved == 0 {
		return nil
	}
	return tx.Commit()
}

// LocalizePeople переводит имена на язык lang по таблице person_names. Ключ результата —
// имя в нижнем регистре, поэтому перевод находится и по оригиналу («Paul Greengrass»),
// и по русскому написанию. Значение — имя на нужном языке, а если перевода нет, исходное
// имя: наличие ключа означает «человек уже известен» (см. PeopleNeedTranslation).
func (r *Repo) LocalizePeople(ctx context.Context, lang string, names []string) (map[string]string, error) {
	out := map[string]string{}
	list := uniqueNames(names)
	if len(list) == 0 {
		return out, nil
	}
	payload, err := json.Marshal(list)
	if err != nil {
		return out, err
	}
	rows, err := r.conn.QueryContext(ctx, `
		SELECT DISTINCT src.k, COALESCE(t.name, n.name)
		FROM (SELECT lower(x) AS k FROM jsonb_array_elements_text($2::jsonb) AS x) AS src
		JOIN person_names n ON lower(n.name) = src.k
		LEFT JOIN person_names t ON t.person_id = n.person_id AND t.lang = $1
	`, lang, string(payload))
	if err != nil {
		return out, err
	}
	defer rows.Close()

	for rows.Next() {
		var src, dst string
		if err := rows.Scan(&src, &dst); err != nil {
			return out, err
		}
		out[src] = dst
	}
	return out, rows.Err()
}

// TranslatedName возвращает перевод имени из карты LocalizePeople, а если перевода нет — само имя.
func TranslatedName(tr map[string]string, name string) string {
	if v, ok := tr[personKey(name)]; ok && v != "" {
		return v
	}
	return name
}

// PeopleNeedTranslation сообщает, стоит ли добирать переводы из TMDB: все имена записаны
// латиницей (кириллица — это уже русские имена, например из Wikidata) и хотя бы одного из них
// ещё нет в person_names. tr — карта из LocalizePeople.
func PeopleNeedTranslation(names []string, tr map[string]string) bool {
	missing := false
	for _, n := range names {
		if personKey(n) == "" {
			continue
		}
		if !isLatinName(n) {
			return false // есть кириллица — имена уже на русском
		}
		if _, ok := tr[personKey(n)]; !ok {
			missing = true
		}
	}
	return missing
}

// FilmsNeedingPeopleNames возвращает фильмы, у которых режиссёр/актёры записаны латиницей и хотя
// бы одного имени ещё нет в person_names. По таким записям перевод можно добрать из TMDB
// (tmdb_id обязателен) — используется одноразовым добором имён.
func (r *Repo) FilmsNeedingPeopleNames(ctx context.Context, limit int) ([]Film, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT `+filmColsLite+`
		FROM films f
		WHERE COALESCE(f.tmdb_id, '') <> ''
		  AND (COALESCE(f.director, '') <> '' OR COALESCE(NULLIF(f.actors, ''), '[]') <> '[]')
		  AND COALESCE(f.director, '') !~ '[А-Яа-яЁё]'
		  AND COALESCE(f.actors, '') !~ '[А-Яа-яЁё]'
		  AND EXISTS (
			SELECT 1 FROM (
				SELECT btrim(f.director) AS n
				UNION ALL
				SELECT btrim(x) FROM jsonb_array_elements_text(COALESCE(NULLIF(f.actors, ''), '[]')::jsonb) AS x
			) AS p
			WHERE btrim(COALESCE(p.n, '')) <> ''
			  AND NOT EXISTS (SELECT 1 FROM person_names pn WHERE lower(pn.name) = lower(btrim(p.n)))
		  )
		ORDER BY f.created_at
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var films []Film
	for rows.Next() {
		f, err := scanFilmLite(rows)
		if err != nil {
			return nil, err
		}
		films = append(films, f)
	}
	return films, rows.Err()
}

// PeopleNames — имена людей карточки (режиссёр и актёры) без пустых значений.
func (f Film) PeopleNames() []string {
	out := make([]string, 0, len(f.Actors)+1)
	for _, n := range append([]string{f.Director}, f.Actors...) {
		if s := strings.TrimSpace(n); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// personKey — ключ имени в карте переводов (без ведущих/хвостовых пробелов, в нижнем регистре).
func personKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// isLatinName — в имени нет кириллицы (латиница с диакритикой, дефисы и точки допустимы).
func isLatinName(name string) bool {
	for _, r := range name {
		if unicode.In(r, unicode.Cyrillic) {
			return false
		}
	}
	return true
}

// uniqueNames убирает пустые и повторяющиеся имена (без учёта регистра).
func uniqueNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		k := personKey(n)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, strings.TrimSpace(n))
	}
	return out
}
