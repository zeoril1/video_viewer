package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Source — сохранённая магнет-ссылка с метаданными раздачи. Таблица
// sources хранит результаты поиска на трекерах: по одному фильму может
// быть несколько записей (разные озвучки/качество/сезоны).
//
// Ключ записи — info_hash (хеш содержимого), а не полная строка магнита:
// у одного релиза (одинаковый xt=urn:btih:...) трекер отдаёт магниты с
// разными сессионными токенами в трекерах, и сравнение по строке магнита
// плодило бы дубли при каждом поиске.
type Source struct {
	ID        int64
	FilmID    string // imdb_id / kp<id> фильма
	InfoHash  string // xt=urn:btih:<hex> из магнита
	Magnet    string
	Title     string // заголовок раздачи
	Size      string
	Seeds     int
	Quality   string // 2160/1080/720/480 (парсится из заголовка)
	Audio     string // dub/multi/two/single/original/subs
	Season    int    // сезон (0 — не определён/полный сборник)
	Provider  string // имя провайдера (jackett и т.п.)
	CheckedAt time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

const sourcesSchema = `
CREATE TABLE IF NOT EXISTS sources (
	id BIGSERIAL PRIMARY KEY,
	film_id TEXT NOT NULL,
	info_hash TEXT NOT NULL DEFAULT '',
	magnet TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	size TEXT NOT NULL DEFAULT '',
	seeds INT NOT NULL DEFAULT 0,
	quality TEXT NOT NULL DEFAULT '',
	audio TEXT NOT NULL DEFAULT '',
	season INT NOT NULL DEFAULT 0,
	provider TEXT NOT NULL DEFAULT '',
	checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (film_id, info_hash)
);
CREATE INDEX IF NOT EXISTS idx_sources_film ON sources (film_id);
`

// ensureSourcesSchema создаёт таблицу источников и применяет миграции
// (переход с ключа по magnet на ключ по info_hash).
func (r *Repo) ensureSourcesSchema(ctx context.Context) error {
	if _, err := r.conn.ExecContext(ctx, sourcesSchema); err != nil {
		return err
	}
	if _, err := r.conn.ExecContext(ctx, "ALTER TABLE sources ADD COLUMN IF NOT EXISTS info_hash TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Старый уникальный ключ по (film_id, magnet) убираем.
	if _, err := r.conn.ExecContext(ctx, `DO $$ BEGIN
		IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname='sources_film_id_magnet_key') THEN
			ALTER TABLE sources DROP CONSTRAINT sources_film_id_magnet_key;
		END IF;
	END $$`); err != nil {
		return err
	}
	// Для старых строк заполняем info_hash из магнита.
	if _, err := r.conn.ExecContext(ctx, `
		UPDATE sources
		SET info_hash = lower(substring(magnet from 'xt=urn:btih:([0-9a-fA-F]{40})'))
		WHERE info_hash = ''
	`); err != nil {
		return err
	}
	// Строки без валидного info_hash нельзя дедуплицировать — удаляем.
	if _, err := r.conn.ExecContext(ctx, `DELETE FROM sources WHERE info_hash = ''`); err != nil {
		return err
	}
	// Дедупликация по (film_id, info_hash): оставляем строку с меньшим id
	// (у одного релиза раньше могло быть несколько строк с разными
	// трекерами в магните).
	if _, err := r.conn.ExecContext(ctx, `
		DELETE FROM sources a USING sources b
		WHERE a.id > b.id AND a.film_id = b.film_id AND a.info_hash = b.info_hash
	`); err != nil {
		return err
	}
	// Новый ключ по (film_id, info_hash).
	if _, err := r.conn.ExecContext(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='sources_film_id_info_hash_key') THEN
			ALTER TABLE sources ADD CONSTRAINT sources_film_id_info_hash_key UNIQUE (film_id, info_hash);
		END IF;
	END $$`); err != nil {
		return err
	}
	return nil
}

// btihRe — вытаскивает info hash из магнита (xt=urn:btih:<40 hex>).
var btihRe = regexp.MustCompile(`(?i)xt=urn:btih:([0-9a-f]{40})`)

// magnetInfoHash возвращает info hash магнита (нижний регистр) или "".
func magnetInfoHash(magnet string) string {
	if m := btihRe.FindStringSubmatch(magnet); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// SaveSources сохраняет свежую выдачу источников фильма. Ключ записи —
// info_hash: новые релизы вставляются, существующие обновляются ТОЛЬКО
// если изменились метаданные (размер/сиды/качество/озвучка/сезон/
// провайдер), а источники, которых больше нет в выдаче, удаляются.
// Полная строка магнита и checked_at освежаются при каждом появлении
// релиза (трекеры в магните — сессионные токены). Возвращает число
// добавленных/обновлённых/удалённых записей. Если выдача пустая — все
// сохранённые источники фильма удаляются.
func (r *Repo) SaveSources(ctx context.Context, filmID string, srcs []Source) (added, updated, removed int, err error) {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO sources (film_id, info_hash, magnet, title, size, seeds, quality, audio, season, provider, checked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (film_id, info_hash) DO UPDATE SET
			magnet     = EXCLUDED.magnet,
			title      = EXCLUDED.title,
			size       = EXCLUDED.size,
			seeds      = EXCLUDED.seeds,
			quality    = EXCLUDED.quality,
			audio      = EXCLUDED.audio,
			season     = EXCLUDED.season,
			provider   = EXCLUDED.provider,
			checked_at = now(),
			updated_at = now()
		WHERE sources.title    IS DISTINCT FROM EXCLUDED.title
		   OR sources.size     IS DISTINCT FROM EXCLUDED.size
		   OR sources.seeds    IS DISTINCT FROM EXCLUDED.seeds
		   OR sources.quality  IS DISTINCT FROM EXCLUDED.quality
		   OR sources.audio    IS DISTINCT FROM EXCLUDED.audio
		   OR sources.season   IS DISTINCT FROM EXCLUDED.season
		   OR sources.provider IS DISTINCT FROM EXCLUDED.provider
		RETURNING (xmax = 0) AS is_insert
	`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = stmt.Close() }()

	// Освежение магнита (трекеры меняются) без учёта как «обновление».
	refresh, err := tx.PrepareContext(ctx, `
		UPDATE sources SET magnet = $1, checked_at = now()
		WHERE film_id = $2 AND info_hash = $3
	`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = refresh.Close() }()

	hashes := make([]string, 0, len(srcs))
	for _, s := range srcs {
		hash := magnetInfoHash(s.Magnet)
		if hash == "" {
			continue
		}
		hashes = append(hashes, hash)
		var isInsert bool
		err := stmt.QueryRowContext(ctx,
			filmID, hash, s.Magnet, s.Title, s.Size, s.Seeds,
			s.Quality, s.Audio, s.Season, s.Provider,
		).Scan(&isInsert)
		if err == sql.ErrNoRows {
			// Метаданные не изменились — только освежаем магнет/checked_at.
			_, _ = refresh.ExecContext(ctx, s.Magnet, filmID, hash)
			continue
		}
		if err != nil {
			return 0, 0, 0, fmt.Errorf("save source %s: %w", hash, err)
		}
		if isInsert {
			added++
		} else {
			updated++
		}
	}

	// Удаляем источники фильма, которых больше нет в свежей выдаче.
	if len(hashes) > 0 {
		placeholders := make([]string, len(hashes))
		args := make([]any, 0, len(hashes)+1)
		args = append(args, filmID)
		for i, h := range hashes {
			placeholders[i] = fmt.Sprintf("$%d", i+2)
			args = append(args, h)
		}
		q := "DELETE FROM sources WHERE film_id = $1 AND info_hash NOT IN (" + strings.Join(placeholders, ",") + ")"
		res, derr := tx.ExecContext(ctx, q, args...)
		if derr != nil {
			return 0, 0, 0, fmt.Errorf("prune sources: %w", derr)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			removed = int(n)
		}
	} else if res, derr := tx.ExecContext(ctx, "DELETE FROM sources WHERE film_id = $1", filmID); derr != nil {
		return 0, 0, 0, fmt.Errorf("clear sources: %w", derr)
	} else if n, _ := res.RowsAffected(); n > 0 {
		removed = int(n)
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return added, updated, removed, nil
}

// ListSources возвращает сохранённые источники фильма (по убыванию сидов).
func (r *Repo) ListSources(ctx context.Context, filmID string) ([]Source, error) {
	rows, err := r.conn.QueryContext(ctx, `
		SELECT id, film_id, info_hash, magnet, title, size, seeds, quality, audio, season, provider,
		       checked_at, created_at, updated_at
		FROM sources
		WHERE film_id = $1
		ORDER BY seeds DESC, id
	`, filmID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Source
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.FilmID, &s.InfoHash, &s.Magnet, &s.Title, &s.Size, &s.Seeds,
			&s.Quality, &s.Audio, &s.Season, &s.Provider,
			&s.CheckedAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
