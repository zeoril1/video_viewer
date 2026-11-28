package proxycheck

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const Interval = 5 * time.Minute
const healthyLifetime = 10 * time.Minute
const refreshTimeout = 2 * time.Minute
const routeAttemptTimeout = 3 * time.Second

var ErrUnavailable = errors.New("no recently verified public proxy is available")

type Item struct {
	Address   string    `json:"address"`
	LatencyMS int64     `json:"latency_ms"`
	CheckedAt time.Time `json:"checked_at"`
	Source    string    `json:"source"`
}

type Status struct {
	Config          Config     `json:"config"`
	IntervalSeconds int        `json:"interval_seconds"`
	Running         bool       `json:"running"`
	LastStartedAt   *time.Time `json:"last_started_at,omitempty"`
	LastFinishedAt  *time.Time `json:"last_finished_at,omitempty"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	LastError       string     `json:"last_error"`
	Candidates      int        `json:"candidates"`
	Available       int        `json:"available"`
	Items           []Item     `json:"items"`
}

type Manager struct {
	path                     string
	update                   sync.Mutex
	mu                       sync.Mutex
	status                   Status
	items                    map[string]Item
	transports               map[string]*http.Transport
	failed                   map[string]time.Time
	cursors                  map[string]uint64
	closed, started, pending bool
	generation               uint64
	index                    uint64
	nextTick                 time.Time
	ctx                      context.Context
	cancel                   context.CancelFunc
	runCancel                context.CancelFunc
	wg                       sync.WaitGroup
	// Internal seams keep tests deterministic without mass network requests.
	now          func() time.Time
	ticker       func(time.Duration) (<-chan time.Time, func())
	fetch        func(context.Context, string) ([]string, error)
	check        func(context.Context, string) (int64, error)
	dial         dialFunc
	tlsConfig    *tls.Config
	targets      []probeTarget
	probeTimeout time.Duration
}

func NewManager(path string) (*Manager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{path: path, ctx: ctx, cancel: cancel, items: map[string]Item{}, transports: map[string]*http.Transport{}, failed: map[string]time.Time{}, cursors: map[string]uint64{}, now: time.Now, dial: dialPublic, targets: append([]probeTarget(nil), probeTargets...)}
	m.ticker = func(d time.Duration) (<-chan time.Time, func()) { t := time.NewTicker(d); return t.C, t.Stop }
	m.fetch = m.fetchSource
	m.probeTimeout = 2500 * time.Millisecond
	m.check = m.checkProxy
	m.status = Status{Config: DefaultConfig(), IntervalSeconds: 300, Items: []Item{}}
	if strings.TrimSpace(path) == "" {
		m.status.Config = Config{Sources: []string{}, Static: []string{}}
		m.status.LastError = "Не задан путь к настройкам резервных прокси"
		return m, errors.New(m.status.LastError)
	}
	c, err := readConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		m.status.Config = Config{Sources: []string{}, Static: []string{}}
		m.status.LastError = "Не удалось прочитать настройки резервных прокси; сохраните их заново в админке"
		return m, errors.New(m.status.LastError)
	}
	m.status.Config = c
	return m, nil
}

func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.closed || m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.nextTick = m.now().UTC().Add(Interval)
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		ticks, stop := m.ticker(Interval)
		defer stop()
		m.Refresh()
		for {
			select {
			case <-ctx.Done():
				m.stop()
				return
			case <-m.ctx.Done():
				return
			case <-ticks:
				m.mu.Lock()
				m.nextTick = m.now().UTC().Add(Interval)
				if m.status.Config.Enabled && !m.closed {
					next := m.nextTick
					m.status.NextRunAt = &next
				}
				m.mu.Unlock()
				m.Refresh()
			}
		}
	}()
}

func (m *Manager) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	m.pending = false
	m.cancel()
	if m.runCancel != nil {
		m.runCancel()
	}
	m.status.NextRunAt = nil
	for key, t := range m.transports {
		t.CloseIdleConnections()
		delete(m.transports, key)
	}
	m.items = map[string]Item{}
	m.failed = map[string]time.Time{}
	m.cursors = map[string]uint64{}
}

func (m *Manager) Close() { m.stop(); m.wg.Wait() }

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.status
	s.Config = copyConfig(s.Config)
	s.Items = []Item{}
	if !m.closed && s.Config.Enabled {
		now := m.now()
		for _, item := range m.items {
			if !now.Before(item.CheckedAt) && now.Sub(item.CheckedAt) < healthyLifetime {
				s.Items = append(s.Items, item)
			}
		}
	}
	sort.Slice(s.Items, func(i, j int) bool {
		if s.Items[i].LatencyMS == s.Items[j].LatencyMS {
			return s.Items[i].Address < s.Items[j].Address
		}
		return s.Items[i].LatencyMS < s.Items[j].LatencyMS
	})
	s.Available = len(s.Items)
	if s.LastStartedAt != nil {
		t := *s.LastStartedAt
		s.LastStartedAt = &t
	}
	if s.LastFinishedAt != nil {
		t := *s.LastFinishedAt
		s.LastFinishedAt = &t
	}
	if s.NextRunAt != nil {
		t := *s.NextRunAt
		s.NextRunAt = &t
	}
	return s
}

func (m *Manager) Replace(c Config) (Status, error) {
	c, err := validateConfig(c)
	if err != nil {
		return m.Status(), err
	}
	m.update.Lock()
	defer m.update.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return m.Status(), net.ErrClosed
	}
	m.mu.Unlock()
	if err := saveConfig(m.path, c); err != nil {
		return m.Status(), errors.New("Не удалось сохранить настройки резервных прокси")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return m.Status(), net.ErrClosed
	}
	m.generation++
	m.status.Config = c
	m.status.LastError = ""
	m.status.Candidates = 0
	m.status.NextRunAt = nil
	m.items = map[string]Item{}
	m.failed = map[string]time.Time{}
	m.cursors = map[string]uint64{}
	for key, t := range m.transports {
		t.CloseIdleConnections()
		delete(m.transports, key)
	}
	m.pending = c.Enabled
	if m.runCancel != nil {
		m.runCancel()
	}
	running := m.status.Running
	m.mu.Unlock()
	if !running {
		m.Refresh()
	}
	return m.Status(), nil
}

func (m *Manager) Refresh() bool {
	m.mu.Lock()
	if m.closed || !m.status.Config.Enabled || m.status.Running {
		m.mu.Unlock()
		return false
	}
	cfg := copyConfig(m.status.Config)
	generation := m.generation
	cursors := make(map[string]uint64, len(m.cursors))
	for source, cursor := range m.cursors {
		cursors[source] = cursor
	}
	old := make([]Item, 0, len(m.items))
	for _, item := range m.items {
		old = append(old, item)
	}
	sort.Slice(old, func(i, j int) bool { return old[i].CheckedAt.Before(old[j].CheckedAt) })
	ctx, cancel := context.WithTimeout(m.ctx, refreshTimeout)
	m.runCancel = cancel
	started := m.now().UTC()
	next := started.Add(Interval)
	if m.started && m.nextTick.After(started) {
		next = m.nextTick
	}
	m.status.Running = true
	m.status.LastStartedAt = &started
	m.status.NextRunAt = &next
	m.pending = false
	m.wg.Add(1)
	m.mu.Unlock()
	go func() { defer m.wg.Done(); defer cancel(); m.refresh(ctx, cfg, generation, old, cursors) }()
	return true
}

func (m *Manager) refresh(ctx context.Context, cfg Config, generation uint64, old []Item, cursors map[string]uint64) {
	// Always recheck old entries, even when every source list is unavailable.
	candidates := make([]Item, 0, maxCandidates)
	seen := map[string]bool{}
	add := func(address, source string) {
		if normalized, ok := normalizeProxy(address); ok && !seen[normalized] && len(candidates) < maxCandidates {
			seen[normalized] = true
			candidates = append(candidates, Item{Address: normalized, Source: source})
		}
	}
	for _, item := range old {
		add(item.Address, item.Source)
	}
	for _, address := range cfg.Static {
		add(address, "static")
	}
	lists := make([][]string, len(cfg.Sources))
	errs := make([]error, len(cfg.Sources))
	var fetches sync.WaitGroup
	sem := make(chan struct{}, 3)
	for i, source := range cfg.Sources {
		fetches.Add(1)
		go func(i int, source string) {
			defer fetches.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			lists[i], errs[i] = m.fetch(ctx, source)
		}(i, source)
	}
	fetches.Wait()
	// Interleave sources so one large provider cannot consume the whole pool.
	for n := 0; n < maxParsedCandidates && len(candidates) < maxCandidates; n++ {
		added := false
		for i, list := range lists {
			if n < len(list) && len(candidates) < maxCandidates {
				source := cfg.Sources[i]
				position := int(cursors[source] % uint64(len(list)))
				cursors[source]++
				add(list[position], source)
				added = true
			}
		}
		if !added {
			break
		}
	}
	healthy := map[string]Item{}
	for _, item := range old {
		if age := m.now().Sub(item.CheckedAt); age >= 0 && age < healthyLifetime {
			healthy[item.Address] = item
		}
	}
	var checks sync.WaitGroup
	var resultMu sync.Mutex
	jobs := make(chan Item)
	for range 16 {
		checks.Add(1)
		go func() {
			defer checks.Done()
			for item := range jobs {
				if ctx.Err() != nil {
					continue
				}
				latency, err := m.check(ctx, item.Address)
				resultMu.Lock()
				if err != nil {
					delete(healthy, item.Address)
				} else {
					item.LatencyMS = latency
					item.CheckedAt = m.now().UTC()
					healthy[item.Address] = item
				}
				resultMu.Unlock()
			}
		}()
	}
	for _, item := range candidates {
		select {
		case jobs <- item:
		case <-ctx.Done():
		}
	}
	close(jobs)
	checks.Wait()
	var failures []string
	failedSources := 0
	for _, err := range errs {
		if err != nil {
			failedSources++
		}
	}
	if failedSources > 0 {
		failures = append(failures, fmt.Sprintf("Не удалось обновить источники: %d из %d", failedSources, len(errs)))
	}
	if len(healthy) == 0 {
		failures = append(failures, "Ни один прокси не прошёл проверку HTTPS")
	}
	if ctx.Err() != nil {
		failures = append(failures, "Проверка прервана или превышено время ожидания")
	}
	m.mu.Lock()
	if generation == m.generation && !m.closed {
		// A proxy can fail during application use while a refresh is running;
		// a successful subsequent probe is the only way to reintroduce it.
		for address, item := range healthy {
			if failedAt, failed := m.failed[address]; failed && !item.CheckedAt.After(failedAt) {
				delete(healthy, address)
			}
		}
		m.failed = map[string]time.Time{}
		m.cursors = cursors
		m.items = healthy
		if len(healthy) == 0 && !strings.Contains(strings.Join(failures, "; "), "Ни один прокси") {
			failures = append(failures, "Ни один прокси не прошёл проверку HTTPS")
		}
		m.status.Candidates = len(candidates)
		m.status.LastError = strings.Join(failures, "; ")
		for address, t := range m.transports {
			if _, ok := healthy[address]; !ok {
				t.CloseIdleConnections()
				delete(m.transports, address)
			}
		}
	}
	finished := m.now().UTC()
	m.status.LastFinishedAt = &finished
	m.status.Running = false
	m.runCancel = nil
	pending := m.pending && !m.closed
	m.mu.Unlock()
	if pending {
		m.Refresh()
	}
}

func (m *Manager) healthyItems() []Item { return m.Status().Items }

func (m *Manager) markFailed(address string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, address)
	if len(m.items) == 0 {
		m.status.LastError = "Проверенные резервные прокси недоступны"
	}
	if m.status.Running {
		m.failed[address] = m.now()
	}
	if t := m.transports[address]; t != nil {
		t.CloseIdleConnections()
		delete(m.transports, address)
	}
}

func (m *Manager) transport(address string) (*http.Transport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[address]
	age := m.now().Sub(item.CheckedAt)
	if m.closed || !m.status.Config.Enabled || !ok || age < 0 || age >= healthyLifetime {
		return nil, ErrUnavailable
	}
	if t := m.transports[address]; t != nil {
		return t, nil
	}
	t := m.proxyTransport(address)
	m.transports[address] = t
	return t, nil
}

func (m *Manager) orderedItems() []Item {
	items := m.healthyItems()
	if len(items) == 0 {
		return items
	}
	m.mu.Lock()
	start := int(m.index % uint64(len(items)))
	m.index++
	m.mu.Unlock()
	return append(items[start:], items[:start]...)
}

type responseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b responseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.cancel()
	}
	return n, err
}
func (b responseBody) Close() error { err := b.ReadCloser.Close(); b.cancel(); return err }

func (m *Manager) RoundTrip(r *http.Request) (response *http.Response, resultErr error) {
	defer func() {
		if resultErr != nil && r != nil && r.Body != nil {
			_ = r.Body.Close()
		}
	}()
	if !requestDestination(r) {
		return nil, errors.New("public proxy destination is not allowed")
	}
	items := m.orderedItems()
	if len(items) == 0 {
		return nil, ErrUnavailable
	}
	replayable := (r.Method == http.MethodGet || r.Method == http.MethodHead) && (r.Body == nil || r.Body == http.NoBody || r.GetBody != nil)
	for i, item := range items[:min(3, len(items))] {
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		if i > 0 && !replayable {
			break
		}
		request := r.Clone(r.Context())
		if i > 0 && r.Body != nil && r.Body != http.NoBody {
			body, err := r.GetBody()
			if err != nil {
				return nil, err
			}
			request.Body = body
		}
		t, err := m.transport(item.Address)
		if err != nil {
			if request.Body != nil && i > 0 {
				_ = request.Body.Close()
			}
			continue
		}
		attempt, cancel := context.WithCancel(request.Context())
		expired := make(chan struct{})
		timer := time.AfterFunc(routeAttemptTimeout, func() { cancel(); close(expired) })
		resp, err := t.RoundTrip(request.WithContext(attempt))
		if !timer.Stop() {
			<-expired
		}
		if err == nil && attempt.Err() == nil && resp.StatusCode != http.StatusProxyAuthRequired {
			resp.Body = responseBody{ReadCloser: resp.Body, cancel: cancel}
			return resp, nil
		}
		cancel()
		if resp != nil {
			_ = resp.Body.Close()
		}
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		m.markFailed(item.Address)
	}
	return nil, ErrUnavailable
}

func (m *Manager) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if (network != "tcp" && network != "tcp4" && network != "tcp6") || !validDestination(address) {
		return nil, errors.New("public proxy tunnel destination is not allowed")
	}
	items := m.orderedItems()
	if len(items) == 0 {
		return nil, ErrUnavailable
	}
	for _, item := range items[:min(3, len(items))] {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Recheck freshness immediately before dialing an item from the snapshot.
		if _, err := m.transport(item.Address); err != nil {
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, routeAttemptTimeout)
		conn, err := m.openProxyTunnel(attempt, item.Address, address)
		cancel()
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		m.markFailed(item.Address)
	}
	return nil, ErrUnavailable
}
