package rbac

import (
	"reflect"
	"testing"
)

// TestHighestReduction covers the pure "highest privilege wins" logic shared by
// EffectiveGlobalRole and EffectiveRoleForCluster. Those (and
// GrantsFromGroups) require Postgres (there
// is no test DB harness in this repo yet), so the coverage here
// focuses on the part that is testable without a database: reducing the
// candidates to a single effective role.
func TestHighestReduction(t *testing.T) {
	cases := []struct {
		name     string
		roles    []Role
		wantRole Role
		wantOK   bool
	}{
		{"no candidates", nil, RoleReadonly, false},
		{"one readonly", []Role{RoleReadonly}, RoleReadonly, true},
		{"one admin", []Role{RoleAdmin}, RoleAdmin, true},
		{"admin wins over readonly", []Role{RoleReadonly, RoleAdmin}, RoleAdmin, true},
		{"several readonly remain readonly", []Role{RoleReadonly, RoleReadonly}, RoleReadonly, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := highest(tc.roles)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.wantRole {
				t.Errorf("role = %q, want %q", got, tc.wantRole)
			}
		})
	}
}

func TestRoleAtLeast(t *testing.T) {
	cases := []struct {
		role Role
		min  Role
		want bool
	}{
		{RoleAdmin, RoleAdmin, true},
		{RoleAdmin, RoleReadonly, true},
		{RoleReadonly, RoleAdmin, false},
		{RoleReadonly, RoleReadonly, true},
		{"", RoleReadonly, false},
		{"", "", true},
	}
	for _, tc := range cases {
		if got := tc.role.AtLeast(tc.min); got != tc.want {
			t.Errorf("Role(%q).AtLeast(%q) = %v, want %v", tc.role, tc.min, got, tc.want)
		}
	}
}

func TestReduceSSOGrants(t *testing.T) {
	cases := []struct {
		name string
		rows []MappingRow
		want []SSOGrant
	}{
		{"nothing mapped", nil, nil},
		{"highest global wins", []MappingRow{
			{Role: RoleReadonly, ScopeType: "global"}, {Role: RoleEditor, ScopeType: "global"},
		}, []SSOGrant{{Role: RoleEditor, ScopeType: "global"}}},
		{"cluster only", []MappingRow{
			{Role: RoleAdmin, ScopeType: "cluster", ScopeRef: "7"},
		}, []SSOGrant{{Role: RoleAdmin, ScopeType: "cluster", ScopeRef: "7"}}},
		{"highest per cluster, order kept", []MappingRow{
			{Role: RoleReadonly, ScopeType: "cluster", ScopeRef: "2"},
			{Role: RoleEditor, ScopeType: "cluster", ScopeRef: "1"},
			{Role: RoleAdmin, ScopeType: "cluster", ScopeRef: "2"},
		}, []SSOGrant{
			{Role: RoleAdmin, ScopeType: "cluster", ScopeRef: "2"},
			{Role: RoleEditor, ScopeType: "cluster", ScopeRef: "1"},
		}},
		{"cluster grant not above global is dropped", []MappingRow{
			{Role: RoleEditor, ScopeType: "global"},
			{Role: RoleEditor, ScopeType: "cluster", ScopeRef: "1"},
			{Role: RoleReadonly, ScopeType: "cluster", ScopeRef: "2"},
			{Role: RoleAdmin, ScopeType: "cluster", ScopeRef: "3"},
		}, []SSOGrant{
			{Role: RoleEditor, ScopeType: "global"},
			{Role: RoleAdmin, ScopeType: "cluster", ScopeRef: "3"},
		}},
		{"namespace, invalid role and empty ref ignored", []MappingRow{
			{Role: RoleAdmin, ScopeType: "namespace", ScopeRef: "1/x"},
			{Role: "root", ScopeType: "global"},
			{Role: RoleAdmin, ScopeType: "cluster"},
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReduceSSOGrants(tc.rows); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ReduceSSOGrants = %+v, want %+v", got, tc.want)
			}
		})
	}
}
