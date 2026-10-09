package streamapi

import (
	"errors"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zeoril1/video_viewer/internal/disklimit"
)

var errResources = errors.New("Сервер занят или достигнут лимит диска. Повторите позже.")

func (m *hlsManager) diskAvailable() bool {
	if m.minFreeBytes <= 0 {
		return true
	}
	free, err := disklimit.Free(m.dataDir)
	return err == nil && free >= uint64(m.minFreeBytes)
}

// FFmpeg writes independently; bound its output with a one-second watchdog.
// The spool budget is enforced before every write; HLS can overshoot between ticks.
func (m *hlsManager) watchResources() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			m.enforceResources(time.Now())
		}
	}
}

func hlsDirectoryBytes(dir string) int64 {
	var size int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size
}

func (m *hlsManager) resourcePressure() bool {
	return (m.maxDiskBytes > 0 && hlsDirectoryBytes(m.dataDir) > m.maxDiskBytes) || !m.diskAvailable()
}

// A fast source remux or a paused viewer must not erase every other viewer's
// cache. Evict abandoned sessions first. If pressure remains, seal one largest
// output window, retaining all requested segments and a buffer ahead of playback.
func (m *hlsManager) enforceResources(now time.Time) {
	if !m.resourcePressure() {
		return
	}
	var abandoned []*hlsSession
	m.mu.Lock()
	for key, s := range m.sessions {
		if s.lastUsed.Before(now.Add(-hlsIdleTimeout)) {
			abandoned = append(abandoned, s)
			delete(m.sessions, key)
		}
	}
	m.mu.Unlock()
	for _, s := range abandoned {
		log.Printf("hls: resource pressure: evict idle session %s", s.id)
		s.stop()
	}
	if !m.resourcePressure() {
		return
	}

	type candidate struct {
		key  string
		s    *hlsSession
		size int64
	}
	var candidates []candidate
	m.mu.Lock()
	for key, s := range m.sessions {
		if !s.resourceLimited {
			candidates = append(candidates, candidate{key: key, s: s})
		}
	}
	m.mu.Unlock()
	for i := range candidates {
		candidates[i].size = hlsDirectoryBytes(candidates[i].s.dir)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].size > candidates[j].size })
	for _, candidate := range candidates {
		m.mu.Lock()
		if m.sessions[candidate.key] != candidate.s || candidate.s.resourceLimited {
			m.mu.Unlock()
			continue
		}
		s := candidate.s
		s.resourceLimited = true
		if s.output != nil {
			s.output.close()
		}
		// Wait's observer closes done before taking m.mu, so it can be awaited
		// here while requests are blocked from registering additional segments.
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		if s.done != nil {
			<-s.done
		}
		if s.output != nil {
			s.output.uploads.Wait()
		}
		data, err := os.ReadFile(s.playlist)
		if err != nil {
			// No usable window was produced yet. Remove only this failed output.
			delete(m.sessions, candidate.key)
			m.mu.Unlock()
			s.stop()
			log.Printf("hls: resource pressure: stop unready session %s: %v", s.id, err)
			return
		}
		body, unused, until := sealHLSPlaylist(data, s.playhead+180, s.requestedSegments)
		s.resourcePlaylist = body
		if s.subs >= 0 {
			if data, err := os.ReadFile(filepath.Join(s.dir, "playlist_vtt.m3u8")); err == nil {
				body, names, _ := sealHLSPlaylist(data, until, s.requestedSegments)
				s.resourceSubPlaylist = body
				unused = append(unused, names...)
			}
		}
		for _, name := range unused {
			// Names came from ffmpeg's playlist and passed the strict allowlist.
			_ = os.Remove(filepath.Join(s.dir, name))
		}
		// A killed producer may leave an unpublished, incomplete segment.
		entries, _ := os.ReadDir(s.dir)
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() && strings.HasSuffix(name, ".tmp") && hlsSegmentName.MatchString(strings.TrimSuffix(name, ".tmp")) {
				_ = os.Remove(filepath.Join(s.dir, name))
			}
		}
		m.mu.Unlock()
		log.Printf("hls: resource pressure: sealed session %s at %.1fs; other sessions retained", s.id, until)
		return
	}
}

// sealHLSPlaylist finalizes a prefix without deleting any segment that has been
// handed to a client. Reaching this window's ENDLIST lets the player reopen the
// source at its actual position if the film has more to play.
func sealHLSPlaylist(data []byte, until float64, requested map[string]bool) ([]byte, []string, float64) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	type entry struct {
		line int
		name string
		end  float64
	}
	var entries []entry
	var elapsed, duration float64
	lastKeep := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "#EXTINF:") {
			value, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			duration, _ = strconv.ParseFloat(value, 64)
			if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
				duration = 0
			}
		}
		if !hlsSegmentName.MatchString(line) || line == "init.mp4" {
			continue
		}
		start := elapsed
		elapsed += duration
		duration = 0
		entries = append(entries, entry{line: i, name: line, end: elapsed})
		if start < until || requested[line] {
			lastKeep = len(entries) - 1
		}
	}
	if lastKeep < 0 {
		// Preserve a valid initial window even if its first GOP is unusually long.
		lastKeep = 0
	}
	if len(entries) == 0 {
		return data, nil, 0
	}
	var unused []string
	for _, entry := range entries[lastKeep+1:] {
		unused = append(unused, entry.name)
	}
	body := strings.Join(lines[:entries[lastKeep].line+1], "\n") + "\n#EXT-X-ENDLIST\n"
	return []byte(body), unused, entries[lastKeep].end
}

func resourceError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResources) {
		w.Header().Set("Retry-After", "10")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.Error(w, "hls start failed: "+err.Error(), http.StatusInternalServerError)
}
