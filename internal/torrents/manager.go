// Package torrents управляет торрент-клиентом: открывает магнеты и стримит их.
// Скачанные куски — в RAM либо, при заданном Config.SpoolDir, в дисковом спуле
// (по файлу на серию, см. storage_spool.go).
package torrents

import (
	"errors"
	"github.com/zeoril1/video_viewer/internal/disklimit"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/zeoril1/video_viewer/internal/catalog"
)

// Config — параметры торрент-клиента.
type Config struct {
	MaxCacheBytes int64
	MinFreeBytes  int64
	ListenPort    int
	// SpoolDir — каталог дискового спула скачанных кусков (по файлу на серию);
	// пусто — прежнее поведение: данные в памяти.
	SpoolDir string
	// CacheTTL — как долго держать «тёплый» торрент после закрытия читателей (0 — defaultCacheTTL).
	CacheTTL time.Duration
	// MaxCached — лимит «тёплых» торрентов (0 — defaultMaxCached); при переполнении выгружается самый старый.
	MaxCached int
}

// DropGrace — сколько ждать после закрытия последнего читателя, прежде чем
// выгрузить торрент (если для него не запрошен тёплый кеш Keep).
const DropGrace = 90 * time.Second

// defaultCacheTTL — время жизни «тёплого» кеша (после просмотра >5%).
const defaultCacheTTL = 24 * time.Hour

const defaultMaxCached = 16

// Manager владеет торрент-клиентом и кэшем открытых торрентов.
type Manager struct {
	closed     bool // guarded by mu; prevents late browser leases after shutdown
	client     *torrent.Client
	spool      *spoolClient // дисковый спул (nil — in-memory)
	mu         sync.Mutex
	open       map[string]*torrent.Torrent // ключ: info hash
	readers    map[string]int              // активные читатели по hash
	dropTimers map[string]*time.Timer      // отложенный выгруз по hash
	keepUntil  map[string]time.Time        // «тёплый» кеш: держать до этого времени
	fileWants  map[string]map[int]int      // востребованные файлы (серии) по hash
	cacheTTL   time.Duration               // TTL тёплого кеша
	maxCached  int                         // лимит тёплых торрентов
	rates      map[string]downloadSample   // samples used by the storage dashboard
}

// NewManager создаёт торрент-клиент (хранилище — спул либо RAM). Берём
// torrent.NewDefaultClientConfig: свой ClientConfig без дефолтов (rate limiters,
// DHT) валит NewClient.
func NewManager(cfg Config) (*Manager, error) {
	cc := torrent.NewDefaultClientConfig()
	cc.Seed = false
	cc.DataDir = ""
	cc.ListenPort = cfg.ListenPort

	var spool *spoolClient
	if cfg.SpoolDir != "" {
		if err := os.MkdirAll(cfg.SpoolDir, 0755); err != nil {
			return nil, err
		}
		removeAbandonedSpool(cfg.SpoolDir)
		spool = newSpoolClient(cfg.SpoolDir)
		spool.budget = disklimit.New(cfg.SpoolDir, cfg.MaxCacheBytes, cfg.MinFreeBytes)
		cc.DefaultStorage = spool
		log.Printf("torrents: дисковый спул включён (%s)", cfg.SpoolDir)
	} else {
		cc.DefaultStorage = memoryStorage{}
	}

	client, err := torrent.NewClient(cc)
	if err != nil {
		return nil, err
	}
	cacheTTL := cfg.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = defaultCacheTTL
	}
	maxCached := cfg.MaxCached
	if maxCached <= 0 {
		maxCached = defaultMaxCached
	}
	return &Manager{
		client:     client,
		spool:      spool,
		open:       make(map[string]*torrent.Torrent),
		readers:    make(map[string]int),
		dropTimers: make(map[string]*time.Timer),
		keepUntil:  make(map[string]time.Time),
		fileWants:  make(map[string]map[int]int),
		cacheTTL:   cacheTTL,
		maxCached:  maxCached,
		rates:      make(map[string]downloadSample),
	}, nil
}

