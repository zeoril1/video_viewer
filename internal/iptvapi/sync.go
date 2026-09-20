package iptvapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/iptv"
)

// epgWindowBack/epgWindowForward — окно программы, которое держим в БД:
// немного назад (для «сейчас идёт») и на двое суток вперёд.
const (
	epgWindowBack    = 7 * 24 * time.Hour
	epgWindowForward = 48 * time.Hour
)

// SyncPlaylist обновляет каналы плейлиста (и его телепрограмму, если задан
// epg_url); возвращает число каналов.
func (s *Server) SyncPlaylist(ctx context.Context, id int64) (int, error) {
	pl, ok, err := s.cfg.DB.GetIPTVPlaylist(ctx, id)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("плейлист %d не найден", id)
	}
	chans, err := s.fetchPlaylistChannels(ctx, pl)
	if err != nil {
		_ = s.cfg.DB.SetIPTVPlaylistSync(ctx, id, 0, err.Error())
		return 0, err
	}
	if len(chans) == 0 {
		err := fmt.Errorf("плейлист пуст: %s", pl.URL)
		_ = s.cfg.DB.SetIPTVPlaylistSync(ctx, id, 0, err.Error())
		return 0, err
	}
	// Русские названия каналов и ключи отсева дублей считаем ДО записи в БД.
	named := s.prepareChannels(ctx, pl.ID, chans)
	added, removed, err := s.cfg.DB.ReplaceIPTVChannels(ctx, id, chans)
	if err != nil {
		_ = s.cfg.DB.SetIPTVPlaylistSync(ctx, id, 0, err.Error())
		return 0, err
	}
	// Категории одного канала из разных плейлистов объединяем (удаление могло
	// убрать часть групп) и считаем число каналов без дублей.
	if _, err := s.cfg.DB.MergeIPTVChannelGroups(ctx); err != nil {
		log.Printf("iptv: объединение групп каналов: %v", err)
	}
	uniq, err := s.cfg.DB.CountIPTVChannels(ctx, id)
	if err != nil {
		uniq = len(chans)
	}
	log.Printf("iptv: плейлист %q: каналов %d (+%d −%d), с русским названием %d, дублей схлопнуто %d",
		pl.Name, len(chans), added, removed, named, len(chans)-uniq)

	// Телепрограмма — отдельным шагом: её отсутствие не должно ломать каналы.
	if pl.EPGURL != "" {
		if n, err := s.SyncEPG(ctx, pl); err != nil {
			log.Printf("iptv: программа плейлиста %q: %v", pl.Name, err)
		} else {
			log.Printf("iptv: программа плейлиста %q: передач %d", pl.Name, n)
		}
	}
	if err := s.cfg.DB.SetIPTVPlaylistSync(ctx, id, len(chans), ""); err != nil {
		return len(chans), err
	}
	return len(chans), nil
}

// fetchPlaylistChannels получает каналы плейлиста: M3U-ссылка или Xtream.
func (s *Server) fetchPlaylistChannels(ctx context.Context, pl db.IPTVPlaylist) ([]db.IPTVChannel, error) {
	switch pl.Kind {
	case "xtream":
		hc := &http.Client{Timeout: 60 * time.Second}
		x := iptv.Xtream{BaseURL: pl.URL, Username: pl.Username, Password: pl.Password, UA: pl.UA, Referer: pl.Referer}
		list, err := x.LiveChannels(ctx, hc)
		if err != nil {
			return nil, err
		}
		return toDBChannels(list, pl), nil
	default: // m3u
		if strings.TrimSpace(pl.URL) == "" {
			return nil, fmt.Errorf("не задана ссылка на плейлист")
		}
		hc := &http.Client{Timeout: 120 * time.Second}
		data, err := fetchBytes(ctx, hc, pl.URL, pl.UA, pl.Referer)
		if err != nil {
			return nil, fmt.Errorf("скачивание плейлиста: %w", err)
		}
		list, err := iptv.ParseM3U(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			// Xtream get.php без type=m3u_plus возвращает JSON — даём понятную ошибку.
			return nil, fmt.Errorf("в плейлисте не найдено каналов (M3U-формат?)")
		}
		return toDBChannels(list, pl), nil
	}
}

