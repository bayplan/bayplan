package oidc

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchMemberGroupsPaginates(t *testing.T) {
	var page2URL string

	mux := http.NewServeMux()
	mux.HandleFunc("/page1", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"value":[{"id":"g1"},{"id":"g2"}],"@odata.nextLink":"` + page2URL + `"}`))
	})
	mux.HandleFunc("/page2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"value":[{"id":"g3"}]}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	page2URL = srv.URL + "/page2"

	// FetchMemberGroups always points at the fixed Graph URL; to test
	// pagination without a real network, the pagination logic is exercised
	// against our server by feeding the first page directly through an
	// equivalent local function. Since FetchMemberGroups has the Graph URL
	// hard-coded, the decode + nextLink-following contract is verified here by
	// calling the test server as if it were the first page.
	groups, err := fetchMemberGroupsFrom(t.Context(), srv.Client(), "test-token", srv.URL+"/page1")
	if err != nil {
		t.Fatalf("fetchMemberGroupsFrom: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("groups = %v, want 3 elements", groups)
	}
	want := map[string]bool{"g1": true, "g2": true, "g3": true}
	for _, g := range groups {
		if !want[g] {
			t.Errorf("unexpected group: %q", g)
		}
	}
}
