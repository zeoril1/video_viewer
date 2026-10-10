package iptvapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/db"
)

// tokenTTL — сколько живёт ссылка на сегмент/вложенный плейлист: живые
// плейлисты обновляются постоянно, а использование продлевает срок.
const tokenTTL = 10 * time.Minute

// maxTokens — предохранитель на размер карты токенов.
const maxTokens = 20000

// tokenRe — формат токена (hex), по нему валидируем путь.
var tokenRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// liveManager обслуживает воспроизведение IPTV-каналов в двух режимах:
//   - proxy: канал уже отдаёт HLS — плейлист провайдера переписывается,
//     клиент ходит только к нам (креды и CORS не утекают);
//   - ffmpeg: MPEG-TS или несовместимый звук — live-сессия ffmpeg
//     (видео copy, звук AAC в fMP4), раздача HLS из временного каталога.
type liveManager struct {
	cfg Config

	mu       sync.Mutex
	sessions map[int64]*liveSession // key — id канала
	tokens   map[string]tokenRef    // key — токен ссылки

	hc *http.Client
}

// liveSession — запущенный ffmpeg для канала.
type liveSession struct {
	channelID int64
	dir       string
	// sid — имя каталога сессии, попадает в ссылки сегментов
	// (/api/iptv/live/{id}/{sid}/{name}): после перезапуска ffmpeg плеер
	// запрашивает новый плейлист, а не склеивает куски разных запусков.
	sid string
	cmd *exec.Cmd

	lastUsed time.Time
	exited   bool
	err      error
}

// tokenRef — зарегистрированная upstream-ссылка (сегмент/плейлист).
type tokenRef struct {
	url     string
	ua      string
	referer string
	expires time.Time
}

func newLiveManager(cfg Config) *liveManager {
	if cfg.DataDir == "" {
		cfg.DataDir = os.TempDir()
	}
	_ = os.MkdirAll(cfg.DataDir, 0o755)
	return &liveManager{
		cfg:      cfg,
		sessions: make(map[int64]*liveSession),
		tokens:   make(map[string]tokenRef),
		// Таймаут с запасом: провайдеры любят держать соединение открытым.
		hc: &http.Client{Timeout: 30 * time.Second},
	}
}

// fetch скачивает upstream-ссылку. rangeHdr — необязательный Range клиента.
func (m *liveManager) fetch(ctx context.Context, raw, ua, referer, rangeHdr string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, upstreamRequestError{cause: err}
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	resp, err := m.hc.Do(req)
	if err != nil {
		return nil, upstreamRequestError{cause: err}
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// Ошибки net/http содержат URL запроса, включая логин в пути и query-токены.
// Причину сохраняем для errors.Is/As, но не выдаём адрес в ответе или логах.
type upstreamRequestError struct{ cause error }

func (e upstreamRequestError) Error() string {
	if errors.Is(e.cause, context.Canceled) {
		return "upstream request canceled"
	}
	var timeout interface{ Timeout() bool }
	if errors.Is(e.cause, context.DeadlineExceeded) || errors.As(e.cause, &timeout) && timeout.Timeout() {
		return "upstream request timed out"
	}
	return "upstream request failed"
}

func (e upstreamRequestError) Unwrap() error { return e.cause }

// addToken регистрирует upstream-ссылку и возвращает токен для неё.
func (m *liveManager) addToken(raw, ua, referer string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// Криптостойкость не критична (ссылка живёт минуты) — берём время.
		copy(buf, []byte(strconv.FormatInt(time.Now().UnixNano(), 16)))
	}
	tok := hex.EncodeToString(buf)
	m.mu.Lock()
	if len(m.tokens) >= maxTokens {
		m.dropExpiredTokensLocked(time.Now())
	}
	m.tokens[tok] = tokenRef{url: raw, ua: ua, referer: referer, expires: time.Now().Add(tokenTTL)}
	m.mu.Unlock()
	return tok
}

// lookupToken возвращает ссылку по токену и продлевает её срок.
func (m *liveManager) lookupToken(tok string) (tokenRef, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ref, ok := m.tokens[tok]
	if !ok || time.Now().After(ref.expires) {
		delete(m.tokens, tok)
		return tokenRef{}, false
	}
	ref.expires = time.Now().Add(tokenTTL)
	m.tokens[tok] = ref
	return ref, true
}

// dropExpiredTokensLocked чистит просроченные токены (вызывается с m.mu).
func (m *liveManager) dropExpiredTokensLocked(now time.Time) {
	for k, v := range m.tokens {
		if now.After(v.expires) {
			delete(m.tokens, k)
		}
	}
}

// playlist возвращает HLS-плейлист канала, готовый к отдаче клиенту.
func (m *liveManager) playlist(ctx context.Context, ch db.IPTVChannel, ua, referer string, pl db.IPTVPlaylist) ([]byte, error) {
	if ch.IsHLS {
		resp, err := m.fetch(ctx, ch.StreamURL, ua, referer, "")
		if err == nil {
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			resp.Body.Close()
			if rerr == nil && isHLSPlaylist(body) {
				// После перенаправления относительные URI принадлежат конечному
				// плейлисту, а не адресу канала, с которого начался запрос.
				if resp.Request.URL.String() != ch.StreamURL {
					log.Printf("iptv: канал %d: HLS-плейлист получен после перенаправления", ch.ID)
				}
				return m.rewriteProxy(body, resp.Request.URL, ua, referer), nil
			}
		} else {
			log.Printf("iptv: канал %d: получение HLS-плейлиста не удалось (%v) — пробуем ffmpeg", ch.ID, err)
		}
	}
	return m.playlistFFmpeg(ctx, ch, ua, referer)
}

// rewriteProxy переписывает плейлист провайдера: все ссылки (сегменты,
// вложенные плейлисты, ключи шифрования) уходят через наш /api/iptv/seg.
func (m *liveManager) rewriteProxy(body []byte, base *url.URL, ua, referer string) []byte {
	return rewriteHLS(body, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		abs := base.ResolveReference(u).String()
		return "/api/iptv/seg/" + m.addToken(abs, ua, referer)
	})
}

