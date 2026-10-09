package segmentdetect

import (
	"math"
	"math/bits"
	"sort"

	"github.com/zeoril1/video_viewer/internal/segments"
)

type interval struct{ start, end int }
type audioMatch struct {
	a, b              interval
	matched, distance int
}

// Detect compares only adjacent episodes of the same show. It returns measured
// repeated ranges for each exact file, with recaps belonging only to next.
// Opening and credits must repeat for at least 20 seconds. A recap clip must
// reuse at least four seconds from the preceding episode's body. New narration,
// different mixes, heavily edited recaps, and nonrecurring credits can be missed.
func Detect(previous, next segments.AnalysisRecord) ([]segments.Segment, []segments.Segment) {
	if !compatible(previous, next) {
		return nil, nil
	}
	a, b := previous.Fingerprint, next.Fingerprint
	aHead := interval{0, min(len(a), int(math.Min(600, previous.Duration*0.35)/Step))}
	bHead := interval{0, min(len(b), int(math.Min(600, next.Duration*0.35)/Step))}
	aTail := interval{max(0, len(a)-int(math.Min(480, previous.Duration*0.25)/Step)), len(a)}
	bTail := interval{max(0, len(b)-int(math.Min(480, next.Duration*0.25)/Step)), len(b)}
	var previousSegments, nextSegments []segments.Segment
	intro := selectMatches(findMatches(a, b, aHead, bHead, 40, 480, 6, 80), 1, true)
	for _, match := range intro {
		previousSegments = append(previousSegments, makeSegment(segments.Intro, match.a, previous.Duration))
		nextSegments = append(nextSegments, makeSegment(segments.Intro, match.b, next.Duration))
	}
	// Do not merge nonmatching credit blocks: a scene between them remains intact.
	credits := selectMatches(findMatches(a, b, aTail, bTail, 40, 960, 6, 80), 8, true)
	for _, match := range credits {
		previousSegments = append(previousSegments, makeSegment(segments.Credits, match.a, previous.Duration))
		nextSegments = append(nextSegments, makeSegment(segments.Credits, match.b, next.Duration))
	}
	// The previous body ends at the first credit block. If no reliable opening or
	// credits are available, conservative head/tail windows exclude those regions.
	body := interval{aHead.end, aTail.start}
	knownOpening, knownCredits := false, false
	for _, list := range [][]segments.Segment{previous.Segments, previousSegments} {
		for _, existing := range list {
			if !segments.Validate(existing, previous.Duration) {
				continue
			}
			if existing.Type == segments.Intro {
				end := int(math.Ceil(existing.End / Step))
				if !knownOpening {
					body.start, knownOpening = end, true
				} else {
					body.start = max(body.start, end)
				}
			}
			if existing.Type == segments.Credits {
				start := int(math.Floor(existing.Start / Step))
				if !knownCredits {
					body.end, knownCredits = start, true
				} else {
					body.end = min(body.end, start)
				}
			}
		}
	}
	recapHead := interval{0, min(len(b), int(math.Min(300, next.Duration*0.20)/Step))}
	if len(intro) > 0 {
		recapHead.end = min(recapHead.end, intro[0].b.start)
	}
	for _, existing := range next.Segments {
		if existing.Type == segments.Intro && segments.Validate(existing, next.Duration) {
			recapHead.end = min(recapHead.end, int(existing.Start/Step))
		}
	}
	var recaps []audioMatch
	for _, span := range bodyIntervals(body, previous.Segments, previousSegments, previous.Duration) {
		recaps = append(recaps, findMatches(a, b, span, recapHead, 8, 240, 5, 88)...)
	}
	for _, match := range selectMatches(recaps, 24, false) {
		nextSegments = append(nextSegments, makeSegment(segments.Recap, match.b, next.Duration))
	}
	return segments.Normalize(previousSegments, previous.Duration), segments.Normalize(nextSegments, next.Duration)
}

func compatible(a, b segments.AnalysisRecord) bool {
	if a.FilmID == "" || a.FilmID != b.FilmID || a.MediaKey != "" && a.MediaKey == b.MediaKey ||
		a.Version != Version || b.Version != Version || a.Step != Step || b.Step != Step ||
		!validDuration(a.Duration) || !validDuration(b.Duration) ||
		len(a.Fingerprint) < 1 || len(b.Fingerprint) < 1 || len(a.Fingerprint) > maxFrames || len(b.Fingerprint) > maxFrames ||
		math.Abs(float64(len(a.Fingerprint))*Step-a.Duration) > Step || math.Abs(float64(len(b.Fingerprint))*Step-b.Duration) > Step {
		return false
	}
	return a.Season >= 0 && a.Episode >= 1 && b.Episode >= 1 &&
		(a.Season == b.Season && b.Episode == a.Episode+1 || b.Season == a.Season+1 && b.Episode == 1)
}

func makeSegment(kind string, frames interval, duration float64) segments.Segment {
	return segments.Segment{Type: kind, Start: float64(frames.start) * Step, End: math.Min(float64(frames.end)*Step, duration), Source: "audio_match", AutoSkip: false}
}

