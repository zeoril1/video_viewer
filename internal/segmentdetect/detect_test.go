package segmentdetect

import (
	"math"
	"math/rand"
	"testing"

	"github.com/zeoril1/video_viewer/internal/segments"
)

func record(seed int64, episode int, seconds int) segments.AnalysisRecord {
	frames := make([]uint32, seconds*2)
	random := rand.New(rand.NewSource(seed))
	for i := range frames {
		frames[i] = random.Uint32() | 1
	}
	return segments.AnalysisRecord{MediaKey: string(rune('a' + episode)), FilmID: "tt1234567", Season: 1, Episode: episode,
		Duration: float64(seconds), Version: Version, Fingerprint: frames, Step: Step}
}

func copyAudio(a, b *segments.AnalysisRecord, from, to, duration int) {
	copy(b.Fingerprint[to*2:(to+duration)*2], a.Fingerprint[from*2:(from+duration)*2])
}

func findKind(list []segments.Segment, kind string) []segments.Segment {
	var found []segments.Segment
	for _, item := range list {
		if item.Type == kind {
			found = append(found, item)
		}
	}
	return found
}

func assertRange(t *testing.T, list []segments.Segment, kind string, start, end float64) {
	t.Helper()
	for _, item := range list {
		if item.Type == kind && item.Start == start && item.End == end {
			if item.AutoSkip || item.Source != "audio_match" {
				t.Fatalf("audio detection must remain a suggestion: %+v", item)
			}
			return
		}
	}
	t.Fatalf("missing %s [%g,%g): %+v", kind, start, end, list)
}

func TestDetectShiftedOpeningAndCodecDifferences(t *testing.T) {
	previous, next := record(1, 1, 1200), record(2, 2, 1230)
	copyAudio(&previous, &next, 50, 95, 80)
	for i := 95 * 2; i < 175*2; i++ {
		next.Fingerprint[i] ^= 1<<8 | 1<<17 | 1<<26
	}
	a, b := Detect(previous, next)
	assertRange(t, a, segments.Intro, 50, 130)
	assertRange(t, b, segments.Intro, 95, 175)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("unexpected extra ranges: %+v %+v", a, b)
	}
}

func TestDetectPreservesSceneBetweenCreditBlocks(t *testing.T) {
	previous, next := record(3, 1, 1200), record(4, 2, 1240)
	copyAudio(&previous, &next, 1050, 1090, 50)
	copyAudio(&previous, &next, 1130, 1170, 60)
	a, b := Detect(previous, next)
	assertRange(t, a, segments.Credits, 1050, 1100)
	assertRange(t, a, segments.Credits, 1130, 1190)
	assertRange(t, b, segments.Credits, 1090, 1140)
	assertRange(t, b, segments.Credits, 1170, 1230)
	if len(findKind(a, segments.Credits)) != 2 || len(findKind(b, segments.Credits)) != 2 {
		t.Fatalf("credit blocks were merged: %+v %+v", a, b)
	}
}

func TestDetectRecapReusesOnlyPreviousBody(t *testing.T) {
	previous, next := record(5, 1, 1200), record(6, 2, 1200)
	copyAudio(&previous, &next, 90, 60, 60)
	copyAudio(&previous, &next, 620, 8, 7)
	copyAudio(&previous, &next, 900, 20, 10)
	// The same audio from previously identified credits is not a recap.
	previous.Segments = []segments.Segment{{Type: segments.Credits, Start: 950, End: 980, Source: "chapter"}}
	copyAudio(&previous, &next, 960, 35, 8)
	a, b := Detect(previous, next)
	assertRange(t, a, segments.Intro, 90, 150)
	assertRange(t, b, segments.Intro, 60, 120)
	assertRange(t, b, segments.Recap, 8, 15)
	assertRange(t, b, segments.Recap, 20, 30)
	if len(findKind(a, segments.Recap)) != 0 || len(findKind(b, segments.Recap)) != 2 {
		t.Fatalf("wrong recap ranges: %+v %+v", a, b)
	}
}

func TestDetectRejectsSilenceNoiseAndLowInformation(t *testing.T) {
	for _, mode := range []string{"silence", "constant", "repeating", "noise", "brief"} {
		t.Run(mode, func(t *testing.T) {
			a, b := record(9, 1, 600), record(10, 2, 600)
			if mode != "noise" && mode != "brief" {
				for i := range a.Fingerprint {
					value := uint32(0)
					if mode == "constant" {
						value = 0xaaaa5555
					}
					if mode == "repeating" {
						value = 0xaaaa5555 ^ uint32(i%4)
					}
					a.Fingerprint[i], b.Fingerprint[i] = value, value
				}
			}
			if mode == "brief" {
				copyAudio(&a, &b, 30, 50, 8)
			}
			x, y := Detect(a, b)
			if len(x) != 0 || len(y) != 0 {
				t.Fatalf("false detection from %s: %+v %+v", mode, x, y)
			}
		})
	}
}

func TestDetectValidatesVersionsIdentityAndTime(t *testing.T) {
	a, b := record(12, 1, 600), record(13, 2, 600)
	copyAudio(&a, &b, 30, 50, 50)
	for _, mutate := range []func(*segments.AnalysisRecord){
		func(r *segments.AnalysisRecord) { r.Version++ },
		func(r *segments.AnalysisRecord) { r.Step = 1 },
		func(r *segments.AnalysisRecord) { r.Duration = math.NaN() },
		func(r *segments.AnalysisRecord) { r.Duration = 21601 },
		func(r *segments.AnalysisRecord) { r.FilmID = "tt9999999" },
		func(r *segments.AnalysisRecord) { r.Episode = 7 },
		func(r *segments.AnalysisRecord) { r.MediaKey = a.MediaKey },
		func(r *segments.AnalysisRecord) { r.Fingerprint = r.Fingerprint[:20] },
	} {
		next := b
		mutate(&next)
		if x, y := Detect(a, next); len(x) != 0 || len(y) != 0 {
			t.Fatalf("accepted incompatible pair: %+v", next)
		}
	}
}

func TestDetectAllowsBoundedDropouts(t *testing.T) {
	a, b := record(14, 1, 600), record(15, 2, 600)
	copyAudio(&a, &b, 30, 50, 50)
	b.Fingerprint[130], b.Fingerprint[131] = 0, 0
	x, y := Detect(a, b)
	assertRange(t, x, segments.Intro, 30, 80)
	assertRange(t, y, segments.Intro, 50, 100)
}

func BenchmarkDetectMaximumEpisode(b *testing.B) {
	a, next := record(20, 1, 21600), record(21, 2, 21600)
	for b.Loop() {
		Detect(a, next)
	}
}
