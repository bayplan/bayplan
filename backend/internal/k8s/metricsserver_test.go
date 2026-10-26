package k8s

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The embedded manifest is the official one of the pinned release: if someone
// edits it by hand (or brings another version without updating the constant),
// this test says so.
func TestMetricsServerManifestPinned(t *testing.T) {
	if got := MetricsServerManifestSHA256(); got != metricsServerSHA256 {
		t.Fatalf("metrics-server.yaml is not the components.yaml of %s: sha256 %s", MetricsServerVersion, got)
	}
	if !strings.Contains(metricsServerYAML, "metrics-server:"+MetricsServerVersion) {
		t.Fatalf("the manifest image is not %s", MetricsServerVersion)
	}
}

func TestMetricsServerManifestKubeletInsecureTLS(t *testing.T) {
	args := func(objs []*unstructured.Unstructured) []any {
		for _, o := range objs {
			if o.GetKind() == "Deployment" {
				containers, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
				a, _, _ := unstructured.NestedSlice(containers[0].(map[string]any), "args")
				return a
			}
		}
		t.Fatal("no Deployment")
		return nil
	}
	plain, err := MetricsServerManifest(false)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, o := range plain {
		kinds[o.GetKind()] = true
	}
	for _, k := range []string{"ServiceAccount", "ClusterRole", "ClusterRoleBinding", "RoleBinding", "Service", "Deployment", "APIService"} {
		if !kinds[k] {
			t.Errorf("%s missing from the manifest", k)
		}
	}
	for _, a := range args(plain) {
		if a == kubeletInsecureTLSArg {
			t.Fatal("the official manifest must not carry the insecure flag")
		}
	}
	insecure, err := MetricsServerManifest(true)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, a := range args(insecure) {
		if a == kubeletInsecureTLSArg {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("expected the flag once, got %d", found)
	}
	y, err := MetricsServerManifestYAML(true)
	if err != nil || !strings.Contains(y, kubeletInsecureTLSArg) || strings.Count(y, "\n---\n") < 6 {
		t.Fatalf("YAML: %v", err)
	}
}

func TestSplitBindingsToNewRoles(t *testing.T) {
	objs, err := MetricsServerManifest(false)
	if err != nil {
		t.Fatal(err)
	}
	rest, deferred := SplitBindingsToNewRoles(objs)
	// system:metrics-server references a ClusterRole of the same batch; the
	// bindings to system:auth-delegator and extension-apiserver-authentication-reader
	// point to roles that already exist in every cluster.
	if len(deferred) != 1 || deferred[0].GetName() != "system:metrics-server" {
		t.Fatalf("deferred: %+v", deferred)
	}
	if len(rest)+len(deferred) != len(objs) {
		t.Fatal("objects were lost")
	}
}
