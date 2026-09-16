package iptvapi

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zeoril1/video_viewer/internal/iptv"
)

// refURL — справочник каналов iptv-org: id канала → родное название. По нему
// каналы показываются по-русски («Moya Planeta» → «Моя Планета»).
const refURL = "https://iptv-org.github.io/api/channels.json"

// refTTL — срок годности справочника (файл на несколько мегабайт, состав меняется медленно).
const refTTL = 7 * 24 * time.Hour

// refCache — справочник названий каналов с кэшем на диске. Он не критичен для
// работы: без него каналы остаются с исходными (латинскими) названиями.
type refCache struct {
	url  string
	path string

	mu      sync.Mutex
	idx     *iptv.RefIndex
	fetched time.Time
}

// newRefCache создаёт кэш справочника (dir — каталог данных, пустой — temp).
func newRefCache(url, dir string) *refCache {
	if url == "" {
		url = refURL
	}
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "video-viewer-iptv")
	}
	return &refCache{url: url, path: filepath.Join(dir, "channels-ref.json")}
}

// get возвращает справочник, скачивая его не чаще раза в refTTL; при сбое
// используется прежний (из памяти или с диска).
func (c *refCache) get(ctx context.Context, hc *http.Client) *iptv.RefIndex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx != nil && time.Since(c.fetched) < refTTL {
		return c.idx
	}
	// Кэш на диске переживает перезапуск сервиса: свежий файл читаем сразу,
	// чтобы не тянуть мегабайты при каждом старте.
	if c.idx == nil {
		if idx, when, ok := c.loadFile(); ok {
			c.idx, c.fetched = idx, when
			if time.Since(when) < refTTL {
				return c.idx
			}
		}
	}
	data, err := fetchBytes(ctx, hc, c.url, "", "")
	if err != nil {
		if c.idx != nil {
			c.fetched = time.Now().Add(refTTL / 2) // повтор попытки не раньше чем через полсрока
			log.Printf("iptv: справочник каналов: %v (использую прежний)", err)
			return c.idx
		}
		log.Printf("iptv: справочник каналов: %v (названия останутся исходными)", err)
		return nil
	}
	idx, err := iptv.ParseRefIndex(data)
	if err != nil {
		log.Printf("iptv: справочник каналов: %v", err)
		return c.idx
	}
	if c.idx == nil {
		log.Printf("iptv: справочник каналов загружен: записей %d", idx.Len())
	}
	c.idx, c.fetched = idx, time.Now()
	if err := os.WriteFile(c.path, data, 0o644); err != nil {
		log.Printf("iptv: кэш справочника каналов: %v", err)
	}
	return c.idx
}

// peek возвращает справочник БЕЗ сетевых запросов (память или диск): nil —
// ещё не загружен и скачивать его прямо сейчас нельзя. Нужен там, где
// справочник только дополняет ответ (страна вещания в списке каналов).
func (c *refCache) peek() *iptv.RefIndex {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx != nil {
		return c.idx
	}
	if idx, when, ok := c.loadFile(); ok {
		c.idx, c.fetched = idx, when
		return idx
	}
	return nil
}

// loadFile читает справочник с диска (ок — если файла нет).
func (c *refCache) loadFile() (*iptv.RefIndex, time.Time, bool) {
	st, err := os.Stat(c.path)
	if err != nil {
		return nil, time.Time{}, false
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return nil, time.Time{}, false
	}
	idx, err := iptv.ParseRefIndex(data)
	if err != nil {
		log.Printf("iptv: кэш справочника каналов повреждён: %v", err)
		return nil, time.Time{}, false
	}
	return idx, st.ModTime(), true
}
