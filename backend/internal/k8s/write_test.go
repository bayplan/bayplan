package k8s

import (
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestParseManifests(t *testing.T) {
	objs, err := ParseManifests("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: b\n  namespace: web\n---\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || objs[0].GetKind() != "ConfigMap" || objs[1].GetNamespace() != "web" {
		t.Fatalf("objs: %+v", objs)
	}
	// JSON works too (the decoder is YAML-or-JSON).
	objs, err = ParseManifests(`{"apiVersion":"v1","kind":"Secret","metadata":{"generateName":"s-"}}`)
	if err != nil || len(objs) != 1 || objs[0].GetGenerateName() != "s-" {
		t.Fatalf("json: %v %+v", err, objs)
	}
}

func TestParseManifestsRejectsNonManifests(t *testing.T) {
	cases := map[string]string{
		"values.yaml":    "replicaCount: 2\nimage:\n  tag: 1.0\n",
		"no name":        "apiVersion: v1\nkind: ConfigMap\nmetadata: {}\n",
		"no kind":        "apiVersion: v1\nmetadata:\n  name: x\n",
		"empty":          "---\n",
		"broken yaml":    "apiVersion: v1\nkind: [\n",
		"top-level list": "- a\n- b\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifests(raw)
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("expected ErrInvalidManifest, got %v", err)
			}
		})
	}
}

func TestParseManifestsLimitsDocuments(t *testing.T) {
	var sb strings.Builder
	for i := 0; i <= maxApplyDocs; i++ {
		sb.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\n")
	}
	if _, err := ParseManifests(sb.String()); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("expected the document cap, got %v", err)
	}
}

func TestKindKeyFor(t *testing.T) {
	if KindKeyFor("apps/v1", "Deployment") != "deployments" || KindKeyFor("v1", "ConfigMap") != KindConfigMaps {
		t.Fatal("KindKeyFor does not resolve known kinds")
	}
	if KindKeyFor("example.com/v1", "Widget") != "" {
		t.Fatal("an unknown CRD must return empty")
	}
}

func TestGuardTarget(t *testing.T) {
	ns := schema.GroupKind{Kind: "Namespace"}
	cases := []struct {
		namespaced bool
		gk         schema.GroupKind
		ns, name   string
		want       string
	}{
		{true, schema.GroupKind{Kind: "Secret"}, "bayplan", "db", "bayplan"},
		{false, ns, "", "bayplan", "bayplan"},
		{false, ns, "bayplan", "other", "other"},
		{false, schema.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}, "bayplan", "x", ""},
		{false, schema.GroupKind{Group: "example.com", Kind: "Namespace"}, "", "bayplan", ""},
	}
	for _, c := range cases {
		if got := guardTarget(c.namespaced, c.gk, c.ns, c.name); got != c.want {
			t.Errorf("%+v: got %q want %q", c, got, c.want)
		}
	}
}
