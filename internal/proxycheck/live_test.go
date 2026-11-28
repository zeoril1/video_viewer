package proxycheck

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Explicit, bounded diagnostic only: normal tests never contact public proxies.
// It uses the production TLS checker and its two fixed non-media targets.
func TestLiveBoundedHTTPSProxySmoke(t *testing.T) {
	if os.Getenv("PROXYCHECK_LIVE_SMOKE") != "1" {
		t.Skip("set PROXYCHECK_LIVE_SMOKE=1 for at most 10 proxy probes, 2 workers, 25 seconds")
	}
	m := testManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	sources := DefaultConfig().Sources
	lists := make([][]string, len(sources))
	var sourceWG sync.WaitGroup
	var sourcesOK atomic.Int32
	for i, source := range sources {
		sourceWG.Add(1)
		go func(i int, source string) {
			defer sourceWG.Done()
			list, err := m.fetchSource(ctx, source)
			if err != nil {
				t.Logf("source %d: %v", i+1, err)
				return
			}
			rand.Shuffle(len(list), func(a, b int) { list[a], list[b] = list[b], list[a] })
			lists[i] = list
			sourcesOK.Add(1)
		}(i, source)
	}
	sourceWG.Wait()
	var candidates []string
	seen := map[string]bool{}
	for n := 0; n < maxParsedCandidates && len(candidates) < 10; n++ {
		for _, list := range lists {
			if n < len(list) && !seen[list[n]] && len(candidates) < 10 {
				candidates = append(candidates, list[n])
				seen[list[n]] = true
			}
		}
	}
	var checked, verified atomic.Int32
	var wg sync.WaitGroup
	var mu sync.Mutex
	var latencies []int64
	failureTypes := map[string]int{}
	jobs := make(chan string)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for address := range jobs {
				if ctx.Err() != nil {
					continue
				}
				checked.Add(1)
				if latency, err := m.checkProxy(ctx, address); err == nil {
					verified.Add(1)
					mu.Lock()
					latencies = append(latencies, latency)
					mu.Unlock()
				} else {
					kind := probeErrorKind(err)
					var failure *probeFailure
					if errors.As(err, &failure) {
						kind = strings.Join(failure.kinds, "+")
					}
					mu.Lock()
					failureTypes[kind]++
					mu.Unlock()
				}
			}
		}()
	}
	for _, address := range candidates {
		select {
		case jobs <- address:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	t.Logf("sources_ok=%d/%d candidates=%d checked=%d verified=%d latency_ms=%v failure_types=%v", sourcesOK.Load(), len(sources), len(candidates), checked.Load(), verified.Load(), latencies, failureTypes)
	if sourcesOK.Load() == 0 {
		t.Log("source discovery was unavailable; this does not establish proxy availability")
	}
}
