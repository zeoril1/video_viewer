package magnet

import "testing"

func TestParseTitle(t *testing.T) {
	cases := []struct {
		title   string
		quality string
		audio   string
		season  int
	}{
		{"Одержимость / Whiplash (2014) BDRip 1080p MVO", "1080", "multi", 0},
		{"Название (2020) WEB-DLRip 720p Дублированный", "720", "dub", 0},
		{"Сериал Сезон 2 (2021) WEBRip 1080p Оригинальная дорожка", "1080", "original", 2},
		{"Show S03E01 2160p 4K UHD 2VO", "2160", "two", 3},
		{"Something 480p SD AVC", "480", "", 0},
		{"Фильм (2019) HDRip Оригинальная дорожка", "", "original", 0},
		{"Аниме Сезон 1 (2022) 1080p Субтитры", "1080", "subs", 1},
		{"NoQuality NoAudio (2019)", "", "", 0},
		{"Кино (2020) WEB-DLRip FullHD Профессиональный многоголосый", "1080", "multi", 0},
		{"Сериал 2023 сезон 4 720p Одноголосый", "720", "single", 4},
		// Сезон в квадратных скобках (NNM): "[S8]", "[S9]".
		{"Рик и Морти / Rick and Morty [S8] (2025) WEB-DL 1080p", "1080", "", 8},
		{"Рик и Морти / Rick and Morty [S9] (2026) WEB-DL 1080p", "1080", "", 9},
		// Диапазон сезонов — полный сборник (сезон 0).
		{"Рик и Морти / Rick and Morty [S1-9] (2013-2026) BDRip 1080p", "1080", "", 0},
		{"Сериал S1-S2 (2020) WEBRip 720p", "720", "", 0},
		// Отдельный эпизод в раздаче одного сезона.
		{"Рик и Морти / Rick and Morty, S1E1-11 of 11 (2013-2014) HDRip", "", "", 1},
		// Известные студии озвучки в заголовке.
		{"Рик и Морти / Rick and Morty [S1-9] (2013-2026) BDRip 1080p-Сыендук", "1080", "multi", 0},
		{"Сериал (2021) WEB-DL 1080p LostFilm", "1080", "multi", 0},
		{"Мультфильм (2018) BDRip 720p Кубик в Кубе", "720", "single", 0},
		{"Фильм (2019) WEBRip 1080p Авторский перевод Гоблин", "1080", "single", 0},
		// Реальные названия раздач (Очень страшное кино 2026, rutracker/megapeer).
		{"Очень страшное кино / Scary Movie (Майкл Тиддес / Michael Tiddes) [2026, США, Комедия, ужасы, WEB-DL] Dub (Продубляж)", "", "dub", 0},
		{"Очень страшное кино / Scary Movie (2026) WEB-DL-HEVC 2160p | 4K | HDR10+ | А | Котов | Расширенная версия", "2160", "single", 0},
		{"Очень страшное кино / Scary Movie (2026) WEB-DL 1080p | D, A | Продубляж, Котов | Расширенная версия", "1080", "dub", 0},
		{"Очень страшное кино / Scary Movie (2026) WEB-DL-AVC от ELEKTRI4KA | D-Продубляж", "", "dub", 0},
		// Буквенные коды озвучек в теговых полях (Дюна: Часть вторая 2024).
		{"Дюна: Часть вторая / Dune: Part Two (2024) Blu-Ray Remux 2160p | 4K | HDR10 | Dolby Vision | D, P, A", "2160", "dub", 0},
		{"Дюна: Часть вторая / Dune: Part Two (2024) UHD BDRemux 2160p | 4K | HDR | Dolby Vision | D, P | UKR", "2160", "dub", 0},
		{"Дюна: Часть вторая / Dune: Part Two (2024) BDRip 1080p от селезень | D", "1080", "dub", 0},
		{"Дюна: Часть вторая / Dune: Part Two (2024) HDRip | D", "", "dub", 0},
		{"Дюна: Часть вторая / Dune: Part Two (2024) WEB-DL 1080p от ExKinoRay | D, P, A", "1080", "dub", 0},
		{"Фильм / Movie (2024) UHD BDRip 2160p | D, Р", "2160", "dub", 0},
		{"Сериал / Series (2023) 1080p | P", "1080", "multi", 0},
		{"Фильм / Movie (2022) 1080p | A", "1080", "single", 0},
		// Негативные: одиночные буквы/слова НЕ принимаем за коды озвучки.
		{"Тихое место / A Quiet Place (2018) BDRip 1080p", "1080", "", 0},
		{"Дюна: Часть вторая / Dune: Part Two (2024) UHD BDRip [AV1/2160p] [4K, HDR, Dolby Vision Profile 10, 10-bit]", "2160", "", 0},
	}
	for _, c := range cases {
		q, a, s := ParseTitle(c.title)
		if q != c.quality || a != c.audio || s != c.season {
			t.Errorf("ParseTitle(%q) = (%q, %q, %d), want (%q, %q, %d)",
				c.title, q, a, s, c.quality, c.audio, c.season)
		}
	}
}

func TestIsFullCollection(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"Рик и Морти / Rick and Morty [S1-9] (2013-2026) BDRip 1080p", true},
		{"Сериал S1-S2 (2020) WEBRip 720p", true},
		{"Рик и Морти / Rick and Morty, S1E1-11 of 11 (2013-2014) HDRip", false},
		{"Сериал Сезон 3 (2021) WEBRip 1080p", false},
		{"Show S03E01 2160p 4K UHD 2VO", false},
		{"Рик и Морти / Rick and Morty [S9] (2026) WEB-DL 1080p", false},
	}
	for _, c := range cases {
		if got := IsFullCollection(c.title); got != c.want {
			t.Errorf("IsFullCollection(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}

func TestParseTitleFullHD(t *testing.T) {
	// srcResRe берёт самое левое совпадение: «FullHD» даёт 1080, хотя в
	// заголовке есть и «4K» — левосторонний матч регэкспа (заданное поведение).
	q, _, _ := ParseTitle("FullHD 4K")
	if q != "1080" {
		t.Errorf("ParseTitle(FullHD 4K) quality = %q, want 1080", q)
	}
}
