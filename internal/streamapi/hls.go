package streamapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// audioTrack — звуковая дорожка из ffprobe (Ordinal — номер среди аудио,
// он же track в /hls.m3u8; маппить по Index нельзя: у MKV индекс 0 — видео).
type audioTrack struct {
	Index    int    `json:"index"`
	Ordinal  int    `json:"ordinal"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
}

// subtitleTrack — субтитр-дорожка из ffprobe (Ordinal — subs в /hls.m3u8).
// ffmpeg 6.1 пишет в HLS только ОДИН субтитр, поэтому его выбор перезапускает сессию.
type subtitleTrack struct {
	Index    int    `json:"index"`
	Ordinal  int    `json:"ordinal"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
}

func selectSubtitle(items []subtitleTrack, ordinal int) (subtitleTrack, error) {
	for _, item := range items {
		if item.Ordinal == ordinal && isTextSubtitleCodec(item.Codec) {
			return item, nil
		}
	}
	return subtitleTrack{}, fmt.Errorf("subtitle track %d not found", ordinal)
}

// hlsSession — запущенный ffmpeg-процесс HLS (видео копируется или перекодируется, звук — в AAC).
type hlsSession struct {
	id       string
	magnet   string
	file     int // индекс файла в торренте (серия); -1 — авто
	track    int
	subs     int     // выбранная субтитр-дорожка (-1 — без субтитров)
	start    float64 // позиция в секундах, с которой начата сессия (перемотка)
	quality  string  // "source", "2160", "1080", "720", "480"
	dir      string
	playlist string
	cmd      *exec.Cmd
	lastUsed time.Time
	segments int // счётчик отданных сегментов (для диагностики)

	// subsLabel — имя субтитр-дорожки для master-плейлиста (#EXT-X-MEDIA NAME).
	subsLabel string

	// exited/exitErr фиксирует горутина-наблюдатель: servePlaylist отдаёт
	// ошибку сразу, не ожидая плейлист от упавшего ffmpeg (нет пиров и т.п.).
	exited  bool
	exitErr error
}

// maxProbeCache — лимит записей кэша ffprobe (ключ = id|magnet|file), иначе кэш растёт бесконечно.
const maxProbeCache = 256

type hlsManager struct {
	mu         sync.Mutex
	sessions   map[string]*hlsSession
	launches   map[string]uint64
	nextLaunch uint64
	selfBase   string
	dataDir    string
	probeCache map[string]probeResult
	probeOrder []string // порядок вставки ключей probeCache (для эвикции LRU-подобной)
}

// probeResult — результат ffprobe торрента (дорожки + длительность + видео).
type probeResult struct {
	Tracks    []audioTrack
	Subtitles []subtitleTrack
	Duration  float64
	// VideoCodec — кодек видео (h264/hevc/av1/vp9/...); пусто — не определён.
	VideoCodec string
	// VideoHeight — вертикальное разрешение видео (0 — не определено).
	VideoHeight int
	// VideoStart — start_time видео (сек): у части MKV видео стартует позже
	// аудио (напр. 0.94с) — тогда поток начинаем с начала видео, иначе первые
	// секунды идёт только звук (см. ensure: -ss VideoStart + noaccurate_seek).
	VideoStart float64
	// Partial — лёгкий результат videoStartProbe (только VideoStart): tracks()
	// по нему делает полное ffprobe, иначе duration/дорожки были бы 0.
	Partial bool
}

func newHLSManager(selfBase string) *hlsManager {
	dir, err := os.MkdirTemp("", "video-viewer-hls")
	if err != nil {
		dir = os.TempDir()
	}
	return &hlsManager{
		sessions:   make(map[string]*hlsSession),
		launches:   make(map[string]uint64),
		selfBase:   selfBase,
		dataDir:    dir,
		probeCache: make(map[string]probeResult),
	}
}

// inputURL — URL внутреннего эндпоинта с сырым файлом (ffmpeg читает его с Range);
// file — индекс серии в торренте, -1 — авто.
func (m *hlsManager) inputURL(id, magnet string, file int) string {
	v := url.Values{}
	v.Set("magnet", magnet)
	if file >= 0 {
		v.Set("file", strconv.Itoa(file))
	}
	return m.selfBase + "/api/stream/" + url.PathEscape(id) + "?" + v.Encode()
}

