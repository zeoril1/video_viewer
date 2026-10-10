package torrents

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/zeoril1/video_viewer/internal/disklimit"
)

var (
	ErrInvalidCacheKey  = errors.New("invalid cache identifier")
	ErrCacheNotFound    = errors.New("cached file not found")
	ErrCacheBusy        = errors.New("cache is in use by playback or preparation")
	ErrCacheUnsupported = errors.New("individual files cannot be removed from memory storage")
)

type StorageDisk struct {
	disklimit.DiskStats
	Error string `json:"error,omitempty"`
}

type StorageSnapshot struct {
	Mode         string          `json:"mode"`
	UsedBytes    int64           `json:"used_bytes"`
	LogicalBytes int64           `json:"logical_bytes"`
	WrittenBytes int64           `json:"written_bytes"`
	Disk         StorageDisk     `json:"disk"`
	Torrents     []CachedTorrent `json:"torrents"`
}

type CachedTorrent struct {
	Hash           string       `json:"hash"`
	Name           string       `json:"name"`
	Total          int64        `json:"total"`
	Downloaded     int64        `json:"downloaded"`
	StoredBytes    int64        `json:"stored_bytes"`
	LogicalBytes   int64        `json:"logical_bytes"`
	WrittenBytes   int64        `json:"written_bytes"`
	DownloadRate   float64      `json:"download_rate"`
	ActiveReaders  int          `json:"active_readers"`
	Busy           bool         `json:"busy"`
	MetadataReady  bool         `json:"metadata_ready"`
	PendingRemoval bool         `json:"pending_removal,omitempty"`
	Kept           bool         `json:"kept"`
	Files          []CachedFile `json:"files"`
}

type CachedFile struct {
	Index        int     `json:"index"`
	Path         string  `json:"path"`
	Size         int64   `json:"size"`
	Downloaded   int64   `json:"downloaded"`
	StoredBytes  int64   `json:"stored_bytes"`
	LogicalBytes int64   `json:"logical_bytes"`
	WrittenBytes int64   `json:"written_bytes"`
	Percent      float64 `json:"percent"`
	DownloadRate float64 `json:"download_rate"`
	Downloading  bool    `json:"downloading"`
	Available    bool    `json:"available"`
}

type downloadSample struct {
	at        time.Time
	bytes     int64
	files     []int64
	rate      float64
	fileRates []float64
}

func nextDownloadSample(previous downloadSample, now time.Time, bytes int64, files []int64) downloadSample {
	if !previous.at.IsZero() && now.Sub(previous.at) < time.Second {
		return previous
	}
	next := downloadSample{at: now, bytes: bytes, files: files, fileRates: make([]float64, len(files))}
	if previous.at.IsZero() {
		return next
	}
	seconds := now.Sub(previous.at).Seconds()
	next.rate = float64(max(int64(0), bytes-previous.bytes)) / seconds
	for i, count := range files {
		if i < len(previous.files) {
			next.fileRates[i] = float64(max(int64(0), count-previous.files[i])) / seconds
		}
	}
	return next
}

