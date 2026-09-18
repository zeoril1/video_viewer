package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// IPTVPlaylist — плейлист IPTV, добавленный админом.
// URL/Username/Password — источник (M3U-ссылка либо панель Xtream Codes): эти поля НИКОГДА
// не отдаются клиенту — браузер получает только id канала, а поток тянет наш сервер.
type IPTVPlaylist struct {
	ID       int64
	Name     string
	Kind     string // m3u | xtream
	URL      string
	Username string
	Password string
	EPGURL   string
	UA       string // общий User-Agent для потока (если провайдер требует)
	Referer  string
	Enabled  bool

	LastSync     time.Time
	LastError    string
	ChannelCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// IPTVChannel — канал плейлиста (клиентская часть: stream_url не отдаём).
type IPTVChannel struct {
	ID         int64
	PlaylistID int64
	ExtID      string
	Name       string
	NameRU     string // русское название из справочника iptv-org ("" — нет)
	Group      string
	Logo       string
	EPGID      string
	StreamURL  string // внутреннее поле: адрес потока у провайдера
	IsHLS      bool
	UA         string
	Referer    string
	Num        int
	EPGKey     string // ключ сопоставления с программами XMLTV
	// DedupKey — ключ канала: одинаковый у дублей из разных плейлистов и у вариантов потока (см. iptv.ChannelKey).
	DedupKey string
	// Quality — оценка варианта потока: из дублей в списке остаётся лучший.
	Quality   int
	UpdatedAt time.Time
}

// IPTVGroup — группа каналов (для чипов в интерфейсе).
type IPTVGroup struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// IPTVProgram — передача телепрограммы.
type IPTVProgram struct {
	Key      string
	Start    time.Time
	Stop     time.Time
	Title    string
	Desc     string
	Category string
}

const iptvSchema = `
CREATE TABLE IF NOT EXISTS iptv_playlists (
	id            BIGSERIAL PRIMARY KEY,
	name          TEXT NOT NULL,
	kind          TEXT NOT NULL DEFAULT 'm3u',
	url           TEXT NOT NULL DEFAULT '',
	username      TEXT NOT NULL DEFAULT '',
	password      TEXT NOT NULL DEFAULT '',
	epg_url       TEXT NOT NULL DEFAULT '',
	user_agent    TEXT NOT NULL DEFAULT '',
	referrer      TEXT NOT NULL DEFAULT '',
	enabled       BOOLEAN NOT NULL DEFAULT TRUE,
	last_sync     TIMESTAMPTZ,
	last_error    TEXT NOT NULL DEFAULT '',
	channel_count INT NOT NULL DEFAULT 0,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS iptv_channels (
	id          BIGSERIAL PRIMARY KEY,
	playlist_id BIGINT NOT NULL REFERENCES iptv_playlists(id) ON DELETE CASCADE,
	ext_id      TEXT NOT NULL,
	name        TEXT NOT NULL DEFAULT '',
	grp         TEXT NOT NULL DEFAULT '',
	grp_all     TEXT NOT NULL DEFAULT '',
	name_ru     TEXT NOT NULL DEFAULT '',
	dedup_key   TEXT NOT NULL DEFAULT '',
	quality     INT NOT NULL DEFAULT 0,
	logo        TEXT NOT NULL DEFAULT '',
	epg_id      TEXT NOT NULL DEFAULT '',
	stream_url  TEXT NOT NULL DEFAULT '',
	is_hls      BOOLEAN NOT NULL DEFAULT TRUE,
	user_agent  TEXT NOT NULL DEFAULT '',
	referrer    TEXT NOT NULL DEFAULT '',
	num         INT NOT NULL DEFAULT 0,
	epg_key     TEXT NOT NULL DEFAULT '',
	updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (playlist_id, ext_id)
);
CREATE INDEX IF NOT EXISTS idx_iptv_channels_playlist ON iptv_channels (playlist_id, num);
-- Плейлисты, заведённые до появления русских названий и отсева дублей: колонки
-- добавляем на месте (CREATE TABLE IF NOT EXISTS их уже не создаст).
-- ВАЖНО: ALTER-ы идут РАНЬШЕ индексов. Вся схема выполняется одним Exec
-- (одна транзакция), поэтому CREATE INDEX по отсутствующей в старой таблице
-- колонке (dedup_key) откатывал и сами миграции: сервис падал на старте с
-- «column "dedup_key" does not exist».
ALTER TABLE iptv_channels ADD COLUMN IF NOT EXISTS grp_all   TEXT NOT NULL DEFAULT '';
ALTER TABLE iptv_channels ADD COLUMN IF NOT EXISTS name_ru   TEXT NOT NULL DEFAULT '';
ALTER TABLE iptv_channels ADD COLUMN IF NOT EXISTS dedup_key TEXT NOT NULL DEFAULT '';
ALTER TABLE iptv_channels ADD COLUMN IF NOT EXISTS quality   INT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_iptv_channels_dedup ON iptv_channels (dedup_key);
CREATE TABLE IF NOT EXISTS iptv_programs (
	id       BIGSERIAL PRIMARY KEY,
	ch_key   TEXT NOT NULL,
	start_at TIMESTAMPTZ NOT NULL,
	stop_at  TIMESTAMPTZ NOT NULL,
	title    TEXT NOT NULL DEFAULT '',
	descr    TEXT NOT NULL DEFAULT '',
	category TEXT NOT NULL DEFAULT '',
	UNIQUE (ch_key, start_at)
);
CREATE INDEX IF NOT EXISTS idx_iptv_programs_key ON iptv_programs (ch_key, start_at);
`

// ensureIPTVSchema создаёт таблицы IPTV (плейлисты, каналы, программа).
func (r *Repo) ensureIPTVSchema(ctx context.Context) error {
	_, err := r.conn.ExecContext(ctx, iptvSchema)
	return err
}

const playlistCols = `id, name, kind, url, username, password, epg_url, user_agent, referrer,
	enabled, last_sync, last_error, channel_count, created_at, updated_at`

func scanPlaylist(row interface{ Scan(...any) error }) (IPTVPlaylist, error) {
	var p IPTVPlaylist
	var lastSync sql.NullTime
	if err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.URL, &p.Username, &p.Password, &p.EPGURL,
		&p.UA, &p.Referer, &p.Enabled, &lastSync, &p.LastError, &p.ChannelCount,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return IPTVPlaylist{}, err
	}
	if lastSync.Valid {
		p.LastSync = lastSync.Time
	}
	return p, nil
}

