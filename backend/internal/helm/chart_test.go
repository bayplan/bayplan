package helm

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Renders the server's own chart (deploy/helm/bayplan) with the Helm engine,
// in the main configurations, and checks that everything that comes out is
// valid YAML with apiVersion/kind. Without the helm CLI on the machine, it is
// the only check that the chart is not broken before publishing it.
func TestServerChartRenders(t *testing.T) {
	const chartDir = "../../../deploy/helm/bayplan"
	if _, err := os.Stat(chartDir); err != nil {
		t.Skipf("chart not accessible: %v", err)
	}
	ch, err := loader.Load(chartDir)
	if err != nil {
		t.Fatalf("loader.Load: %v", err)
	}

	cases := map[string]map[string]any{
		"default (bundled postgres, no ingress)": {},
		"local cluster in cluster-admin": {
			"localCluster": map[string]any{"enabled": true, "name": "local", "rbacMode": "cluster-admin"},
		},
		"no local cluster": {
			"localCluster": map[string]any{"enabled": false},
		},
		"ingress with Let's Encrypt": {
			"ingress":     map[string]any{"enabled": true, "host": "panel.example.com", "className": "nginx", "tls": map[string]any{"enabled": true, "source": "letsEncrypt"}},
			"letsEncrypt": map[string]any{"email": "ops@example.com"},
		},
		"ingress with self-signed CA and private registry": {
			"ingress":       map[string]any{"enabled": true, "host": "panel.example.com", "tls": map[string]any{"enabled": true, "source": "selfSigned"}},
			"imageRegistry": "registry.example.com/mirror",
		},
		"ingress TLS and external DB": {
			"ingress":             map[string]any{"enabled": true, "host": "panel.example.com", "tls": map[string]any{"enabled": true, "secretName": "tls"}},
			"postgresql":          map[string]any{"enabled": false},
			"externalDatabaseURL": "postgres://u:p@db:5432/bayplan",
			"encryptionKey":       "abc",
		},
		"network policy and a password that needs URL encoding": {
			"networkPolicy": map[string]any{"enabled": true},
			"postgresql":    map[string]any{"password": "p@ss:w/rd?#"},
		},
	}
	for name, vals := range cases {
		t.Run(name, func(t *testing.T) {
			opts := chartutil.ReleaseOptions{Name: "bayplan", Namespace: "bayplan"}
			// A 1.30+ cluster: the admission policies are only rendered when the
			// API server serves ValidatingAdmissionPolicy v1.
			caps := chartutil.DefaultCapabilities.Copy()
			caps.APIVersions = append(caps.APIVersions, "admissionregistration.k8s.io/v1/ValidatingAdmissionPolicy")
			values, err := chartutil.ToRenderValues(ch, vals, opts, caps)
			if err != nil {
				t.Fatalf("ToRenderValues: %v", err)
			}
			out, err := engine.Render(ch, values)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			kinds := 0
			for file, content := range out {
				if strings.HasSuffix(file, "NOTES.txt") || strings.TrimSpace(content) == "" {
					continue
				}
				dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(content)), 4096)
				for {
					var obj map[string]any
					err := dec.Decode(&obj)
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatalf("%s: invalid YAML: %v\n%s", file, err, content)
					}
					if len(obj) == 0 {
						continue
					}
					if obj["apiVersion"] == nil || obj["kind"] == nil {
						t.Fatalf("%s: document without apiVersion/kind:\n%s", file, content)
					}
					kinds++
				}
			}
			if kinds < 4 {
				t.Fatalf("expected at least 4 objects, there are %d", kinds)
			}
			all := ""
			for file, content := range out {
				if !strings.HasSuffix(file, "NOTES.txt") {
					all += content
				}
			}
			localEnabled := true
			adminMode := false
			if lc, ok := vals["localCluster"].(map[string]any); ok {
				localEnabled, _ = lc["enabled"].(bool)
				adminMode = lc["rbacMode"] == "cluster-admin"
			}
			hasRole := strings.Contains(all, "name: bayplan-technical\n")
			hasSA := strings.Contains(all, "kind: ServiceAccount\nmetadata:\n  name: bayplan-technical")
			if localEnabled && (!hasRole || !strings.Contains(all, "name: bayplan-impersonated-admin") || !strings.Contains(all, "LOCAL_CLUSTER")) {
				t.Fatal("with localCluster.enabled the chart must render the panel RBAC and LOCAL_CLUSTER")
			}
			if hasSA {
				t.Fatal("the chart must not create the bayplan-technical SA: the SA is the Deployment's")
			}
			if !localEnabled && (hasRole || strings.Contains(all, "LOCAL_CLUSTER")) {
				t.Fatal("without localCluster the chart must render neither RBAC nor LOCAL_CLUSTER")
			}
			// The technical SA subject of files/rbac.yaml is rewritten by exact
			// string match: if serviceaccount.yaml changes its layout the
			// replace silently does nothing and the bindings point at a
			// kube-system SA that does not exist.
			if localEnabled {
				if strings.Contains(all, "bayplan-technical\n    namespace: kube-system") || strings.Contains(all, "namespace: kube-system") {
					t.Fatal("localCluster: the bindings still point at the kube-system technical SA")
				}
				if got := strings.Count(all, "  - kind: ServiceAccount\n    name: bayplan\n    namespace: bayplan\n"); got < 2 {
					t.Fatalf("localCluster: expected the technical bindings rewritten to the release SA bayplan/bayplan, found %d", got)
				}
			}
			// The bundled Postgres shares name/instance labels with the server:
			// without component: server the Service would also route to it.
			if !strings.Contains(all, "  selector:\n    app.kubernetes.io/name: bayplan\n    app.kubernetes.io/instance: bayplan\n    app.kubernetes.io/component: server\n") {
				t.Fatal("the server Service selector must carry app.kubernetes.io/component: server")
			}
			if !strings.Contains(all, "matchLabels:\n      app.kubernetes.io/name: bayplan\n      app.kubernetes.io/instance: bayplan\n      app.kubernetes.io/component: server\n") {
				t.Fatal("the server Deployment selector must carry app.kubernetes.io/component: server")
			}
			if np, _ := vals["networkPolicy"].(map[string]any); np != nil {
				if !strings.Contains(all, "kind: NetworkPolicy") || !strings.Contains(all, "kubernetes.io/metadata.name: \"ingress-nginx\"") {
					t.Fatal("networkPolicy.enabled: NetworkPolicy for the ingress namespace expected")
				}
				if !strings.Contains(all, "postgres://bayplan:p%40ss%3Aw%2Frd%3F%23@bayplan-db:5432/") {
					t.Fatal("databaseURL: the bundled Postgres password must be URL-encoded")
				}
			} else if strings.Contains(all, "kind: NetworkPolicy") {
				t.Fatal("NetworkPolicy must be opt-in")
			}
			if localEnabled != strings.Contains(all, "name: bayplan-protect-panel-identities") {
				t.Fatalf("panel identity policy: expected %v", localEnabled)
			}
			if (localEnabled && !adminMode) != strings.Contains(all, "name: bayplan-limit-admin-rbac") {
				t.Fatalf("admin RBAC policy: expected only with the local cluster in scoped mode")
			}
			// The own-namespace policy must not use a namespaceSelector: it does
			// not filter cluster-scoped objects and denied every one of them.
			if localEnabled && (!strings.Contains(all, `has(request.namespace) && request.namespace == 'bayplan'`) || strings.Contains(all, "matchConstraints:\n    namespaceSelector")) {
				t.Fatal("own-namespace policy: expected a request.namespace matchCondition and no namespaceSelector")
			}
			if adminMode != strings.Contains(all, "name: bayplan-cluster-admin") {
				t.Fatalf("cluster-admin binding: expected %v", adminMode)
			}
			if !strings.Contains(all, "name: BOOTSTRAP_PASSWORD") || !strings.Contains(all, "name: bayplan-bootstrap") {
				t.Fatal("the bootstrap password Secret and its env var must always be rendered")
			}
			tls, _ := vals["ingress"].(map[string]any)
			source := ""
			if tls != nil {
				source, _ = tls["tls"].(map[string]any)["source"].(string)
			}
			switch source {
			case "letsEncrypt":
				if !strings.Contains(all, "acme-v02.api.letsencrypt.org") || !strings.Contains(all, "cert-manager.io/issuer: bayplan-letsencrypt") || !strings.Contains(all, "ingressClassName: nginx") {
					t.Fatal("letsEncrypt: Issuer, ingress annotation and HTTP-01 class expected")
				}
			case "selfSigned":
				if !strings.Contains(all, "isCA: true") || !strings.Contains(all, "cert-manager.io/issuer: bayplan-ca") || !strings.Contains(all, "AGENT_CA_FILE") || !strings.Contains(all, "secretName: bayplan-ca") {
					t.Fatal("selfSigned: CA chain, ingress annotation and the CA mounted for the agents expected")
				}
			}
			if _, ok := vals["imageRegistry"]; ok {
				for _, img := range []string{"image: registry.example.com/mirror/bayplan/bayplan:", "image: registry.example.com/mirror/library/postgres:", "value: \"registry.example.com/mirror/bayplan/bayplan-agent:"} {
					if !strings.Contains(all, img) {
						t.Fatalf("imageRegistry: expected %q", img)
					}
				}
			}
		})
	}
}

