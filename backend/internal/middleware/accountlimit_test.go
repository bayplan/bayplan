package middleware

import (
	"testing"
	"time"
)

func TestAccountLimiter(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	l := NewAccountLimiter(time.Minute, 3)
	l.now = func() time.Time { return clock }

	// Successes never consume.
	for i := 0; i < 10; i++ {
		if !l.Acquire("admin") {
			t.Fatal("successful logins must not exhaust the account")
		}
		l.Release("admin", false)
	}

	// In-flight attempts count: three parallel guesses take the whole burst
	// before any of them has finished.
	for i := 0; i < 3; i++ {
		if !l.Acquire("admin") {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}
	if l.Acquire("admin") {
		t.Fatal("a fourth parallel attempt must be refused")
	}
	for i := 0; i < 3; i++ {
		l.Release("admin", true)
	}
	if l.Acquire("admin") {
		t.Fatal("three failures with burst 3 must exhaust the account")
	}
	if !l.Acquire("other") {
		t.Fatal("accounts are independent")
	}
	l.Release("other", false)

	clock = clock.Add(time.Minute)
	if !l.Acquire("admin") {
		t.Fatal("one token comes back per minute")
	}
	l.Release("admin", false)

	// Idle and full again: swept.
	clock = clock.Add(time.Hour)
	l.Acquire("x")
	l.Release("x", false)
	if _, ok := l.accts["admin"]; ok {
		t.Fatal("a refilled idle account should be forgotten")
	}
}
