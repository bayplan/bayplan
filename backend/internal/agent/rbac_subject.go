package agent

import (
	"fmt"
	"strings"
)

// Subject is the identity the panel's RBAC is granted to in a cluster that
// does NOT use a ServiceAccount: with the cloud connector, the identity the API
// server sees is the provider's (the IAM principal mapped to a group in EKS,
// the service principal's object id in AKS, the service account's email in
// GKE).
type Subject struct {
	Kind string // "User" or "Group"
	Name string
}

// RenderRBAC returns the panel's RBAC (technical read ClusterRole,
// impersonator and the three impersonated groups) granted to `s` instead of to
// the bayplan-technical ServiceAccount. It reuses the embedded copy of
// deploy/rbac/serviceaccount.yaml so that a new permission reaches the three
// connectors at once: the ServiceAccount document is dropped and the subject
// of the two ClusterRoleBindings that reference it is replaced.
func RenderRBAC(s Subject, clusterAdmin bool) string {
	subject := fmt.Sprintf("  - kind: %s\n    name: %q\n    apiGroup: rbac.authorization.k8s.io", s.Kind, s.Name)
	var docs []string
	for _, doc := range strings.Split(rbacYAML, "\n---\n") {
		// "\nkind: ServiceAccount" only matches the SA document, not the
		// subjects ("  - kind: ServiceAccount").
		if strings.Contains(doc, "\nkind: ServiceAccount\n") {
			continue
		}
		doc = strings.ReplaceAll(doc,
			"  - kind: ServiceAccount\n    name: bayplan-technical\n    namespace: kube-system", subject)
		docs = append(docs, doc)
	}
	out := strings.Join(docs, "\n---\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if clusterAdmin {
		out += ClusterAdminBindings(subject)
	}
	return out
}
