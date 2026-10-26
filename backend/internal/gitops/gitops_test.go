package gitops

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bayplan/bayplan/internal/k8s"
	"golang.org/x/crypto/ssh"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const cmYAML = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\ndata:\n  k: v\n"

func TestLoadManifestsIgnoresNonManifestsAndHiddenDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "deploy", "b.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: b\n")
	writeFile(t, filepath.Join(root, "deploy", "a.yml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n")
	writeFile(t, filepath.Join(root, "deploy", "values.yaml"), "replicaCount: 2\nimage:\n  tag: 1.0\n")
	writeFile(t, filepath.Join(root, "deploy", ".hidden.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: hidden\n")
	writeFile(t, filepath.Join(root, "deploy", ".git", "x.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: git\n")
	writeFile(t, filepath.Join(root, "deploy", "README.md"), "# nothing")

	objs, err := loadManifests(root, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range objs {
		names = append(names, o.GetName())
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("expected [a b] (order by name, without values or hidden files), got %v", names)
	}
}

func TestLoadManifestsRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := loadManifests(root, "../etc"); err == nil {
		t.Fatal("expected an error for escaping the repository")
	}
}

func TestLabelObjectsAndPruneCandidates(t *testing.T) {
	objs, err := k8s.ParseManifests("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n  labels:\n    app: x\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: s\n  namespace: web\n")
	if err != nil {
		t.Fatal(err)
	}
	labelObjects(objs, 7)
	if objs[0].GetLabels()[SourceLabel] != "7" || objs[0].GetLabels()["app"] != "x" {
		t.Fatalf("label set wrong: %v", objs[0].GetLabels())
	}

	existing := []k8s.ObjectRef{
		{KindKey: "configmaps", Kind: "ConfigMap", Namespace: "default", Name: "a"},
		{KindKey: "configmaps", Kind: "ConfigMap", Namespace: "default", Name: "old"},
		{KindKey: "services", Kind: "Service", Namespace: "web", Name: "s"},
		{KindKey: "deployments", Kind: "Deployment", Namespace: "web", Name: "gone"},
	}
	applied := []k8s.ApplyResult{
		{Kind: "ConfigMap", Namespace: "default", Name: "a"},
		{Kind: "Service", Namespace: "web", Name: "s"},
	}
	got := PruneCandidates(existing, applied)
	if len(got) != 2 || got[0].Name != "old" || got[1].Name != "gone" {
		t.Fatalf("expected [old gone], got %+v", got)
	}
}

func TestBuildKustomize(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "k", "kustomization.yaml"), "namespace: apps\nresources:\n- cm.yaml\ncommonLabels:\n  team: core\n")
	writeFile(t, filepath.Join(root, "k", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\ndata:\n  k: v\n")
	objs, err := buildKustomize(root, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].GetNamespace() != "apps" || objs[0].GetLabels()["team"] != "core" {
		t.Fatalf("kustomize did not apply namespace/labels: %+v", objs)
	}
	// A reference outside the root directory is rejected (LoadRestrictionsRootOnly).
	writeFile(t, filepath.Join(root, "outside.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: out\n")
	writeFile(t, filepath.Join(root, "k2", "kustomization.yaml"), "resources:\n- ../outside.yaml\n")
	if _, err := buildKustomize(root, "k2"); err == nil {
		t.Fatal("expected an error for a resource outside the kustomize root")
	}
}

func TestSSHAuthRequiresKeyAndKnownHosts(t *testing.T) {
	dir := t.TempDir()
	if _, err := sshAuth("git", "not a key", "github.com ssh-ed25519 AAAA", dir); err == nil {
		t.Fatal("expected an error with an invalid key")
	}
	key := genTestKey(t)
	if _, err := sshAuth("git", key, "", dir); err == nil || !strings.Contains(err.Error(), "known_hosts") {
		t.Fatalf("without known_hosts it must fail: %v", err)
	}
	if _, err := sshAuth("git", key, "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl", dir); err != nil {
		t.Fatalf("with a valid known_hosts it must build: %v", err)
	}
}

func genTestKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func TestRejectLinkLocal(t *testing.T) {
	for _, bad := range []string{"http://169.254.169.254/latest", "https://[fe80::1]/repo.git", "git@169.254.169.254:org/repo.git", "ssh://git@169.254.1.1:22/x"} {
		if err := RejectLinkLocal(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	for _, ok := range []string{"https://github.com/org/repo.git", "git@github.com:org/repo.git", "http://10.0.0.5/repo.git"} {
		if err := RejectLinkLocal(ok); err != nil {
			t.Errorf("%q should not be rejected: %v", ok, err)
		}
	}
}

func TestBuildKustomizeRejectsRemoteResources(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "k", "kustomization.yaml"), "resources:\n- https://github.com/org/repo//base?ref=v1\n")
	if _, err := buildKustomize(root, "k"); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("expected rejection of a remote resource, got %v", err)
	}
	writeFile(t, filepath.Join(root, "k2", "kustomization.yaml"), "resources:\n- github.com/org/repo/base\n")
	if _, err := buildKustomize(root, "k2"); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("expected rejection of a nonexistent path (git), got %v", err)
	}
	// Nested local base: valid.
	writeFile(t, filepath.Join(root, "k3", "base", "kustomization.yaml"), "resources:\n- cm.yaml\n")
	writeFile(t, filepath.Join(root, "k3", "base", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n")
	writeFile(t, filepath.Join(root, "k3", "kustomization.yaml"), "resources:\n- base\nnamePrefix: dev-\n")
	objs, err := buildKustomize(root, "k3")
	if err != nil || len(objs) != 1 || objs[0].GetName() != "dev-cfg" {
		t.Fatalf("local base: %v %+v", err, objs)
	}
}

func TestFindRemoteRefAnywhere(t *testing.T) {
	root := t.TempDir()
	// Remote reference outside resources: helmCharts.repo.
	writeFile(t, filepath.Join(root, "h", "kustomization.yaml"), "helmCharts:\n- name: nginx\n  repo: https://charts.bitnami.com/bitnami\n")
	if _, err := buildKustomize(root, "h"); err == nil || !strings.Contains(err.Error(), "remote references") {
		t.Fatalf("helmCharts.repo should be rejected: %v", err)
	}
	// Scheme-less shortcut that kustomize treats as git.
	writeFile(t, filepath.Join(root, "g", "kustomization.yaml"), "resources:\n- github.com/org/repo/base\n")
	if _, err := buildKustomize(root, "g"); err == nil {
		t.Fatal("github.com/… should be rejected")
	}
	for _, ok := range []string{"base", "../base", "cm.yaml", "v1.2.3", "app.kubernetes.io/name"} {
		if looksRemote(ok) {
			t.Errorf("%q is not remote", ok)
		}
	}
}

func TestLoadManifestsIgnoresSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.yaml")
	writeFile(t, outside, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: leaked\n")
	writeFile(t, filepath.Join(root, "d", "ok.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ok\n")
	if err := os.Symlink(outside, filepath.Join(root, "d", "link.yaml")); err != nil {
		t.Skip("no symlinks")
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "d", "linkdir")); err != nil {
		t.Skip("no symlinks")
	}
	objs, err := loadManifests(root, "d")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs[0].GetName() != "ok" {
		t.Fatalf("a symlink must not be read: %+v", objs)
	}
	// A path that is a symlink pointing outside the clone is rejected.
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "escape")); err == nil {
		if _, err := loadManifests(root, "escape"); err == nil {
			t.Fatal("expected rejection of a path linked outside the repo")
		}
	}
}

func TestBuildKustomizeRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "k", "kustomization.yaml"), "configMapGenerator:\n- name: c\n  files:\n  - env.txt\n")
	if err := os.Symlink("/etc/hostname", filepath.Join(root, "k", "env.txt")); err != nil {
		t.Skip("no symlinks")
	}
	if _, err := buildKustomize(root, "k"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected rejection for a symlink, got %v", err)
	}
}