// Close закрывает клиент: торренты удаляют свои спул-файлы, плюс подчищаем осиротевшие.
func (m *Manager) Close() {
	// Гасим отложенные выгрузки: их таймеры не должны срабатывать после закрытия клиента.
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, tm := range m.dropTimers {
		tm.Stop()
	}
	clear(m.dropTimers)
	m.mu.Unlock()

	m.client.Close()
	if m.spool != nil {
		if err := m.spool.Close(); err != nil {
			log.Printf("torrents: close spool: %v", err)
		}
	}
}

func hashOf(magnet string) (string, error) {
	mi, err := metainfo.ParseMagnetUri(magnet)
	if err != nil {
		return "", errors.New("parse magnet: " + err.Error())
	}
	return mi.InfoHash.String(), nil
}

// getOrOpenLocked возвращает уже открытый торрент либо открывает его (только под m.mu).
func (m *Manager) getOrOpenLocked(item catalog.Item, hash string) (*torrent.Torrent, error) {
	if t, ok := m.open[hash]; ok {
		return t, nil
	}
	t, err := m.client.AddMagnet(item.Magnet)
	if err != nil {
		return nil, err
	}
	m.open[hash] = t
	return t, nil
}

// Acquire возвращает торрент и регистрирует активного читателя; release() обязателен
// (при нуле читателей торрент выгружается через DropGrace, освобождая данные).
//
// ВАЖНО: поиск/создание торрента, инкремент читателей и отмена таймера выгрузки —
// под ОДНОЙ блокировкой: иначе таймер мог сработать между ними и Acquire вернул бы
// УЖЕ ЗАКРЫТЫЙ торрент (ошибка при возобновлении просмотра после выгрузки).
func (m *Manager) Acquire(item catalog.Item) (*torrent.Torrent, func(), error) {
	hash, err := hashOf(item.Magnet)
	if err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, nil, ErrCacheNotFound
	}
	m.trimDiskLocked()
	t, err := m.getOrOpenLocked(item, hash)
	if err != nil {
		return nil, nil, err
	}
	// A file eviction leaves its replacement torrent suspended so it cannot
	// silently recreate the file. An explicit playback/prepare request resumes it.
	t.AllowDataDownload()

	m.readers[hash]++
	m.cancelDropLocked(hash)

	var once sync.Once
	release := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.closed || m.open[hash] != t {
				return
			}
			m.readers[hash]--
			if m.readers[hash] <= 0 {
				delete(m.readers, hash)
				m.scheduleDropLocked(hash)
			}
		})
	}
	return t, release, nil
}

// drop немедленно выгружает простаивающий торрент, освобождая данные (при спуле — удаляя файл).
func (m *Manager) drop(hash string) {
	m.mu.Lock()
	removed := m.dropLocked(hash)
	m.mu.Unlock()
	returnDroppedMemory(hash, removed)
}

// A stopped AfterFunc callback may already be waiting for mu. Its timer's
// identity is the schedule generation: only the current timer can remove data.
func (m *Manager) dropScheduledLocked(hash string, timer *time.Timer, source *torrent.Torrent) bool {
	if m.closed || timer == nil || m.dropTimers[hash] != timer {
		return false
	}
	if m.open[hash] != source {
		// Individual-file eviction can replace an idle torrent. The old
		// callback must not remove that replacement; give it a new schedule.
		m.cancelDropLocked(hash)
		m.scheduleDropLocked(hash)
		return false
	}
	if !m.idleLocked(hash) {
		m.cancelDropLocked(hash)
		return false // the last reader/file preparation release schedules again
	}
	if m.keepUntil[hash].After(time.Now()) {
		m.scheduleDropLocked(hash)
		return false
	}
	return m.dropLocked(hash)
}

func returnDroppedMemory(hash string, removed bool) {
	if !removed {
		return
	}
	log.Printf("torrents: dropped %s (memory freed)", hash)
	// Возвращаем память ОС: Go отдаёт её лениво — принудительный GC + FreeOSMemory.
	runtime.GC()
	debug.FreeOSMemory()
	log.Printf("torrents: memory returned to OS")
}

func (m *Manager) cancelDropLocked(hash string) {
	if timer := m.dropTimers[hash]; timer != nil {
		timer.Stop()
	}
	delete(m.dropTimers, hash)
}

