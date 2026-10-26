package k8s

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// metrics-server.yaml is the OFFICIAL components.yaml of kubernetes-sigs/metrics-server
// (release MetricsServerVersion), untouched: what the operator would install with
// `kubectl apply -f`. It is embedded so an installation without internet access
// can install it from the panel, and the pinned checksum detects someone editing
// it by hand instead of bringing a new release (TestMetricsServerManifestPinned).
//
//go:embed metrics-server.yaml
var metricsServerYAML string

const (
	MetricsServerVersion = "v0.9.0"
	metricsServerSHA256  = "1cec29a5267809306a2c6ec74a3e449abbb705b4a8beed0c8a1963910f72c79b"
	// kubeletInsecureTLSArg is the flag needed when the kubelets serve with a
	// self-signed certificate (kubeadm, labs, kind): without it metrics-server
	// starts but collects nothing ("x509: cannot validate certificate"). It is
	// the number one tweak in the project's issues.
	kubeletInsecureTLSArg = "--kubelet-insecure-tls"
)

// MetricsServerManifestSHA256 returns the checksum of the embedded manifest.
func MetricsServerManifestSHA256() string {
	sum := sha256.Sum256([]byte(metricsServerYAML))
	return hex.EncodeToString(sum[:])
}

// MetricsServerManifest returns the objects of the official manifest, with the
// insecure kubelet flag added to the Deployment if requested. Pure.
func MetricsServerManifest(kubeletInsecureTLS bool) ([]*unstructured.Unstructured, error) {
	objs, err := ParseManifests(metricsServerYAML)
	if err != nil {
		return nil, fmt.Errorf("invalid embedded metrics-server manifest: %w", err)
	}
	if !kubeletInsecureTLS {
		return objs, nil
	}
	for _, o := range objs {
		if o.GetKind() != "Deployment" || o.GetName() != "metrics-server" {
			continue
		}
		containers, found, err := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
		if err != nil || !found || len(containers) == 0 {
			return nil, fmt.Errorf("metrics-server manifest: the Deployment has no containers")
		}
		c, ok := containers[0].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("metrics-server manifest: unexpected container")
		}
		args, _, _ := unstructured.NestedSlice(c, "args")
		has := false
		for _, a := range args {
			if a == kubeletInsecureTLSArg {
				has = true
			}
		}
		if !has {
			args = append(args, kubeletInsecureTLSArg)
		}
		if err := unstructured.SetNestedSlice(c, args, "args"); err != nil {
			return nil, err
		}
		containers[0] = c
		if err := unstructured.SetNestedSlice(o.Object, containers, "spec", "template", "spec", "containers"); err != nil {
			return nil, err
		}
	}
	return objs, nil
}

// MetricsServerManifestYAML returns the manifest as text, to download it and
// apply it with kubectl when the panel has no permission to apply it.
func MetricsServerManifestYAML(kubeletInsecureTLS bool) (string, error) {
	objs, err := MetricsServerManifest(kubeletInsecureTLS)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, o := range objs {
		raw, err := yaml.Marshal(o.Object)
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString("---\n")
		}
		b.Write(raw)
	}
	return b.String(), nil
}

// SplitBindingsToNewRoles separates the (Cluster)RoleBindings whose roleRef
// points to a (Cluster)Role that comes in the same batch. They cannot be
// rehearsed in a dry-run: the RBAC escalation check loads the referenced role
// and in dry-run it does not exist yet. Pure; returns the rest in order and the
// set-aside ones.
func SplitBindingsToNewRoles(objs []*unstructured.Unstructured) (rest, deferred []*unstructured.Unstructured) {
	newRoles := map[string]bool{}
	for _, o := range objs {
		if o.GetKind() == "ClusterRole" || o.GetKind() == "Role" {
			newRoles[o.GetKind()+"/"+o.GetName()] = true
		}
	}
	for _, o := range objs {
		kind := o.GetKind()
		if kind == "ClusterRoleBinding" || kind == "RoleBinding" {
			refKind, _, _ := unstructured.NestedString(o.Object, "roleRef", "kind")
			refName, _, _ := unstructured.NestedString(o.Object, "roleRef", "name")
			if newRoles[refKind+"/"+refName] {
				deferred = append(deferred, o)
				continue
			}
		}
		rest = append(rest, o)
	}
	return rest, deferred
}