// StorageStatus samples useful received bytes and per-file written ranges.
// Torrent/client calls finish before taking spool locks to keep lock order
// compatible with anacrolix's storage callbacks during Drop.
func (m *Manager) StorageStatus() StorageSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := StorageSnapshot{Mode: "memory", Torrents: make([]CachedTorrent, 0, len(m.open))}
	if m.spool != nil {
		out.Mode = "disk"
		stats, err := disklimit.Stats(m.spool.dir)
		out.Disk.DiskStats = stats
		if err != nil {
			out.Disk.Error = err.Error()
		}
	}
	if m.rates == nil {
		m.rates = make(map[string]downloadSample)
	}
	for hash, t := range m.open {
		row := CachedTorrent{Hash: hash, Name: t.Name(), ActiveReaders: m.readers[hash],
			Busy: m.readers[hash] > 0 || len(m.fileWants[hash]) > 0,
			Kept: m.keepUntil[hash].After(now), Files: []CachedFile{}}
		stats := t.Stats()
		if info := t.Info(); info != nil {
			row.MetadataReady = true
			row.Total = info.TotalLength()
			row.Downloaded = t.BytesCompleted()
			for i, f := range t.Files() {
				downloaded := min(f.Length(), max(int64(0), f.BytesCompleted()))
				file := CachedFile{Index: i, Path: f.Path(), Size: f.Length(), Downloaded: downloaded,
					Downloading: m.fileWants[hash][i] > 0 && downloaded < f.Length()}
				if file.Size > 0 {
					file.Percent = float64(file.Downloaded) * 100 / float64(file.Size)
				}
				if m.spool == nil {
					file.Available = downloaded > 0
					file.WrittenBytes = downloaded
					file.StoredBytes = downloaded
				}
				row.Files = append(row.Files, file)
			}
		}
		if m.spool != nil {
			for i, usage := range m.spool.fileUsage(hash) {
				if i >= len(row.Files) {
					break
				}
				row.Files[i].StoredBytes = usage.allocated
				row.Files[i].LogicalBytes = usage.logical
				row.Files[i].WrittenBytes = usage.written
				row.Files[i].Available = usage.available
			}
		}
		counts := make([]int64, len(row.Files))
		for i, f := range row.Files {
			counts[i] = f.WrittenBytes
			row.StoredBytes += f.StoredBytes
			row.LogicalBytes += f.LogicalBytes
			row.WrittenBytes += f.WrittenBytes
		}
		sample := nextDownloadSample(m.rates[hash], now, stats.BytesReadUsefulData.Int64(), counts)
		m.rates[hash] = sample
		row.DownloadRate = sample.rate
		for i := range row.Files {
			if i < len(sample.fileRates) {
				row.Files[i].DownloadRate = sample.fileRates[i]
			}
		}
		out.UsedBytes += row.StoredBytes
		out.LogicalBytes += row.LogicalBytes
		out.WrittenBytes += row.WrittenBytes
		out.Torrents = append(out.Torrents, row)
	}
	if m.spool != nil {
		for _, row := range m.spool.pendingRemovalRows() {
			if _, active := m.open[row.Hash]; active {
				continue
			}
			out.UsedBytes += row.StoredBytes
			out.LogicalBytes += row.LogicalBytes
			out.WrittenBytes += row.WrittenBytes
			out.Torrents = append(out.Torrents, row)
		}
	}
	sort.Slice(out.Torrents, func(i, j int) bool { return out.Torrents[i].Hash < out.Torrents[j].Hash })
	return out
}

func normalizedCacheHash(hash string) (string, error) {
	if len(hash) != 40 {
		return "", ErrInvalidCacheKey
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", ErrInvalidCacheKey
	}
	return strings.ToLower(hash), nil
}

