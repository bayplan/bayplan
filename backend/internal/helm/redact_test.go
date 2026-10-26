package helm

import (
	"strings"
	"testing"
)

func TestRedactSecretsOnlyTouchesSecrets(t *testing.T) {
	manifest := `# Source: chart/templates/cm.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
data:
  key: visible
---
# Source: chart/templates/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: db
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: '{"stringData":{"token":"plain-token"}}'
type: Opaque
data:
  password: c3VwZXJzZWNyZXQ=
stringData:
  token: plain-token
`
	out, n := RedactSecrets(manifest)
	if n != 1 {
		t.Fatalf("expected 1 redacted secret, got %d", n)
	}
	if strings.Contains(out, "c3VwZXJzZWNyZXQ=") || strings.Contains(out, "plain-token") {
		t.Fatalf("secret values visible:\n%s", out)
	}
	if !strings.Contains(out, "key: visible") || !strings.Contains(out, "# Source: chart/templates/cm.yaml") {
		t.Fatalf("the ConfigMap must stay intact, with comments:\n%s", out)
	}
	if !strings.Contains(out, "name: db") || strings.Count(out, "<redacted>") != 2 {
		t.Fatalf("the secret must still be there with masked values:\n%s", out)
	}
}