// findMatches scans diagonals using constant memory per comparison. For a
// six-hour episode the largest comparison (body versus next recap window) is
// below 26 million Hamming distances. Short dropouts of at most one second are
// tolerated, but a different scene breaks a run and cannot be skipped with it.
func findMatches(a, b []uint32, aRange, bRange interval, minimum, maximum, tolerance, percent int) []audioMatch {
	if aRange.end-aRange.start < minimum || bRange.end-bRange.start < minimum {
		return nil
	}
	var matches []audioMatch
	appendRun := func(aStart, bStart, last, count, distance int) {
		length := last + 1
		if length < minimum || length > maximum || count*100 < length*percent {
			return
		}
		// Require stable boundaries. A single coincidental similar frame before
		// an opening must not pull preceding story into the suggested range.
		first, end, consecutive := -1, 0, 0
		for i := 0; i < length; i++ {
			if a[aStart+i] != 0 && b[bStart+i] != 0 && bits.OnesCount32(a[aStart+i]^b[bStart+i]) <= tolerance {
				consecutive++
				if consecutive >= 3 {
					if first < 0 {
						first = i - 2
					}
					end = i + 1
				}
			} else {
				consecutive = 0
			}
		}
		if first < 0 || end-first < minimum {
			return
		}
		aStart, bStart, length = aStart+first, bStart+first, end-first
		count, distance = 0, 0
		for i := 0; i < length; i++ {
			difference := bits.OnesCount32(a[aStart+i] ^ b[bStart+i])
			if a[aStart+i] != 0 && b[bStart+i] != 0 && difference <= tolerance {
				count++
				distance += difference
			}
		}
		aSpan, bSpan := interval{aStart, aStart + length}, interval{bStart, bStart + length}
		if !informative(a[aSpan.start:aSpan.end], minimum) || !informative(b[bSpan.start:bSpan.end], minimum) {
			return
		}
		match := audioMatch{a: aSpan, b: bSpan, matched: count, distance: distance}
		if len(matches) < 256 {
			matches = append(matches, match)
			return
		}
		// Repetitive input cannot produce an unbounded candidate list.
		worst := 0
		for i := 1; i < len(matches); i++ {
			if betterMatch(matches[worst], matches[i]) {
				worst = i
			}
		}
		if betterMatch(match, matches[worst]) {
			matches[worst] = match
		}
	}
	for diagonal := -(bRange.end - bRange.start - minimum); diagonal <= aRange.end-aRange.start-minimum; diagonal++ {
		aIndex, bIndex := aRange.start+max(diagonal, 0), bRange.start+max(-diagonal, 0)
		startA, startB, last, count, distance, misses := -1, -1, -1, 0, 0, 0
		for aIndex < aRange.end && bIndex < bRange.end {
			difference := bits.OnesCount32(a[aIndex] ^ b[bIndex])
			if a[aIndex] != 0 && b[bIndex] != 0 && difference <= tolerance {
				if startA < 0 {
					startA, startB = aIndex, bIndex
				}
				last, count, distance, misses = aIndex-startA, count+1, distance+difference, 0
			} else if startA >= 0 {
				misses++
				if misses > 2 {
					appendRun(startA, startB, last, count, distance)
					startA, startB, last, count, distance, misses = -1, -1, -1, 0, 0, 0
				}
			}
			aIndex++
			bIndex++
		}
		if startA >= 0 {
			appendRun(startA, startB, last, count, distance)
		}
	}
	return matches
}

func informative(frames []uint32, minimum int) bool {
	unique := make(map[uint32]struct{}, 12)
	var union, intersection uint32 = 0, ^uint32(0)
	var transitions, valid, previous uint32
	for _, frame := range frames {
		if frame == 0 {
			continue
		}
		if valid > 0 {
			transitions += uint32(bits.OnesCount32(previous ^ frame))
		}
		previous = frame
		valid++
		union |= frame
		intersection &= frame
		if len(unique) < 12 {
			unique[frame] = struct{}{}
		}
	}
	return int(valid) >= minimum && len(unique) >= min(12, max(4, minimum/2)) && bits.OnesCount32(union^intersection) >= 10 && transitions >= (valid-1)*2
}

func betterMatch(a, b audioMatch) bool {
	if a.matched != b.matched {
		return a.matched > b.matched
	}
	if a.distance != b.distance {
		return a.distance < b.distance
	}
	if a.b.start != b.b.start {
		return a.b.start < b.b.start
	}
	return a.a.start < b.a.start
}

func selectMatches(matches []audioMatch, limit int, both bool) []audioMatch {
	sort.Slice(matches, func(i, j int) bool { return betterMatch(matches[i], matches[j]) })
	var selected []audioMatch
	for _, match := range matches {
		overlap := false
		for _, existing := range selected {
			if overlaps(match.b, existing.b) || both && overlaps(match.a, existing.a) {
				overlap = true
				break
			}
		}
		if !overlap {
			selected = append(selected, match)
			if len(selected) == limit {
				break
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].b.start < selected[j].b.start })
	return selected
}

func overlaps(a, b interval) bool { return a.start < b.end && b.start < a.end }

func bodyIntervals(body interval, existing, detected []segments.Segment, duration float64) []interval {
	if body.end <= body.start {
		return nil
	}
	spans := []interval{body}
	for _, list := range [][]segments.Segment{existing, detected} {
		for _, skip := range list {
			if !segments.Validate(skip, duration) {
				continue
			}
			excluded := interval{int(math.Floor(skip.Start / Step)), int(math.Ceil(skip.End / Step))}
			var remaining []interval
			for _, span := range spans {
				if !overlaps(span, excluded) {
					remaining = append(remaining, span)
					continue
				}
				if span.start < excluded.start {
					remaining = append(remaining, interval{span.start, excluded.start})
				}
				if span.end > excluded.end {
					remaining = append(remaining, interval{excluded.end, span.end})
				}
			}
			spans = remaining
		}
	}
	return spans
}
