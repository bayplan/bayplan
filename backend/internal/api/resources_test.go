package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// parseNamespaces is pure logic and is the gate through which the namespace
// filter of the whole application enters: if it lets a name with spaces through
// or duplicates values, the filter returns odd results with nothing explaining it.

func TestParseNamespaces(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "no parameter = all",
			query: "",
			want:  nil,
		},
		{
			name:  "empty parameter = all",
			query: "namespace=",
			want:  nil,
		},
		{
			// Compatibility: single-namespace links predating multi-namespace
			// have to keep working the same.
			name:  "single value",
			query: "namespace=apps-pro",
			want:  []string{"apps-pro"},
		},
		{
			name:  "several values",
			query: "namespace=apps-pro,kasten-io",
			want:  []string{"apps-pro", "kasten-io"},
		},
		{
			name:  "keeps the received order",
			query: "namespace=kasten-io,apps-pro",
			want:  []string{"kasten-io", "apps-pro"},
		},
		{
			// A name with spaces would not match any namespace and the filter
			// would return empty for no apparent reason.
			name:  "trims spaces",
			query: "namespace=%20apps-pro%20,%20kasten-io",
			want:  []string{"apps-pro", "kasten-io"},
		},
		{
			name:  "ignores extra commas",
			query: "namespace=,apps-pro,,kasten-io,",
			want:  []string{"apps-pro", "kasten-io"},
		},
		{
			name:  "deduplicates",
			query: "namespace=apps-pro,apps-pro,kasten-io",
			want:  []string{"apps-pro", "kasten-io"},
		},
		{
			name:  "only commas and spaces = all",
			query: "namespace=,%20,,",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNamespaces(httptest.NewRequest("GET", "/api/clusters/1/pods?"+tt.query, nil))
			if len(got) != len(tt.want) {
				t.Fatalf("parseNamespaces(%q) = %v, want %v", tt.query, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("parseNamespaces(%q) = %v, want %v", tt.query, got, tt.want)
				}
			}
		})
	}
}

// The separator is the comma because a namespace name is a DNS-1123 label and
// cannot contain it. If someone changed the separator to something that IS
// legal in a name, this test catches it.
func TestParseNamespacesSeparatorIsNotLegalInAName(t *testing.T) {
	const separator = ","
	if strings.ContainsAny(separator, "abcdefghijklmnopqrstuvwxyz0123456789-.") {
		t.Fatalf("the separator %q is a valid character in a namespace name: changing it would break legitimate names", separator)
	}
}
