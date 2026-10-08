package torrents

import "sync"

// PinCached holds an already-open source while a browser plays buffered HLS.
// FFmpeg may finish reading well before playback ends; no new magnet, network
// download or file priority is introduced by this lease.
func (m *Manager) PinCached(hash string) (func(), error) {
	hash, err := normalizedCacheHash(hash)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	t := m.open[hash]
	if m.closed || t == nil {
		m.mu.Unlock()
		return nil, ErrCacheNotFound
	}
	m.readers[hash]++
	if timer := m.dropTimers[hash]; timer != nil {
		timer.Stop()
		delete(m.dropTimers, hash)
	}
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			// After Close, a delayed browser release must not create
			// a timer or decrement an unrelated replacement torrent.
			if m.closed || m.open[hash] != t {
				return
			}
			m.readers[hash]--
			if m.readers[hash] <= 0 {
				delete(m.readers, hash)
				m.scheduleDropLocked(hash)
			}
		})
	}, nil
}
