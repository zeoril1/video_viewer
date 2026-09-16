package iptv

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Справочник каналов iptv-org (https://iptv-org.github.io/api/channels.json).
//
// Плейлисты называют каналы латиницей («Moya Planeta»), а в справочнике у
// многих каналов есть родное название в alt_names — его и показываем. Оттуда же
// берём страну вещания: одноимённые каналы разных стран иначе не различить.
type refChannel struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	AltNames []string `json:"alt_names"`
	Country  string   `json:"country"`
}

// RefIndex — разобранный справочник: id канала → русское название и код страны.
type RefIndex struct {
	ru map[string]string
	co map[string]string
	n  int
}

// ParseRefIndex разбирает справочник каналов iptv-org.
func ParseRefIndex(data []byte) (*RefIndex, error) {
	var list []refChannel
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("разбор справочника каналов: %w", err)
	}
	idx := &RefIndex{
		ru: make(map[string]string, len(list)),
		co: make(map[string]string, len(list)),
		n:  len(list),
	}
	for _, c := range list {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			continue
		}
		if name := nativeName(c); name != "" {
			idx.ru[id] = name
		}
		if cc := strings.TrimSpace(c.Country); cc != "" {
			idx.co[id] = strings.ToUpper(cc)
		}
	}
	return idx, nil
}

// nativeName выбирает название на родном языке: само имя канала или первое
// альтернативное, записанное кириллицей.
func nativeName(c refChannel) string {
	if hasCyrillic(c.Name) {
		return strings.TrimSpace(c.Name)
	}
	for _, a := range c.AltNames {
		if hasCyrillic(a) {
			return strings.TrimSpace(a)
		}
	}
	return ""
}

// RussianName возвращает русское название канала по его id ("" — канала нет
// в справочнике или родного названия у него нет).
func (r *RefIndex) RussianName(channelID string) string {
	if r == nil {
		return ""
	}
	return r.ru[refKey(channelID)]
}

// Country возвращает код страны вещания канала по его id ("" — неизвестна).
// Им различаются одноимённые каналы разных стран.
func (r *RefIndex) Country(channelID string) string {
	if r == nil {
		return ""
	}
	return r.co[refKey(channelID)]
}

// refKey — id канала без пометки варианта («MoyaPlaneta.ru@SD» →
// «MoyaPlaneta.ru»): в справочнике канал один, а вариантов потока много.
func refKey(channelID string) string {
	id := strings.TrimSpace(channelID)
	if i := strings.IndexByte(id, '@'); i >= 0 {
		id = id[:i]
	}
	return id
}

// Len — число записей справочника (для логов о загрузке).
func (r *RefIndex) Len() int {
	if r == nil {
		return 0
	}
	return r.n
}
