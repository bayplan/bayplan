package oidc

import (
	"testing"
	"time"
)

func TestGroupCacheHitAndMiss(t *testing.T) {
	c := NewGroupCache(50 * time.Millisecond)

	if _, ok := c.Get("oid1"); ok {
		t.Fatal("expected a miss before Set")
	}

	c.Set("oid1", []string{"g1", "g2"})
	got, ok := c.Get("oid1")
	if !ok {
		t.Fatal("expected a hit right after Set")
	}
	if len(got) != 2 || got[0] != "g1" {
		t.Errorf("groups = %v", got)
	}
}

func TestGroupCacheExpires(t *testing.T) {
	c := NewGroupCache(10 * time.Millisecond)
	c.Set("oid1", []string{"g1"})

	time.Sleep(30 * time.Millisecond)

	if _, ok := c.Get("oid1"); ok {
		t.Fatal("expected a miss after the TTL expired")
	}
}

func TestGroupCacheEvictsExpiredOnSet(t *testing.T) {
	c := NewGroupCache(10 * time.Millisecond)
	c.Set("old1", []string{"g"})
	c.Set("old2", []string{"g"})
	time.Sleep(30 * time.Millisecond)
	c.Set("fresh", []string{"g"})
	if n := c.Len(); n != 1 {
		t.Fatalf("entries after sweep = %d, want 1", n)
	}
	if _, ok := c.Get("fresh"); !ok {
		t.Fatal("the fresh entry must survive the sweep")
	}
}