func (m *Manager) idleLocked(hash string) bool {
	return m.readers[hash] <= 0 && len(m.fileWants[hash]) == 0
}

// dropLocked выгружает торрент немедленно (только под m.mu); true — был открыт и выгружен.
func (m *Manager) dropLocked(hash string) bool {
	// Active playback pins and file preparation both protect the source.
	if !m.idleLocked(hash) {
		return false
	}
	m.cancelDropLocked(hash)
	t, ok := m.open[hash]
	if ok {
		t.Drop()
		delete(m.open, hash)
	}
	delete(m.keepUntil, hash)
	delete(m.fileWants, hash)
	delete(m.rates, hash)
	return ok
}

// scheduleDropLocked планирует выгрузку после закрытия последнего читателя: через
// DropGrace либо, если запрошен тёплый кеш (Keep), — по истечении его TTL.
func (m *Manager) scheduleDropLocked(hash string) {
	m.cancelDropLocked(hash)
	if m.closed || m.open[hash] == nil || !m.idleLocked(hash) {
		return
	}
	now := time.Now()
	delay := DropGrace
	if until, ok := m.keepUntil[hash]; ok && until.After(now) {
		delay = until.Sub(now)
	}
	source := m.open[hash]
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		// Read the captured timer only after locking: a very short Keep TTL
		// may start this callback before AfterFunc returns its timer pointer.
		m.mu.Lock()
		removed := m.dropScheduledLocked(hash, timer, source)
		m.mu.Unlock()
		returnDroppedMemory(hash, removed)
	})
	m.dropTimers[hash] = timer
}

// Keep помечает торрент «тёплым кешем»: после закрытия читателей он выгружается не
// через DropGrace, а по истечении dur (0 — cacheTTL), сохраняя скачанные данные,
// чтобы другой пользователь того же infohash получил их без повторного скачивания.
// Фронтенд зовёт после просмотра >5% длительности.
func (m *Manager) Keep(magnet string, dur time.Duration) {
	hash, err := hashOf(magnet)
	if err != nil {
		return
	}
	if dur <= 0 {
		dur = m.cacheTTL
	}
	until := time.Now().Add(dur)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	if existing := m.keepUntil[hash]; existing.After(until) {
		until = existing
	}
	m.keepUntil[hash] = until
	// Простаивающий открытый торрент — продлеваем отложенную выгрузку до TTL.
	if _, open := m.open[hash]; open && m.readers[hash] <= 0 {
		m.scheduleDropLocked(hash)
	}
	m.mu.Unlock()

	m.enforceCacheCap()
	log.Printf("torrents: keep %s till %s", hash, until.Format(time.RFC3339))
}

// enforceCacheCap не даёт «тёплому» кешу разрастись: при переполнении выгружает
// простаивающие торренты с самым ранним TTL.
func (m *Manager) enforceCacheCap() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Подчищаем записи кеша, чей TTL истёк (торрент уже выгружен).
	for h, u := range m.keepUntil {
		if !u.After(time.Now()) {
			delete(m.keepUntil, h)
		}
	}
	type cand struct {
		hash  string
		until time.Time
	}
	var open []cand
	for h := range m.open {
		if u, ok := m.keepUntil[h]; ok && u.After(time.Now()) {
			open = append(open, cand{h, u})
		}
	}
	over := len(open) - m.maxCached
	if over <= 0 {
		return
	}
	sort.Slice(open, func(i, j int) bool { return open[i].until.Before(open[j].until) })
	for _, c := range open {
		if over <= 0 {
			return
		}
		if !m.idleLocked(c.hash) {
			continue // активный просмотр — не выгружаем
		}
		if m.dropLocked(c.hash) {
			over--
		}
	}
}

