package streamapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	defaultHLSForwardBytes = int64(1 << 30)
	androidHLSForwardBytes = int64(60_000_000)
	hlsOutputPath          = "/api/stream/hls-output/"
	maxHLSPlaylistBytes    = 8 << 20
)

// Android uses the ordinary player buffer instead of the desktop 1 GiB window.
// The initial manifest fixes this target for the output's whole lifetime; later
// segment requests and playback heartbeats retain the same session and window.
func (m *hlsManager) forwardBytesForRequest(r *http.Request) int64 {
	limit := m.forwardBytes
	if limit <= 0 {
		limit = defaultHLSForwardBytes
	}
	ua := strings.ToLower(r.UserAgent())
	if (strings.Contains(ua, "android") || strings.Contains(ua, "vvtv/")) && limit > androidHLSForwardBytes {
		limit = androidHLSForwardBytes
	}
	return limit
}

var errHLSSegmentTooLarge = errors.New("HLS fragment exceeds the maximum fragment size")

type hlsBufferedFile struct {
	bytes   int64
	start   float64 // known alongside end after a completed media playlist.
	end     float64 // zero until a complete playlist establishes its media time.
	writing bool
}

// Only media ahead of the playhead counts toward the forward limit. Consumed
// files remain available for the separate back cache until pruneConsumed runs.
// This lock is never held while waiting for HTTP, FFmpeg, or the manager lock.
type hlsOutputWindow struct {
	mu            sync.Mutex
	limit         int64 // forward target; one unpublished GOP may exceed it.
	fragmentLimit int64 // hard bound for unpublished output, including init/VTT.
	playhead      float64
	files         map[string]hlsBufferedFile
	wakeup        chan struct{}
	done          chan struct{}
	closed        bool
	waiting       int
	uploads       sync.WaitGroup
}

func newHLSOutputWindow(limit int64) *hlsOutputWindow {
	return newHLSOutputWindowWithFragmentLimit(limit, limit)
}

func newHLSOutputWindowWithFragmentLimit(limit, fragmentLimit int64) *hlsOutputWindow {
	if limit <= 0 {
		limit = defaultHLSForwardBytes
	}
	if fragmentLimit <= 0 {
		fragmentLimit = defaultHLSForwardBytes
	}
	if fragmentLimit < limit {
		fragmentLimit = limit
	}
	return &hlsOutputWindow{limit: limit, fragmentLimit: fragmentLimit, files: make(map[string]hlsBufferedFile), wakeup: make(chan struct{}), done: make(chan struct{})}
}

func newHLSOutputToken() (string, error) {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func (b *hlsOutputWindow) notifyLocked() {
	close(b.wakeup)
	b.wakeup = make(chan struct{})
}

func (b *hlsOutputWindow) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.done)
	}
}

func (b *hlsOutputWindow) beginUpload() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.uploads.Add(1)
	return true
}

func (b *hlsOutputWindow) advance(position float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.playhead = position
	b.notifyLocked()
}

func (b *hlsOutputWindow) forwardBytesLocked() int64 {
	var total int64
	for name, f := range b.files {
		if f.end == 0 || f.end > b.playhead {
			// A soft Android window counts media ahead of the current GOP,
			// including when paused or exactly at its first frame. Keep its file
			// for playback and seeking. Desktop hard windows are unchanged.
			if b.fragmentLimit > b.limit && strings.HasPrefix(name, "seg_") && f.start <= b.playhead && b.playhead < f.end {
				continue
			}
			total += f.bytes
		}
	}
	return total
}

func (b *hlsOutputWindow) begin(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	if _, exists := b.files[name]; exists {
		return false
	}
	b.files[name] = hlsBufferedFile{writing: true}
	return true
}

// Reserve each chunk before it reaches disk, including unpublished .tmp bytes.
// A soft forward target may hold one larger GOP, which must finish before it
// becomes playable. Preserve the hard bound and wait for other forward media
// to drain instead of rejecting a valid source-quality GOP at the soft target.
func (b *hlsOutputWindow) reserve(ctx context.Context, name string, size int64) error {
	for {
		b.mu.Lock()
		file, exists := b.files[name]
		if b.closed || !exists {
			b.mu.Unlock()
			return context.Canceled
		}
		minimum := size
		for heldName, held := range b.files {
			// Init and untimed subtitle output cannot be freed by watching a
			// completed segment. A previous completed video GOP can become
			// playable once its playlist arrives; it must not enlarge this GOP.
			if held.end == 0 && (heldName == name || held.writing || !strings.HasPrefix(heldName, "seg_")) {
				minimum += held.bytes
			}
		}
		if minimum > b.fragmentLimit {
			b.mu.Unlock()
			return errHLSSegmentTooLarge
		}
		if b.forwardBytesLocked()+size <= max(b.limit, minimum) {
			file.bytes += size
			b.files[name] = file
			b.mu.Unlock()
			return nil
		}
		wakeup := b.wakeup
		b.waiting++
		b.mu.Unlock()
		var waitErr error
		select {
		case <-ctx.Done():
			waitErr = ctx.Err()
		case <-b.done:
			waitErr = context.Canceled
		case <-wakeup:
		}
		b.mu.Lock()
		b.waiting--
		b.mu.Unlock()
		if waitErr != nil {
			return waitErr
		}
	}
}

