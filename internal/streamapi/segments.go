package streamapi

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/zeoril1/video_viewer/internal/catalog"
	"github.com/zeoril1/video_viewer/internal/segments"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

type mediaChapter struct {
	Start ffprobeNum `json:"start_time"`
	End   ffprobeNum `json:"end_time"`
	Tags  struct {
		Title string `json:"title"`
	} `json:"tags"`
}

var chapterTypes = []struct {
	typ string
	re  *regexp.Regexp
}{
	{segments.Recap, regexp.MustCompile(`(?i)^(?:recap|previously(?: on)?|previous episode|повтор|краткое содержание|в предыдущих сериях|ранее в сериале)(?:$|[\s:._\-\[(])`)},
	{segments.Intro, regexp.MustCompile(`(?i)^(?:intro(?:duction)?|opening(?: credits)?|op(?:\s*\d+)?|заставка|вступление|опенинг)(?:$|[\s:._\-\[(])`)},
	{segments.Credits, regexp.MustCompile(`(?i)^(?:credits|end credits|ending(?: credits| theme)?|outro|ed(?:\s*\d+)?|титры|финальные титры|заключительные титры|эндинг)(?:$|[\s:._\-\[(])`)},
}

// Named chapters are useful only when their location and length fit the type.
// Generic chapter numbers and closing scenes are never inferred as credits.
func chapterSegments(chapters []mediaChapter, duration float64) []segments.Segment {
	result := []segments.Segment{}
	for _, chapter := range chapters {
		for _, rule := range chapterTypes {
			if !rule.re.MatchString(strings.TrimSpace(chapter.Tags.Title)) {
				continue
			}
			s := segments.Segment{Type: rule.typ, Start: chapter.Start.Val, End: chapter.End.Val, Source: "chapter", AutoSkip: true}
			length := s.End - s.Start
			if !segments.Validate(s, duration) || length < 3 {
				break
			}
			switch rule.typ {
			case segments.Intro:
				if s.Start > 600 || s.Start > duration*0.4 || length > 180 {
					break
				}
				result = append(result, s)
			case segments.Recap:
				if s.Start <= 300 && s.Start <= duration*0.25 && length <= 300 {
					result = append(result, s)
				}
			case segments.Credits:
				if s.Start >= duration*0.65 && length <= 600 {
					result = append(result, s)
				}
			}
			break
		}
	}
	result = segments.Normalize(result, duration)
	// Broken overlapping chapter ranges require a human check before auto-skip.
	for i := range result {
		for j := i + 1; j < len(result); j++ {
			if result[j].Start < result[i].End && result[i].Start < result[j].End {
				result[i].AutoSkip = false
				result[j].AutoSkip = false
			}
		}
	}
	return result
}

// Resolve through the same selection function as the media endpoint: even an
// out-of-range explicit file falls back to the largest file in existing streams.
func resolveMediaKey(ctx context.Context, mgr *torrents.Manager, id, magnet string, requested int) string {
	if mgr == nil {
		return ""
	}
	t, release, err := mgr.Acquire(catalog.Item{ID: id, Magnet: magnet})
	if err != nil {
		return ""
	}
	defer release()
	select {
	case <-ctx.Done():
		return ""
	case <-t.GotInfo():
	default:
		return "" // Never wait for extra torrent data to provide optional metadata.
	}
	f, err := selectVideoFile(t, requested)
	if err != nil {
		return ""
	}
	for index, candidate := range t.Files() {
		if candidate == f {
			return segmentMediaKey(magnet, index)
		}
	}
	return ""
}

func segmentMediaKey(magnet string, file int) string {
	mi, err := metainfo.ParseMagnetUri(magnet)
	if err != nil || file < 0 || mi.InfoHash == (metainfo.Hash{}) {
		return ""
	}
	return fmt.Sprintf("segments.%s.%d", mi.InfoHash.String(), file)
}
