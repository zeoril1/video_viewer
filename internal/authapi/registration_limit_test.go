package authapi

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistrationLimitIsAtomic(t *testing.T) {
	l := newLoginLimiter()
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.takeRegistration("192.0.2.1|") {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != loginMaxAttempts {
		t.Fatalf("accepted %d", accepted.Load())
	}
	if !l.takeRegistration("192.0.2.2|") {
		t.Fatal("other client blocked")
	}
	if !l.allow("192.0.2.1|user") {
		t.Fatal("registration blocked login")
	}
	l.state["192.0.2.1|"].window = time.Now().Add(-loginWindow - time.Second)
	if !l.takeRegistration("192.0.2.1|") {
		t.Fatal("window did not reset")
	}
}
