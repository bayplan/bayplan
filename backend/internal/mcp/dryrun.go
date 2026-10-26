package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"
)

// dryRunLedger remembers which manifests a user has rehearsed, so that a real
// apply_manifest is only accepted after a successful dry-run of the SAME YAML
// on the SAME cluster and namespace by the same user. The tool description
// already asks the model for it; this enforces it, because a model steered by
// text inside the cluster (a log line, an annotation) would skip the step the
// user relies on to see what is about to change.
type dryRunLedger struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

func newDryRunLedger(ttl time.Duration) *dryRunLedger {
	return &dryRunLedger{ttl: ttl, seen: map[string]time.Time{}}
}

func dryRunKey(userID, clusterID int64, namespace, yaml string) string {
	h := sha256.New()
	for _, part := range []string{strconv.FormatInt(userID, 10), strconv.FormatInt(clusterID, 10), namespace, yaml} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (l *dryRunLedger) record(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, at := range l.seen {
		if now.Sub(at) > l.ttl {
			delete(l.seen, k)
		}
	}
	l.seen[key] = now
}

// consume reports whether key was rehearsed within the TTL and forgets it: a
// second real apply needs a new dry-run.
func (l *dryRunLedger) consume(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	at, ok := l.seen[key]
	delete(l.seen, key)
	return ok && now.Sub(at) <= l.ttl
}
