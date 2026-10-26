package oidc

import (
	"encoding/json"
	"reflect"
	"testing"
)

func rawClaims(t *testing.T, v map[string]any) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExtractClaims(t *testing.T) {
	t.Run("entra: oid wins over sub and regular groups", func(t *testing.T) {
		c := ExtractClaims(rawClaims(t, map[string]any{
			"oid": "oid-1", "preferred_username": "ana@example.com", "name": "Ana", "groups": []string{"g1", " g2 "},
		}), "sub-1", "", "groups")
		if c.Subject != "oid-1" || c.Username != "ana@example.com" || c.Name != "Ana" {
			t.Fatalf("claims = %+v", c)
		}
		if !reflect.DeepEqual(c.Groups, []string{"g1", "g2"}) || c.GroupsOverflowed {
			t.Fatalf("groups = %v overflowed=%v", c.Groups, c.GroupsOverflowed)
		}
	})
	t.Run("generic: sub as subject, configurable username claim", func(t *testing.T) {
		c := ExtractClaims(rawClaims(t, map[string]any{"email": "bob@x.io", "upn": "bob"}), "sub-9", "upn", "roles")
		if c.Subject != "sub-9" || c.Username != "bob" || len(c.Groups) != 0 {
			t.Fatalf("claims = %+v", c)
		}
	})
	t.Run("without username or email falls back to the subject", func(t *testing.T) {
		c := ExtractClaims(rawClaims(t, map[string]any{}), "sub-2", "", "")
		if c.Username != "sub-2" {
			t.Fatalf("username = %q", c.Username)
		}
	})
	t.Run("real overage: no groups but _claim_names.groups present", func(t *testing.T) {
		c := ExtractClaims(rawClaims(t, map[string]any{"_claim_names": map[string]string{"groups": "src1"}}), "s", "", "groups")
		if !c.GroupsOverflowed {
			t.Fatal("expected overflow")
		}
	})
	t.Run("no groups and no _claim_names is not overage", func(t *testing.T) {
		c := ExtractClaims(rawClaims(t, map[string]any{"groups": []string{}}), "s", "", "groups")
		if c.GroupsOverflowed {
			t.Fatal("should not have flagged overflow")
		}
	})
	t.Run("groups as a comma-separated string", func(t *testing.T) {
		c := ExtractClaims(rawClaims(t, map[string]any{"groups": "a, b c"}), "s", "", "groups")
		if !reflect.DeepEqual(c.Groups, []string{"a", "b", "c"}) {
			t.Fatalf("groups = %v", c.Groups)
		}
	})
	t.Run("without a configured groups claim they are ignored even if present", func(t *testing.T) {
		c := ExtractClaims(rawClaims(t, map[string]any{"groups": []string{"g1"}}), "s", "", "")
		if len(c.Groups) != 0 {
			t.Fatalf("groups = %v", c.Groups)
		}
	})
}
