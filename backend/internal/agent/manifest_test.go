package agent

import (
	"github.com/bayplan/bayplan/internal/k8s"
	"os"
	"strings"
	"testing"
)

// The embedded copy of the RBAC must be identical to the source of truth in
// deploy/rbac: if they drift apart, the agent and the direct connector end up
// with different permissions without anyone noticing.
func TestEmbeddedRBACMatchesDeploy(t *testing.T) {
	src, err := os.ReadFile("../../../deploy/rbac/serviceaccount.yaml")
	if err != nil {
		t.Skipf("deploy/rbac/serviceaccount.yaml not reachable from here: %v", err)
	}
	if string(src) != rbacYAML {
		t.Fatal("internal/agent/rbac.yaml differs from deploy/rbac/serviceaccount.yaml: copy the file again")
	}
	pol, err := os.ReadFile("../../../deploy/rbac/admission-policy.yaml")
	if err != nil || string(pol) != policyYAML {
		t.Fatal("internal/agent/policy.yaml differs from deploy/rbac/admission-policy.yaml: copy the file again")
	}
	if chart, err := os.ReadFile("../../../deploy/helm/bayplan/files/admission-policy.yaml"); err == nil && string(chart) != policyYAML {
		t.Fatal("deploy/helm/bayplan/files/admission-policy.yaml differs from deploy/rbac/admission-policy.yaml: copy the file again")
	}
	// Third copy: the chart's, for the local cluster.
	if chart, err := os.ReadFile("../../../deploy/helm/bayplan/files/rbac.yaml"); err == nil && string(chart) != rbacYAML {
		t.Fatal("deploy/helm/bayplan/files/rbac.yaml differs from deploy/rbac/serviceaccount.yaml: copy the file again")
	}
}

func TestRenderManifest(t *testing.T) {
	m := RenderManifest(ManifestParams{ServerURL: "https://panel.example", ClusterID: 7, AgentKey: "k3y", Image: "ghcr.io/x/bayplan-agent:1.0"})
	for _, want := range []string{
		"kind: Namespace", "name: bayplan-agent", "kind: Deployment", `value: "https://panel.example"`, `value: "7"`,
		`agentKey: "k3y"`, "image: ghcr.io/x/bayplan-agent:1.0", "bayplan-impersonated-editor", "bayplan-impersonated-admin",
	} {
		if !strings.Contains(m, want) {
			t.Errorf("missing %q in the manifest", want)
		}
	}
	// The admission policies name both connectors' SAs on purpose (and
	// metrics-server's kube-system objects); the rest must not.
	rbacPart, _, _ := strings.Cut(m, "# ValidatingAdmissionPolicies")
	if strings.Contains(rbacPart, "kube-system") || strings.Contains(rbacPart, "bayplan-technical") {
		t.Error("the agent manifest must not reference the direct connector's SA")
	}
	for _, want := range []string{"name: bayplan-protect-panel-identities", "name: bayplan-protect-agent-namespace", "name: bayplan-limit-admin-rbac"} {
		if !strings.Contains(m, want) {
			t.Errorf("missing policy %q in the manifest", want)
		}
	}
}

func TestRenderRBACForSubject(t *testing.T) {
	out := RenderRBAC(Subject{Kind: "User", Name: "sa@proj.iam.gserviceaccount.com"}, false)
	if strings.Contains(out, "kind: ServiceAccount") {
		t.Fatalf("the RBAC for a cloud identity must not reference any ServiceAccount:\n%s", out)
	}
	if got := strings.Count(out, `name: "sa@proj.iam.gserviceaccount.com"`); got != 2 {
		t.Fatalf("expected the subject in the 2 bindings (technical and impersonator), got %d", got)
	}
	for _, want := range []string{"name: bayplan-technical", "name: bayplan-impersonator", "name: bayplan-impersonated-admin", "name: bayplan-impersonated-editor", "name: bayplan-impersonated-readonly"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestClusterAdminMode(t *testing.T) {
	m := RenderManifest(ManifestParams{ServerURL: "https://p", ClusterID: 1, AgentKey: "k", Image: "img", ClusterAdmin: true})
	if !strings.Contains(m, "name: bayplan-cluster-admin") || !strings.Contains(m, "name: bayplan-impersonated-admin-cluster-admin") {
		t.Fatal("missing the bindings to cluster-admin")
	}
	if strings.Contains(m, "bayplan-limit-admin-rbac") || !strings.Contains(m, "bayplan-protect-panel-identities") {
		t.Fatal("cluster-admin mode keeps the identity policies and drops the admin RBAC one")
	}
	// The scoped RBAC is still there: going back is deleting two bindings.
	if !strings.Contains(m, "name: bayplan-impersonated-editor") {
		t.Fatal("cluster-admin mode must not remove the scoped roles")
	}
	if strings.Contains(RenderManifest(ManifestParams{ServerURL: "https://p", ClusterID: 1, AgentKey: "k", Image: "img"}), "name: cluster-admin") {
		t.Fatal("scoped mode must not bind anything to cluster-admin")
	}
	r := RenderRBAC(Subject{Kind: "User", Name: "x@example.com"}, true)
	if !strings.Contains(r, "bayplan-cluster-admin") || !strings.Contains(r, `name: "x@example.com"`) {
		t.Fatal("RenderRBAC cluster-admin")
	}
}

func TestRenderRBACOnly(t *testing.T) {
	y := RenderRBACOnly()
	if strings.Contains(y, "\nkind: ServiceAccount\n") || strings.Contains(y, "kind: Deployment") || strings.Contains(y, "kind: Secret") {
		t.Fatal("the self-applicable RBAC must not carry SA, Secret or Deployment")
	}
	for _, want := range []string{"name: bayplan-agent\n", "name: bayplan-impersonated-admin", "bayplan:editors"} {
		if !strings.Contains(y, want) {
			t.Fatalf("missing %q", want)
		}
	}
	// Neither the self-update role nor cluster-admin travel to the agent.
	if strings.Contains(y, "cluster-admin") || strings.Contains(y, "bayplan-agent-rbac") {
		t.Fatal("the self-applicable RBAC must not carry cluster-admin or bayplan-agent-rbac")
	}
	if strings.Contains(selfUpdateRBAC, `"bayplan-agent-rbac"`) {
		t.Fatal("the agent must not be able to edit its own self-update role")
	}
	// Every document is a ClusterRole or ClusterRoleBinding: the agent only
	// knows how to apply those two kinds.
	for _, doc := range strings.Split(strings.TrimSuffix(y, "\n---\n"), "\n---\n") {
		if !strings.Contains(doc, "kind: ClusterRole\n") && !strings.Contains(doc, "kind: ClusterRoleBinding\n") {
			t.Fatalf("unexpected document: %.60s", doc)
		}
	}
	if len(RBACHash(y)) != 16 {
		t.Fatal("hash")
	}
	// The full manifest carries the self-update role.
	if !strings.Contains(RenderManifest(ManifestParams{ServerURL: "https://p", ClusterID: 1, AgentKey: "k", Image: "i"}), "name: bayplan-agent-rbac") {
		t.Fatal("the manifest must include bayplan-agent-rbac")
	}
}

// The namespace the manifest installs the agent in is the one k8s protects.
func TestAgentNamespaceMatchesProtected(t *testing.T) {
	if agentNamespace != k8s.AgentNamespace {
		t.Fatalf("manifest installs in %q but k8s protects %q", agentNamespace, k8s.AgentNamespace)
	}
}