// SaveIPTVPlaylist вставляет (ID == 0) или обновляет плейлист.
// Пустой пароль при обновлении означает «оставить прежний» — админ может править поля, не переписывая пароль.
func (r *Repo) SaveIPTVPlaylist(ctx context.Context, p IPTVPlaylist) (int64, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Kind = strings.TrimSpace(strings.ToLower(p.Kind))
	if p.Kind == "" {
		p.Kind = "m3u"
	}
	if p.Name == "" {
		p.Name = "IPTV"
	}
	if p.ID == 0 {
		var id int64
		err := r.conn.QueryRowContext(ctx, `
			INSERT INTO iptv_playlists (name, kind, url, username, password, epg_url, user_agent, referrer, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			p.Name, p.Kind, p.URL, p.Username, p.Password, p.EPGURL, p.UA, p.Referer, p.Enabled,
		).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("insert iptv playlist: %w", err)
		}
		return id, nil
	}
	_, err := r.conn.ExecContext(ctx, `
		UPDATE iptv_playlists SET
			name = $2, kind = $3, url = $4, username = $5,
			password = CASE WHEN $6 = '' THEN password ELSE $6 END,
			epg_url = $7, user_agent = $8, referrer = $9, enabled = $10, updated_at = now()
		WHERE id = $1`,
		p.ID, p.Name, p.Kind, p.URL, p.Username, p.Password, p.EPGURL, p.UA, p.Referer, p.Enabled,
	)
	if err != nil {
		return 0, fmt.Errorf("update iptv playlist %d: %w", p.ID, err)
	}
	return p.ID, nil
}

// ListIPTVPlaylists возвращает плейлисты (для админки и синка).
func (r *Repo) ListIPTVPlaylists(ctx context.Context) ([]IPTVPlaylist, error) {
	rows, err := r.conn.QueryContext(ctx, `SELECT `+playlistCols+` FROM iptv_playlists ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IPTVPlaylist
	for rows.Next() {
		p, err := scanPlaylist(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetIPTVPlaylist возвращает плейлист по id.
func (r *Repo) GetIPTVPlaylist(ctx context.Context, id int64) (IPTVPlaylist, bool, error) {
	row := r.conn.QueryRowContext(ctx, `SELECT `+playlistCols+` FROM iptv_playlists WHERE id = $1`, id)
	p, err := scanPlaylist(row)
	if err == sql.ErrNoRows {
		return IPTVPlaylist{}, false, nil
	}
	if err != nil {
		return IPTVPlaylist{}, false, err
	}
	return p, true, nil
}

// DeleteIPTVPlaylist удаляет плейлист вместе с каналами (ON DELETE CASCADE).
func (r *Repo) DeleteIPTVPlaylist(ctx context.Context, id int64) error {
	_, err := r.conn.ExecContext(ctx, `DELETE FROM iptv_playlists WHERE id = $1`, id)
	return err
}

// SetIPTVPlaylistSync фиксирует результат синка (время, число каналов, ошибку).
func (r *Repo) SetIPTVPlaylistSync(ctx context.Context, id int64, channels int, errMsg string) error {
	_, err := r.conn.ExecContext(ctx, `
		UPDATE iptv_playlists
		SET last_sync = now(), channel_count = $2, last_error = $3, updated_at = now()
		WHERE id = $1`, id, channels, errMsg)
	return err
}

// ReplaceIPTVChannels сохраняет свежий список каналов плейлиста: новые вставляются, изменившиеся обновляются,
// пропавшие из плейлиста удаляются (логотип/группа/название могли поменяться у провайдера).
func (r *Repo) ReplaceIPTVChannels(ctx context.Context, playlistID int64, chans []IPTVChannel) (added, removed int, err error) {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO iptv_channels
			(playlist_id, ext_id, name, grp, grp_all, name_ru, dedup_key, quality, logo, epg_id, stream_url,
			 is_hls, user_agent, referrer, num, updated_at)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, now())
		ON CONFLICT (playlist_id, ext_id) DO UPDATE SET
			name       = EXCLUDED.name,
			grp        = EXCLUDED.grp,
			grp_all    = EXCLUDED.grp_all,
			name_ru    = EXCLUDED.name_ru,
			dedup_key  = EXCLUDED.dedup_key,
			quality    = EXCLUDED.quality,
			logo       = EXCLUDED.logo,
			epg_id     = EXCLUDED.epg_id,
			stream_url = EXCLUDED.stream_url,
			is_hls     = EXCLUDED.is_hls,
			user_agent = EXCLUDED.user_agent,
			referrer   = EXCLUDED.referrer,
			num        = EXCLUDED.num,
			updated_at = now()
		RETURNING (xmax = 0) AS is_insert`)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = stmt.Close() }()

	extIDs := make([]string, 0, len(chans))
	for _, c := range chans {
		if strings.TrimSpace(c.ExtID) == "" || strings.TrimSpace(c.StreamURL) == "" {
			continue
		}
		extIDs = append(extIDs, c.ExtID)
		var isInsert bool
		if err := stmt.QueryRowContext(ctx, playlistID, c.ExtID, c.Name, c.Group, c.NameRU,
			c.DedupKey, c.Quality, c.Logo, c.EPGID, c.StreamURL, c.IsHLS, c.UA, c.Referer, c.Num).Scan(&isInsert); err != nil {
			return 0, 0, fmt.Errorf("upsert iptv channel %q: %w", c.Name, err)
		}
		if isInsert {
			added++
		}
	}

	if len(extIDs) > 0 {
		placeholders := make([]string, len(extIDs))
		args := make([]any, 0, len(extIDs)+1)
		args = append(args, playlistID)
		for i, e := range extIDs {
			placeholders[i] = fmt.Sprintf("$%d", i+2)
			args = append(args, e)
		}
		res, derr := tx.ExecContext(ctx,
			"DELETE FROM iptv_channels WHERE playlist_id = $1 AND ext_id NOT IN ("+strings.Join(placeholders, ",")+")", args...)
		if derr != nil {
			return 0, 0, fmt.Errorf("prune iptv channels: %w", derr)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			removed = int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return added, removed, nil
}

const channelCols = `id, playlist_id, ext_id, name, grp, logo, epg_id, stream_url, is_hls,
	user_agent, referrer, num, epg_key, name_ru, dedup_key, quality, updated_at`

// channelColsMerged — как channelCols, но группа канала объединена по всем плейлистам
// (см. MergeIPTVChannelGroups): один канал лежит в разных категориях, и находить его нужно в каждой.
const channelColsMerged = `id, playlist_id, ext_id, name,
	COALESCE(NULLIF(grp_all, ''), grp) AS grp, logo, epg_id, stream_url, is_hls,
	user_agent, referrer, num, epg_key, name_ru, dedup_key, quality, updated_at`

// channelKeyExpr — ключ канала в SQL; пустой dedup_key (строки до первого синка с русскими названиями) считаем уникальным по id.
const channelKeyExpr = `COALESCE(NULLIF(dedup_key, ''), '#' || id::text)`

// enabledPlaylistCond — канал принадлежит ВКЛЮЧЁННОМУ плейлисту: выключенный остаётся в базе со своими
// каналами и программой (его можно включить обратно), но в списке каналов не участвует — так убирают
// целый источник, не удаляя его.
const enabledPlaylistCond = `EXISTS (SELECT 1 FROM iptv_playlists p
	WHERE p.id = iptv_channels.playlist_id AND p.enabled)`

func scanChannel(row interface{ Scan(...any) error }) (IPTVChannel, error) {
	var c IPTVChannel
	err := row.Scan(&c.ID, &c.PlaylistID, &c.ExtID, &c.Name, &c.Group, &c.Logo, &c.EPGID,
		&c.StreamURL, &c.IsHLS, &c.UA, &c.Referer, &c.Num, &c.EPGKey, &c.NameRU, &c.DedupKey,
		&c.Quality, &c.UpdatedAt)
	return c, err
}

// ListIPTVChannels возвращает каналы: фильтр по плейлисту, группе и подстроке названия
// (регистронезависимо), с постраничной выдачей.
// Каналы отдаются без дублей: из нескольких записей одного канала (один и тот же канал в разных
// плейлистах — «Моя Планета» в русском и природном списке — и варианты потока разного качества)
// остаётся одна, лучшая по качеству.
func (r *Repo) ListIPTVChannels(ctx context.Context, playlistID int64, group, query string, limit, offset int) ([]IPTVChannel, error) {
	if limit <= 0 || limit > 2000 {
		limit = 1000
	}
	where := " WHERE 1=1"
	args := []any{}
	if playlistID > 0 {
		args = append(args, playlistID)
		where += fmt.Sprintf(" AND playlist_id = $%d", len(args))
	} else {
		// Без запроса конкретного плейлиста каналы выключенных не показываем (в админке плейлист виден,
		// можно включить обратно); с конкретным id каналы отдаются независимо от состояния — так работают админка и синхронизация программы.
		where += " AND " + enabledPlaylistCond
	}
	if s := strings.TrimSpace(query); s != "" {
		args = append(args, "%"+strings.ToLower(s)+"%")
		where += fmt.Sprintf(" AND (lower(name) LIKE $%d OR lower(name_ru) LIKE $%d OR lower(grp) LIKE $%d)",
			len(args), len(args), len(args))
	}
	q := `WITH d AS (SELECT DISTINCT ON (` + channelKeyExpr + `) ` + channelColsMerged +
		` FROM iptv_channels` + where + ` ORDER BY ` + channelKeyExpr + `, quality DESC, id)` +
		` SELECT * FROM d`
	if g := strings.TrimSpace(group); g != "" {
		// У iptv-org группа — список через «;» («Comedy;Movies»): совпадение должно быть по любой из частей.
		args = append(args, g)
		q += fmt.Sprintf(" WHERE $%d = ANY (SELECT trim(x) FROM unnest(string_to_array(grp, ';')) AS x)", len(args))
	}
	args = append(args, limit, offset)
	q += fmt.Sprintf(" ORDER BY playlist_id, num, name LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := r.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IPTVChannel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetIPTVChannel возвращает канал по id.
func (r *Repo) GetIPTVChannel(ctx context.Context, id int64) (IPTVChannel, bool, error) {
	row := r.conn.QueryRowContext(ctx, `SELECT `+channelCols+` FROM iptv_channels WHERE id = $1`, id)
	c, err := scanChannel(row)
	if err == sql.ErrNoRows {
		return IPTVChannel{}, false, nil
	}
	if err != nil {
		return IPTVChannel{}, false, err
	}
	return c, true, nil
}

// IPTVGroups возвращает группы каналов с числом каналов в каждой. Составные группы («Comedy;Movies»)
// разбиваются по «;» — иначе в интерфейсе сотня чипов вида «Animation;Classic;Family;Kids».
// Считаем по тем же каналам, что отдаёт ListIPTVChannels: дубли счётчик не удваивают,
// а канал из нескольких категорий (grp_all — см. MergeIPTVChannelGroups) виден в каждой.
func (r *Repo) IPTVGroups(ctx context.Context, playlistID int64) ([]IPTVGroup, error) {
	where := " WHERE COALESCE(NULLIF(grp_all, ''), grp) <> ''"
	args := []any{}
	if playlistID > 0 {
		args = append(args, playlistID)
		where += " AND playlist_id = $1"
	} else {
		where += " AND " + enabledPlaylistCond
	}
	q := `WITH d AS (
		SELECT DISTINCT ON (` + channelKeyExpr + `) COALESCE(NULLIF(grp_all, ''), grp) AS grp
		FROM iptv_channels` + where + ` ORDER BY ` + channelKeyExpr + `, quality DESC, id
	)
	SELECT trim(g) AS name, count(*) AS n FROM (
		SELECT unnest(string_to_array(grp, ';')) AS g FROM d
	) t WHERE trim(g) <> '' GROUP BY trim(g) ORDER BY n DESC, name`
	rows, err := r.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IPTVGroup
	for rows.Next() {
		var g IPTVGroup
		if err := rows.Scan(&g.Name, &g.Count); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetIPTVPlaylistEnabled включает/выключает плейлист: выключенный остаётся в базе (каналы и программа целы),
// но в списке каналов не участвует и фоновой синхронизацией не обновляется (см. syncAll).
func (r *Repo) SetIPTVPlaylistEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := r.conn.ExecContext(ctx,
		`UPDATE iptv_playlists SET enabled = $2, updated_at = now() WHERE id = $1`, id, enabled)
	if err != nil {
		return fmt.Errorf("переключение плейлиста %d: %w", id, err)
	}
	return nil
}

// MergeIPTVChannelGroups пересчитывает объединённые категории каналов (grp_all): один канал лежит
// в разных плейлистах и категориях («Моя Планета» — и в общих, и в «Природа и отдых»), и в списке
// он должен находиться в чипе любой из них. Вызывается после синка и после удаления плейлиста.
func (r *Repo) MergeIPTVChannelGroups(ctx context.Context) (int64, error) {
	res, err := r.conn.ExecContext(ctx, `
		UPDATE iptv_channels c
		SET grp_all = COALESCE(m.g, '')
		FROM (
			SELECT dedup_key, string_agg(DISTINCT grp, ';') AS g
			FROM iptv_channels WHERE dedup_key <> '' AND grp <> ''
			GROUP BY dedup_key
		) m
		WHERE c.dedup_key = m.dedup_key AND c.grp_all IS DISTINCT FROM COALESCE(m.g, '')`)
	if err != nil {
		return 0, fmt.Errorf("merge iptv groups: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CountIPTVChannels возвращает число каналов без дублей (по всем плейлистам или по одному).
func (r *Repo) CountIPTVChannels(ctx context.Context, playlistID int64) (int, error) {
	var n int
	q := `SELECT count(DISTINCT ` + channelKeyExpr + `) FROM iptv_channels`
	args := []any{}
	if playlistID > 0 {
		q += " WHERE playlist_id = $1"
		args = append(args, playlistID)
	}
	err := r.conn.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// IPTVChannelCounts — число каналов БЕЗ дублей по каждому плейлисту. Дедупликация та же, что в
// ListIPTVChannels, поэтому в панели плейлистов число совпадает с числом карточек на экране,
// а playlist.channel_count — это число записей плейлиста (варианты SD/HD/«Архив» одного канала
// считаются в нём отдельно).
func (r *Repo) IPTVChannelCounts(ctx context.Context) (map[int64]int, error) {
	rows, err := r.conn.QueryContext(ctx,
		`SELECT playlist_id, count(DISTINCT `+channelKeyExpr+`) FROM iptv_channels GROUP BY playlist_id`)
	if err != nil {
		return nil, fmt.Errorf("число каналов по плейлистам: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var (
			id int64
			n  int
		)
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// nameKeyExpr — ключ НАЗВАНИЯ в dedup_key: вторая часть ключа канала после «|» (см. iptv.ChannelKey);
// без «|» ключом названия является весь dedup_key — так выглядит канал без tvg-id.
const nameKeyExpr = `CASE WHEN position('|' IN dedup_key) > 0
	THEN split_part(dedup_key, '|', 2) ELSE dedup_key END`

// IPTVDuplicateNameKeys возвращает ключи названий, которые в базе носят НЕСКОЛЬКО разных каналов:
// по ним видно, что одного названия для списка мало («National Geographic» вещает в Болгарии,
// Чехии, Венгрии… — к таким названиям интерфейс дописывает страну вещания).
// Учитываются только включённые плейлисты: выключенный в списке не виден и не должен заставлять уточнять чужие названия.
func (r *Repo) IPTVDuplicateNameKeys(ctx context.Context) (map[string]struct{}, error) {
	q := `SELECT nk FROM (
		SELECT DISTINCT ON (dedup_key) ` + nameKeyExpr + ` AS nk
		FROM iptv_channels WHERE dedup_key <> '' AND ` + enabledPlaylistCond + `
	) t WHERE nk <> '' GROUP BY nk HAVING count(*) > 1`
	rows, err := r.conn.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("повторяющиеся названия каналов: %w", err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = struct{}{}
	}
	return out, rows.Err()
}

// SetIPTVChannelEPGKeys проставляет ключи сопоставления каналов с XMLTV (ext_id → ключ программы).
func (r *Repo) SetIPTVChannelEPGKeys(ctx context.Context, playlistID int64, keys map[string]string) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `UPDATE iptv_channels SET epg_key = $3 WHERE playlist_id = $1 AND ext_id = $2`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for extID, key := range keys {
		if _, err := stmt.ExecContext(ctx, playlistID, extID, key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReplaceIPTVPrograms заменяет программу одного канала в окне [from, to):
// старые записи окна удаляются, новые вставляются (повторный синк не плодит дубли).
func (r *Repo) ReplaceIPTVPrograms(ctx context.Context, chKey string, progs []IPTVProgram) error {
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM iptv_programs WHERE ch_key = $1`, chKey); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO iptv_programs (ch_key, start_at, stop_at, title, descr, category)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (ch_key, start_at) DO UPDATE SET
			stop_at = EXCLUDED.stop_at, title = EXCLUDED.title,
			descr = EXCLUDED.descr, category = EXCLUDED.category`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, p := range progs {
		if _, err := stmt.ExecContext(ctx, chKey, p.Start, p.Stop, p.Title, p.Desc, p.Category); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListIPTVPrograms возвращает программу канала в окне [from, to).
func (r *Repo) ListIPTVPrograms(ctx context.Context, chKey string, from, to time.Time, limit int) ([]IPTVProgram, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.conn.QueryContext(ctx, `
		SELECT ch_key, start_at, stop_at, title, descr, category
		FROM iptv_programs
		WHERE ch_key = $1 AND stop_at > $2 AND start_at < $3
		ORDER BY start_at LIMIT $4`, chKey, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPrograms(rows)
}

// ListIPTVProgramsForKeys возвращает программы сразу для нескольких ключей (для колонки «сейчас/далее»).
func (r *Repo) ListIPTVProgramsForKeys(ctx context.Context, keys []string, from, to time.Time) (map[string][]IPTVProgram, error) {
	out := make(map[string][]IPTVProgram, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := r.conn.QueryContext(ctx, `
		SELECT ch_key, start_at, stop_at, title, descr, category
		FROM iptv_programs
		WHERE ch_key = ANY($1) AND stop_at > $2 AND start_at < $3
		ORDER BY ch_key, start_at`, keys, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	progs, err := scanPrograms(rows)
	if err != nil {
		return nil, err
	}
	for _, p := range progs {
		out[p.Key] = append(out[p.Key], p)
	}
	return out, nil
}

// PruneIPTVPrograms удаляет передачи, закончившиеся раньше указанного времени.
func (r *Repo) PruneIPTVPrograms(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.conn.ExecContext(ctx, `DELETE FROM iptv_programs WHERE stop_at < $1`, before)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func scanPrograms(rows *sql.Rows) ([]IPTVProgram, error) {
	var out []IPTVProgram
	for rows.Next() {
		var p IPTVProgram
		if err := rows.Scan(&p.Key, &p.Start, &p.Stop, &p.Title, &p.Desc, &p.Category); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
