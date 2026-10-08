package streamapi

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const hlsBackCache = 180.0

// A browser may play its forward buffer without requesting a segment for over
// 90 seconds. Heartbeats keep the exact current HLS session alive independently
// of network requests. Old queued seek/track updates cannot touch its successor.
func (m *hlsManager) touchPlayback(id, session, magnet string, file int, start float64, track, subs int, quality string, position float64) bool {
	if session == "" || position < 0 || start < 0 || math.IsNaN(position) || math.IsInf(position, 0) || math.IsNaN(start) || math.IsInf(start, 0) {
		return false
	}
	if quality == "" {
		quality = "source"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[hlsSessionKey(id, session)]
	if s == nil || s.magnet != strings.TrimSpace(magnet) || s.file != file || s.track != track || s.subs != subs || s.start != start || s.quality != quality {
		return false
	}
	now := time.Now()
	s.lastUsed, s.lastPlayback = now, now
	s.playhead = math.Max(0, position-s.start)
	return true
}

var consumedHLSName = regexp.MustCompile(`^(seg_[0-9]+\.(m4s|ts)|playlist[0-9]+\.vtt)$`)

// Only completed, owned segment names from FFmpeg's local playlist are used.
// Init files, .tmp output and subtitle metadata are never removed here.
func consumedSegmentNames(data []byte, before float64) []string {
	var elapsed, duration float64
	var pending bool
	result := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXTINF:") {
			if pending {
				return result
			}
			var err error
			duration, err = strconv.ParseFloat(strings.SplitN(strings.TrimPrefix(line, "#EXTINF:"), ",", 2)[0], 64)
			if err != nil || duration <= 0 || math.IsInf(duration, 0) || math.IsNaN(duration) {
				return result
			}
			pending = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !pending {
			continue
		}
		elapsed += duration
		pending = false
		if elapsed < before && consumedHLSName.MatchString(line) {
			result = append(result, line)
		}
	}
	return result
}

func (m *hlsManager) pruneConsumedOnce(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		// An abandoned client gives no trustworthy current position. Normal idle
		// cleanup handles it; an active viewer keeps three minutes behind itself.
		if s.lastPlayback.IsZero() || now.Sub(s.lastPlayback) > viewerTTL || s.playhead <= hlsBackCache {
			continue
		}
		for _, name := range []string{filepath.Base(s.playlist), "playlist_vtt.m3u8"} {
			data, err := os.ReadFile(filepath.Join(s.dir, name))
			if err != nil {
				continue
			}
			for _, segment := range consumedSegmentNames(data, s.playhead-hlsBackCache) {
				_ = os.Remove(filepath.Join(s.dir, segment))
			}
		}
	}
}

func (m *hlsManager) pruneConsumed() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case now := <-ticker.C:
			m.pruneConsumedOnce(now)
		}
	}
}
