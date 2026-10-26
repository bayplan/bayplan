package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Query assembly and filter parsing are pure logic: they can be tested without
// a database, and that is where the expensive mistakes live (a stray OFFSET, a
// filter that is not applied, an injection through concatenation).

func filterFrom(t *testing.T, query string) auditFilter {
	t.Helper()
	f, err := parseAuditFilter(httptest.NewRequest("GET", "/api/audit?"+query, nil))
	if err != nil {
		t.Fatalf("parseAuditFilter(%q) returned error: %v", query, err)
	}
	return f
}

func TestParseAuditFilterRejectsInvalidValues(t *testing.T) {
	cases := []string{
		"result=madeup",
		"from=yesterday",
		"to=2026-13-45",
		"limit=0",
		"limit=abc",
		"cursor=noComma",
		"cursor=notADate,5",
		"cursor=2026-08-01T10:00:00Z,notANumber",
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			if _, err := parseAuditFilter(httptest.NewRequest("GET", "/api/audit?"+q, nil)); err == nil {
				t.Errorf("expected error for %q", q)
			}
		})
	}
}

func TestParseAuditFilterCapsTheLimit(t *testing.T) {
	if got := filterFrom(t, "").limit; got != defaultAuditPageSize {
		t.Errorf("default limit = %d, expected %d", got, defaultAuditPageSize)
	}
	if got := filterFrom(t, "limit=999999").limit; got != maxAuditPageSize {
		t.Errorf("limit = %d, expected it to be capped at %d", got, maxAuditPageSize)
	}
}

func TestBuildAuditQueryAlwaysUsesKeysetAndNeverOffset(t *testing.T) {
	f := filterFrom(t, "cursor=2026-08-01T10:00:00Z,42")
	query, args := buildAuditQuery(f, auditColumns, 10)

	if strings.Contains(strings.ToUpper(query), "OFFSET") {
		t.Fatalf("the query uses OFFSET, which on a partitioned table forces reading and discarding whole partitions:\n%s", query)
	}
	if !strings.Contains(query, "(ts, id) < (") {
		t.Errorf("missing the keyset tuple comparison:\n%s", query)
	}
	if !strings.Contains(query, "ORDER BY ts DESC, id DESC") {
		t.Errorf("the order must be by (ts, id) descending for the cursor to be consistent:\n%s", query)
	}
	// ts, id and the limit.
	if len(args) != 3 {
		t.Errorf("args = %v, expected cursor (ts, id) + limit", args)
	}
}

func TestBuildAuditQueryParametrizesEveryFilter(t *testing.T) {
	f := filterFrom(t, "actor=alice&action=user.delete&cluster=PRO&result=denied&q=last_admin&from=2026-07-01T00:00:00Z&to=2026-08-01T00:00:00Z")
	query, args := buildAuditQuery(f, auditColumns, 50)

	// No user value may appear literally in the SQL: all of them go as
	// parameters. If one slipped in concatenated, this catches it.
	for _, value := range []string{"alice", "user.delete", "PRO", "denied", "last_admin"} {
		if strings.Contains(query, value) {
			t.Errorf("value %q appears literally in the query, it should be parametrized:\n%s", value, query)
		}
	}
	// 7 filters + limit.
	if len(args) != 8 {
		t.Fatalf("args = %d (%v), expected 7 filters + limit", len(args), args)
	}
	for _, fragment := range []string{"actor_username ILIKE", "action ILIKE", "cluster ILIKE", "result =", "detail::text ILIKE", "ts >=", "ts <="} {
		if !strings.Contains(query, fragment) {
			t.Errorf("missing filter %q in:\n%s", fragment, query)
		}
	}
}

func TestBuildAuditQueryWithoutFiltersHasNoWhere(t *testing.T) {
	query, args := buildAuditQuery(filterFrom(t, ""), auditColumns, 50)
	if strings.Contains(query, "WHERE") {
		t.Errorf("without filters there should be no WHERE:\n%s", query)
	}
	if len(args) != 1 {
		t.Errorf("args = %v, expected only the limit", args)
	}
}

func TestParseAuditFilterReadsTheFullCursor(t *testing.T) {
	f := filterFrom(t, "cursor=2026-08-01T10:00:00.123456Z,42")
	expected := time.Date(2026, 8, 1, 10, 0, 0, 123456000, time.UTC)
	if !f.cursorTS.Equal(expected) {
		t.Errorf("cursorTS = %v, expected %v (microseconds matter: ts is not unique)", f.cursorTS, expected)
	}
	if f.cursorID != 42 {
		t.Errorf("cursorID = %d, expected 42", f.cursorID)
	}
}
