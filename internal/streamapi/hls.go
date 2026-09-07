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

// audioTrack — информация о звуковой дорожке из ffprobe.
// Ordinal — ПОРЯДКОВЫЙ номер среди аудио-потоков (0 = первый); он же
// используется как track в /hls.m3u8 (для -map 0:a:N). Index — глобальный
// индекс потока в файле (информационно; маппить по нему нельзя — у MKV
// глобальный индекс 0 часто является видео).
type audioTrack struct {
	Index    int    `json:"index"`
	Ordinal  int    `json:"ordinal"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
}

// subtitleTrack — информация о субтитр-дорожке из ffprobe. Ordinal —
// порядковый номер среди субтитр-потоков (0 = первый); он же используется
// как subs в /hls.m3u8 (для -map 0:s:N). В HLS уходит только ОДИН
// выбранный субтитр (ffmpeg 6.1: WebVTT-муксер пишет один поток на
// плейлист), поэтому выбор субтитра перезапускает ffmpeg-сессию.
type subtitleTrack struct {
	Index    int    `json:"index"`
	Ordinal  int    `json:"ordinal"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
}

// hlsSession — запущенный ffmpeg, транскодирующий торрент в HLS
// (видео копируется как есть или перекодируется под выбранное качество,
// звук перекодируется в AAC).
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

	// subsLabel — человекочитаемое имя выбранной субтитр-дорожки
	// (title/язык) для подстановки в master-плейлист (#EXT-X-MEDIA NAME).
	subsLabel string

	// exited/exitErr фиксируются горутиной-наблюдателем, когда ffmpeg
	// завершился. Позволяют servePlaylist быстро вернуть ошибку вместо
	// долгого ожидания плейлиста, если ffmpeg упал (нет пиров и т.п.).
	exited  bool
	exitErr error
}

// maxProbeCache — максимум записей в кэше ffprobe (ключ = id|magnet|file).
// Кэш ограничен, чтобы не расти бесконечно за долгую сессию.
const maxProbeCache = 256

// hlsManager управляет ffmpeg-сессиями HLS.
type hlsManager struct {
	mu         sync.Mutex
	sessions   map[string]*hlsSession
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
	// VideoStart — start_time видео-потока (сек). У некоторых рипов (MKV)
	// видео-дорожка стартует позже аудио (напр. 0.94с) — из-за этого при
	// старте HLS-потока первые секунды идёт только звук. Поток для таких
	// файлов начинаем с начала видео (-ss VideoStart + noaccurate_seek).
	VideoStart float64
	// Partial — true для ЛЁГКОГО результата videoStartProbe (только
	// VideoStart). Такой результат НЕ отдаётся как полная проба /tracks:
	// tracks() по нему делает полное ffprobe, иначе duration/дорожки были бы 0.
	Partial bool
}

func newHLSManager(selfBase string) *hlsManager {
	dir, err := os.MkdirTemp("", "video-viewer-hls")
	if err != nil {
		dir = os.TempDir()
	}
	return &hlsManager{
		sessions:   make(map[string]*hlsSession),
		selfBase:   selfBase,
		dataDir:    dir,
		probeCache: make(map[string]probeResult),
	}
}

// inputURL — URL внутреннего эндпоинта с сырым файлом видео,
// из которого ffmpeg читает (с поддержкой Range для перемотки).
// file — индекс конкретного файла (серии) в торренте, -1 — авто.
func (m *hlsManager) inputURL(id, magnet string, file int) string {
	v := url.Values{}
	v.Set("magnet", magnet)
	if file >= 0 {
		v.Set("file", strconv.Itoa(file))
	}
	return m.selfBase + "/api/stream/" + url.PathEscape(id) + "?" + v.Encode()
}

// ffprobeNum — число из вывода ffprobe. Обычно ffprobe отдаёт числа как
// JSON-числа, но для некоторых контейнеров start_time приходит строкой
// ("N/A" или "0.000000"). Принимаем оба варианта; при строке "N/A"/пустой
// Val остаётся 0.
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