// toDBChannels переводит каналы парсера в записи БД, подставляя общие
// заголовки плейлиста, если у канала своих нет.
//
// ext_id — ключ канала в плейлисте (UNIQUE(playlist_id, ext_id)), а в M3U им
// служит tvg-id, который НЕ уникален: один канал идёт несколькими потоками
// (SD/HD/«Архив») и все они несут один tvg-id. Без нумерации потоки затирали
// бы друг друга — в базе оставался последний из них (обычно «Архив»), а
// остальные каналы терялись. Дубли нумеруем («zvezda#2»), показ дублей всё
// равно схлопывает их по dedup_key и выбирает лучший вариант потока.
func toDBChannels(list []iptv.Channel, pl db.IPTVPlaylist) []db.IPTVChannel {
	out := make([]db.IPTVChannel, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, c := range list {
		ua, ref := channelHeaders(c.UA, c.Referer, pl.UA, pl.Referer)
		ext := strings.TrimSpace(c.ExtID)
		if ext != "" {
			base := ext
			for i := 2; seen[ext]; i++ {
				ext = base + "#" + strconv.Itoa(i)
			}
			seen[ext] = true
		}
		out = append(out, db.IPTVChannel{
			PlaylistID:  pl.ID,
			CatchupDays: c.CatchupDays, CatchupSource: c.CatchupSource, CatchupMode: c.CatchupMode,
			ExtID:     ext,
			Name:      strings.TrimSpace(c.Name),
			Group:     strings.TrimSpace(c.Group),
			Logo:      strings.TrimSpace(c.Logo),
			EPGID:     strings.TrimSpace(c.EPGID),
			StreamURL: c.URL,
			IsHLS:     c.IsHLS,
			UA:        ua,
			Referer:   ref,
			Num:       c.Num,
		})
	}
	return out
}

// prepareChannels проставляет каналам русское название, ключ отсева дублей и
// оценку качества потока; возвращает число каналов с русским названием.
// Русские названия берём из справочника iptv-org по tvg-id (плейлисты
// называют каналы латиницей); иностранным каналам остаётся исходное имя.
func (s *Server) prepareChannels(ctx context.Context, playlistID int64, chans []db.IPTVChannel) int {
	var ref *iptv.RefIndex
	if s.refs != nil {
		ref = s.refs.get(ctx, &http.Client{Timeout: 120 * time.Second})
	}
	named := 0
	for i := range chans {
		c := &chans[i]
		// tvg-id лежит в epg_id (в ext_id — только если tvg_id нет).
		id := c.EPGID
		if strings.TrimSpace(id) == "" {
			id = c.ExtID
		}
		c.NameRU = ref.RussianName(id)
		if c.NameRU != "" {
			named++
		}
		// Ключ дублей — по названию для показа: «Мир HD (720p)» и «Mir (576p)»
		// одного канала должны совпасть (оба станут «Мир»).
		c.DedupKey = iptv.ChannelKey(id, iptv.DisplayName(c.Name, c.NameRU))
		if c.DedupKey == "" {
			// Названия нет — оставляем канал уникальным вместо схлопывания всех.
			c.DedupKey = fmt.Sprintf("#%d:%s", playlistID, c.ExtID)
		}
		c.Quality = iptv.StreamQuality(c.Name, c.IsHLS)
	}
	return named
}

// SyncEPG обновляет телепрограмму плейлиста: сопоставляет каналы с XMLTV,
// сохраняет программу по нашим каналам, возвращает число сохранённых передач.
//
// Два прохода по XMLTV-потоку (каналы → передачи только нужных каналов):
// программа iptvx.one — ~70 МиБ в архиве и ~560 МиБ распакованного XMLTV,
// целиком в память её брать нельзя.
func (s *Server) SyncEPG(ctx context.Context, pl db.IPTVPlaylist) (int, error) {
	if strings.TrimSpace(pl.EPGURL) == "" {
		return 0, nil
	}
	hc := &http.Client{Timeout: 180 * time.Second}
	open := func() (io.ReadCloser, error) {
		rc, err := fetchStream(ctx, hc, pl.EPGURL, pl.UA, pl.Referer)
		if err != nil {
			return nil, fmt.Errorf("скачивание программы: %w", err)
		}
		return rc, nil
	}

	// Первый проход — только каналы XMLTV (передачи отбрасываем фильтром).
	rc, err := open()
	if err != nil {
		return 0, err
	}
	epgChans, _, err := iptv.ParseXMLTVFilter(rc, func(string) bool { return false })
	rc.Close()
	if err != nil {
		return 0, err
	}
	if len(epgChans) == 0 {
		return 0, fmt.Errorf("в программе нет каналов")
	}

	ourChannels, err := s.cfg.DB.ListIPTVChannels(ctx, pl.ID, "", "", 5000, 0)
	if err != nil {
		return 0, err
	}

	// Индексы для сопоставления: по id (точно), по нормализованному имени
	// канала и по упрощённому ключу (без «.ru»/«@SD»/качества).
	byID := make(map[string]string, len(epgChans))
	byNorm := make(map[string]string, len(epgChans)*2)
	byBase := make(map[string]string, len(epgChans)*2)
	add := func(m map[string]string, s, id string) {
		if s == "" {
			return
		}
		if _, exists := m[s]; !exists {
			m[s] = id
		}
	}
	for _, c := range epgChans {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		byID[strings.ToLower(id)] = id
		for _, s := range append([]string{id, c.Name}, c.Aliases...) {
			add(byNorm, iptv.NormalizeName(s), id)
			add(byBase, iptv.BaseKey(s), id)
		}
	}

	keys := make(map[string]string, len(ourChannels)) // ext_id → ключ XMLTV
	wanted := make(map[string]bool, len(ourChannels))
	for _, c := range ourChannels {
		if key := matchEPG(c, byID, byNorm, byBase); key != "" {
			keys[c.ExtID] = key
			wanted[key] = true
		}
	}
	if len(keys) == 0 {
		return 0, fmt.Errorf("ни один канал не сопоставлен с телепрограммой (id/названия не совпали)")
	}
	if err := s.cfg.DB.SetIPTVChannelEPGKeys(ctx, pl.ID, keys); err != nil {
		return 0, err
	}

	// Второй проход — передачи только наших каналов.
	rc, err = open()
	if err != nil {
		return 0, err
	}
	_, progs, err := iptv.ParseXMLTVFilter(rc, func(channelID string) bool {
		return wanted[channelID]
	})
	rc.Close()
	if err != nil {
		return 0, err
	}

	now := s.now()
	from, to := now.Add(-epgWindowBack), now.Add(epgWindowForward)
	byKey := make(map[string][]db.IPTVProgram, len(wanted))
	for _, p := range progs {
		if p.Stop.Before(from) || p.Start.After(to) {
			continue
		}
		byKey[p.ChannelID] = append(byKey[p.ChannelID], db.IPTVProgram{
			Key: p.ChannelID, Start: p.Start, Stop: p.Stop,
			Title: p.Title, Desc: p.Desc, Category: p.Category,
		})
	}
	saved := 0
	for key, list := range byKey {
		if err := s.cfg.DB.ReplaceIPTVPrograms(ctx, key, list); err != nil {
			return saved, err
		}
		saved += len(list)
	}
	// Чистим совсем старые передачи, чтобы таблица не росла.
	if n, err := s.cfg.DB.PruneIPTVPrograms(ctx, now.Add(-epgWindowBack)); err == nil && n > 0 {
		log.Printf("iptv: удалено устаревших передач: %d", n)
	}
	return saved, nil
}

// matchEPG подбирает ключ XMLTV для нашего канала: точный tvg-id, затем
// нормализованное название, затем упрощённый ключ («.ru»/«@SD»/качество — ими
// различаются, например, iptv-org «MatchTV.ru@SD» и open-epg «Матч ТВ.ru»).
func matchEPG(c db.IPTVChannel, byID, byNorm, byBase map[string]string) string {
	if id := strings.ToLower(strings.TrimSpace(c.EPGID)); id != "" {
		if key, ok := byID[id]; ok {
			return key
		}
	}
	name, epgID := iptv.NormalizeName(c.Name), iptv.NormalizeName(c.EPGID)
	for _, k := range []string{name, epgID} {
		if k == "" {
			continue
		}
		if key, ok := byNorm[k]; ok {
			return key
		}
	}
	for _, k := range []string{iptv.BaseKey(c.Name), iptv.BaseKey(c.EPGID)} {
		if k == "" {
			continue
		}
		if key, ok := byBase[k]; ok {
			return key
		}
	}
	return ""
}

// ---- Фоновые задачи ----

// startBackground запускает периодическое обновление плейлистов и программы
// и очистку live-сессий; возвращает функцию остановки.
func (s *Server) startBackground() func() {
	// context.WithCancel(nil) паникует — родительский контекст подставляем
	// явно: сервис должен подниматься и без него (например, в тестах).
	parent := s.cfg.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)

	done := make(chan struct{}, 3)
	// Первый синк — сразу после старта (не блокируя сервер).
	if s.cfg.SyncInterval > 0 {
		go func() {
			defer func() { done <- struct{}{} }()
			time.Sleep(5 * time.Second)
			s.syncAll(ctx)
			tick(ctx, s.cfg.SyncInterval, s.syncAll)
		}()
	} else {
		done <- struct{}{}
	}
	if s.cfg.EPGInterval > 0 {
		go func() {
			defer func() { done <- struct{}{} }()
			tick(ctx, s.cfg.EPGInterval, s.syncEPGAll)
		}()
	} else {
		done <- struct{}{}
	}
	go func() {
		defer func() { done <- struct{}{} }()
		s.live.runCleanup(ctx)
	}()

	return func() {
		cancel()
		for i := 0; i < 3; i++ {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
		}
		s.live.stopAll()
	}
}

