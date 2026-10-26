package middleware

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestIPRateLimiterEvictsIdle(t *testing.T) {
	l := NewIPRateLimiter(rate.Every(time.Minute), 5) // refill takes 5 min < min idle
	clock := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return clock }

	l.limiterFor("10.0.0.1")
	l.limiterFor("10.0.0.2")
	if n := l.size(); n != 2 {
		t.Fatalf("size = %d, want 2", n)
	}

	// 10.0.0.2 stays active, 10.0.0.1 goes idle.
	clock = clock.Add(rateLimiterMinIdle / 2)
	l.limiterFor("10.0.0.2")
	clock = clock.Add(rateLimiterMinIdle/2 + time.Second)
	l.limiterFor("10.0.0.3")
	if n := l.size(); n != 2 {
		t.Fatalf("size after sweep = %d, want 2 (idle IP evicted)", n)
	}
	if _, ok := l.limiters["10.0.0.1"]; ok {
		t.Fatal("idle IP was not evicted")
	}
}

func TestIPRateLimiterKeepsThrottledUntilRefilled(t *testing.T) {
	// 1 token per hour, burst 2: the bucket needs 2 h to refill, longer than
	// the minimum idle time, so the entry must survive past it.
	l := NewIPRateLimiter(rate.Every(time.Hour), 2)
	clock := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return clock }

	lim := l.limiterFor("10.0.0.9")
	if l.idleTTL != 2*time.Hour {
		t.Fatalf("idleTTL = %v, want 2h", l.idleTTL)
	}
	clock = clock.Add(time.Hour)
	l.limiterFor("10.0.0.10") // triggers a sweep
	if got := l.limiters["10.0.0.9"]; got == nil || got.lim != lim {
		t.Fatal("a throttled IP was forgotten before its bucket refilled")
	}
}