// ffprobeNum — число из ffprobe: часть контейнеров отдаёт start_time строкой
// ("N/A" или "0.000000") — принимаем оба варианта ("N/A"/пусто → 0).
type ffprobeNum struct {
	Val float64
}

func (n *ffprobeNum) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" || s == "N/A" {
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil
		}
		n.Val = v
		return nil
	}
	return json.Unmarshal(b, &n.Val)
}

// tracks возвращает дорожки/субтитры/длительность файла торрента (ffprobe,
// кэш на сессию); file — индекс серии, -1 — авто (крупнейший видеофайл).
func (m *hlsManager) tracks(ctx context.Context, id, magnet string, file int) (probeResult, error) {
	key := fmt.Sprintf("%s|%s|file%d", id, magnet, file)
	m.mu.Lock()
	// Partial-результат videoStartProbe не годится как полная проба — зондируем целиком.
	if pr, ok := m.probeCache[key]; ok && !pr.Partial {
		m.mu.Unlock()
		log.Printf("tracks %s: из кэша (file=%d, дорожек=%d, video=%s/%dp)", id, file, len(pr.Tracks), pr.VideoCodec, pr.VideoHeight)
		return pr, nil
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "stream=index,codec_name,codec_type,height,start_time:stream_tags=language,title:format=duration",
		"-of", "json",
		m.inputURL(id, magnet, file),
	)
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		log.Printf("ffprobe %s failed: %v (stderr: %s)", id, err, strings.TrimSpace(string(stderr)))
		return probeResult{}, fmt.Errorf("ffprobe: %w", err)
	}
	var parsed struct {
		Streams []struct {
			Index     int        `json:"index"`
			Codec     string     `json:"codec_name"`
			Type      string     `json:"codec_type"`
			Height    int        `json:"height"`
			StartTime ffprobeNum `json:"start_time"`
			Tags      struct {
				Language string `json:"language"`
				Title    string `json:"title"`
			} `json:"tags"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return probeResult{}, fmt.Errorf("parse ffprobe: %w", err)
	}
	var (
		tracks      []audioTrack
		subtitles   []subtitleTrack
		videoCodec  string
		videoHeight int
		videoStart  float64
	)
	for _, s := range parsed.Streams {
		switch s.Type {
		case "video":
			if videoCodec == "" {
				videoCodec = s.Codec
				videoHeight = s.Height
				if s.StartTime.Val > 0 {
					videoStart = s.StartTime.Val
				}
			}
		case "audio":
			tracks = append(tracks, audioTrack{
				Index:    s.Index,
				Ordinal:  len(tracks),
				Codec:    s.Codec,
				Language: s.Tags.Language,
				Title:    s.Tags.Title,
			})
		case "subtitle":
			// Только текст: растровые (PGS/DVDSUB) в WebVTT не конвертируются.
			if isTextSubtitleCodec(s.Codec) {
				subtitles = append(subtitles, subtitleTrack{
					Index:    s.Index,
					Ordinal:  len(subtitles),
					Codec:    s.Codec,
					Language: s.Tags.Language,
					Title:    s.Tags.Title,
				})
			}
		}
	}
	duration, _ := strconv.ParseFloat(parsed.Format.Duration, 64)
	pr := probeResult{Tracks: tracks, Subtitles: subtitles, Duration: duration, VideoCodec: videoCodec, VideoHeight: videoHeight, VideoStart: videoStart}
	m.mu.Lock()
	// Эвикция: без неё кэш рос бы бесконечно (ключ — полная строка магнита).
	if len(m.probeCache) >= maxProbeCache {
		for _, k := range m.probeOrder {
			if len(m.probeCache) < maxProbeCache {
				break
			}
			delete(m.probeCache, k)
		}
	}
	m.probeCache[key] = pr
	m.probeOrder = append(m.probeOrder, key)
	// Ограничиваем и список порядка (могут накопиться дубликаты ключей).
	if len(m.probeOrder) > maxProbeCache*2 {
		m.probeOrder = append([]string{}, m.probeOrder[len(m.probeOrder)-maxProbeCache:]...)
	}
	m.mu.Unlock()
	log.Printf("tracks %s: %d дорожек, %d субтитров, duration=%.1fs, video=%s/%dp", id, len(tracks), len(subtitles), duration, videoCodec, videoHeight)
	return pr, nil
}

// isTextSubtitleCodec — кодеки, которые ffmpeg конвертирует в WebVTT; растровые
// (hdmv_pgs_subtitle/dvd_subtitle и т.п.) не поддерживаются.
func isTextSubtitleCodec(c string) bool {
	switch c {
	case "subrip", "srt", "ass", "ssa", "mov_text", "webvtt", "text", "subviewer", "subviewer1", "jacosub", "mpl2", "microdvd", "sami", "stl", "scc":
		return true
	}
	return false
}

// subtitleLabel — имя для master-плейлиста: title, иначе язык, иначе «Субтитры N».
func subtitleLabel(t subtitleTrack) string {
	if t.Title != "" {
		return t.Title
	}
	if t.Language != "" {
		return strings.ToUpper(t.Language)
	}
	return fmt.Sprintf("Субтитры %d", t.Ordinal+1)
}

func probeKey(id, magnet string, file int) string {
	return fmt.Sprintf("%s|%s|file%d", id, magnet, file)
}

// videoStartProbe — start_time видео (сек) из кэша или лёгким ffprobe (короткий
// таймаут); ok=false, если нет данных. Нужен, т.к. у части рипов видео стартует позже аудио.
func (m *hlsManager) videoStartProbe(ctx context.Context, id, magnet string, file int) (float64, bool) {
	key := probeKey(id, magnet, file)
	m.mu.Lock()
	if pr, ok := m.probeCache[key]; ok {
		m.mu.Unlock()
		return pr.VideoStart, pr.VideoStart > 0
	}
	m.mu.Unlock()

	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(pctx, "ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=start_time",
		"-of", "csv=p=0",
		m.inputURL(id, magnet, file),
	)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("ffprobe video_start %s: %v", id, err)
		return 0, false
	}
	vs, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || vs <= 0 {
		return 0, false
	}

	// Кэшируем: имеющуюся запись дополняем, не затирая Partial-флаг полной пробы.
	m.mu.Lock()
	if pr, ok := m.probeCache[key]; ok {
		if pr.VideoStart == 0 {
			pr.VideoStart = vs
			m.probeCache[key] = pr
		}
	} else {
		// Лёгкий результат (Partial): tracks() не примет его за полную пробу.
		m.probeCache[key] = probeResult{VideoStart: vs, Partial: true}
		m.probeOrder = append(m.probeOrder, key)
	}
	m.mu.Unlock()
	return vs, true
}

// ensure держит один ffmpeg на фильм и сессию просмотра: смена дорожки/серии/
// позиции перезапускает только процесс этого зрителя (без токена — старые клиенты).
func (m *hlsManager) ensure(ctx context.Context, id, magnet string, file, track, subs int, start float64, quality string, playback ...string) (*hlsSession, error) {
	key := hlsSessionKey(id, playback...)
	m.mu.Lock()
	generation := m.beginLaunchLocked(key)
	defer m.finishLaunch(key, generation)
	if ctx.Err() != nil {
		m.mu.Unlock()
		return nil, ctx.Err()
	}
	if s, ok := m.sessions[key]; ok {
		if s.magnet == magnet && s.file == file && s.track == track && s.subs == subs && s.start == start && s.quality == quality && !(s.exited && s.exitErr != nil) {
			s.lastUsed = time.Now()
			m.mu.Unlock()
			return s, nil
		}
		// Параметры изменились (серия/дорожка/субтитры/перемотка/качество) — перезапуск.
		log.Printf("hls: ensure %s: перезапуск (file=%d track=%d subs=%d start=%.0f quality=%s)", id, file, track, subs, start, quality)
		s.stop()
		delete(m.sessions, key)
	}
	// Лок снимаем ПЕРЕД пробой: videoStartProbe берёт m.mu сам, а мьютексы
	// в Go нереентерабельны (иначе вешались все HLS-запросы при start=0).
	m.mu.Unlock()

	// subs — номер в отфильтрованном списке текстовых дорожек API.
	// Маппим по глобальному Index: перед текстом в файле могут идти PGS/DVDSUB.
	subsLabel := ""
	subsMap := ""
	if subs >= 0 {
		pr, err := m.tracks(ctx, id, magnet, file)
		if err != nil {
			return nil, fmt.Errorf("probe subtitles: %w", err)
		}
		selected, err := selectSubtitle(pr.Subtitles, subs)
		if err != nil {
			return nil, err
		}
		subsLabel = subtitleLabel(selected)
		subsMap = fmt.Sprintf("0:%d", selected.Index)
	}

	// Зондируем ВНЕ лока: ffprobe может идти секунды и заблокировал бы HLS.
	vstart := 0.0
	if start <= 0 {
		if vs, ok := m.videoStartProbe(ctx, id, magnet, file); ok {
			vstart = vs
		}
	}

	dir, err := os.MkdirTemp(m.dataDir, "hls")
	if err != nil {
		return nil, err
	}
	playlist := filepath.Join(dir, "playlist.m3u8")
	segPattern := filepath.Join(dir, "seg_%05d.m4s")
	// Позиция старта: при перемотке — start; для свежего потока — начало видео,
	// если оно в исходнике стартует позже звука (adelay не годится: сдвинул бы
	// звук навсегда; -ss + noaccurate_seek ставит A/V в одну точку).
	seekStart := start
	if start <= 0 && vstart > 0.1 {
		seekStart = vstart
		log.Printf("hls: ensure %s: видео стартует с %.3fs — поток с этой позиции (A/V согласованы)", id, vstart)
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if seekStart > 0 {
		// Быстрый input-seek по HTTP-стриму (Range).
		args = append(args, "-ss", strconv.FormatFloat(seekStart, 'f', -1, 64))
		// ВАЖНО: при -c:v copy точный seek берёт видео с ключевого кадра, а звук —
		// ровно с seekStart (звук отстаёт на GOP). noaccurate_seek синхронизирует
		// A/V; при перекодировании видео точный seek корректен.
		if qualityHeight(quality) == 0 {
			args = append(args, "-noaccurate_seek")
		}
	}
	args = append(args,
		"-i", m.inputURL(id, magnet, file),
		"-map", "0:v:0",
	)
	// track — ПОРЯДКОВЫЙ номер аудио-потока (ordinal в /tracks): -map 0:a:N не
	// зависит от глобального индекса, а маппинг по индексу (у MKV 0 — видео)
	// давал два видео-потока без звука → bufferAppendError у hls.js.
	// track=-1 — аудио нет, не маппим.
	if track >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:a:%d", track))
	}
	// Субтитр (ordinal) → WebVTT: hls.js покажет дорожку только из master-плейлиста,
	// поэтому задаём var_stream_map s:0,sgroup:subtitle и -master_pl_name.
	hasSubs := subs >= 0
	if hasSubs {
		args = append(args, "-map", subsMap)
	}
	if h := qualityHeight(quality); h > 0 {
		// Понижение качества (H.264, высота ≤ исходной). Важно для HDR/10-бит: без
		// -pix_fmt yuv420p ffmpeg сохранит глубину исходника, и MSE такой поток не
		// декодирует; тонамаппинг нужен, чтобы HDR-рипы не выглядели выцветшими.
		args = append(args,
			"-vf", fmt.Sprintf("scale=-2:'min(%d,ih)',tonemap=hable", h),
			"-pix_fmt", "yuv420p",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		)
	} else {
		args = append(args, "-c:v", "copy")
	}
	args = append(args,
		"-c:a", "aac", "-b:a", "192k", "-ac", "2",
		"-f", "hls",
		"-hls_time", "6",
		"-hls_list_size", "0",
		// fMP4: hls.js/MSE играет HEVC только во fMP4 (в MPEG-TS — нет).
		"-hls_segment_type", "fmp4",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", segPattern,
	)
	if hasSubs {
		// WebVTT + master-плейлист: sgroup связывает видеовариант с субтитром.
		args = append(args, "-c:s", "webvtt")
		vsm := "v:0,s:0,sgroup:subtitle"
		if track >= 0 {
			vsm = "v:0,a:0,s:0,sgroup:subtitle"
		}
		args = append(args, "-var_stream_map", vsm, "-master_pl_name", "master.m3u8")
	}
	args = append(args, playlist)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = &ffmpegLogWriter{}
	log.Printf("ffmpeg %s track=%d subs=%d start=%.0fs quality=%s: ffmpeg %s", id, track, subs, start, quality, strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		log.Printf("ffmpeg %s track=%d: start failed: %v", id, track, err)
		return nil, err
	}
	s := &hlsSession{id: id, magnet: magnet, file: file, track: track, subs: subs, subsLabel: subsLabel, start: start, quality: quality, dir: dir, playlist: playlist, cmd: cmd, lastUsed: time.Now()}
	if !m.publishSession(ctx, key, generation, s) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.RemoveAll(dir)
		return nil, context.Canceled
	}

	go func() {
		werr := cmd.Wait()
		m.mu.Lock()
		s.exited = true
		s.exitErr = werr
		m.mu.Unlock()
		log.Printf("ffmpeg %s track=%d subs=%d: завершился: %v", id, track, subs, werr)
	}()
	log.Printf("hls: start %s track=%d subs=%d", id, track, subs)
	return s, nil
}

// stop убивает ffmpeg и удаляет временные файлы (Wait делает горутина-наблюдатель).
func (s *hlsSession) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = os.RemoveAll(s.dir)
}

// stopSessions останавливает ffmpeg-сессию фильма (освобождает память и чтение торрента).
func (m *hlsManager) stopSessions(id string) {
	m.mu.Lock()
	delete(m.launches, id)
	if s, ok := m.sessions[id]; ok {
		s.stop()
		delete(m.sessions, id)
		m.mu.Unlock()
		log.Printf("hls: stop %s", id)
		return
	}
	m.mu.Unlock()
}

// stopAll останавливает все сессии при завершении сервера (чтобы не осталось осиротевших ffmpeg).
func (m *hlsManager) stopAll() {
	m.mu.Lock()
	clear(m.launches)
	for k, s := range m.sessions {
		s.stop()
		delete(m.sessions, k)
	}
	m.mu.Unlock()
}

// qualityHeight — высота кадра для качества (0 — исходное, без транскодинга).
func qualityHeight(q string) int {
	switch strings.TrimSpace(q) {
	case "2160":
		return 2160
	case "1080":
		return 1080
	case "720":
		return 720
	case "480":
		return 480
	}
	return 0
}

// status возвращает число активных ffmpeg-сессий (для диагностики).
func (m *hlsManager) status() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// cleanup периодически останавливает давно не использованные сессии.
func (m *hlsManager) cleanup() {
	for {
		time.Sleep(30 * time.Second)
		cutoff := time.Now().Add(-90 * time.Second)
		// Останавливаем без лока: RemoveAll может быть медленным и заблокировал бы HLS.
		var stale []*hlsSession
		m.mu.Lock()
		for k, s := range m.sessions {
			if s.lastUsed.Before(cutoff) {
				stale = append(stale, s)
				delete(m.sessions, k)
			}
		}
		m.mu.Unlock()
		for _, s := range stale {
			log.Printf("hls: cleanup: остановка простоявшей сессии %s track=%d", s.id, s.track)
			s.stop()
		}
	}
}

// trackParam разбирает параметр track (по умолчанию 0 — первая дорожка).
func trackParam(r *http.Request) int {
	t, err := strconv.Atoi(r.URL.Query().Get("track"))
	if err != nil {
		return 0
	}
	return t
}

// subsParam разбирает параметр subs (по умолчанию -1 — без субтитров).
func subsParam(r *http.Request) int {
	s, err := strconv.Atoi(r.URL.Query().Get("subs"))
	if err != nil {
		return -1
	}
	return s
}

// handleTracks — GET /api/films/{id}/tracks: дорожки, субтитры, длительность,
// кодек/высота видео (для фолбэка на H.264, если браузер не играет HEVC).
func handleTracks(hls *hlsManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		magnet := strings.TrimSpace(r.URL.Query().Get("magnet"))
		if magnet == "" {
			http.Error(w, "missing magnet", http.StatusBadRequest)
			return
		}
		pr, err := hls.tracks(r.Context(), id, magnet, fileParam(r))
		if err != nil {
			// Нет пиров/данных: отдаём 504, чтобы фронтенд показал ошибку, а не
			// скрыл селектор дорожек (будто в файле нет звука).
			log.Printf("tracks: %s: %v", id, err)
			http.Error(w, "tracks unavailable (no peers?): "+err.Error(), http.StatusGatewayTimeout)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":          id,
			"items":       pr.Tracks,
			"subtitles":   pr.Subtitles,
			"duration":    pr.Duration,
			"codec":       pr.VideoCodec,
			"height":      pr.VideoHeight,
			"video_start": pr.VideoStart, // сдвиг видео-дорожки (для adelay)
		})
	}
}

// servePlaylist — GET /api/films/{id}/hls.m3u8?magnet&track&subs&start&quality
// (start — перемотка в сек; quality — source|2160|1080|720|480; subs — -1 без субтитров).
// Запускает ffmpeg и ждёт появления плейлиста (с субтитрами — master-плейлиста).
func (m *hlsManager) servePlaylist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	magnet := strings.TrimSpace(r.URL.Query().Get("magnet"))
	if magnet == "" {
		http.Error(w, "missing magnet", http.StatusBadRequest)
		return
	}
	track := trackParam(r)
	file := fileParam(r)
	subs := subsParam(r)
	start, _ := strconv.ParseFloat(r.URL.Query().Get("start"), 64)
	quality := strings.TrimSpace(r.URL.Query().Get("quality"))
	if quality == "" {
		quality = "source"
	}
	s, err := m.ensure(r.Context(), id, magnet, file, track, subs, start, quality, r.URL.Query().Get("session"))
	if err != nil {
		http.Error(w, "hls start failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// С субтитрами ждём master-плейлист (ffmpeg создаёт его после первого сегмента).
	waitFor := s.playlist
	if s.subs >= 0 {
		waitFor = filepath.Join(s.dir, "master.m3u8")
	}
	// Окно 100с — запас под медленный старт торрента; упавший ffmpeg отдаёт ошибку сразу.
	t0 := time.Now()
	deadline := t0.Add(100 * time.Second)
	for {
		if _, err := os.Stat(waitFor); err == nil {
			log.Printf("hls: плейлист готов %s track=%d subs=%d (ожидание %s)", id, track, s.subs, time.Since(t0).Round(time.Millisecond))
			break
		}
		m.mu.Lock()
		exited, exitErr := s.exited, s.exitErr
		m.mu.Unlock()
		if exited {
			log.Printf("hls: ffmpeg %s track=%d subs=%d завершился до плейлиста: %v", id, track, s.subs, exitErr)
			http.Error(w, "ffmpeg exited before playlist ready: "+fmt.Sprint(exitErr), http.StatusBadGateway)
			return
		}
		if time.Now().After(deadline) {
			log.Printf("hls: таймаут плейлиста %s track=%d (нет пиров?)", id, track)
			http.Error(w, "hls playlist timeout (no peers?)", http.StatusGatewayTimeout)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	if s.subs >= 0 {
		m.serveMaster(w, r, s)
		return
	}
	m.serveMediaPlaylistFrom(w, r, s)
}

// playlistURLParams — общие query-параметры URL плейлистов/сегментов (magnet/track/file).
func playlistURLParams(id, magnet string, track, file int) (enc, common string) {
	enc = url.QueryEscape(magnet)
	common = "?magnet=" + enc + "&track=" + strconv.Itoa(track)
	if file >= 0 {
		common += "&file=" + strconv.Itoa(file)
	}
	return enc, common
}

func segmentBaseURL(id string, common string) string {
	return "/api/films/" + url.PathEscape(id) + "/hls/segments/$1" + common
}

// serveMaster отдаёт master-плейлист, подменяя ссылки на наши URL и имя
// субтитр-дорожки (ffmpeg 6.1 пишет NAME="subtitle_0" и DEFAULT=YES).
func (m *hlsManager) serveMaster(w http.ResponseWriter, r *http.Request, s *hlsSession) {
	id := r.PathValue("id")
	data, err := os.ReadFile(filepath.Join(s.dir, "master.m3u8"))
	if err != nil {
		http.Error(w, "read master playlist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, common := playlistURLParams(id, s.magnet, s.track, s.file)
	if session := r.URL.Query().Get("session"); session != "" {
		common += "&session=" + url.QueryEscape(session)
	}
	// media-плейлист: /hls/pl.m3u8 (он же playlist.m3u8 в каталоге сессии).
	mediaURL := "/api/films/" + url.PathEscape(id) + "/hls/pl.m3u8" + common
	// субтитр-плейлист: /hls/subs/0.m3u8 (единственный WebVTT-рендеринг).
	subURL := "/api/films/" + url.PathEscape(id) + "/hls/subs/0.m3u8" + common

	body := string(data)
	body = strings.ReplaceAll(body, `URI="playlist_vtt.m3u8"`, `URI="`+subURL+`"`)
	body = regexp.MustCompile(`(?m)^playlist\.m3u8$`).ReplaceAllString(body, mediaURL)
	// ffmpeg 6.1 не умеет sname → NAME="subtitle_N"; DEFAULT=YES снимаем,
	// чтобы hls.js не включал субтитры сам.
	if s.subsLabel != "" {
		body = regexp.MustCompile(`NAME="subtitle_\d+"`).ReplaceAllString(body, `NAME="`+s.subsLabel+`"`)
	}
	body = strings.ReplaceAll(body, `DEFAULT=YES`, `DEFAULT=NO`)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = io.WriteString(w, body)
}

// findSession ищет сессию фильма с теми же источником/дорожкой/серией.
func (m *hlsManager) findSession(id, magnet string, track, file int) (*hlsSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	if s.magnet != magnet || s.track != track || (file >= 0 && s.file != file) {
		return nil, false
	}
	s.lastUsed = time.Now()
	return s, true
}

// serveMediaPlaylist — GET /api/films/{id}/hls/pl.m3u8: media-плейлист сессии
// (hls.js запрашивает его из master-плейлиста).
func (m *hlsManager) serveMediaPlaylist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	magnet := strings.TrimSpace(r.URL.Query().Get("magnet"))
	if magnet == "" {
		http.Error(w, "missing magnet", http.StatusBadRequest)
		return
	}
	track := trackParam(r)
	file := fileParam(r)
	s, ok := m.findSession(hlsSessionKey(id, r.URL.Query().Get("session")), magnet, track, file)
	if !ok {
		http.NotFound(w, r)
		return
	}
	m.serveMediaPlaylistFrom(w, r, s)
}

func (m *hlsManager) serveMediaPlaylistFrom(w http.ResponseWriter, r *http.Request, s *hlsSession) {
	id := r.PathValue("id")
	data, err := os.ReadFile(s.playlist)
	if err != nil {
		http.Error(w, "read playlist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Имена сегментов → абсолютные URL; file в параметрах, чтобы не спутать
	// сегменты сессий при переключении источника/дорожки/серии.
	_, common := playlistURLParams(id, s.magnet, s.track, s.file)
	if session := r.URL.Query().Get("session"); session != "" {
		common += "&session=" + url.QueryEscape(session)
	}
	re := regexp.MustCompile(`(init\.mp4|seg_\d+\.(?:m4s|ts))`)
	body := re.ReplaceAllString(string(data), segmentBaseURL(id, common))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = io.WriteString(w, body)
}

// serveSubPlaylist — GET /api/films/{id}/hls/subs/{idx}.m3u8: WebVTT-плейлист
// субтитр-дорожки со ссылками на .vtt-сегменты.
func (m *hlsManager) serveSubPlaylist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	magnet := strings.TrimSpace(r.URL.Query().Get("magnet"))
	if magnet == "" {
		http.Error(w, "missing magnet", http.StatusBadRequest)
		return
	}
	track := trackParam(r)
	file := fileParam(r)
	s, ok := m.findSession(hlsSessionKey(id, r.URL.Query().Get("session")), magnet, track, file)
	if !ok || s.subs < 0 {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "playlist_vtt.m3u8"))
	if err != nil {
		http.Error(w, "read subtitle playlist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, common := playlistURLParams(id, s.magnet, s.track, s.file)
	if session := r.URL.Query().Get("session"); session != "" {
		common += "&session=" + url.QueryEscape(session)
	}
	re := regexp.MustCompile(`(playlist\d+\.vtt)`)
	body := re.ReplaceAllString(string(data), segmentBaseURL(id, common))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = io.WriteString(w, body)
}

// serveSegment — GET /api/films/{id}/hls/segments/{name}: сегмент HLS.
func (m *hlsManager) serveSegment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.PathValue("name")
	magnet := strings.TrimSpace(r.URL.Query().Get("magnet"))
	if magnet == "" {
		http.Error(w, "missing magnet", http.StatusBadRequest)
		return
	}
	track := trackParam(r)
	file := fileParam(r)
	m.mu.Lock()
	s, ok := m.sessions[hlsSessionKey(id, r.URL.Query().Get("session"))]
	if ok {
		// Параметры обязаны совпадать: «хвостовые» запросы старого плейлиста
		// не должны получить сегменты новой сессии.
		if s.magnet != magnet || s.track != track || (file >= 0 && s.file != file) {
			ok = false
		} else {
			s.lastUsed = time.Now()
			s.segments++
		}
	}
	m.mu.Unlock()
	if !ok {
		log.Printf("hls: сегмент %s/%s: сессия не найдена (track=%d file=%d)", id, name, track, file)
		http.NotFound(w, r)
		return
	}
	if s.segments == 1 {
		log.Printf("hls: первый сегмент %s %s (track=%d)", id, name, track)
	}
	// Только имена сегментов, без обхода каталога.
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		http.NotFound(w, r)
		return
	}
	// fMP4 (.m4s/.mp4), субтитры .vtt, старые .ts — тоже отдаём.
	switch {
	case strings.HasSuffix(name, ".m4s"):
		w.Header().Set("Content-Type", "video/iso.segment")
	case strings.HasSuffix(name, ".mp4"):
		w.Header().Set("Content-Type", "video/mp4")
	case strings.HasSuffix(name, ".vtt"):
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "video/mp2t")
	}
	http.ServeFile(w, r, filepath.Join(s.dir, name))
}

// ffmpegLogWriter пишет stderr ffmpeg в лог целыми строками (ffmpeg шлёт куски).
type ffmpegLogWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *ffmpegLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
		if line != "" {
			log.Printf("ffmpeg: %s", line)
		}
	}
	return len(p), nil
}

func hlsSessionKey(id string, playback ...string) string {
	if len(playback) == 0 || playback[0] == "" {
		return id
	}
	return id + "\x00" + playback[0]
}

// Поколения запуска не дают старому запросу заменить новый (резерв — под m.mu).
func (m *hlsManager) beginLaunchLocked(key string) uint64 {
	m.nextLaunch++
	if m.launches == nil {
		m.launches = make(map[string]uint64)
	}
	m.launches[key] = m.nextLaunch
	return m.nextLaunch
}
func (m *hlsManager) finishLaunch(key string, generation uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.launches[key] == generation {
		delete(m.launches, key)
	}
}
func (m *hlsManager) publishSession(ctx context.Context, key string, generation uint64, s *hlsSession) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || m.launches[key] != generation {
		return false
	}
	if previous := m.sessions[key]; previous != nil {
		previous.stop()
	}
	m.sessions[key] = s
	return true
}
