package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewKeyAndHash(t *testing.T) {
	key, hash, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(key) < 40 || strings.ContainsAny(key, "+/=") {
		t.Fatalf("the key must be URL-safe and long: %q", key)
	}
	sum := sha256.Sum256([]byte(key))
	if hash != hex.EncodeToString(sum[:]) || HashKey(key) != hash {
		t.Fatalf("inconsistent hash")
	}
	k2, _, _ := NewKey()
	if k2 == key {
		t.Fatal("two identical keys")
	}
}

// authorize rejects, before touching the DB, what is already wrong in the
// handshake: with a nil pool, any query would panic, so these cases prove the
// rejection happens first.
func TestAuthorizeRejectsMalformedHandshake(t *testing.T) {
	h := &Hub{}
	cases := []struct {
		name string
		id   string
		auth string
		want string
	}{
		{"no id", "", "Bearer x", "invalid cluster header"},
		{"non-numeric id", "abc", "Bearer x", "invalid cluster header"},
		{"no credential", "1", "", "missing agent credential"},
		{"empty bearer", "1", "Bearer ", "missing agent credential"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/agent/connect", nil)
			if c.id != "" {
				req.Header.Set(HeaderClusterID, c.id)
			}
			if c.auth != "" {
				req.Header.Set("Authorization", c.auth)
			}
			_, ok, err := h.authorize(req)
			if ok || err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ok=%v err=%v, expected %q", ok, err, c.want)
			}
		})
	}
}

// A stale session ending after the agent reconnected must not take the fresh
// one down (it used to mark the cluster offline up to 60 s after reconnecting).
func TestSessionGenerations(t *testing.T) {
	h := &Hub{connected: map[int64]*tunnelSession{}}
	now := time.Now()
	old, prev := h.register(7, now)
	if prev != nil {
		t.Fatal("first session has no predecessor")
	}
	cur, prev := h.register(7, now)
	if prev != old || cur.gen == old.gen {
		t.Fatalf("the second session must replace the first: prev=%v", prev)
	}
	if h.release(7, old) {
		t.Fatal("releasing the stale session must not count as a disconnection")
	}
	if h.connected[7] != cur {
		t.Fatal("the stale session removed the current one")
	}
	if !h.release(7, cur) {
		t.Fatal("releasing the current session is a disconnection")
	}
	if _, ok := h.connected[7]; ok {
		t.Fatal("the cluster must no longer be connected")
	}
}

// Evicting closes the hijacked connection, also when the eviction comes
// before the upgrade.
func TestEvictClosesConn(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	s := &tunnelSession{}
	s.evict()
	s.setConn(a)
	if _, err := a.Write([]byte("x")); err == nil {
		t.Fatal("the connection must be closed after eviction")
	}
}
