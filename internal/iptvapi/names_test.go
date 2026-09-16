package iptvapi

import (
	"testing"

	"github.com/zeoril1/video_viewer/internal/db"
	"github.com/zeoril1/video_viewer/internal/iptv"
)

// TestQualifyName — уточнение названия канала страной вещания: без него
// десятки строк «National Geographic» из разных стран не отличить. У
// российских каналов страна не дописывается (иначе «Россия» — в каждой
// второй строке), но остаётся вариант вещания — например, сдвиг по времени.
func TestQualifyName(t *testing.T) {
	ref, err := iptv.ParseRefIndex([]byte(`[
		{"id":"NationalGeographic.bg","name":"National Geographic","country":"BG"},
		{"id":"NationalGeographic.ru","name":"National Geographic","alt_names":["National Geographic Россия"],"country":"RU"},
		{"id":"Perviy.ru","name":"Первый канал","country":"RU"},
		{"id":"LoveNature.ca","name":"Love Nature","country":"CA"}
	]`))
	if err != nil {
		t.Fatalf("разбор справочника: %v", err)
	}
	cases := []struct {
		name, id, want string
	}{
		{"National Geographic", "NationalGeographic.bg@SD", "National Geographic · Болгария"},
		{"National Geographic Россия", "NationalGeographic.ru@SD", "National Geographic Россия"},
		{"Первый канал", "Perviy.ru@Plus4", "Первый канал · +4"},
		{"Первый канал", "Perviy.ru@SD", "Первый канал"},
		{"Love Nature 4K [Geo-blocked]", "LoveNature.ca@4K", "Love Nature 4K [Geo-blocked] · Канада"},
		{"Канал без id", "777", "Канал без id"}, // страна неизвестна, варианта нет
	}
	for _, c := range cases {
		c := c
		ch := db.IPTVChannel{Name: c.name, EPGID: c.id}
		if got := qualifyName(c.name, ch, ref, ""); got != c.want {
			t.Errorf("qualifyName(%q, %q) = %q, ждали %q", c.name, c.id, got, c.want)
		}
	}
	// Без справочника уточнять нечем — название остаётся исходным.
	ch := db.IPTVChannel{Name: "National Geographic", EPGID: "NationalGeographic.bg@SD"}
	if got := qualifyName("National Geographic", ch, nil, ""); got != "National Geographic" {
		t.Errorf("без справочника имя %q, ждали исходное", got)
	}
}

// TestQualifyNamePlaylist — последнее уточнение: у канала без tvg-id (такие
// плейлисты сообществ) нет ни страны, ни варианта вещания, поэтому источник и
// есть то, чем один «Наука» отличается от другого.
func TestQualifyNamePlaylist(t *testing.T) {
	ref, err := iptv.ParseRefIndex([]byte(`[{"id":"Nauka.ru","name":"Наука","country":"RU"},
		{"id":"Nauka.ua","name":"Наука","country":"UA"}]`))
	if err != nil {
		t.Fatalf("разбор справочника: %v", err)
	}
	cases := []struct {
		name, id, playlist, want string
	}{
		{"Наука", "358", "loganettv", "Наука · loganettv"},
		// У известного канала уточнение — страна, а не плейлист.
		{"Наука", "Nauka.ua@SD", "loganettv", "Наука · Украина"},
		// Имя плейлиста не задано — уточнять нечем.
		{"Наука", "358", "  ", "Наука"},
	}
	for _, c := range cases {
		ch := db.IPTVChannel{Name: c.name, EPGID: c.id, PlaylistID: 11}
		if got := qualifyName(c.name, ch, ref, c.playlist); got != c.want {
			t.Errorf("qualifyName(%q, %q, %q) = %q, ждали %q", c.name, c.id, c.playlist, got, c.want)
		}
	}
}