func (b *hlsOutputWindow) finish(name string, published bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if published {
		f := b.files[name]
		f.writing = false
		b.files[name] = f
	} else {
		delete(b.files, name)
	}
	b.notifyLocked()
}

func (b *hlsOutputWindow) updateTimeline(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var elapsed, duration float64
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXTINF:") {
			value, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			var err error
			duration, err = strconv.ParseFloat(value, 64)
			if err != nil || duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
				return // Keep uncertain output counted; never free unknown media time.
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if duration <= 0 || !consumedHLSName.MatchString(line) {
			return
		}
		start := elapsed
		elapsed += duration
		duration = 0
		if f, exists := b.files[line]; exists && !f.writing {
			f.start = start
			f.end = elapsed
			b.files[line] = f
		}
	}
	b.notifyLocked()
}

func (b *hlsOutputWindow) forget(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if f, exists := b.files[name]; exists && !f.writing && f.end > 0 && f.end <= b.playhead {
		delete(b.files, name)
	}
}

func hlsOutputName(name string) bool {
	return hlsSegmentName.MatchString(name) || name == "playlist.m3u8" || name == "playlist_vtt.m3u8" || name == "master.m3u8"
}

func loopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

// FFmpeg uploads on loopback using an unguessable token for this generation.
// Public clients cannot upload files or address a successor's private directory.
func (m *hlsManager) serveOutput(w http.ResponseWriter, r *http.Request) {
	name, token := r.PathValue("name"), r.PathValue("token")
	if !loopbackRequest(r) || !hlsOutputName(name) {
		http.NotFound(w, r)
		return
	}
	m.mu.Lock()
	s := m.outputs[token]
	m.mu.Unlock()
	if s == nil || s.output == nil {
		http.NotFound(w, r)
		return
	}
	if !s.output.beginUpload() {
		http.Error(w, "output is stopped", http.StatusServiceUnavailable)
		return
	}
	defer s.output.uploads.Done()
	if strings.HasSuffix(name, ".m3u8") {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHLSPlaylistBytes))
		if err != nil {
			http.Error(w, "playlist upload failed", http.StatusBadRequest)
			return
		}
		// FFmpeg normally writes basenames. Strip its own upload base if an
		// absolute URI is emitted; private URLs must never reach the browser.
		base := m.selfBase + hlsOutputPath + token + "/"
		data = []byte(strings.ReplaceAll(string(data), base, ""))
		err = s.output.publishPlaylist(s.dir, name, data)
		if err != nil {
			http.Error(w, "playlist publish failed", http.StatusServiceUnavailable)
			return
		}
		if name != "master.m3u8" {
			s.output.updateTimeline(data)
		}
		w.WriteHeader(http.StatusCreated)
		return
	}
	if !s.output.begin(name) {
		http.Error(w, "output is stopped or already exists", http.StatusConflict)
		return
	}
	published := false
	defer func() { s.output.finish(name, published) }()
	path := filepath.Join(s.dir, name+".tmp")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		http.Error(w, "segment upload failed", http.StatusInternalServerError)
		return
	}
	defer func() {
		_ = f.Close()
		if !published {
			_ = os.Remove(path)
		}
	}()
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := r.Body.Read(buffer)
		if n > 0 {
			if err = s.output.reserve(r.Context(), name, int64(n)); err != nil {
				status := http.StatusServiceUnavailable
				if errors.Is(err, errHLSSegmentTooLarge) {
					status = http.StatusRequestEntityTooLarge
				}
				http.Error(w, err.Error(), status)
				return
			}
			if _, err = f.Write(buffer[:n]); err != nil {
				http.Error(w, "segment write failed", http.StatusInsufficientStorage)
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			http.Error(w, "segment upload interrupted", http.StatusBadRequest)
			return
		}
	}
	if err = f.Close(); err == nil {
		err = s.output.publishSegment(path, filepath.Join(s.dir, name))
	}
	if err != nil {
		http.Error(w, "segment publish failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	published = true
	w.WriteHeader(http.StatusCreated)
}

// Serialize the final rename with cancellation. Once stop returns, no delayed
// upload may recreate the old session's deleted directory or publish a tail.
func (b *hlsOutputWindow) publishSegment(temp, target string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return context.Canceled
	}
	return os.Rename(temp, target)
}

func (b *hlsOutputWindow) publishPlaylist(dir, name string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return context.Canceled
	}
	path := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(path, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}
