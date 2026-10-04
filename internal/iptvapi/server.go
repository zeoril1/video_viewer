// Package iptvapi — HTTP-сервис IPTV: список каналов, телепрограмма (EPG),
// управление плейлистами (только админ) и воспроизведение live-потоков.
//
//	GET /api/iptv/play/{id}.m3u8  — плейлист канала
//	GET /api/iptv/seg/{token}     — сегменты/вложенные плейлисты (прокси)
//	GET /api/iptv/live/{id}/{f}   — сегменты, сгенерированные ffmpeg
//
// Креды провайдера не попадают в клиент, CORS не мешает, а MPEG-TS
// перепаковывается в HLS на сервере.
package iptvapi

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zeoril1/video_viewer/internal/authn"
	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/iptv"
)

// Config — зависимости сервиса IPTV.
type Config struct {
	DB *db.Repo // общая БД

	// DataDir — каталог для временных HLS-сессий ffmpeg (live-каналы в TS).
	DataDir string
	// MaxSessions — сколько live-сессий ffmpeg держать одновременно.
	MaxSessions int
	// SessionIdle — через сколько без запросов гасить live-сессию.
	SessionIdle time.Duration
	// FFmpegPath — путь к ffmpeg ("" — искать в PATH).
	FFmpegPath string
	// SyncInterval — период фонового обновления плейлистов (0 — только вручную).
	SyncInterval time.Duration
	// EPGInterval — период фонового обновления телепрограммы.
	EPGInterval time.Duration
	// RefURL — справочник каналов iptv-org (родные названия); пусто — стандартный.
	RefURL string
	// Context — родительский контекст приложения (для фоновых задач).
	Context context.Context
}

// Server — состояние сервиса IPTV.
type Server struct {
	cfg    Config
	live   *liveManager
	refs   *refCache
	selfID string
}

// NewServer собирает HTTP-обработчики. Возвращаемая функция останавливает
// фоновые задачи и live-сессии (graceful shutdown).
func NewServer(cfg Config) (http.Handler, func()) {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = 8
	}
	if cfg.SessionIdle <= 0 {
		cfg.SessionIdle = 60 * time.Second
	}
	s := &Server{cfg: cfg, live: newLiveManager(cfg), refs: newRefCache(cfg.RefURL, cfg.DataDir)}

	mux := http.NewServeMux()

	// ---- Каналы и программа ----
	mux.HandleFunc("GET /api/iptv/channels", s.handleChannels)
	mux.HandleFunc("GET /api/iptv/epg", s.handleEPG)
	mux.HandleFunc("GET /api/iptv/guide", s.handleGuide)
	mux.HandleFunc("GET /api/iptv/archive/{id}", s.handleArchive)
	// Экспорт плейлиста со ссылками на наш прокси (для VLC, телефонов):
	// креды провайдера и здесь не раскрываются.
	mux.HandleFunc("GET /api/iptv/export.m3u", s.handleExportM3U)

	// ---- Воспроизведение ----
	// Шаблон именно «/play/»: мультиплексор Go 1.22 требует, чтобы {id}
	// занимал сегмент целиком, поэтому «{id}.m3u8» паникует при регистрации.
	mux.HandleFunc("GET /api/iptv/play/", s.handlePlay)
	mux.HandleFunc("GET /api/iptv/seg/{token}", s.handleSegment)
	mux.HandleFunc("GET /api/iptv/live/{id}/{sid}/{name}", s.handleLiveFile)

	// ---- Управление плейлистами (только админ) ----
	mux.HandleFunc("GET /api/iptv/playlists", s.handleListPlaylists)
	mux.HandleFunc("POST /api/iptv/playlists", s.handleSavePlaylist)
	mux.HandleFunc("DELETE /api/iptv/playlists/{id}", s.handleDeletePlaylist)
	mux.HandleFunc("POST /api/iptv/playlists/{id}/sync", s.handleSyncPlaylist)
	mux.HandleFunc("POST /api/iptv/playlists/{id}/enabled", s.handleSetPlaylistEnabled)
	mux.HandleFunc("POST /api/iptv/epg/sync", s.handleSyncEPG)

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	stop := s.startBackground()
	return httpx.LogMiddleware(mux), stop
}

// ---- Каналы ----

// channelView — канал для клиента. Внутренние поля (stream_url, креды, UA)
// наружу не отдаются: браузер не должен знать адрес провайдера.
type channelView struct {
	CatchupDays int    `json:"catchup_days"`
	ID          int64  `json:"id"`
	PlaylistID  int64  `json:"playlist_id"`
	Name        string `json:"name"`
	// NameOriginal — исходное название из плейлиста (латиница, пометки
	// качества); показывается в подсказке, если отличается от имени интерфейса.
	NameOriginal string     `json:"name_original,omitempty"`
	Group        string     `json:"group,omitempty"`
	Logo         string     `json:"logo,omitempty"`
	Num          int        `json:"num"`
	HasEPG       bool       `json:"has_epg"`
	Now          *programme `json:"now,omitempty"`
	Next         *programme `json:"next,omitempty"`
	// PlayURL — ссылка на HLS канала (относительная: клиент сам добавит хост).
	PlayURL string `json:"play_url"`
}