// tracks возвращает звуковые дорожки и длительность файла торрента (ffprobe).
// Результат кэшируется на время сессии; file — индекс файла (серии),
// -1 — авто (самый крупный видеофайл).
func (m *hlsManager) tracks(ctx context.Context, id, magnet string, file int) (probeResult, error) {
	key := fmt.Sprintf("%s|%s|file%d", id, magnet, file)
	m.mu.Lock()
	// Частичный результат videoStartProbe (только VideoStart) не годится
	// как полная проба — по нему делаем полное ffprobe.
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
			// Только ТЕКСТОВЫЕ субтитры, которые ffmpeg умеет конвертировать
			// в WebVTT для HLS. Растровые (PGS/DVDSUB и т.п.) не конвертируются
			// — их не показываем в селекторе, чтобы не предлагать нерабочее.
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
	// Кэш ограничен: без эвикции он рос бы бесконечно (ключ содержит
	// полную строку магнита). При достижении лимита вытесняем самые
	// старые записи.
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

// isTextSubtitleCodec возвращает true для текстовых субтитр-кодеков, которые
// ffmpeg конвертирует в WebVTT для HLS (выбор в селекторе субтитров).
// Растровые форматы (hdmv_pgs_subtitle/dvd_subtitle и т.п.) в WebVTT не
// конвертируются — их не предлагаем.
func isTextSubtitleCodec(c string) bool {
	switch c {
	case "subrip", "srt", "ass", "ssa", "mov_text", "webvtt", "text", "subviewer", "subviewer1", "jacosub", "mpl2", "microdvd", "sami", "stl", "scc":
		return true
	}
	return false
}

// subtitleLabel — человекочитаемое имя субтитр-дорожки для master-плейлиста
// (#EXT-X-MEDIA NAME): title, иначе язык, иначе «Субтитры N».
func subtitleLabel(t subtitleTrack) string {
	if t.Title != "" {
		return t.Title
	}
	if t.Language != "" {
		return strings.ToUpper(t.Language)
	}
	return fmt.Sprintf("Субтитры %d", t.Ordinal+1)
}

// probeKey — ключ кэша ffprobe для (id, magnet, file).
func probeKey(id, magnet string, file int) string {
	return fmt.Sprintf("%s|%s|file%d", id, magnet, file)
}

// videoStartProbe возвращает start_time видео-потока (сек) для
// (id,magnet,file) — из кэша ffprobe или лёгким зондированием видео-потока
// (с коротким таймаутом). ok=false, если определить не удалось (нет
// пиров/данных). Нужно для adelay: у некоторых рипов видео-дорожка стартует
// позже аудио, из-за чего при старте HLS-потока звук отстаёт от картинки.
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

	// Кэшируем (не затираем более полный результат от tracks()).
	m.mu.Lock()
	if pr, ok := m.probeCache[key]; ok {
		// Уже есть запись (полная проба /tracks или другой videoStartProbe) —
		// дописываем VideoStart, не затирая Partial-флаг полной записи.
		if pr.VideoStart == 0 {
			pr.VideoStart = vs
			m.probeCache[key] = pr
		}
	} else {
		// Нет полной пробы — сохраняем ЛЁГКИЙ результат (Partial), чтобы
		// tracks() не отдал его как полную пробу (duration/tracks будут 0).
		m.probeCache[key] = probeResult{VideoStart: vs, Partial: true}
		m.probeOrder = append(m.probeOrder, key)
	}
	m.mu.Unlock()
	return vs, true
}

