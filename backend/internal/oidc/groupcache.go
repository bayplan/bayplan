package oidc

import (
	"sync"
	"time"
)

// GroupCache memoizes the group list resolved via Microsoft Graph (the overage
// fallback) so that call is not repeated within short windows. It does not aim
// to re-evaluate group membership mid-session (Bayplan's session does not keep
// the Entra token); the real bound on "stale privilege after leaving a group"
// is SESSION_ABSOLUTE_TIMEOUT + the next login, not this cache's TTL.
// Re-evaluating live would require storing an encrypted refresh token — a
// future improvement, not included.
type GroupCache struct {
	mu  sync.RWMutex
	ttl time.Duration
	m   map[string]cacheEntry
}

type cacheEntry struct {
	groups []string
	expiry time.Time
}

func NewGroupCache(ttl time.Duration) *GroupCache {
	return &GroupCache{ttl: ttl, m: make(map[string]cacheEntry)}
}

// Get returns the cached groups for oid if they have not expired.
func (c *GroupCache) Get(oid string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[oid]
	if !ok || time.Now().After(e.expiry) {
		return nil, false
	}
	return e.groups, true
}

// Set stores groups for oid with the configured TTL. Expired entries are
// swept here: one per user who ever hit the overage fallback would otherwise
// stay in memory for the life of the process. Set only runs on a login that
// missed the cache, so the O(n) sweep is not on any hot path.
func (c *GroupCache) Set(oid string, groups []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, e := range c.m {
		if now.After(e.expiry) {
			delete(c.m, k)
		}
	}
	c.m[oid] = cacheEntry{groups: groups, expiry: now.Add(c.ttl)}
}

// Len returns the number of entries held (expired or not), for tests.
func (c *GroupCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}