// ResolveCachedFile reads only existing metadata; it never opens a magnet or
// starts a download. -1 selects the largest video, matching automatic playback.
func (m *Manager) ResolveCachedFile(hash string, index int) (int, error) {
	hash, err := normalizedCacheHash(hash)
	if err != nil || index < -1 {
		return 0, ErrInvalidCacheKey
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.open[hash]
	if t == nil || t.Info() == nil {
		return 0, ErrCacheNotFound
	}
	files := t.Files()
	if index >= 0 {
		if index >= len(files) {
			return 0, ErrInvalidCacheKey
		}
		return index, nil
	}
	best, fallback := -1, -1
	var largest int64
	var largestFallback int64
	for i, f := range files {
		if fallback < 0 || f.Length() > largestFallback {
			fallback, largestFallback = i, f.Length()
		}
		switch strings.ToLower(filepath.Ext(f.Path())) {
		case ".mp4", ".mkv", ".avi", ".webm", ".m4v", ".mov", ".ts", ".mpg", ".mpeg", ".ogv", ".flv", ".wmv", ".3gp":
			if best < 0 || f.Length() > largest {
				best, largest = i, f.Length()
			}
		}
	}
	if best < 0 {
		best = fallback
	}
	if best < 0 {
		return 0, ErrCacheNotFound
	}
	return best, nil
}

// RemoveCached removes an idle torrent, or one owned spool file. A nil index
// drops the complete torrent. No path supplied by the caller reaches the disk.
func (m *Manager) RemoveCached(hash string, index *int) error {
	hash, err := normalizedCacheHash(hash)
	if err != nil || (index != nil && *index < 0) {
		return ErrInvalidCacheKey
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.open[hash]
	if t == nil {
		if m.spool != nil {
			if st := m.spool.torrent(hash); st != nil && !st.isOpen() {
				if index != nil {
					return st.removePendingFile(*index)
				}
				return st.Close()
			}
		}
		return ErrCacheNotFound
	}
	if m.readers[hash] > 0 || len(m.fileWants[hash]) > 0 {
		return ErrCacheBusy
	}
	if index == nil && (m.spool == nil || t.Info() == nil) {
		m.dropLocked(hash)
		return nil
	}
	if index != nil && (t.Info() == nil || *index >= len(t.Files())) {
		return ErrInvalidCacheKey
	}
	if m.spool == nil {
		return ErrCacheUnsupported
	}
	st := m.spool.torrent(hash)
	if index == nil && st == nil {
		m.dropLocked(hash)
		return nil
	}
	if index != nil && !cacheFileExists(st, *index) {
		return ErrCacheNotFound
	}
	meta := t.Metainfo()
	spec, err := torrent.TorrentSpecFromMetaInfoErr(&meta)
	if err != nil {
		return err
	}
	spec.DisallowDataDownload = true
	// Metainfo() currently returns an empty, non-nil PieceLayers map for v1.
	// anacrolix treats non-nil maps as v2 and rejects every v1 file without a
	// piece root, so normalize that generated metadata before replacing it.
	if !t.Info().HasV2() {
		spec.PieceLayers = nil
	}
	// Drop synchronously stops requests and all storage operations. Retaining
	// once lets the replacement reuse neighboring files and completion maps.
	st.mu.Lock()
	st.retainOnClose = true
	st.mu.Unlock()
	t.Drop()
	delete(m.open, hash)
	var removeErr error
	if index != nil {
		removeErr = st.removeFile(*index)
	} else {
		// Torrent.Drop logs storage errors instead of returning them. Admin
		// deletion must explicitly remove files so a Windows sharing violation
		// cannot report success while leaving invisible data on the disk.
		var errs []error
		for i, file := range st.files {
			if !file.exists() {
				continue
			}
			if err := st.removeFile(i); err != nil {
				errs = append(errs, err)
			}
		}
		removeErr = errors.Join(errs...)
		if removeErr == nil {
			// All descriptors/data have already been removed. Close now only
			// forgets the retained spool and prevents any stale storage reuse.
			if err := st.Close(); err != nil {
				return err
			}
			m.dropLocked(hash) // cancel timers and clear Keep/rate bookkeeping
			return nil
		}
	}
	// AddTorrentSpec drops a newly added torrent when MergeSpec fails. Preserve
	// retained files in that failure path, too; a later Acquire can reopen them.
	st.mu.Lock()
	st.retainOnClose = true
	st.mu.Unlock()
	replacement, _, reopenErr := m.client.AddTorrentSpec(spec)
	st.mu.Lock()
	st.retainOnClose = false
	st.mu.Unlock()
	if reopenErr != nil {
		return errors.Join(removeErr, reopenErr)
	}
	m.open[hash] = replacement
	for _, f := range replacement.Files() {
		f.SetPriority(torrent.PiecePriorityNone)
	}
	delete(m.rates, hash)
	return removeErr
}