// tick вызывает fn каждые d до отмены контекста.
func tick(ctx context.Context, d time.Duration, fn func(context.Context)) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

// syncAll обновляет каналы всех включённых плейлистов.
func (s *Server) syncAll(ctx context.Context) {
	playlists, err := s.cfg.DB.ListIPTVPlaylists(ctx)
	if err != nil {
		log.Printf("iptv: список плейлистов: %v", err)
		return
	}
	for _, p := range playlists {
		if !p.Enabled {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		n, err := s.SyncPlaylist(cctx, p.ID)
		cancel()
		if err != nil {
			log.Printf("iptv: синк плейлиста %q: %v", p.Name, err)
			continue
		}
		log.Printf("iptv: плейлист %q обновлён: %d каналов", p.Name, n)
	}
}

// syncEPGAll обновляет телепрограмму всех плейлистов с epg_url.
func (s *Server) syncEPGAll(ctx context.Context) {
	playlists, err := s.cfg.DB.ListIPTVPlaylists(ctx)
	if err != nil {
		return
	}
	for _, p := range playlists {
		if !p.Enabled || p.EPGURL == "" {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		n, err := s.SyncEPG(cctx, p)
		cancel()
		if err != nil {
			log.Printf("iptv: программа плейлиста %q: %v", p.Name, err)
			continue
		}
		log.Printf("iptv: программа плейлиста %q обновлена: %d передач", p.Name, n)
	}
}

// fileExists — есть ли файл (используется для ожидания плейлиста ffmpeg).
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
