package streamapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/zeoril1/video_viewer/internal/httpx"
	"github.com/zeoril1/video_viewer/internal/remoteauth"
	"github.com/zeoril1/video_viewer/internal/torrents"
)

const viewerTTL = 35 * time.Second

var viewerSessionPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,80}$`)

type viewer struct {
	Session        string    `json:"session"`
	Username       string    `json:"username"`
	FilmID         string    `json:"film_id"`
	Hash           string    `json:"hash"`
	File           int       `json:"file"`
	Season         int       `json:"season"`
	Episode        int       `json:"episode"`
	WatchedSeconds float64   `json:"watched_seconds"`
	Position       float64   `json:"position"`
	Duration       float64   `json:"duration"`
	Playing        bool      `json:"playing"`
	StartedAt      time.Time `json:"started_at"`
	LastSeen       time.Time `json:"last_seen"`
	owner          string
	release        func()
}

type viewingStore struct {
	mu     sync.Mutex
	items  map[string]*viewer
	closed map[string]time.Time
	now    func() time.Time
}

func newViewingStore() *viewingStore {
	return &viewingStore{items: make(map[string]*viewer), closed: make(map[string]time.Time), now: time.Now}
}

func torrentHash(magnet string) (string, error) {
	m, err := metainfo.ParseMagnetUri(magnet)
	if err != nil {
		return "", err
	}
	return strings.ToLower(m.InfoHash.HexString()), nil
}

func viewerOwner(r *http.Request) string {
	address := r.RemoteAddr
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	identity := "guest:" + address
	if ip := r.Header.Get("X-Video-Viewer-Client-IP"); ip != "" {
		identity = "guest:" + ip
	}
	if cookie, err := r.Cookie("video_viewer_session"); err == nil && cookie.Value != "" {
		identity = cookie.Value
	}
	h := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(h[:])
}

func (s *viewingStore) pruneLocked(now time.Time) {
	for key, item := range s.items {
		if now.Sub(item.LastSeen) > viewerTTL {
			if item.release != nil {
				item.release()
			}
			delete(s.items, key)
		}
	}
	for key, until := range s.closed {
		if !now.Before(until) {
			delete(s.closed, key)
		}
	}
}

// Keepalive DELETE may overtake a pending heartbeat while the browser closes.
// Remember finished sessions briefly so their late requests cannot recreate them.
func (s *viewingStore) close(owner, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	key := owner + ":" + session
	if item := s.items[key]; item != nil && item.release != nil {
		item.release()
	}
	delete(s.items, key)
	if len(s.closed) >= 4096 {
		var oldest string
		var earliest time.Time
		for k, until := range s.closed {
			if oldest == "" || until.Before(earliest) {
				oldest, earliest = k, until
			}
		}
		delete(s.closed, oldest)
	}
	s.closed[key] = now.Add(time.Minute)
}

// The server measures elapsed playback between timely heartbeats. Position is
// displayed separately; seeking is never counted as minutes watched.
func (s *viewingStore) update(v viewer, admit ...func(*viewer, *viewer) bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	key := v.owner + ":" + v.Session
	if _, closed := s.closed[key]; closed {
		return true
	}
	old := s.items[key]
	if old == nil {
		count := 0
		for _, item := range s.items {
			if item.owner == v.owner {
				count++
			}
		}
		if len(s.items) >= 2048 || count >= 16 {
			return false
		}
	}
	if len(admit) > 0 && !admit[0](&v, old) {
		return true
	}
	if old != nil && old.release != nil {
		if old.Hash != v.Hash {
			old.release()
		} else if v.release == nil {
			v.release = old.release
		}
	}
	v.StartedAt, v.LastSeen = now, now
	if old != nil && old.Hash == v.Hash && old.File == v.File && old.FilmID == v.FilmID {
		v.StartedAt, v.WatchedSeconds = old.StartedAt, old.WatchedSeconds
		elapsed := now.Sub(old.LastSeen).Seconds()
		// Event heartbeats close intervals on pause/waiting; the previous state
		// describes the interval just elapsed. Lost heartbeats cannot add minutes.
		if old.Playing && elapsed > 0 {
			v.WatchedSeconds += math.Min(elapsed, 15)
		}
	}
	s.items[key] = &v
	return true
}

func (s *viewingStore) cleanup(done <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			s.mu.Lock()
			for key, item := range s.items {
				if item.release != nil {
					item.release()
				}
				delete(s.items, key)
			}
			s.mu.Unlock()
			return
		case <-ticker.C:
			s.mu.Lock()
			s.pruneLocked(s.now())
			s.mu.Unlock()
		}
	}
}

func (s *viewingStore) snapshot() []viewer {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	items := make([]viewer, 0, len(s.items))
	for _, item := range s.items {
		v := *item
		if elapsed := now.Sub(v.LastSeen).Seconds(); v.Playing && elapsed > 0 {
			v.WatchedSeconds += math.Min(elapsed, 15)
		}
		items = append(items, v)
	}
	return items
}

func (s *viewingStore) handle(mgr *torrents.Manager, auth *remoteauth.Client, managers ...*hlsManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !httpx.CheckOrigin(w, r) {
			return
		}
		var body struct {
			Session     string   `json:"session"`
			FilmID      string   `json:"film_id"`
			Magnet      string   `json:"magnet"`
			File        int      `json:"file"`
			Season      int      `json:"season"`
			Episode     int      `json:"episode"`
			Position    float64  `json:"position"`
			Duration    float64  `json:"duration"`
			Playing     bool     `json:"playing"`
			HLSSession  string   `json:"hls_session"`
			StreamStart *float64 `json:"stream_start"`
			Track       *int     `json:"track"`
			Subs        *int     `json:"subs"`
			Quality     string   `json:"quality"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		if dec.Decode(&body) != nil || dec.Decode(new(any)) != io.EOF || !viewerSessionPattern.MatchString(body.Session) {
			http.Error(w, "invalid viewing session", http.StatusBadRequest)
			return
		}
		owner := viewerOwner(r)
		if r.Method == http.MethodDelete {
			s.close(owner, body.Session)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if len(body.FilmID) > 128 || strings.TrimSpace(body.FilmID) == "" || body.File < -1 || body.Season < 0 || body.Episode < 0 || body.Position < 0 || body.Duration < 0 || body.Duration > 7*24*3600 || math.IsInf(body.Position, 0) || math.IsInf(body.Duration, 0) {
			http.Error(w, "invalid viewing state", http.StatusBadRequest)
			return
		}
		hash, err := torrentHash(body.Magnet)
		if err != nil {
			http.Error(w, "invalid magnet", http.StatusBadRequest)
			return
		}
		if mgr == nil {
			http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
			return
		}
		index, err := mgr.ResolveCachedFile(hash, body.File)
		if err != nil {
			http.Error(w, "file is not open", http.StatusConflict)
			return
		}
		u, status := auth.Current(r)
		username := "Гость"
		if status == http.StatusOK {
			username = u.Username
		} else if status != http.StatusUnauthorized {
			http.Error(w, "auth unavailable", http.StatusServiceUnavailable)
			return
		}
		v := viewer{owner: owner, Session: body.Session, Username: username, FilmID: body.FilmID, Hash: hash, File: index,
			Season: body.Season, Episode: body.Episode, Position: body.Position, Duration: body.Duration, Playing: body.Playing}
		admit := func(v *viewer, old *viewer) bool {
			if body.HLSSession == "" || len(managers) == 0 {
				return true
			}
			if !viewerSessionPattern.MatchString(body.HLSSession) || body.StreamStart == nil || body.Track == nil || body.Subs == nil {
				return false
			}
			hls := managers[0]
			if !hls.touchPlayback(body.FilmID, body.HLSSession, body.Magnet, body.File, *body.StreamStart, *body.Track, *body.Subs, body.Quality, body.Position) {
				return false
			}
			if old != nil && old.Hash == v.Hash && old.release != nil {
				v.release = old.release
				return true
			}
			release, err := mgr.PinCached(v.Hash)
			if err != nil {
				return false
			}
			v.release = release
			return true
		}
		if !s.update(v, admit) {
			http.Error(w, "too many viewing sessions", http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