// playlistFFmpeg запускает live-сессию ffmpeg и отдаёт её плейлист.
func (m *liveManager) playlistFFmpeg(ctx context.Context, ch db.IPTVChannel, ua, referer string) ([]byte, error) {
	sess, err := m.ensureSession(ctx, ch, ua, referer)
	if err != nil {
		return nil, err
	}
	index := filepath.Join(sess.dir, "index.m3u8")
	if err := waitFile(ctx, index, 25*time.Second); err != nil {
		if serr := sess.sessionErr(); serr != nil {
			return nil, fmt.Errorf("ffmpeg: %w", serr)
		}
		return nil, err
	}
	body, err := os.ReadFile(index)
	if err != nil {
		return nil, err
	}
	sess.touch()
	id := strconv.FormatInt(ch.ID, 10)
	return rewriteHLS(body, func(raw string) string {
		return "/api/iptv/live/" + id + "/" + sess.sid + "/" + path.Base(raw)
	}), nil
}

// ensureSession создаёт (или переиспользует) ffmpeg-сессию канала.
func (m *liveManager) ensureSession(ctx context.Context, ch db.IPTVChannel, ua, referer string) (*liveSession, error) {
	m.mu.Lock()
	if s, ok := m.sessions[ch.ID]; ok {
		if !s.exited {
			s.touch()
			m.mu.Unlock()
			return s, nil
		}
		s.kill()
		delete(m.sessions, ch.ID)
	}
	// Освобождаем место: гасим самую давнюю простаивающую сессию.
	if len(m.sessions) >= m.cfg.MaxSessions {
		var victim *liveSession
		for _, s := range m.sessions {
			if victim == nil || s.lastUsed.Before(victim.lastUsed) {
				victim = s
			}
		}
		if victim != nil {
			log.Printf("iptv: лимит live-сессий (%d) — останавливаю канал %d", m.cfg.MaxSessions, victim.channelID)
			victim.kill()
			delete(m.sessions, victim.channelID)
		}
	}
	m.mu.Unlock()

	dir, err := os.MkdirTemp(m.cfg.DataDir, fmt.Sprintf("ch%d-", ch.ID))
	if err != nil {
		return nil, err
	}
	ffmpeg := m.cfg.FFmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-user_agent", ua,
	}
	if referer != "" {
		args = append(args, "-headers", "Referer: "+referer+"\r\n")
	}
	args = append(args,
		"-i", ch.StreamURL,
		// Видео копируем (без нагрузки на CPU), звук перекодируем в AAC:
		// в MPEG-TS российские каналы часто вещают MP2/AC3, которые
		// браузерный hls.js не декодирует.
		"-map", "0:v:0?", "-map", "0:a:0?",
		"-c:v", "copy",
		"-c:a", "aac", "-b:a", "160k", "-ac", "2",
		"-max_muxing_queue_size", "2048",
		"-f", "hls",
		"-hls_time", "4",
		"-hls_list_size", "8",
		// temp_file — сегмент появляется в каталоге только целиком (ffmpeg
		// пишет .tmp и переименовывает); без этого плеер скачивает недописанный файл.
		"-hls_flags", "delete_segments+omit_endlist+independent_segments+temp_file",
		// fMP4 вместо MPEG-TS: кодеки берутся из init-файла, тогда как в TS
		// hls.js угадывает звук по ADTS и выдаёт неиграемый mp4a.40.5 (HE-AAC).
		"-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(dir, "seg_%05d.m4s"),
		filepath.Join(dir, "index.m3u8"),
	)
	if ch.IsArchive {
		for i := range args {
			if args[i] == "-hls_list_size" {
				args[i+1] = "0"
			}
			if args[i] == "-hls_flags" {
				args[i+1] = "independent_segments+temp_file"
			}
		}
		args = append(args[:len(args)-1], "-hls_playlist_type", "event", args[len(args)-1])
	}
	cmd := exec.Command(ffmpeg, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &ffmpegLog{channel: ch.ID, name: ch.Name}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("запуск ffmpeg: %w", err)
	}
	sess := &liveSession{channelID: ch.ID, dir: dir, sid: filepath.Base(dir), cmd: cmd, lastUsed: time.Now()}
	m.mu.Lock()
	m.sessions[ch.ID] = sess
	m.mu.Unlock()

	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		sess.exited = true
		sess.err = err
		m.mu.Unlock()
		if err != nil {
			log.Printf("iptv: ffmpeg канала %d (%s) завершился: %v", ch.ID, ch.Name, err)
		}
	}()
	log.Printf("iptv: запущена live-сессия канала %d (%s)", ch.ID, ch.Name)
	return sess, nil
}