func TestValidateChartName(t *testing.T) {
	ok := []struct {
		name string
		oci  bool
	}{
		{"nginx", false}, {"kube-prometheus-stack", false}, {"my_chart.v2", false},
		{"team/nginx", true}, {"a/b/c", true},
	}
	for _, c := range ok {
		if err := ValidateChartName(c.name, c.oci); err != nil {
			t.Errorf("%q (oci=%v) must be valid: %v", c.name, c.oci, err)
		}
	}
	bad := []struct {
		name string
		oci  bool
	}{
		{"", false}, {"..", false}, {"../etc", false}, {"/etc", false}, {".hidden", false},
		{"a/b", false}, {`a\b`, false}, {"a..b", false}, {" nginx", false}, {"nginx:1.0", false},
		{"../x", true}, {"a//b", true}, {"/a", true}, {"a/", true}, {"a/./b", true}, {"a@sha256", true},
	}
	for _, c := range bad {
		if err := ValidateChartName(c.name, c.oci); err == nil {
			t.Errorf("%q (oci=%v) must be rejected", c.name, c.oci)
		}
	}
}

func TestPruneOld(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old-1.0.0.tgz")
	fresh := filepath.Join(dir, "fresh-1.0.0.tgz")
	index := filepath.Join(dir, "repo-index.yaml")
	for _, p := range []string{old, fresh, index} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-30 * 24 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(index, past, past)
	pruneOld(dir, time.Now().Add(-chartCacheMaxAge))
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an old chart archive must be pruned")
	}
	for _, p := range []string{fresh, index} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s must be kept: %v", p, err)
		}
	}
}

// Artifact Hub lists the images from a Chart.yaml annotation that nothing
// renders: when Dependabot bumps Postgres in values.yaml, the annotation would
// keep advertising (and scanning) the old one.
func TestServerChartArtifactHubImages(t *testing.T) {
	const chartDir = "../../../deploy/helm/bayplan"
	ch, err := loader.Load(chartDir)
	if err != nil {
		t.Skipf("chart not accessible: %v", err)
	}
	pg, _ := ch.Values["postgresql"].(map[string]any)
	img, _ := pg["image"].(string)
	if img == "" {
		t.Fatal("postgresql.image not found in values.yaml")
	}
	if !strings.Contains(ch.Metadata.Annotations["artifacthub.io/images"], "image: "+img+"\n") {
		t.Fatalf("artifacthub.io/images in Chart.yaml does not list %s", img)
	}
}
