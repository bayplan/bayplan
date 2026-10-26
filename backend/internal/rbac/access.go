package rbac

import (
	"sort"
	"strconv"
)

// AccessSource is the origin of the effective role on a cluster. Without it, a
// permissions table says WHAT someone can do but not WHY, which is exactly what
// has to be known to take it away: revoking the cluster assignment is useless
// if the permission actually came from a global role.
type AccessSource string

const (
	SourceGlobal  AccessSource = "global"
	SourceCluster AccessSource = "cluster"
	// SourceBoth: has both and they match in level, so both have to be revoked
	// for the access to go away.
	SourceBoth AccessSource = "both"
)

// Assignment is a role_assignments row reduced to what matters here.
type Assignment struct {
	Role      Role
	ScopeType string
	ScopeRef  string
}

// ClusterRef identifies a cluster for the permissions table.
type ClusterRef struct {
	ID   int64
	Name string
}

// ClusterAccess is a user's effective access to one specific cluster.
type ClusterAccess struct {
	ClusterID int64        `json:"clusterId"`
	Cluster   string       `json:"cluster"`
	Role      Role         `json:"role,omitempty"`
	Source    AccessSource `json:"source,omitempty"`
}

// EffectiveAccess resolves what a user can do on each cluster and where it
// comes from.
//
// A pure function over the rows already read, not another query: the
// resolution (global OR cluster, the highest wins) is the SAME rule that
// EffectiveRoleForCluster applies on every request, and having it in one
// testable place keeps the permissions screen and the middleware from ending up
// disagreeing — which would be the worst possible failure in a table that says
// who can do what.
//
// ALL clusters are returned, also those without access (empty Role): a
// permissions matrix that only shows the ones someone does have does not allow
// checking at a glance that someone does NOT reach production.
func EffectiveAccess(assignments []Assignment, clusters []ClusterRef) (Role, []ClusterAccess) {
	var globalRoles []Role
	byCluster := map[string][]Role{}

	for _, a := range assignments {
		switch a.ScopeType {
		case "global":
			globalRoles = append(globalRoles, a.Role)
		case "cluster":
			byCluster[a.ScopeRef] = append(byCluster[a.ScopeRef], a.Role)
		}
		// scope_type 'namespace' is ignored on purpose: it exists in the enum
		// but no read filters by namespace and the grant API rejects it, so
		// showing it here would suggest a permission that does not exist.
	}
	globalRole, hasGlobal := highest(globalRoles)
	if !hasGlobal {
		globalRole = ""
	}

	access := make([]ClusterAccess, 0, len(clusters))
	for _, c := range clusters {
		entry := ClusterAccess{ClusterID: c.ID, Cluster: c.Name}
		clusterRole, hasCluster := highest(byCluster[strconv.FormatInt(c.ID, 10)])
		if !hasCluster {
			clusterRole = ""
		}

		switch {
		case !hasGlobal && !hasCluster:
			// No access: Role and Source are left empty.
		case hasGlobal && !hasCluster:
			entry.Role, entry.Source = globalRole, SourceGlobal
		case !hasGlobal && hasCluster:
			entry.Role, entry.Source = clusterRole, SourceCluster
		case globalRole == clusterRole:
			entry.Role, entry.Source = globalRole, SourceBoth
		case globalRole.rank() > clusterRole.rank():
			entry.Role, entry.Source = globalRole, SourceGlobal
		default:
			entry.Role, entry.Source = clusterRole, SourceCluster
		}
		access = append(access, entry)
	}

	sort.Slice(access, func(i, j int) bool { return access[i].Cluster < access[j].Cluster })
	return globalRole, access
}