// touch отмечает активность сессии.
func (s *liveSession) touch() { s.lastUsed = time.Now() }

// sessionErr возвращает ошибку завершившегося ffmpeg.
func (s *liveSession) sessionErr() error {
	if s.err != nil {
		return s.err
	}
	return fmt.Errorf("сессия остановлена")
}

// kill гасит ffmpeg и удаляет временный каталог сессии.
func (s *liveSession) kill() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = os.RemoveAll(s.dir)
}

// localFile возвращает путь к файлу сессии (сегмент/плейлист), если имя
// безопасно и ссылка ведёт на живую сессию sid.
func (m *liveManager) localFile(channelID int64, sid, name string) (string, bool) {
	if !safeName(name) || sid == "" {
		return "", false
	}
	m.mu.Lock()
	sess, ok := m.sessions[channelID]
	if ok {
		sess.touch()
	}
	dir := ""
	if sess != nil && sess.sid == sid {
		dir = sess.dir
	}
	m.mu.Unlock()
	if !ok || dir == "" {
		return "", false
	}
	p := filepath.Join(dir, name)
	if !fileExists(p) {
		return "", false
	}
	return p, true
}

// runCleanup периодически гасит простаивающие сессии и чистит токены.
func (m *liveManager) runCleanup(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			m.mu.Lock()
			for id, s := range m.sessions {
				if s.exited || now.Sub(s.lastUsed) > m.cfg.SessionIdle {
					s.kill()
					delete(m.sessions, id)
				}
			}
			m.dropExpiredTokensLocked(now)
			m.mu.Unlock()
		}
	}
}

// stopAll гасит все live-сессии (graceful shutdown).
func (m *liveManager) stopAll() {
	m.mu.Lock()
	sessions := make([]*liveSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[int64]*liveSession)
	m.tokens = make(map[string]tokenRef)
	m.mu.Unlock()
	for _, s := range sessions {
		s.kill()
	}
}

// ---- HTTP-обработчики воспроизведения ----

// playID разбирает id канала из пути /api/iptv/play/{id} (имя может быть с
// расширением). Мультиплексор Go не умеет шаблон «{id}.m3u8» — {id} должен
// занимать сегмент целиком, поэтому разбираем сами. 0 — id не число.
func playID(r *http.Request) int64 {
	name := path.Base(r.URL.Path)
	name = strings.TrimSuffix(name, ".m3u8")
	name = strings.TrimSuffix(name, ".m3u")
	return atoi64(name)
}

