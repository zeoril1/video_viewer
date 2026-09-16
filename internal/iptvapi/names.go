package iptvapi

import (
	"context"
	"log"
	"strings"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/iptv"
)

// Уточнение названий каналов-однофамильцев.
//
// Один канал вещает в десятках стран, и в списке оказываются десятки строк
// «National Geographic»: названия одинаковые, а страна есть только в id канала —
// поэтому к таким названиям дописывается страна: «National Geographic · Болгария».
// Уточнение добавляется ТОЛЬКО к неуникальным названиям (иначе «· Великобритания»
// в каждой строке — шум); порядок уточнений: страна → вариант вещания → плейлист.

// channelNamer — состояние для уточнения названий в пределах одного запроса.
type channelNamer struct {
	ref       *iptv.RefIndex
	dupes     map[string]struct{} // ключи названий, встречающихся у разных каналов
	playlists map[int64]string    // id плейлиста → имя (последнее уточнение)
}

// newChannelNamer собирает данные для уточнения: справочник берётся из кэша
// (без скачивания, чтобы не задерживать ответ), дубли и плейлисты — из базы.
func (s *Server) newChannelNamer(ctx context.Context) *channelNamer {
	n := &channelNamer{ref: s.refs.peek(), playlists: map[int64]string{}}
	dupes, err := s.cfg.DB.IPTVDuplicateNameKeys(ctx)
	if err != nil {
		log.Printf("iptv: неуникальные названия каналов: %v", err)
		return n
	}
	n.dupes = dupes
	// Имя плейлиста — последнее уточнение: у канала может не быть ни страны,
	// ни варианта вещания, а в базе он один на два плейлиста.
	pls, err := s.cfg.DB.ListIPTVPlaylists(ctx)
	if err != nil {
		log.Printf("iptv: плейлисты для уточнения названий: %v", err)
		return n
	}
	for _, p := range pls {
		n.playlists[p.ID] = p.Name
	}
	return n
}

// qualify — название для интерфейса: русское имя из справочника плюс уточнение,
// если без него название не отличить от других каналов в списке.
func (n *channelNamer) qualify(base string, c db.IPTVChannel) string {
	if n == nil || len(n.dupes) == 0 {
		return base
	}
	if _, ok := n.dupes[iptv.NameKey(base)]; !ok {
		return base
	}
	return qualifyName(base, c, n.ref, n.playlists[c.PlaylistID])
}

// qualifyName дописывает уточнение: страну, а если её нет — вариант вещания,
// а если и его нет (канал без tvg-id) — имя плейлиста-источника: «Наука · loganettv».
// Название возвращается без изменений, если уточнять нечем.
func qualifyName(base string, c db.IPTVChannel, ref *iptv.RefIndex, playlist string) string {
	id := channelID(c)
	if q := iptv.ChannelQualifier(id, base, ref.Country(id)); q != "" {
		return base + " · " + q
	}
	if pl := strings.TrimSpace(playlist); pl != "" {
		return base + " · " + pl
	}
	return base
}

// channelID — id канала в плейлисте (tvg-id, иначе ext_id): по нему справочник
// iptv-org знает страну вещания.
func channelID(c db.IPTVChannel) string {
	if id := strings.TrimSpace(c.EPGID); id != "" {
		return id
	}
	return strings.TrimSpace(c.ExtID)
}
