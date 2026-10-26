package middleware

import (
	"sync"
	"time"
)

// AccountLimiter throttles FAILED logins per account. It is not a
// rate.Limiter on purpose: the check and the charge have to be one step
// (Acquire reserves the attempt while bcrypt runs, so a burst of parallel
// guesses cannot all pass a "still has tokens?" check before any of them is
// charged), and a successful login must give its reservation back without
// consuming anything, which rate.Reservation cannot do once its time passed.
type AccountLimiter struct {
	mu    sync.Mutex
	per   time.Duration // one token back every per
	burst float64
	now   func() time.Time
	accts map[string]*account
	sweep time.Time
}

type account struct {
	tokens   float64
	last     time.Time
	inflight int
}

func NewAccountLimiter(per time.Duration, burst int) *AccountLimiter {
	return &AccountLimiter{per: per, burst: float64(burst), now: time.Now, accts: map[string]*account{}}
}

func (l *AccountLimiter) refill(a *account, now time.Time) {
	a.tokens += float64(now.Sub(a.last)) / float64(l.per)
	if a.tokens > l.burst {
		a.tokens = l.burst
	}
	a.last = now
}

// Acquire reserves one attempt for key, counting the ones still in flight.
// false means the account is out of attempts. Every true must be followed by
// exactly one Release.
func (l *AccountLimiter) Acquire(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.sweep) > time.Minute {
		l.sweepLocked(now)
	}
	a, ok := l.accts[key]
	if !ok {
		a = &account{tokens: l.burst, last: now}
		l.accts[key] = a
	}
	l.refill(a, now)
	if a.tokens-float64(a.inflight) < 1 {
		return false
	}
	a.inflight++
	return true
}

// Release ends a reservation; only a failed attempt costs a token.
func (l *AccountLimiter) Release(key string, failed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.accts[key]
	if !ok {
		return
	}
	a.inflight--
	if failed {
		l.refill(a, l.now())
		a.tokens--
	}
}

// sweepLocked forgets accounts that are idle and full again: dropping one
// earlier would hand a throttled account a fresh burst.
func (l *AccountLimiter) sweepLocked(now time.Time) {
	l.sweep = now
	for k, a := range l.accts {
		if a.inflight == 0 {
			l.refill(a, now)
			if a.tokens >= l.burst {
				delete(l.accts, k)
			}
		}
	}
}
