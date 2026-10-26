package k8s

import (
	"k8s.io/client-go/kubernetes"

	"github.com/bayplan/bayplan/internal/rbac"
)

// actionClientset builds a clientset that impersonates the real Bayplan user
// ("bayplan:" prefix so it never collides with the cluster's native identities)
// on top of the technical ServiceAccount's base identity. That way the native
// audit of the Kubernetes API server records who really acted, not only our
// internal audit_log.
//
// Kubernetes authorizes the impersonated request with the RBAC of the
// IMPERSONATED identity, not with the technical ServiceAccount's: that is why a
// group ("bayplan:admins" / "bayplan:readonly") is impersonated too, one that
// does have a ClusterRoleBinding with the real permissions (see deploy/rbac).
// The impersonated user (without the group) is still recorded in the native
// audit for traceability, even though the group is what authorizes.
//
// Security note: the technical SA needs the "impersonate" verb on
// users/groups, scoped to the identities the panel uses — the real security
// boundary is the backend's internal RBAC.
func (c *clusterConn) actionClientset(actingUser string, actingGroup string) (*kubernetes.Clientset, error) {
	cfg := c.restConfig
	cfg.Impersonate.UserName = impersonatedUsername(actingUser)
	cfg.Impersonate.Groups = []string{actingGroup}
	return kubernetes.NewForConfig(&cfg)
}

func impersonatedUsername(username string) string {
	return "bayplan:" + username
}

// GroupAdmins, GroupEditors and GroupReadonly are the impersonated groups that
// carry the real RBAC in the managed cluster (see deploy/rbac/serviceaccount.yaml).
const (
	GroupAdmins   = "bayplan:admins"
	GroupEditors  = "bayplan:editors"
	GroupReadonly = "bayplan:readonly"
)

// GroupForRole maps the internal role to the impersonated group. The cluster's
// native audit thus records which privilege each request acted with.
func GroupForRole(role rbac.Role) string {
	switch {
	case role.IsAdmin():
		return GroupAdmins
	case role.CanWrite():
		return GroupEditors
	default:
		return GroupReadonly
	}
}