type programme struct {
	Title    string `json:"title"`
	Desc     string `json:"desc,omitempty"`
	Category string `json:"category,omitempty"`
	Start    string `json:"start"` // RFC3339
	Stop     string `json:"stop"`
}

func programmeView(p db.IPTVProgram) programme {
	return programme{
		Title:    p.Title,
		Desc:     p.Desc,
		Category: p.Category,
		Start:    p.Start.Format(time.RFC3339),
		Stop:     p.Stop.Format(time.RFC3339),
	}
}

// handleChannels — GET /api/iptv/channels?playlist=&group=&q=&limit=&offset=
// Возвращает плейлисты, группы, каналы и (если есть программа) «сейчас/далее».
func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	playlistID := atoi64(q.Get("playlist"))
	group := q.Get("group")
	search := q.Get("q")
	limit := atoiOr(q.Get("limit"), 0)
	offset := atoiOr(q.Get("offset"), 0)

	channels, err := s.cfg.DB.ListIPTVChannels(ctx, playlistID, group, search, limit, offset)
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	groups, err := s.cfg.DB.IPTVGroups(ctx, playlistID)
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	playlists, err := s.cfg.DB.ListIPTVPlaylists(ctx)
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Программа для видимых каналов: одним запросом на все ключи.
	keys := make([]string, 0, len(channels))
	for _, c := range channels {
		if c.EPGKey != "" {
			keys = append(keys, c.EPGKey)
		}
	}
	// Время — сервисное (по внешним серверам, см. clock.go): системные часы
	// могут уйти, и тогда «сейчас/далее» не совпало бы с программой.
	now := s.now()
	progs, err := s.cfg.DB.ListIPTVProgramsForKeys(ctx, keys, now.Add(-3*time.Hour), now.Add(24*time.Hour))
	if err != nil {
		log.Printf("iptv: программы каналов: %v", err)
		progs = nil
	}

	namer := s.newChannelNamer(ctx)
	views := make([]channelView, 0, len(channels))
	for _, c := range channels {
		name := iptv.DisplayName(c.Name, c.NameRU)
		v := channelView{
			ID:          c.ID,
			PlaylistID:  c.PlaylistID,
			Name:        namer.qualify(name, c),
			Group:       c.Group,
			Logo:        c.Logo,
			Num:         c.Num,
			CatchupDays: c.CatchupDays,
			PlayURL:     "/api/iptv/play/" + strconv.FormatInt(c.ID, 10) + ".m3u8",
		}
		// Исходное имя — в подсказку, если отличается от имени интерфейса.
		if orig := strings.TrimSpace(c.Name); orig != "" && orig != name {
			v.NameOriginal = orig
		}
		if list := progs[c.EPGKey]; len(list) > 0 {
			v.Now, v.Next = nowNext(list, now)
			v.HasEPG = v.Now != nil || v.Next != nil
		}
		views = append(views, v)
	}

	type playlistView struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		// Channels — число ЗАПИСЕЙ плейлиста, Visible — каналов без дублей
		// (совпадает со списком карточек): в M3U один канал идёт несколькими
		// потоками (SD/HD/«Архив»), и по записям их 561, а каналов — 415.
		Channels  int    `json:"channels"`
		Visible   int    `json:"visible"`
		Kind      string `json:"kind"`
		HasEPG    bool   `json:"has_epg"`
		LastSync  string `json:"last_sync,omitempty"`
		LastError string `json:"last_error,omitempty"`
	}
	// Число каналов без дублей — одним запросом на все плейлисты.
	counts, err := s.cfg.DB.IPTVChannelCounts(ctx)
	if err != nil {
		log.Printf("iptv: число каналов по плейлистам: %v", err)
		counts = nil
	}
	pv := make([]playlistView, 0, len(playlists))
	for _, p := range playlists {
		item := playlistView{
			ID: p.ID, Name: p.Name, Channels: p.ChannelCount, Visible: counts[p.ID], Kind: p.Kind,
			HasEPG: p.EPGURL != "", LastError: p.LastError,
		}
		if item.Visible == 0 {
			item.Visible = item.Channels // старая база/пустой плейлист — лучше записей, чем нуля
		}
		if !p.LastSync.IsZero() {
			item.LastSync = p.LastSync.Format(time.RFC3339)
		}
		pv = append(pv, item)
	}

	authn.WriteJSON(w, map[string]any{
		"playlists": pv,
		"groups":    groups,
		"channels":  views,
		// now — сервисное время (UTC): клиент считает по нему ход передачи,
		// чтобы системные часы браузера не сдвигали «сейчас».
		"now": now.Format(time.RFC3339),
	})
}

