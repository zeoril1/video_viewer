package iptv

import "testing"

// TestParseRefIndexRussianNames — справочник iptv-org даёт родные названия
// каналов: по id из плейлиста («MoyaPlaneta.ru@SD») находим «Моя Планета».
func TestParseRefIndexRussianNames(t *testing.T) {
	data := []byte(`[
		{"id":"MoyaPlaneta.ru","name":"Moya Planeta","alt_names":["Моя Планета"],"country":"RU"},
		{"id":"LoveNature.ca","name":"Love Nature","alt_names":[],"country":"CA"},
		{"id":"BBCEarth.ca","name":"BBC Earth","alt_names":["Rush HD"],"country":"CA"},
		{"id":"Dikayaokhota.ru","name":"Дикая охота","alt_names":[],"country":"RU"}
	]`)
	ref, err := ParseRefIndex(data)
	if err != nil {
		t.Fatalf("разбор справочника: %v", err)
	}
	if ref.Len() != 4 {
		t.Errorf("записей %d, ждали 4", ref.Len())
	}
	cases := map[string]string{
		"MoyaPlaneta.ru@SD": "Моя Планета",
		"LoveNature.ca":     "",
		"BBCEarth.ca@HD":    "", // «Rush HD» — не русское название
		"Dikayaokhota.ru":   "Дикая охота",
		"Unknown.tv":        "",
	}
	for id, want := range cases {
		if got := ref.RussianName(id); got != want {
			t.Errorf("RussianName(%q) = %q, ждали %q", id, got, want)
		}
	}
	var nilRef *RefIndex
	if got := nilRef.RussianName("MoyaPlaneta.ru"); got != "" {
		t.Errorf("без справочника имя %q, ждали пусто", got)
	}
}

// TestRefIndexCountry — из справочника берём и страну вещания канала: ею
// различаются одноимённые каналы («National Geographic» в Болгарии и Чехии).
func TestRefIndexCountry(t *testing.T) {
	data := []byte(`[
		{"id":"NationalGeographic.bg","name":"National Geographic","country":"bg"},
		{"id":"MoyaPlaneta.ru","name":"Moya Planeta","alt_names":["Моя Планета"],"country":"RU"},
		{"id":"NoCountry.tv","name":"No Country","alt_names":[],"country":""}
	]`)
	ref, err := ParseRefIndex(data)
	if err != nil {
		t.Fatalf("разбор справочника: %v", err)
	}
	cases := map[string]string{
		"NationalGeographic.bg@SD": "BG", // пометка варианта отбрасывается, регистр — к верхнему
		"MoyaPlaneta.ru":           "RU",
		"NoCountry.tv":             "",
		"Unknown.tv":               "",
	}
	for id, want := range cases {
		if got := ref.Country(id); got != want {
			t.Errorf("Country(%q) = %q, ждали %q", id, got, want)
		}
	}
	var nilRef *RefIndex
	if got := nilRef.Country("NationalGeographic.bg"); got != "" {
		t.Errorf("без справочника страна %q, ждали пусто", got)
	}
}

// TestDisplayName — в интерфейс отдаём русское название, если оно известно,
// и в любом случае без пометки качества в скобках.
func TestDisplayName(t *testing.T) {
	cases := []struct {
		name, nameRU, want string
	}{
		{"Moya Planeta (1080p)", "Моя Планета", "Моя Планета"},
		{"Dikaya okhota HD (1080p)", "Дикая охота", "Дикая охота"},
		{"Belarus-5 (1080p) [Not 24/7]", "Беларусь 5", "Беларусь 5"}, // пометку дописывает плейлист
		{"Love Nature 4K (2160p) [Geo-blocked]", "", "Love Nature 4K [Geo-blocked]"},
		{"Agro TV (480p)", "", "Agro TV"},
	}
	for _, c := range cases {
		if got := DisplayName(c.name, c.nameRU); got != c.want {
			t.Errorf("DisplayName(%q, %q) = %q, ждали %q", c.name, c.nameRU, got, c.want)
		}
	}
}

// TestChannelKeyDedupesSameChannel — один канал из разных плейлистов и разные
// варианты его потока дают один ключ, а разные ленты (BBC Earth и BBC Earth
// Czechia с общим tvg-id) — разные.
func TestChannelKeyDedupesSameChannel(t *testing.T) {
	mir := ChannelKey("Mir.ru@SD", DisplayName("Мир HD (1080p)", "Мир"))
	other := ChannelKey("Mir.ru", DisplayName("Mir (720p)", "Мир"))
	if mir == "" || mir != other {
		t.Errorf("варианты одного канала не совпали: %q и %q", mir, other)
	}
	// Тот же канал в другом плейлисте, где справочник не нашёл русское имя.
	if k := ChannelKey("Mir.ru", DisplayName("Mir (576p)", "")); k == mir {
		t.Errorf("латинское имя без справочника не должно совпадать с русским: %q", k)
	}
	// Разные ленты вещания с одним tvg-id различаются названием.
	en := ChannelKey("BBCEarth.uk", "BBC Earth")
	cz := ChannelKey("BBCEarth.uk", "BBC Earth Czechia")
	if en == cz {
		t.Errorf("разные ленты схлопнулись в один ключ: %q", en)
	}
	// Порядковый номер вместо tvg-id в ключе не участвует.
	if k := ChannelKey("7", "Моя Планета"); k != NameKey("Моя Планета") {
		t.Errorf("номер как tvg-id попал в ключ: %q", k)
	}
}

// TestStreamQuality — из вариантов потока одного канала выживает лучший:
// выше разрешение, без гео-блокировки и без пометки «не 24/7».
func TestStreamQuality(t *testing.T) {
	full := StreamQuality("Моя Планета (1080p)", true)
	if q := StreamQuality("Моя Планета (720p)", true); q >= full {
		t.Errorf("720p (%d) не должен быть лучше 1080p (%d)", q, full)
	}
	if q := StreamQuality("Love Nature 4K (2160p) [Geo-blocked]", true); q >= StreamQuality("Love Nature (1080p)", true) {
		t.Errorf("гео-блокированный 4K (%d) не должен выигрывать у 1080p", q)
	}
	if q := StreamQuality("Agro TV (1080p) [Not 24/7]", true); q >= StreamQuality("Agro TV (720p)", true) {
		t.Errorf("1080p «не 24/7» (%d) не должен выигрывать у стабильного 720p", q)
	}
	if q := StreamQuality("Agro TV (480p)", false); q <= StreamQuality("Agro TV (360p)", false) {
		t.Errorf("480p (%d) должен быть лучше 360p", q)
	}
	if q := StreamQuality("Канал (576p)", true); q <= StreamQuality("Канал (576p)", false) {
		t.Errorf("HLS-вариант (%d) должен выигрывать у TS при равном разрешении", q)
	}
	// «Архив» — перемотка канала, а не живой эфир: у одного tvg-id рядом идут
	// «Звезда», «Звезда HD» и «Звезда (Архив)», и каналом должен остаться прямой поток.
	if q := StreamQuality("Звезда (Архив)", true); q >= StreamQuality("Звезда", true) {
		t.Errorf("архивный поток (%d) не должен выигрывать у прямого (%d)", q, StreamQuality("Звезда", true))
	}
}