// handlePlay — GET /api/iptv/play/{id}.m3u8: HLS-плейлист канала.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	id := playID(r)
	if id <= 0 {
		http.NotFound(w, r)
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
	pl, _, err := s.cfg.DB.GetIPTVPlaylist(r.Context(), ch.PlaylistID)
	if err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ua, referer := channelHeaders(ch.UA, ch.Referer, pl.UA, pl.Referer)
	// ?remux=1 — принудительная перепаковка на сервере: нужна, когда поток
	// HLS, но браузер не умеет его звук (AC-3/E-AC-3 в TS).
	if r.URL.Query().Get("remux") == "1" {
		body, err := s.live.playlistFFmpeg(r.Context(), ch, ua, referer)
		if err != nil {
			log.Printf("iptv: канал %d (%s), ремукс: %v", ch.ID, ch.Name, err)
			http.Error(w, "stream: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
		return
	}
	body, err := s.live.playlist(r.Context(), ch, ua, referer, pl)
	if err != nil {
		log.Printf("iptv: канал %d (%s): %v", ch.ID, ch.Name, err)
		http.Error(w, "stream: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	// Живой плейлист кэшировать нельзя.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// handleSegment — GET /api/iptv/seg/{token}: прокси сегмента или вложенного
// плейлиста провайдера. URL берётся только из нашего токена (защита от SSRF).
func (s *Server) handleSegment(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if !tokenRe.MatchString(tok) {
		http.NotFound(w, r)
		return
	}
	ref, ok := s.live.lookupToken(tok)
	if !ok {
		http.NotFound(w, r)
		return
	}
	resp, err := s.live.fetch(r.Context(), ref.url, ref.ua, ref.referer, r.Header.Get("Range"))
	if err != nil {
		resource := "сегмента"
		if u, perr := url.Parse(ref.url); perr == nil && (strings.HasSuffix(u.Path, ".m3u8") || strings.HasSuffix(u.Path, ".m3u")) {
			resource = "вложенного HLS-плейлиста"
		}
		log.Printf("iptv: получение %s: %v", resource, err)
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Читаем «голову» ответа: по ней отличаем сегмент от вложенного плейлиста.
	// На Content-Length ориентироваться нельзя — провайдеры часто отдают
	// chunked, а вложенный плейлист обязательно нужно переписать: иначе
	// относительные ссылки сегментов уйдут в /api/iptv/seg/{token}.
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if isHLSPlaylist(head) {
		body := head
		if rest, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20)); rerr == nil {
			body = append(body, rest...)
		}
		if resp.Request.URL.String() != ref.url {
			log.Printf("iptv: вложенный HLS-плейлист получен после перенаправления")
		}
		out := s.live.rewriteProxy(body, resp.Request.URL, ref.ua, ref.referer)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(out)
		return
	}
	copyHeader(w, resp)
	w.WriteHeader(resp.StatusCode)
	if _, werr := w.Write(head); werr != nil {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

// handleLiveFile — GET /api/iptv/live/{id}/{sid}/{name}: файл ffmpeg-сессии
// канала (плейлист, init-файл fMP4 или сегмент).
func (s *Server) handleLiveFile(w http.ResponseWriter, r *http.Request) {
	id := atoi64(r.PathValue("id"))
	if id <= 0 {
		http.NotFound(w, r)
		return
	}
	sid := r.PathValue("sid")
	name := r.PathValue("name")
	p, ok := s.live.localFile(id, sid, name)
	if !ok {
		// Сессия могла быть остановлена или перезапущена — клиент перезапросит
		// плейлист (hls.js это умеет).
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
	} else if strings.HasSuffix(name, ".m4s") || strings.HasSuffix(name, ".mp4") {
		// init.mp4 и медиасегменты fMP4.
		w.Header().Set("Content-Type", "video/mp4")
	}
	http.ServeFile(w, r, p)
}

// ---- Вспомогательные функции ----

// isHLSPlaylist — является ли тело HLS-плейлистом.
func isHLSPlaylist(b []byte) bool {
	head := b
	if len(head) > 2048 {
		head = head[:2048]
	}
	return bytes.Contains(head, []byte("#EXTM3U"))
}

// rewriteHLS переписывает ссылки в HLS-плейлисте через resolve: обычные
// строки-URI, URI в #EXT-X-KEY/#EXT-X-MAP/#EXT-X-PART/#EXT-X-PRELOAD-HINT и
// ref-атрибуты #EXT-X-MEDIA.
func rewriteHLS(body []byte, resolve func(raw string) string) []byte {
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "#"):
			lines[i] = rewriteTagURI(trimmed, resolve)
		default:
			lines[i] = resolve(strings.TrimSpace(trimmed))
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// rewriteTagURI переписывает URI="..." внутри тега HLS (если он есть).
func rewriteTagURI(tag string, resolve func(raw string) string) string {
	idx := strings.Index(tag, `URI="`)
	if idx < 0 {
		return tag
	}
	rest := tag[idx+len(`URI="`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return tag
	}
	raw := rest[:end]
	return tag[:idx+len(`URI="`)] + resolve(raw) + rest[end:]
}

// safeName — безопасное имя файла сессии (без путей и «..»).
func safeName(name string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	if name != path.Base(name) {
		return false
	}
	return true
}

// copyHeader переносит значимые заголовки апстрима в ответ.
func copyHeader(w http.ResponseWriter, resp *http.Response) {
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "video/mp2t")
	}
}

// ffmpegLog пишет stderr ffmpeg в лог сервиса (с префиксом канала).
type ffmpegLog struct {
	channel int64
	name    string
	buf     []byte
}

func (l *ffmpegLog) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(l.buf[:i]))
		l.buf = l.buf[i+1:]
		if line != "" {
			log.Printf("iptv: ffmpeg канал %d (%s): %s", l.channel, l.name, line)
		}
	}
	if len(l.buf) > 64<<10 {
		l.buf = nil
	}
	return len(p), nil
}
