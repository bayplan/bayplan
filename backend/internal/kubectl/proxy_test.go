package kubectl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

// The local proxy has to (1) require the session token, (2) strip it and put
// the real cluster credential and the impersonation, and (3) serve the real
// kubectl binary if it is installed.
func TestSessionProxyInjectsClusterIdentity(t *testing.T) {
	var gotAuth, gotUser string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUser = r.Header.Get("Impersonate-User")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
	}))
	defer upstream.Close()

	cfg := &rest.Config{Host: upstream.URL, BearerToken: "cluster-token"}
	cfg.Impersonate.UserName = "bayplan:alice"
	cert, certPEM, err := selfSignedLoopbackCert()
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{path: "/bin/true", cert: cert, certPEM: certPEM}
	s, err := r.Open(context.Background(), cfg, "default", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	base := "https://" + s.listener.Addr().String()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := client.Get(base + "/api")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without a session token it must be 403, got %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/api", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with a session token it must pass, got %d", resp.StatusCode)
	}
	if gotAuth != "Bearer cluster-token" || gotUser != "bayplan:alice" {
		t.Fatalf("the upstream must see the cluster credential and the impersonation: %q %q", gotAuth, gotUser)
	}

	kubectlPath, err := exec.LookPath("kubectl")
	if err != nil {
		t.Skip("kubectl not installed")
	}
	out, err := exec.Command(kubectlPath, "--kubeconfig", filepath.Join(s.dir, "kubeconfig"), "get", "--raw", "/api").CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl against the proxy: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "APIVersions") {
		t.Fatalf("unexpected response: %s", out)
	}
}

// With a protected namespace the proxy refuses what the line validator cannot
// see (e.g. `get -f -` with a manifest in that namespace) before it reaches
// the API server.
func TestSessionProxyBlocksProtectedNamespace(t *testing.T) {
	hits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	cert, certPEM, err := selfSignedLoopbackCert()
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{path: "/bin/true", cert: cert, certPEM: certPEM}
	s, err := r.Open(context.Background(), &rest.Config{Host: upstream.URL}, "default", "bayplan")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	for path, want := range map[string]int{
		"/api/v1/namespaces/bayplan/secrets": http.StatusForbidden,
		"/api/v1/namespaces/default/secrets": http.StatusOK,
	} {
		req, _ := http.NewRequest(http.MethodGet, "https://"+s.listener.Addr().String()+path, nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: got %d want %d", path, resp.StatusCode, want)
		}
	}
	if hits != 1 {
		t.Fatalf("only the allowed request must reach the upstream, got %d", hits)
	}
}