// ensure запускает ffmpeg для (id, magnet, file, track, subs, start, quality)
// и возвращает сессию. На фильм держится одна сессия: при изменении
// серии/дорожки/субтитров/перемотке/качестве предыдущая останавливается
// и стартует новая. subs — выбранная субтитр-дорожка (-1 — без субтитров;
// в HLS уходит один субтитр — ffmpeg 6.1 пишет один WebVTT-поток на плейлист).
func (m *hlsManager) ensure(ctx context.Context, id, magnet string, file, track, subs int, start float64, quality string) (*hlsSession, error) {
	m.mu.Lock()
	if s, ok := m.sessions[id]; ok {
		if s.magnet == magnet && s.file == file && s.track == track && s.subs == subs && s.start == start && s.quality == quality {
			s.lastUsed = time.Now()
			m.mu.Unlock()
			return s, nil
		}
		// Параметры изменились (серия/дорожка/субтитры/перемотка/качество) — перезапуск.
		log.Printf("hls: ensure %s: перезапуск (file=%d track=%d subs=%d start=%.0f quality=%s)", id, file, track, subs, start, quality)
		s.stop()
		delete(m.sessions, id)
	}
	// Освобождаем лок ПЕРЕД пробой: videoStartProbe внутри берёт m.mu сам,
	// а Go-мьютексы нереентерабельны — вызов при удержанном локе = мертвяк
	// (вешал все HLS-запросы для свежего потока start=0).
	m.mu.Unlock()

	// Проверяем выбранный субтитр по кэшу ffprobe (фронтенд всегда шлёт
	// ordinals из /tracks, но защищаемся от невалидного subs — иначе
	// -map 0:s:N не найдёт поток и ffmpeg упадёт). Если пробы ещё нет —
	// доверяем параметру (loadTracks всегда зондирует перед playHls).
	subsLabel := ""
	if subs >= 0 {
		key := probeKey(id, magnet, file)
		m.mu.Lock()
		pr, ok := m.probeCache[key]
		m.mu.Unlock()
		if ok {
			if subs >= len(pr.Subtitles) {
				log.Printf("hls: ensure %s: субтитр %d не найден — без субтитров", id, subs)
				subs = -1
			} else {
				subsLabel = subtitleLabel(pr.Subtitles[subs])
			}
		}
	}

	// Сдвиг видео-дорожки (adelay) нужен для свежего потока с начала файла.
	// Зондируем ВНЕ лока: ffprobe может идти секунды, а лок блокировал бы
	// остальные HLS-запросы.
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
	// Позиция старта потока. Для явной перемотки (start>0) — сама start.
	// Для свежего потока (start=0), если видео-дорожка в исходнике стартует
	// ПОЗЖЕ аудио (напр. 0.94с), стартуем с начала видео: иначе первые 0.94с
	// идёт только звук, и звук «отстаёт» от картинки. Запуск с начала видео
	// (-ss VideoStart + noaccurate_seek) ставит видео и звук на одну точку —
	// A/V согласованы с первого кадра (adelay НЕ годится: он сдвигает звук
	// навсегда, создавая постоянный рассинхрон).
	seekStart := start
	if start <= 0 && vstart > 0.1 {
		seekStart = vstart
		log.Printf("hls: ensure %s: видео стартует с %.3fs — поток с этой позиции (A/V согласованы)", id, vstart)
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if seekStart > 0 {
		// Перемотка: начинаем ffmpeg с позиции seekStart (быстрый input-seek
		// по HTTP-стриму с поддержкой Range).
		args = append(args, "-ss", strconv.FormatFloat(seekStart, 'f', -1, 64))
		// ВАЖНО (рассинхрон звука): когда видео КОПИРУЕТСЯ как есть
		// (-c:v copy, качество source), точный seek (accurate_seek по
		// умолчанию) стартует видео с ближайшего ключевого кадра, а звук —
		// ровно с позиции seekStart. Из-за этого звук отстаёт от видео на
		// длину группы кадров. -noaccurate_seek стартует и видео, и звук
		// с одного ключевого кадра — A/V в синхроне. При ПЕРЕКОДИРОВАНИИ
		// видео (2160/1080/720/480) точный seek работает корректно.
		if qualityHeight(quality) == 0 {
			args = append(args, "-noaccurate_seek")
		}
	}
	args = append(args,
		"-i", m.inputURL(id, magnet, file),
		"-map", "0:v:0",
	)
	// Аудио-дорожка: track — это ПОРЯДКОВЫЙ номер аудио-потока (0 = первый),
	// как в /tracks (поле ordinal). -map 0:a:N выбирает N-й аудио-поток файла
	// НЕЗАВИСИМО от его глобального индекса: у MKV видео обычно индекс 0, а
	// аудио 1..N, поэтому маппинг по глобальному индексу (0:0 = видео) давал
	// в HLS два видео-потока без аудио → bufferAppendError у hls.js.
	// track=-1 — аудио в файле нет (по /tracks) — не маппим её вовсе.
	if track >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:a:%d", track))
	}
	// Субтитры: выбранный текстовый субтитр (subs — ordinal среди
	// субтитр-потоков) конвертируем в WebVTT и включаем в HLS. Чтобы
	// hls.js показал дорожку субтитров, нужен master-плейлист с
	// #EXT-X-MEDIA:TYPE=SUBTITLES: задаём var_stream_map с s:0 и
	// sgroup:subtitle и -master_pl_name (ffmpeg сгенерирует master.m3u8
	// и отдельный WebVTT-плейлист playlist_vtt.m3u8 + playlistN.vtt).
	// Без выбранного субтитра команда остаётся прежней (один плейлист).
	hasSubs := subs >= 0
	if hasSubs {
		args = append(args, "-map", fmt.Sprintf("0:s:%d", subs))
	}
	if h := qualityHeight(quality); h > 0 {
		// Серверное понижение качества: перекодируем видео в H.264 с
		// ограничением высоты (не выше исходной — min()). Позволяет
		// смотреть 4K/1080p на слабом канале, выбрав меньшее качество.
		//
		// Важно для HDR/10-бит: без принудительного формата ffmpeg сохраняет
		// глубину исходника (yuv420p10le → Hi10P), а браузеры через MSE такой
		// H.264 не декодируют (bufferAppendError). Поэтому всегда выдаём
		// 8-битный yuv420p и применяем тонамаппинг HDR→SDR (hable/Filmic),
		// чтобы HDR-рипы не выглядели выцветшими и реально игрались.
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
		// fMP4-сегменты: hls.js/MSE поддерживают HEVC (H.265) во fMP4,
		// в отличие от MPEG-TS — иначе для HEVC-рипов был бы фолбэк на
		// сырой поток с невоспроизводимым звуком (AC3/DTS).
		"-hls_segment_type", "fmp4",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", segPattern,
	)
	if hasSubs {
		// Субтитр в WebVTT + master-плейлист с SUBTITLES-рендерингом.
		// sgroup объединяет видеовариант с субтитром; v:0,a:0 (или v:0 без
		// звука) — основной вариант, s:0 — единственный субтитр.
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
	m.mu.Lock()
	// Во время зондирования/старта могла появиться сессия с теми же
	// параметрами (параллельный запрос) — используем её, лишний ffmpeg
	// убиваем; если параметры другие — останавливаем старую и заменяем.
	if existing, ok := m.sessions[id]; ok {
		if existing.magnet == magnet && existing.file == file && existing.track == track && existing.subs == subs && existing.start == start && existing.quality == quality {
			m.mu.Unlock()
			_ = cmd.Process.Kill()
			_ = os.RemoveAll(dir)
			existing.lastUsed = time.Now()
			return existing, nil
		}
		existing.stop()
	}
	m.sessions[id] = s
	m.mu.Unlock()
	// Наблюдаем за ffmpeg: фиксируем завершение процесса. Единственный,
	// кто вызывает cmd.Wait() — эта горутина (stop() только Kill).
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

// stop завершает ffmpeg (Kill) и удаляет временные файлы. Reaping
// процесса выполняет горутина-наблюдатель (см. ensure), поэтому здесь
// не вызываем Process.Wait.
func (s *hlsSession) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = os.RemoveAll(s.dir)
}

// stopSessions останавливает ffmpeg-сессию фильма, освобождая память и
// завершая чтение торрента.
func (m *hlsManager) stopSessions(id string) {
	m.mu.Lock()
	if s, ok := m.sessions[id]; ok {
		s.stop()
		delete(m.sessions, id)
		m.mu.Unlock()
		log.Printf("hls: stop %s", id)
		return
	}
	m.mu.Unlock()
}

// stopAll останавливает все ffmpeg-сессии (вызывается при завершении
// сервера, чтобы не оставлять осиротевшие ffmpeg-процессы).
func (m *hlsManager) stopAll() {
	m.mu.Lock()
	for k, s := range m.sessions {
		s.stop()
		delete(m.sessions, k)
	}
	m.mu.Unlock()
}

// qualityHeight возвращает высоту кадра для выбранного качества
// (0 — исходное качество, без транскодинга).
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

// cleanup периодически останавливает давно не использованные сессии,
// чтобы ffmpeg не висел и не держал торрент в памяти после просмотра.
func (m *hlsManager) cleanup() {
	for {
		time.Sleep(30 * time.Second)
		cutoff := time.Now().Add(-90 * time.Second)
		// Собираем устаревшие сессии под блокировкой, а останавливаем их
		// (Kill + RemoveAll) уже без неё: RemoveAll может быть медленным
		// и не должен блокировать ensure/playlist/segments.
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

// handleTracks — GET /api/films/{id}/tracks?magnet=...
// Возвращает звуковые дорожки, субтитры, длительность, а также кодек/высоту
// видео (для автофолбэка на H.264, если браузер не поддерживает HEVC).
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
			// Данные торрента недоступны (нет пиров/кусков — ffprobe не может
			// прочитать начало файла и упирается в таймаут). Возвращаем ЯВНУЮ
			// ошибку (504), чтобы фронтенд показал сообщение, а не молча
			// скрывал селектор дорожек, как если бы в файле не было звука.
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

// servePlaylist — GET /api/films/{id}/hls.m3u8?magnet=...&track=N&subs=S&start=SS&quality=Q.
// start — позиция в секундах (перемотка: ffmpeg стартует с неё);
// quality — source|2160|1080|720|480 (серверное понижение качества);
// subs — выбранная субтитр-дорожка (-1/отсутствует — без субтитров).
// Запускает/возвращает HLS-плейлист ffmpeg: с субтитрами — MASTER-плейлист
// (#EXT-X-MEDIA TYPE=SUBTITLES + ссылки на media/субтитр-плейлисты), без —
// обычный media-плейлист (как раньше). Если ffmpeg ещё не создал плейлист
// (ждёт метаданные/первый сегмент), ожидает его появления.
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
	s, err := m.ensure(r.Context(), id, magnet, file, track, subs, start, quality)
	if err != nil {
		http.Error(w, "hls start failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Файл, появления которого ждём: при субтитрах — master-плейлист
	// (создаётся ffmpeg после первого сегмента вместе с media-плейлистом),
	// иначе — media-плейлист.
	waitFor := s.playlist
	if s.subs >= 0 {
		waitFor = filepath.Join(s.dir, "master.m3u8")
	}
	// Ждём появления плейлиста. Окно 100с — с запасом под медленный старт
	// торрента (пиры, метаданные, первые куски). Если ffmpeg упал раньше
	// появления плейлиста — возвращаем ошибку сразу, не ждём таймаут.
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

// playlistURLParams — общие query-параметры для URL плейлистов/сегментов
// (magnet/track/file), по которым сервер находит сессию.
func playlistURLParams(id, magnet string, track, file int) (enc, common string) {
	enc = url.QueryEscape(magnet)
	common = "?magnet=" + enc + "&track=" + strconv.Itoa(track)
	if file >= 0 {
		common += "&file=" + strconv.Itoa(file)
	}
	return enc, common
}

// segmentBaseURL — префикс URL эндпоинта сегментов (без имени сегмента).
func segmentBaseURL(id string, common string) string {
	return "/api/films/" + url.PathEscape(id) + "/hls/segments/$1" + common
}

// serveMaster отдаёт master-плейлист сессии с субтитрами, переписывая
// относительные ссылки в абсолютные URL наших эндпоинтов и подставляя
// реальное имя выбранной субтитр-дорожки (ffmpeg 6.1 пишет дефолтное
// NAME="subtitle_0" и DEFAULT=YES — переименовываем и выключаем авто-включение).
func (m *hlsManager) serveMaster(w http.ResponseWriter, r *http.Request, s *hlsSession) {
	id := r.PathValue("id")
	data, err := os.ReadFile(filepath.Join(s.dir, "master.m3u8"))
	if err != nil {
		http.Error(w, "read master playlist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, common := playlistURLParams(id, s.magnet, s.track, s.file)
	// media-плейлист: /hls/pl.m3u8 (он же playlist.m3u8 в каталоге сессии).
	mediaURL := "/api/films/" + url.PathEscape(id) + "/hls/pl.m3u8" + common
	// субтитр-плейлист: /hls/subs/0.m3u8 (единственный WebVTT-рендеринг).
	subURL := "/api/films/" + url.PathEscape(id) + "/hls/subs/0.m3u8" + common

	body := string(data)
	body = strings.ReplaceAll(body, `URI="playlist_vtt.m3u8"`, `URI="`+subURL+`"`)
	body = regexp.MustCompile(`(?m)^playlist\.m3u8$`).ReplaceAllString(body, mediaURL)
	// ffmpeg 6.1 не поддерживает sname — даёт NAME="subtitle_N"; заменяем на
	// реальное имя дорожки. DEFAULT=YES → NO, чтобы hls.js не включал
	// субтитры автоматически (пользователь выбирает сам).
	if s.subsLabel != "" {
		body = regexp.MustCompile(`NAME="subtitle_\d+"`).ReplaceAllString(body, `NAME="`+s.subsLabel+`"`)
	}
	body = strings.ReplaceAll(body, `DEFAULT=YES`, `DEFAULT=NO`)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = io.WriteString(w, body)
}

// findSession возвращает сессию фильма с совпадающими параметрами
// источника/дорожки/серии (как serveSegment). subOK=true, если у сессии
// субтитры включены (субтитр-плейлист существует).
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

// serveMediaPlaylist — GET /api/films/{id}/hls/pl.m3u8?magnet=...&track=N&file=M.
// Отдаёт media-плейлист (video+audio) сессии со ссылками на сегменты,
// переписанными в абсолютные URL. Вызывается hls.js из master-плейлиста.
func (m *hlsManager) serveMediaPlaylist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	magnet := strings.TrimSpace(r.URL.Query().Get("magnet"))
	if magnet == "" {
		http.Error(w, "missing magnet", http.StatusBadRequest)
		return
	}
	track := trackParam(r)
	file := fileParam(r)
	s, ok := m.findSession(id, magnet, track, file)
	if !ok {
		http.NotFound(w, r)
		return
	}
	m.serveMediaPlaylistFrom(w, r, s)
}

// serveMediaPlaylistFrom отдаёт media-плейлист из каталога сессии.
func (m *hlsManager) serveMediaPlaylistFrom(w http.ResponseWriter, r *http.Request, s *hlsSession) {
	id := r.PathValue("id")
	data, err := os.ReadFile(s.playlist)
	if err != nil {
		http.Error(w, "read playlist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Переписываем относительные имена сегментов в абсолютные URL
	// эндпоинта сегментов (иначе hls.js резолвит их не туда). Помимо
	// magnet и track передаём file (серию), чтобы сервер мог отличить
	// сегменты разных сессий при переключении источника/дорожки/серии.
	_, common := playlistURLParams(id, s.magnet, s.track, s.file)
	re := regexp.MustCompile(`(init\.mp4|seg_\d+\.(?:m4s|ts))`)
	body := re.ReplaceAllString(string(data), segmentBaseURL(id, common))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = io.WriteString(w, body)
}

// serveSubPlaylist — GET /api/films/{id}/hls/subs/{idx}.m3u8?magnet=...&track=N.
// Отдаёт WebVTT-плейлист выбранной субтитр-дорожки сессии (playlist_vtt.m3u8)
// со ссылками на .vtt-сегменты, переписанными в абсолютные URL.
func (m *hlsManager) serveSubPlaylist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	magnet := strings.TrimSpace(r.URL.Query().Get("magnet"))
	if magnet == "" {
		http.Error(w, "missing magnet", http.StatusBadRequest)
		return
	}
	track := trackParam(r)
	file := fileParam(r)
	s, ok := m.findSession(id, magnet, track, file)
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
	re := regexp.MustCompile(`(playlist\d+\.vtt)`)
	body := re.ReplaceAllString(string(data), segmentBaseURL(id, common))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = io.WriteString(w, body)
}

// serveSegment — GET /api/films/{id}/hls/segments/{name}?magnet=...&track=N.
// Отдаёт сегмент .ts сгенерированного HLS.
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
	s, ok := m.sessions[id]
	if ok {
		// Сессия ключуется по id фильма, но параметры источника/дорожки/
		// серии должны совпадать с запрошенными: после переключения
		// источника/дорожки «хвостовые» запросы сегментов от старого
		// плейлиста не должны отдавать сегменты новой сессии.
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
	// fMP4: сегменты .m4s, init-сегмент .mp4; старые .ts тоже отдаём.
	// Субтитры: .vtt-сегменты WebVTT-плейлиста.
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

// ffmpegLogWriter буферизует stderr ffmpeg и пишет в лог приложения
// целыми строками (ffmpeg пишет частями — иначе строки лога рвались бы).
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