// nowNext выбирает текущую и следующую передачу из окна.
func nowNext(list []db.IPTVProgram, now time.Time) (cur, next *programme) {
	for i := range list {
		p := list[i]
		if !p.Start.After(now) && p.Stop.After(now) {
			v := programmeView(p)
			cur = &v
			// Следующая — первая, что начнётся после текущей.
			for j := i + 1; j < len(list); j++ {
				if list[j].Start.After(now) {
					nv := programmeView(list[j])
					next = &nv
					break
				}
			}
			return cur, next
		}
		if p.Start.After(now) {
			v := programmeView(p)
			return nil, &v
		}
	}
	return nil, nil
}

// handleEPG — GET /api/iptv/epg?channel=<id>&hours=6
// Телепрограмма канала: прошедшие 2 часа + указанное окно вперёд.
func (s *Server) handleEPG(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.URL.Query().Get("channel"))
	if id <= 0 {
		http.Error(w, "missing channel", http.StatusBadRequest)
		return
	}
	ch, ok, err := s.cfg.DB.GetIPTVChannel(r.Context(), id)
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	hours := atoiOr(r.URL.Query().Get("hours"), 6)
	if hours <= 0 || hours > 48 {
		hours = 6
	}
	now := s.now()
	var list []db.IPTVProgram
	if ch.EPGKey != "" {
		list, err = s.cfg.DB.ListIPTVPrograms(r.Context(), ch.EPGKey, now.Add(-2*time.Hour), now.Add(time.Duration(hours)*time.Hour), 200)
		if err != nil {
			http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	out := make([]programme, 0, len(list))
	for _, p := range list {
		out = append(out, programmeView(p))
	}
	authn.WriteJSON(w, map[string]any{
		"channel":  map[string]any{"id": ch.ID, "name": ch.Name, "group": ch.Group, "logo": ch.Logo},
		"has_epg":  ch.EPGKey != "",
		"programs": out,
		"now":      now.Format(time.RFC3339),
	})
}

// handleExportM3U — GET /api/iptv/export.m3u[?playlist=&group=]
// Плейлист со ссылками на наш прокси для внешних плееров (VLC/телефон);
// креды провайдера не раскрываются.
func (s *Server) handleExportM3U(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	playlistID := atoi64(r.URL.Query().Get("playlist"))
	group := r.URL.Query().Get("group")
	channels, err := s.cfg.DB.ListIPTVChannels(ctx, playlistID, group, "", 5000, 0)
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	base := publicBase(r)
	namer := s.newChannelNamer(ctx)
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, c := range channels {
		b.WriteString("#EXTINF:-1")
		if c.EPGID != "" {
			b.WriteString(` tvg-id="` + escapeAttr(c.EPGID) + `"`)
		}
		if c.Logo != "" {
			b.WriteString(` tvg-logo="` + escapeAttr(c.Logo) + `"`)
		}
		if c.Group != "" {
			b.WriteString(` group-title="` + escapeAttr(c.Group) + `"`)
		}
		name := namer.qualify(iptv.DisplayName(c.Name, c.NameRU), c)
		if name == "" {
			name = c.Group
		}
		b.WriteString("," + name + "\n")
		b.WriteString(base + "/api/iptv/play/" + strconv.FormatInt(c.ID, 10) + ".m3u8\n")
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="iptv.m3u"`)
	_, _ = w.Write([]byte(b.String()))
}

// publicBase восстанавливает внешний базовый адрес запроса
// (шлюз проксирует с сохранением Host, схему даёт X-Forwarded-Proto).
func publicBase(r *http.Request) string {
	return httpx.PublicBase(r)
}

func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, `"`, "")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// ---- Управление плейлистами (админ) ----

// playlistJSON — вход/выход админского API (пароль наружу не отдаём).
type playlistJSON struct {
	ID        int64  `json:"id,omitempty"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	Username  string `json:"username"`
	Password  string `json:"password,omitempty"`
	EPGURL    string `json:"epg_url"`
	UserAgent string `json:"user_agent"`
	Referer   string `json:"referrer"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

func (s *Server) handleListPlaylists(w http.ResponseWriter, r *http.Request) {
	if _, ok := authn.RequireAdmin(s.cfg.DB, w, r); !ok {
		return
	}
	list, err := s.cfg.DB.ListIPTVPlaylists(r.Context())
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(list))
	// visible — каналов без дублей (совпадает со списком карточек), channels —
	// записей плейлиста (варианты SD/HD/«Архив» одного канала считаются отдельно).
	counts, err := s.cfg.DB.IPTVChannelCounts(r.Context())
	if err != nil {
		log.Printf("iptv: число каналов по плейлистам: %v", err)
		counts = nil
	}
	for _, p := range list {
		visible := counts[p.ID]
		if visible == 0 {
			visible = p.ChannelCount
		}
		item := map[string]any{
			"id": p.ID, "name": p.Name, "kind": p.Kind, "url": p.URL,
			"username": p.Username, "epg_url": p.EPGURL, "user_agent": p.UA,
			"referrer": p.Referer, "enabled": p.Enabled,
			"channels": p.ChannelCount, "visible": visible, "last_error": p.LastError,
		}
		if !p.LastSync.IsZero() {
			item["last_sync"] = p.LastSync.Format(time.RFC3339)
		}
		out = append(out, item)
	}
	authn.WriteJSON(w, map[string]any{"playlists": out})
}

func (s *Server) handleSavePlaylist(w http.ResponseWriter, r *http.Request) {
	if _, ok := authn.RequireAdmin(s.cfg.DB, w, r); !ok {
		return
	}
	var in playlistJSON
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	id, err := s.cfg.DB.SaveIPTVPlaylist(r.Context(), db.IPTVPlaylist{
		ID: in.ID, Name: in.Name, Kind: in.Kind, URL: in.URL,
		Username: in.Username, Password: in.Password, EPGURL: in.EPGURL,
		UA: in.UserAgent, Referer: in.Referer, Enabled: enabled,
	})
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Сразу тянем каналы: админ видит результат своих действий.
	if n, err := s.SyncPlaylist(r.Context(), id); err != nil {
		log.Printf("iptv: синк плейлиста %d: %v", id, err)
		if err := s.cfg.DB.SetIPTVPlaylistSync(r.Context(), id, 0, err.Error()); err != nil {
			log.Printf("iptv: запись ошибки синка: %v", err)
		}
		authn.WriteJSON(w, map[string]any{"id": id, "warning": err.Error()})
		return
	} else {
		log.Printf("iptv: плейлист %d синхронизирован, каналов %d", id, n)
	}
	authn.WriteJSON(w, map[string]any{"id": id})
}

func (s *Server) handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	if _, ok := authn.RequireAdmin(s.cfg.DB, w, r); !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	if id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := s.cfg.DB.DeleteIPTVPlaylist(r.Context(), id); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// У оставшихся каналов могли осиротеть категории (они были объединены с
	// группами каналов удалённого плейлиста).
	if _, err := s.cfg.DB.MergeIPTVChannelGroups(r.Context()); err != nil {
		log.Printf("iptv: объединение групп каналов: %v", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetPlaylistEnabled — POST /api/iptv/playlists/{id}/enabled
// {"enabled":false}: каналы выключенного плейлиста пропадают из выдачи, но
// сам плейлист и его креды остаются в базе.
func (s *Server) handleSetPlaylistEnabled(w http.ResponseWriter, r *http.Request) {
	if _, ok := authn.RequireAdmin(s.cfg.DB, w, r); !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	if id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.cfg.DB.SetIPTVPlaylistEnabled(r.Context(), id, in.Enabled); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	authn.WriteJSON(w, map[string]any{"id": id, "enabled": in.Enabled})
}

func (s *Server) handleSyncPlaylist(w http.ResponseWriter, r *http.Request) {
	if _, ok := authn.RequireAdmin(s.cfg.DB, w, r); !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	if id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	n, err := s.SyncPlaylist(r.Context(), id)
	if err != nil {
		_ = s.cfg.DB.SetIPTVPlaylistSync(r.Context(), id, 0, err.Error())
		http.Error(w, "sync: "+err.Error(), http.StatusBadGateway)
		return
	}
	authn.WriteJSON(w, map[string]any{"channels": n})
}

func (s *Server) handleSyncEPG(w http.ResponseWriter, r *http.Request) {
	if _, ok := authn.RequireAdmin(s.cfg.DB, w, r); !ok {
		return
	}
	playlists, err := s.cfg.DB.ListIPTVPlaylists(r.Context())
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	res := map[string]any{}
	for _, p := range playlists {
		if p.EPGURL == "" {
			continue
		}
		n, err := s.SyncEPG(r.Context(), p)
		if err != nil {
			log.Printf("iptv: программа плейлиста %d: %v", p.ID, err)
			res[p.Name] = "ошибка: " + err.Error()
			continue
		}
		res[p.Name] = n
	}
	authn.WriteJSON(w, map[string]any{"programs": res})
}