// WantFile помечает файл (серию) востребованным: остальным файлам торрента ставится
// DoNotDownload, чтобы не качался весь сезон-пак. Рефкаунт позволяет параллельно
// качать разные серии одного торрента. Возвращает функцию снятия метки.
func (m *Manager) WantFile(item catalog.Item, fileIndex int) func() {
	hash, err := hashOf(item.Magnet)
	if err != nil || fileIndex < 0 {
		return func() {}
	}
	m.mu.Lock()
	t := m.open[hash]
	if m.closed || t == nil {
		m.mu.Unlock()
		return func() {}
	}
	if m.fileWants[hash] == nil {
		m.fileWants[hash] = make(map[int]int)
	}
	m.fileWants[hash][fileIndex]++
	m.cancelDropLocked(hash)
	m.mu.Unlock()
	m.applyFilePriorities(hash)

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.closed || m.open[hash] != t {
				m.mu.Unlock()
				return
			}
			if cnt := m.fileWants[hash]; cnt != nil {
				cnt[fileIndex]--
				if cnt[fileIndex] <= 0 {
					delete(cnt, fileIndex)
				}
				if len(cnt) == 0 {
					delete(m.fileWants, hash)
					m.scheduleDropLocked(hash)
				}
			}
			m.mu.Unlock()
			m.applyFilePriorities(hash)
		})
	}
}

// ApplyDownloadPriorities приводит приоритеты файлов к текущему набору WantFile.
// Нужно, когда торрент открыт без выбора файла (список серий /files): иначе
// anacrolix качает ВЕСЬ торрент — у файлов по умолчанию приоритет Normal.
func (m *Manager) ApplyDownloadPriorities(item catalog.Item) {
	hash, err := hashOf(item.Magnet)
	if err != nil {
		return
	}
	m.applyFilePriorities(hash)
}

// applyFilePriorities: востребованные файлы (WantFile) — Normal, остальные — None.
// Вызывается без m.mu: SetPriority берёт блокировку клиента торрента.
func (m *Manager) applyFilePriorities(hash string) {
	m.mu.Lock()
	t := m.open[hash]
	wants := make(map[int]int)
	for i, n := range m.fileWants[hash] {
		wants[i] = n
	}
	m.mu.Unlock()
	if t == nil || t.Info() == nil {
		return
	}
	files := t.Files()
	for i, f := range files {
		prio := torrent.PiecePriorityNone
		if wants[i] > 0 {
			prio = torrent.PiecePriorityNormal
		}
		f.SetPriority(prio)
	}
}

// TorrentStatus — состояние открытого торрента (диагностика памяти и диска).
type TorrentStatus struct {
	Hash       string `json:"hash"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	// Kept — торрент держится как «тёплый» кеш (Keep): просмотрено >5%.
	Kept bool `json:"kept"`
	// SpoolFiles — сколько серий уже в дисковом спуле (только реально качавшиеся);
	// 0 при in-memory хранилище.
	SpoolFiles int `json:"spool_files"`
	// SpoolBytes — суммарный размер этих файлов на диске (байт).
	SpoolBytes int64 `json:"spool_bytes"`
}

// Status возвращает список открытых торрентов и объём скачанных данных.
func (m *Manager) Status() []TorrentStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]TorrentStatus, 0, len(m.open))
	for hash, t := range m.open {
		ts := TorrentStatus{Hash: hash}
		if info := t.Info(); info != nil {
			ts.Total = info.TotalLength()
		}
		ts.Downloaded = t.BytesCompleted()
		ts.Kept = m.keepUntil[hash].After(time.Now())
		if m.spool != nil {
			ts.SpoolFiles, ts.SpoolBytes = m.spool.usage(hash)
		}
		res = append(res, ts)
	}
	return res
}

// Trim idle cached torrents before admitting more data; active readers survive.
func (m *Manager) trimDiskLocked() {
	if m.spool == nil || !m.spool.budget.Pressure() {
		return
	}
	type candidate struct {
		hash  string
		until time.Time
	}
	var idle []candidate
	for h := range m.open {
		if m.idleLocked(h) {
			idle = append(idle, candidate{h, m.keepUntil[h]})
		}
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].until.Before(idle[j].until) })
	for _, c := range idle {
		if !m.spool.budget.Pressure() {
			break
		}
		m.dropLocked(c.hash)
	}
}
func (m *Manager) DiskPressure() bool { return m.spool != nil && m.spool.budget.Pressure() }
