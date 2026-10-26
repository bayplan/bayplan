package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bayplan/bayplan/internal/agent"
)

// The syncer does one server-side apply per document and rejects anything
// that is not a ClusterRole/ClusterRoleBinding: it is not a channel for
// applying arbitrary manifests from the server.
func TestRBACApply(t *testing.T) {
	var calls []string
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.Header.Get("Content-Type") != "application/apply-patch+yaml" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected request: %s %s %s", r.Method, r.Header.Get("Content-Type"), r.Header.Get("Authorization"))
		}
		calls = append(calls, r.URL.Path+"?"+r.URL.RawQuery)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "sneaky") {
			t.Errorf("unknown field reached the API server: %s", body)
		}
		if strings.Contains(r.URL.Path, "/bayplan-impersonated-readonly") {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"clusterroles.rbac.authorization.k8s.io \"forbidden\" is forbidden"}`))
			return
		}
		w.WriteHeader(200)
	}))
	defer api.Close()
	s := &rbacSyncer{apiHostPort: strings.TrimPrefix(api.URL, "https://"), kube: api.Client(), token: func() (string, error) { return "tok", nil }, logger: slog.Default()}

	ok := "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-agent\n  labels: {sneaky: value}\nrules: []\n---\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: bayplan-agent\nroleRef:\n  kind: ClusterRole\n  name: bayplan-agent\n  apiGroup: rbac.authorization.k8s.io\n"
	if err := s.apply(context.Background(), []byte(ok)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "/apis/rbac.authorization.k8s.io/v1/clusterroles/bayplan-agent?fieldManager=bayplan-agent&force=true") || !strings.HasPrefix(calls[1], "/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/bayplan-agent?") {
		t.Fatalf("calls: %v", calls)
	}

	// An empty role with an aggregationRule would turn into cluster-admin once
	// the aggregation controller copies the rules in, and the API server allows
	// it because the agent holds `escalate` on these names: refused before any
	// call, and so is anything the validator does not model.
	aggregated := "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-agent\nrules: []\naggregationRule:\n  clusterRoleSelectors:\n  - matchExpressions:\n    - {key: nope, operator: DoesNotExist}\n"
	calls = nil
	if err := s.apply(context.Background(), []byte(aggregated)); err == nil || !strings.Contains(err.Error(), "aggregationRule") {
		t.Fatalf("expected aggregationRule rejection, got %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("nothing should be applied, got %v", calls)
	}

	// A 403 is returned with the API server's message.
	bad := "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-impersonated-readonly\nrules: []\n"
	if err := s.apply(context.Background(), []byte(bad)); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403, got %v", err)
	}

	// Outside the allowlist it is rejected without calling the API server, and
	// a pass with one bad document does not apply the good ones either.
	calls = nil
	for name, doc := range map[string]string{
		"secret":                           "apiVersion: v1\nkind: Secret\nmetadata:\n  name: x\n",
		"deployment":                       "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: x\n",
		"role":                             "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: x\n",
		"foreign name":                     "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: cluster-admin\nrules: []\n",
		"own role":                         "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-agent-rbac\nrules: []\n",
		"wildcard":                         "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-agent\nrules:\n- apiGroups: [\"*\"]\n  resources: [\"*\"]\n  verbs: [\"*\"]\n",
		"foreign group":                    "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-agent\nrules:\n- apiGroups: [\"certificates.k8s.io\"]\n  resources: [\"certificatesigningrequests/approval\"]\n  verbs: [\"update\"]\n",
		"escalate verb":                    "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-agent\nrules:\n- apiGroups: [\"rbac.authorization.k8s.io\"]\n  resources: [\"clusterroles\"]\n  verbs: [\"escalate\"]\n",
		"nonResourceURLs":                  "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-agent\nrules:\n- nonResourceURLs: [\"/*\"]\n  verbs: [\"get\"]\n",
		"bind to cluster-admin":            "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: bayplan-agent\nroleRef:\n  kind: ClusterRole\n  name: cluster-admin\n  apiGroup: rbac.authorization.k8s.io\nsubjects:\n- kind: ServiceAccount\n  name: bayplan-agent\n  namespace: bayplan-agent\n",
		"impersonate groups without names": "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-impersonator\nrules:\n- apiGroups: [\"\"]\n  resources: [\"groups\"]\n  verbs: [\"impersonate\"]\n",
		"impersonate system:masters":       "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-impersonator\nrules:\n- apiGroups: [\"\"]\n  resources: [\"groups\"]\n  resourceNames: [\"system:masters\"]\n  verbs: [\"impersonate\"]\n",
		"impersonate serviceaccounts":      "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-impersonator\nrules:\n- apiGroups: [\"\"]\n  resources: [\"serviceaccounts\"]\n  verbs: [\"impersonate\"]\n",
		"sensitive resource":               "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-agent\nrules:\n- apiGroups: [\"\"]\n  resources: [\"serviceaccounts/token\"]\n  verbs: [\"create\"]\n",
		"extra verb":                       "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-agent\nrules:\n- apiGroups: [\"\"]\n  resources: [\"nodes\"]\n  verbs: [\"delete\"]\n",
		"rbac patch without names":         "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: bayplan-impersonated-admin\nrules:\n- apiGroups: [\"rbac.authorization.k8s.io\"]\n  resources: [\"clusterrolebindings\"]\n  verbs: [\"patch\"]\n",
		"foreign subject":                  "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: bayplan-impersonated-admin\nroleRef:\n  kind: ClusterRole\n  name: bayplan-impersonated-admin\n  apiGroup: rbac.authorization.k8s.io\nsubjects:\n- kind: User\n  name: mallory\n",
	} {
		combined := ok + "---\n" + doc
		if err := s.apply(context.Background(), []byte(combined)); err == nil || !strings.Contains(err.Error(), "rejected") {
			t.Errorf("%s should be rejected: %v", name, err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("must not call the API server with rejected documents: %v", calls)
	}
}

// What the server actually sends passes the allowlist: if someone extends the
// panel's RBAC with a new group or verb, this test tells them the agent will
// reject it until the list is extended too.
func TestRealRBACPassesAllowlist(t *testing.T) {
	var applied int
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { applied++; w.WriteHeader(200) }))
	defer api.Close()
	s := &rbacSyncer{apiHostPort: strings.TrimPrefix(api.URL, "https://"), kube: api.Client(), token: func() (string, error) { return "tok", nil }, logger: slog.Default()}
	if err := s.apply(context.Background(), []byte(agent.RenderRBACOnly())); err != nil {
		t.Fatal(err)
	}
	if applied < 8 {
		t.Fatalf("expected to apply the panel's roles and bindings, applied %d", applied)
	}
}

func TestUpgradeTarget(t *testing.T) {
	cur := "ghcr.io/bayplan/bayplan-agent:1.2.0"
	if _, ok, err := upgradeTarget(cur, cur); ok || err != nil {
		t.Fatal("same image: nothing to do")
	}
	if img, ok, err := upgradeTarget(cur, "ghcr.io/bayplan/bayplan-agent:1.3.0"); !ok || err != nil || img != "ghcr.io/bayplan/bayplan-agent:1.3.0" {
		t.Fatalf("another tag of the same repo must be accepted: %v %v %q", ok, err, img)
	}
	for _, bad := range []string{"docker.io/evil/agent:1.3.0", "ghcr.io/bayplan/bayplan-agent@sha256:abcd", "ghcr.io/bayplan/bayplan-agent:bad tag", "ghcr.io/bayplan/bayplan-agent-evil:1.0"} {
		if _, ok, err := upgradeTarget(cur, bad); ok || err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if _, ok, _ := upgradeTarget("", "ghcr.io/bayplan/bayplan-agent:1.3.0"); ok {
		t.Fatal("without BAYPLAN_IMAGE there is no self-update")
	}
	// With a port in the registry, the last ':' is still the tag.
	if img, ok, err := upgradeTarget("registry.local:5000/bayplan-agent:1.0", "registry.local:5000/bayplan-agent:1.1"); !ok || err != nil || img == "" {
		t.Fatalf("registry with port: %v %v", ok, err)
	}
}

// The self-upgrade must move BAYPLAN_IMAGE along with the image, or the new
// pod keeps comparing against the old one and re-patches on every sync.
func TestUpgradePatchUpdatesEnv(t *testing.T) {
	var p struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `json:"name"`
						Image string `json:"image"`
						Env   []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	img := "ghcr.io/bayplan/bayplan-agent:1.3.0"
	if err := json.Unmarshal(upgradePatch(img), &p); err != nil {
		t.Fatal(err)
	}
	cs := p.Spec.Template.Spec.Containers
	if len(cs) != 1 || cs[0].Name != "agent" || cs[0].Image != img {
		t.Fatalf("unexpected containers: %+v", cs)
	}
	if len(cs[0].Env) != 1 || cs[0].Env[0].Name != "BAYPLAN_IMAGE" || cs[0].Env[0].Value != img {
		t.Fatalf("BAYPLAN_IMAGE must follow the image: %+v", cs[0].Env)
	}
}
