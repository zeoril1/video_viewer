// Package torrents управляет торрент-клиентом: открывает магнет-ссылки
// из каталога и стримит их в память без записи на диск.
package torrents

import (
	"errors"
	"log"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/zeoril1/video_viewer/internal/catalog"
)

// Config — параметры торрент-клиента.
type Config struct {
	ListenPort int
}

// DropGrace — сколько ждать после закрытия последнего читателя, прежде
// чем выгрузить торрент из памяти (освобождая скачанные данные).
const DropGrace = 90 * time.Second

// Manager владеет торрент-клиентом и кэшем открытых торрентов.
type Manager struct {
	client     *torrent.Client
	mu         sync.Mutex
	open       map[string]*torrent.Torrent // ключ: info hash
	readers    map[string]int              // активные читатели по hash
	dropTimers map[string]*time.Timer      // отложенный выгруз по hash
}

// NewManager создаёт торрент-клиент с in-memory хранилищем.
//
// Берём за основу дефолтный конфиг: при передаче своего ClientConfig
// библиотека не подмешивает дефолты (в т.ч. rate limiters, настройки DHT),
// а без них NewClient падает.
func NewManager(cfg Config) (*Manager, error) {
	cc := torrent.NewDefaultClientConfig()
	cc.Seed = false
	cc.DataDir = ""
	cc.DefaultStorage = memoryStorage{}
	cc.ListenPort = cfg.ListenPort

	client, err := torrent.NewClient(cc)
	if err != nil {
		return nil, err
	}
	return &Manager{
		client:     client,
		open:       make(map[string]*torrent.Torrent),
		readers:    make(map[string]int),
		dropTimers: make(map[string]*time.Timer),
	}, nil
}

// Close закрывает клиент.
func (m *Manager) Close() { m.client.Close() }

// hashOf возвращает info hash из магнет-ссылки (для ключей кэша).
func hashOf(magnet string) (string, error) {
	mi, err := metainfo.ParseMagnetUri(magnet)
	if err != nil {
		return "", errors.New("parse magnet: " + err.Error())
	}
	return mi.InfoHash.String(), nil
}

// getOrOpenLocked возвращает уже открытый торрент либо открывает его
// по магнет-ссылке. Вызывается только при удержанном m.mu.
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

// TorrentFor возвращает уже открытый торрент по записи каталога,
// либо открывает его по магнет-ссылке. Повторные вызовы возвращают
// один и тот же объект.
func (m *Manager) TorrentFor(item catalog.Item) (*torrent.Torrent, error) {
	hash, err := hashOf(item.Magnet)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getOrOpenLocked(item, hash)
}

// Acquire возвращает торрент и регистрирует активного читателя.
// Возвращаемый release() нужно вызывать, когда читатель закрыт:
// при нуле активных читателей торрент будет выгружен из памяти
// через DropGrace (освобождая скачанные данные).
//
// ВАЖНО: поиск/создание торрента, инкремент счётчика читателей и
// отмена таймера выгрузки выполняются под ОДНОЙ блокировкой. Иначе
// между возвратом найденного торрента и регистрацией читателя мог
// сработать таймер выгрузки (drop) — и Acquire вернул бы УЖЕ ЗАКРЫТЫЙ
// торрент, а вместо создания нового потока при возобновлении просмотра
// после выгрузки из памяти была бы ошибка.
func (m *Manager) Acquire(item catalog.Item) (*torrent.Torrent, func(), error) {
	hash, err := hashOf(item.Magnet)
	if err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	t, err := m.getOrOpenLocked(item, hash)
	if err != nil {
		return nil, nil, err
	}

	m.readers[hash]++
	if tm, ok := m.dropTimers[hash]; ok {
		tm.Stop()
		delete(m.dropTimers, hash)
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			m.mu.Lock()
			m.readers[hash]--
			if m.readers[hash] <= 0 {
				delete(m.readers, hash)
				m.dropTimers[hash] = time.AfterFunc(DropGrace, func() { m.drop(hash) })
			}
			m.mu.Unlock()
		})
	}
	return t, release, nil
}

// drop выгружает торрент из клиента, освобождая его память.
func (m *Manager) drop(hash string) {
	m.mu.Lock()
	delete(m.dropTimers, hash)
	// За время ожидания торрент могли снова начать читать.
	if m.readers[hash] > 0 {
		m.mu.Unlock()
		return
	}
	if t, ok := m.open[hash]; ok {
		t.Drop()
		delete(m.open, hash)
		m.mu.Unlock()
		log.Printf("torrents: dropped %s (memory freed)", hash)
		// Возвращаем память ОС: Go отдаёт её лениво, поэтому принудительно
		// запускаем GC и освобождаем неиспользуемые страницы.
		runtime.GC()
		debug.FreeOSMemory()
		log.Printf("torrents: memory returned to OS")
		return
	}
	m.mu.Unlock()
}

// TorrentStatus — состояние открытого торрента (для диагностики памяти).
type TorrentStatus struct {
	Hash       string `json:"hash"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
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
		res = append(res, ts)
	}
	return res
}
