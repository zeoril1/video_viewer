package releasewatch

import "testing"

func TestQualityProvenanceAndResolutionAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		title, format string
		resolution    int
	}{
		{"Movie (2026) TS 1080p", "TS", 1080},
		{"Movie HDCAM 1080p", "CAM", 1080},
		{"Movie HDTC 720p", "TC", 720},
		{"Movie DVDScr", "SCR", 0},
		{"Movie DVD9", "DVD", 0},
		{"Movie WEB-DL 1080p", "WEB", 1080},
		{"Movie WEBRip 2160p", "WEB", 2160},
		{"Movie BRRip 720p", "BD", 720},
		{"Movie Blu-ray 1080p", "BD", 1080},
		{"Movie BDRip 1080p", "BD", 1080},
		{"Movie HDRip 1080p", "UNKNOWN", 1080},
		{"Movie HDTVRip 1080i", "HDTV", 1080},
		{"Show S01-10 BDRip, WEB-DL 1080p / 720p", "UNKNOWN", 0},
		{"Show BDRip 2160p + 4K", "BD", 2160},
	} {
		if got := normalizeFormat(tc.title); got != tc.format {
			t.Errorf("format(%q) = %q; want %q", tc.title, got, tc.format)
		}
		if got := sourceResolution(tc.title, ""); got != tc.resolution {
			t.Errorf("resolution(%q) = %d; want %d", tc.title, got, tc.resolution)
		}
	}
	if !(FormatRank("TS") < FormatRank("WEB") && FormatRank("WEB") < FormatRank("BR")) {
		t.Fatal("source upgrade order must recognize BR aliases")
	}
}

func TestMixedPackQualityNeedsPerFileEvidence(t *testing.T) {
	source := releaseSource{Title: "Show S01-10 BDRip, WEB-DL 1080p/720p | LostFilm", Quality: "1080"}
	files := []releaseFile{
		{Name: "15. Потрошитель.mkv", Season: 7, Episode: 15, Size: 1000},
		{Name: "Show.S07E16.WEB-DL.720p.mkv", Season: 7, Episode: 16, Size: 1000},
		{Name: "Show.S07E17.BDRip.1080p.mkv", Season: 7, Episode: 17, Size: 1000},
	}
	got := NormalizeVariants(variantsForFiles(source, files, true))
	if len(got) != 3 {
		t.Fatalf("want three observed episodes: %v", got)
	}
	if got[0].Format != "UNKNOWN" || got[0].Resolution != 0 {
		t.Errorf("mixed pack cannot promote anonymous file to best declared quality: %v", got[0])
	}
	if got[1].Format != "WEB" || got[1].Resolution != 720 || got[2].Format != "BD" || got[2].Resolution != 1080 {
		t.Errorf("explicit filename quality must be retained: %v", got)
	}
}

func TestDeclaredVoicesAndAliases(t *testing.T) {
	for _, tc := range []struct {
		title, audio string
		want         []string
	}{
		{"Show WEB-DL | Lost Film, New Studio, Кравец, Original", "", []string{"LostFilm", "NewStudio", "Kravec", "Оригинал"}},
		{"Show | NovaMedia", "", []string{"НоваМедиа"}},
		{"Show | HDRezka Studio", "", []string{"HDRezka Studio"}},
		{"Show | Дубляж", "", []string{"Дубляж"}},
		{"Show", "multi", []string{"Многоголосый перевод"}},
		{"A Quiet Place WEB-DL", "", []string{UnknownVoice}},
		{"Johnny English WEB-DL", "", []string{UnknownVoice}},
		{"Show WEB-DL | озвучка: Студия X, Студия Y", "", []string{"Студия X", "Студия Y"}},
		{"Show WEB-DL | озвучка: Lost Film, Студия X", "", []string{"LostFilm", "Студия X"}},
		{"Show WEB-DL | D, P", "", []string{"Дубляж"}},
	} {
		got := declaredVoices(tc.title, tc.audio)
		if len(got) != len(tc.want) {
			t.Errorf("voices(%q) = %v; want %v", tc.title, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("voices(%q) = %v; want %v", tc.title, got, tc.want)
			}
		}
	}
}

func TestVariantIdentityIgnoresCosmeticAliases(t *testing.T) {
	got := NormalizeVariants([]Variant{
		{Season: 7, Episode: 15, Format: "BRRip", Resolution: 1080, Voice: "Кравец"},
		{Season: 7, Episode: 15, Format: "BD", Resolution: 1080, Voice: "Kravec"},
		{Season: 7, Episode: 15, Format: "WEB-DL", Resolution: 1080, Voice: "Lost Film"},
		{Season: 7, Episode: 15, Format: "WEB", Resolution: 1080, Voice: "LostFilm"},
	})
	if len(got) != 2 {
		t.Fatalf("aliases must identify two semantic variants, got %v", got)
	}
	if got[0].Key() == got[1].Key() {
		t.Fatal("different voices must be different variants")
	}
}

func TestFileEvidenceRejectsInventedEpisodesAndExtras(t *testing.T) {
	source := releaseSource{Title: "Show S01-07 WEB-DL 1080p | LostFilm, NewStudio"}
	files := []releaseFile{
		{Name: "15. Потрошитель.mkv", Season: 7, Episode: 15, Size: 1234},
		{Name: "Show.S07E16.NewStudio.mkv", Season: 7, Episode: 16, Size: 1234},
		{Name: "274 seriya.mkv", Season: 13, Episode: 10, Size: 1234},
		{Name: "Unidentified movie.mkv", Season: 1, Episode: 99, Size: 1234},
		{Name: "Show.S07E17.sample.mkv", Season: 7, Episode: 17, Size: 1234},
		{Name: "Show.S07E18.mkv", Season: 7, Episode: 18},
	}
	got := NormalizeVariants(variantsForFiles(source, files, true))
	if len(got) != 5 {
		t.Fatalf("want two voices E15, one E16, two absolute-number mapped E10; got %v", got)
	}
	for _, variant := range got {
		if variant.Episode == 99 || variant.Episode == 17 || variant.Episode == 18 {
			t.Errorf("unverified or extra episode leaked: %v", variant)
		}
		if variant.Episode == 16 && variant.Voice != "NewStudio" {
			t.Errorf("file-specific declared voice should override pack voice set: %v", variant)
		}
	}
}
