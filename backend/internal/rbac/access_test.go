package rbac

import "testing"

var clustersDePrueba = []ClusterRef{
	{ID: 1, Name: "prod"},
	{ID: 2, Name: "staging"},
}

func accessDe(t *testing.T, assignments []Assignment) map[string]ClusterAccess {
	t.Helper()
	_, access := EffectiveAccess(assignments, clustersDePrueba)
	out := map[string]ClusterAccess{}
	for _, a := range access {
		out[a.Cluster] = a
	}
	return out
}

func TestEffectiveAccessWithoutAssignments(t *testing.T) {
	global, access := EffectiveAccess(nil, clustersDePrueba)
	if global != "" {
		t.Errorf("global role = %q, expected empty", global)
	}
	// Clusters without access are returned anyway: a matrix that only shows
	// what someone does have does not allow checking that someone does NOT
	// reach production.
	if len(access) != 2 {
		t.Fatalf("%d clusters returned, expected both 2", len(access))
	}
	for _, a := range access {
		if a.Role != "" || a.Source != "" {
			t.Errorf("%s: expected no access, got %+v", a.Cluster, a)
		}
	}
}

func TestEffectiveAccessGlobalOnly(t *testing.T) {
	got := accessDe(t, []Assignment{{Role: RoleReadonly, ScopeType: "global"}})
	for _, nombre := range []string{"prod", "staging"} {
		if got[nombre].Role != RoleReadonly || got[nombre].Source != SourceGlobal {
			t.Errorf("%s = %+v, expected readonly/global", nombre, got[nombre])
		}
	}
}

func TestEffectiveAccessClusterAdminWithGlobalReadonly(t *testing.T) {
	// The case that motivates the whole screen: the global role does not tell the truth.
	got := accessDe(t, []Assignment{
		{Role: RoleReadonly, ScopeType: "global"},
		{Role: RoleAdmin, ScopeType: "cluster", ScopeRef: "1"},
	})
	if got["prod"].Role != RoleAdmin || got["prod"].Source != SourceCluster {
		t.Errorf("prod = %+v, expected admin through the cluster assignment", got["prod"])
	}
	if got["staging"].Role != RoleReadonly || got["staging"].Source != SourceGlobal {
		t.Errorf("staging = %+v, expected readonly inherited from global", got["staging"])
	}
}

func TestEffectiveAccessAdminGlobalGanaAReadonlyDeCluster(t *testing.T) {
	got := accessDe(t, []Assignment{
		{Role: RoleAdmin, ScopeType: "global"},
		{Role: RoleReadonly, ScopeType: "cluster", ScopeRef: "1"},
	})
	// The highest wins, and the source has to say "global": revoking the
	// cluster assignment would take nothing away.
	if got["prod"].Role != RoleAdmin || got["prod"].Source != SourceGlobal {
		t.Errorf("prod = %+v, expected admin/global", got["prod"])
	}
}

func TestEffectiveAccessSameLevelFromBothSourcesIsBoth(t *testing.T) {
	got := accessDe(t, []Assignment{
		{Role: RoleAdmin, ScopeType: "global"},
		{Role: RoleAdmin, ScopeType: "cluster", ScopeRef: "1"},
	})
	// "both" matters: to remove the access BOTH have to be revoked.
	if got["prod"].Source != SourceBoth {
		t.Errorf("prod source = %q, expected both", got["prod"].Source)
	}
}

func TestEffectiveAccessIgnoresNamespaceScope(t *testing.T) {
	// The namespace scope exists in the enum but no read filters by
	// namespace: showing it would suggest a permission that does not exist.
	got := accessDe(t, []Assignment{{Role: RoleAdmin, ScopeType: "namespace", ScopeRef: "1/default"}})
	if got["prod"].Role != "" {
		t.Errorf("prod = %+v, a namespace assignment must not grant access to the cluster", got["prod"])
	}
}

func TestEffectiveAccessSeveralAssignmentsOfTheSameScope(t *testing.T) {
	// The UNIQUE on role_assignments does not deduplicate globals (scope_ref
	// NULL and in Postgres NULL != NULL), so a user can have global admin and
	// readonly at once: admin wins.
	global, _ := EffectiveAccess([]Assignment{
		{Role: RoleReadonly, ScopeType: "global"},
		{Role: RoleAdmin, ScopeType: "global"},
	}, clustersDePrueba)
	if global != RoleAdmin {
		t.Errorf("global role = %q, expected admin", global)
	}
}

func TestEffectiveAccessSortsByClusterName(t *testing.T) {
	_, access := EffectiveAccess(nil, []ClusterRef{{ID: 1, Name: "Z"}, {ID: 2, Name: "A"}})
	if access[0].Cluster != "A" || access[1].Cluster != "Z" {
		t.Errorf("order = %v, expected alphabetical by name", []string{access[0].Cluster, access[1].Cluster})
	}
}
