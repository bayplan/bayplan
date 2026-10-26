package middleware

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// IPRateLimiter limits requests per source IP. Meant for sensitive endpoints
// (local login). The plan assumes a single instance for the MVP (see
// architecture decisions): if it is ever scaled to several replicas, this
// limit stops being effective per instance and would have to be centralized
// (e.g. in Postgres or a shared counter).
//
// Idle IPs are evicted: the map used to keep one limiter per source address
// for the life of the process, so anyone able to vary their source IP (IPv6
// gives them a /64 to choose from) could grow it without bound.
type IPRateLimiter struct {
	mu        sync.Mutex
	limiters  map[string]*ipLimiter
	r         rate.Limit
	burst     int
	idleTTL   time.Duration
	lastSweep time.Time
	now       func() time.Time // injectable for tests
}

type ipLimiter struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

const (
	// rateLimiterSweepEvery bounds how often the map is walked on access.
	rateLimiterSweepEvery = time.Minute
	// rateLimiterMinIdle is the minimum idle time before an IP is forgotten.
	rateLimiterMinIdle = 10 * time.Minute
)

func NewIPRateLimiter(r rate.Limit, burst int) *IPRateLimiter {
	// An entry may only be dropped once its bucket would have refilled
	// completely: forgetting it earlier would hand a throttled IP a fresh
	// burst, which is exactly what the limiter is there to prevent.
	idle := rateLimiterMinIdle
	if r > 0 {
		if refill := time.Duration(float64(burst) / float64(r) * float64(time.Second)); refill > idle {
			idle = refill
		}
	}
	return &IPRateLimiter{
		limiters: make(map[string]*ipLimiter), r: r, burst: burst, idleTTL: idle, now: time.Now,
	}
}

func (l *IPRateLimiter) limiterFor(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= rateLimiterSweepEvery {
		l.sweepLocked(now)
	}
	e, ok := l.limiters[ip]
	if !ok {
		e = &ipLimiter{lim: rate.NewLimiter(l.r, l.burst)}
		l.limiters[ip] = e
	}
	e.lastSeen = now
	return e.lim
}

func (l *IPRateLimiter) sweepLocked(now time.Time) {
	l.lastSweep = now
	for ip, e := range l.limiters {
		if now.Sub(e.lastSeen) > l.idleTTL {
			delete(l.limiters, ip)
		}
	}
}

// size returns the number of tracked IPs, for tests.
func (l *IPRateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.limiters)
}

// Allow reports whether the request from r may proceed.
func (l *IPRateLimiter) Allow(r *http.Request) bool {
	return l.limiterFor(ClientIP(r)).Allow()
}
