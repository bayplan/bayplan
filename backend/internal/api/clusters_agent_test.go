package api

import (
	"strings"
	"testing"
)

func TestManifestCommand(t *testing.T) {
	url := "https://203.0.113.7:8443/api/agent/manifest/1/tok.yaml"
	if got := manifestCommand(url, ""); got != "kubectl apply -f "+url {
		t.Fatalf("public CA: %q", got)
	}
	got := manifestCommand(url, "Q0E=")
	for _, want := range []string{"--cacert <(echo 'Q0E=' | base64 -d) '" + url + "'", "|| curl -fsSL '" + url + "'", "| kubectl apply -f -"} {
		if !strings.Contains(got, want) {
			t.Fatalf("private CA: %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "--insecure") || strings.Contains(got, " -k") {
		t.Fatalf("must never skip verification: %q", got)
	}
}
