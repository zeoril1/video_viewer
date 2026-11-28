package proxycheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.status.Config = Config{Enabled: true, Sources: []string{}, Static: []string{}}
	m.fetch = func(context.Context, string) ([]string, error) { return nil, errors.New("unexpected list fetch") }
	m.check = func(context.Context, string) (int64, error) { return 12, nil }
	t.Cleanup(m.Close)
	return m
}

func awaitRefresh(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for m.Status().Running {
		if time.Now().After(deadline) {
			t.Fatal("refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestImmediateFiveMinuteRefreshAndTenMinuteExpiry(t *testing.T) {
	m := testManager(t)
	var nanos atomic.Int64
	nanos.Store(time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC).UnixNano())
	m.now = func() time.Time { return time.Unix(0, nanos.Load()).UTC() }
	ticks := make(chan time.Time, 1)
	var stopped atomic.Bool
	m.ticker = func(interval time.Duration) (<-chan time.Time, func()) {
		if interval != 5*time.Minute {
			t.Errorf("timer interval %v", interval)
		}
		return ticks, func() { stopped.Store(true) }
	}
	m.status.Config.Static = []string{"http://8.8.8.8:8080"}
	checks := make(chan struct{}, 3)
	m.check = func(context.Context, string) (int64, error) { checks <- struct{}{}; return 37, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	m.Start(ctx)
	select {
	case <-checks:
	case <-time.After(time.Second):
		t.Fatal("startup did not check immediately")
	}
	awaitRefresh(t, m)
	if s := m.Status(); s.IntervalSeconds != 300 || s.Available != 1 || s.Items[0].LatencyMS != 37 || s.NextRunAt == nil {
		t.Fatalf("startup status %#v", s)
	}
	nanos.Add(int64(Interval))
	ticks <- m.now()
	select {
	case <-checks:
	case <-time.After(time.Second):
		t.Fatal("five-minute tick did not refresh")
	}
	awaitRefresh(t, m)
	if s := m.Status(); s.LastStartedAt == nil || !s.LastStartedAt.Equal(m.now()) || s.NextRunAt == nil || !s.NextRunAt.Equal(m.now().Add(Interval)) {
		t.Fatalf("timer timestamps %#v", s)
	}
	nanos.Add(int64(healthyLifetime))
	if m.Status().Available != 0 {
		t.Fatal("expired proxy still reported healthy")
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/file", nil)
	if _, err := m.RoundTrip(req); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expired proxy used: %v", err)
	}
	cancel()
	m.Close()
	if !stopped.Load() {
		t.Fatal("background timer leaked after Close")
	}
}

func TestRefreshRechecksOldOnListFailureAndDropsFailed(t *testing.T) {
	m := testManager(t)
	now := time.Now()
	m.status.Config.Sources = []string{"https://example.com/list"}
	m.items["http://8.8.8.8:8080"] = Item{Address: "http://8.8.8.8:8080", CheckedAt: now.Add(-5 * time.Minute), Source: "old"}
	m.items["http://1.1.1.1:8080"] = Item{Address: "http://1.1.1.1:8080", CheckedAt: now.Add(-5 * time.Minute), Source: "old"}
	var checked atomic.Int32
	m.check = func(_ context.Context, address string) (int64, error) {
		checked.Add(1)
		if address == "http://1.1.1.1:8080" {
			return 0, errors.New("dead")
		}
		return 40, nil
	}
	if !m.Refresh() {
		t.Fatal("refresh was not started")
	}
	awaitRefresh(t, m)
	s := m.Status()
	if checked.Load() != 2 || s.Candidates != 2 || s.Available != 1 || s.Items[0].Address != "http://8.8.8.8:8080" || s.Items[0].CheckedAt.Before(now) || s.LastError == "" {
		t.Fatalf("list failure lost old healthy/reused dead: %#v checked=%d", s, checked.Load())
	}
	copy := m.Status()
	copy.Config.Sources[0] = "changed"
	copy.Items[0].Address = "changed"
	*copy.LastStartedAt = time.Time{}
	if s := m.Status(); s.Config.Sources[0] == "changed" || s.Items[0].Address == "changed" || s.LastStartedAt.IsZero() {
		t.Fatal("status exposes mutable manager storage")
	}
}

func TestSingleFlightConfigReplacementAndCloseCancellation(t *testing.T) {
	m := testManager(t)
	m.status.Config.Static = []string{"http://8.8.8.8:8080"}
	entered := make(chan struct{})
	exited := make(chan struct{})
	m.check = func(ctx context.Context, _ string) (int64, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return 0, ctx.Err()
	}
	if !m.Refresh() || m.Refresh() {
		t.Fatal("refresh is not single-flight")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("check never entered")
	}
	if _, err := m.Replace(Config{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	m.Close()
	select {
	case <-exited:
	default:
		t.Fatal("Close did not wait for cancelled checker")
	}
	if s := m.Status(); s.Available != 0 || s.Running || s.Config.Enabled {
		t.Fatalf("old generation overwrote disabled config: %#v", s)
	}
	if m.Refresh() {
		t.Fatal("closed manager restarted")
	}
}

func TestBoundedConcurrentChecksInterleaveLists(t *testing.T) {
	m := testManager(t)
	m.status.Config.Sources = []string{"https://one.example/list", "https://two.example/list"}
	m.fetch = func(_ context.Context, source string) ([]string, error) {
		var list []string
		offset := 0
		if source == "https://two.example/list" {
			offset = 1000
		}
		for i := 0; i < 500; i++ {
			list = append(list, fmt.Sprintf("8.8.8.8:%d", 1000+offset+i))
		}
		return list, nil
	}
	var active, peak, count atomic.Int32
	m.check = func(_ context.Context, _ string) (int64, error) {
		count.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		time.Sleep(time.Millisecond)
		return 20, nil
	}
	m.Refresh()
	awaitRefresh(t, m)
	s := m.Status()
	if s.Candidates != 300 || s.Available != 300 || count.Load() != 300 || peak.Load() > 16 {
		t.Fatalf("unbounded checker candidates=%d available=%d checks=%d peak=%d", s.Candidates, s.Available, count.Load(), peak.Load())
	}
	fromSecond := false
	for _, item := range s.Items {
		if item.Source == "https://two.example/list" {
			fromSecond = true
		}
	}
	if !fromSecond {
		t.Fatal("first list starved later source")
	}
}

func TestUseFailureIsNotResurrectedByEarlierRefreshProbe(t *testing.T) {
	m := testManager(t)
	address := "http://8.8.8.8:8080"
	m.items[address] = Item{Address: address, CheckedAt: time.Now()}
	m.status.Config.Static = []string{"http://1.1.1.1:8080"}
	stamped := make(chan struct{})
	releaseSecond := make(chan struct{})
	var clock atomic.Int64
	var firstReturned atomic.Bool
	var stampOnce sync.Once
	base := time.Now()
	m.now = func() time.Time {
		stamp := base.Add(time.Duration(clock.Add(1)))
		if firstReturned.Load() {
			stampOnce.Do(func() { close(stamped) })
		}
		return stamp
	}
	m.check = func(ctx context.Context, proxy string) (int64, error) {
		if proxy == address {
			firstReturned.Store(true)
			return 10, nil
		}
		select {
		case <-releaseSecond:
		case <-ctx.Done():
		}
		return 10, nil
	}
	m.Refresh()
	<-stamped
	m.markFailed(address)
	close(releaseSecond)
	awaitRefresh(t, m)
	for _, item := range m.Status().Items {
		if item.Address == address {
			t.Fatal("refresh resurrected a proxy that failed after its successful probe")
		}
	}
}

func TestDiscoveryRotatesBeyondFirstFailedCandidateBatch(t *testing.T) {
	m := testManager(t)
	m.status.Config.Sources = []string{"https://example.com/list"}
	m.fetch = func(context.Context, string) ([]string, error) {
		var list []string
		for i := 0; i < 1000; i++ {
			list = append(list, fmt.Sprintf("8.8.8.8:%d", 1000+i))
		}
		return list, nil
	}
	var firstBatch, secondBatch sync.Map
	var round atomic.Int32
	m.check = func(_ context.Context, address string) (int64, error) {
		if round.Load() == 0 {
			firstBatch.Store(address, true)
		} else {
			secondBatch.Store(address, true)
		}
		return 0, errors.New("injected dead proxy")
	}
	m.Refresh()
	awaitRefresh(t, m)
	round.Store(1)
	m.Refresh()
	awaitRefresh(t, m)
	count := 0
	secondBatch.Range(func(key, value any) bool {
		count++
		if _, seen := firstBatch.Load(key); seen {
			t.Errorf("dead first batch was checked again before discovering later entries: %s", key)
		}
		return true
	})
	if count != 300 {
		t.Fatalf("second discovery batch %d", count)
	}
}
