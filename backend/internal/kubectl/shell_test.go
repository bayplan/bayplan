package kubectl

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestValidateAllowsNormalCommands(t *testing.T) {
	for _, line := range []string{
		"kubectl get pods -A",
		`k get pod -o jsonpath='{.items[*].metadata.name}'`,
		"kubectl logs -f deploy/api -n app --tail=100",
		"kubectl exec -it api-0 -- sh -c 'cat /etc/passwd'",
		"kubectl apply -f -",
		"kubectl create configmap x --from-file=key=-",
		"kubectl get pods -ojson",
		"kubectl get pods -owide -nkube-system",
		"kubectl apply -Rf -",
		"kubectl exec -it api-0 -c app -- sh",
		"kubectl exec -itc app api-0 -- sh",
		"kubectl describe node worker-1",
		"kubectl rollout restart deploy/api",
	} {
		if _, err := Validate(line); err != nil {
			t.Errorf("%q should be allowed: %v", line, err)
		}
	}
}

func TestValidateBlocksServerSideEscapes(t *testing.T) {
	for _, line := range []string{
		"ls",
		"sh",
		"kubectl get pods | grep api",
		"kubectl get pods > /tmp/x",
		"kubectl get pods; rm -rf /",
		"kubectl apply -f /etc/passwd",
		"kubectl apply -f=./x.yaml",
		"kubectl create configmap x --from-file=/proc/self/environ",
		"kubectl create secret generic s --from-env-file=.env",
		"kubectl get pods --kubeconfig /root/.kube/config",
		"kubectl get pods -s https://evil",
		"kubectl get pods --as=system:admin",
		"kubectl get pods --token=abc",
		"kubectl port-forward svc/db 5432",
		"kubectl proxy",
		"kubectl cp pod:/etc/passwd ./passwd",
		"kubectl edit deploy/api",
		"kubectl config view",
		"kubectl get pods -o go-template-file=/etc/passwd",
		"kubectl get pods --output=jsonpath-file=/etc/hostname",
		"kubectl get pods --profile=cpu --profile-output=/tmp/p",
		"kubectl get $(cat /etc/passwd)",
		"kubectl -n default cp pod:/etc/passwd ./x",
		"kubectl apply -Rf /etc/passwd",
		"kubectl apply -Rf/etc/passwd",
		"kubectl apply -Rf=/etc/passwd",
		"kubectl get pods -As https://evil",
		"kubectl debug node/x -it --image=busybox --custom=/etc/passwd",
		"kubectl -n default get pods",
		"kubectl -v=9 proxy",
		"kubectl krew install x",
		"kubectl convert -f -",
		"kubectl apply -f/etc/passwd",
		"kubectl apply -f=/etc/passwd",
		"kubectl get pods -ojsonpath-file=/etc/passwd",
		"kubectl get pods -ocustom-columns-file=/etc/passwd",
		"kubectl apply -k/etc",
		"kubectl kustomize /etc",
		"kubectl apply -f https://example.com/x.yaml",
		"kubectl apply -f http://169.254.169.254/latest/meta-data",
		"kubectl cluster-info dump --output-directory=/tmp/x",
		"kubectl create secret tls t --cert=/etc/ssl/cert.pem --key=/etc/ssl/key.pem",
	} {
		_, err := Validate(line)
		if err == nil {
			t.Errorf("%q should be rejected", line)
			continue
		}
		if !errors.Is(err, ErrNotAllowed) && !strings.Contains(err.Error(), "quotes") {
			t.Errorf("%q: unexpected error %v", line, err)
		}
	}
}

func TestSplitWords(t *testing.T) {
	w, err := splitWords(`kubectl exec -it pod -- sh -c "echo 'a b'" x\ y`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kubectl", "exec", "-it", "pod", "--", "sh", "-c", "echo 'a b'", "x y"}
	if strings.Join(w, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v", w)
	}
	if _, err := splitWords(`kubectl get "pods`); err == nil {
		t.Fatal("unclosed quotes must fail")
	}
	if _, err := Validate("   "); !IsNoCommand(err) {
		t.Fatalf("empty line: %v", err)
	}
}

func TestParseFlagGroups(t *testing.T) {
	cases := []struct {
		in, subcmd string
		want       string
	}{
		{"-f", "apply", "-f||false"},
		{"-f/etc/passwd", "apply", "-f|/etc/passwd|true"},
		{"-Rf/etc/passwd", "apply", "-R||false -f|/etc/passwd|true"},
		{"-Rf", "apply", "-R||false -f||false"},
		{"-itc", "exec", "-i||false -t||false -c||false"},
		{"-it", "exec", "-i||false -t||false"},
		{"-owide", "get", "-o|wide|true"},
		{"-A=true", "get", "-A|true|true"},
		{"-RA=true", "get", "-R||false -A|true|true"},
		{"-n=x", "get", "-n|x|true"},
		{"-fn", "logs", "-f||false -n||false"},
		{"--filename=/x", "apply", "--filename|/x|true"},
		{"pods", "get", ""},
	}
	for _, c := range cases {
		var got []string
		for _, f := range parseFlag(c.in, c.subcmd) {
			got = append(got, fmt.Sprintf("%s|%s|%v", f.name, f.value, f.hasValue))
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%q: got %q want %q", c.in, strings.Join(got, " "), c.want)
		}
	}
}

func TestValidateProtectedNamespace(t *testing.T) {
	for _, bad := range []string{
		"kubectl get secrets -n bayplan", "kubectl get secrets --namespace=bayplan", "kubectl get secrets --namespace bayplan",
		"kubectl get secrets -nbayplan", "kubectl get secrets -n=bayplan", "kubectl get secrets -A", "kubectl get pods --all-namespaces",
		"kubectl get pods --all-namespaces=true",
		// boolean short flags with =value and short groups
		"kubectl get secrets -A=true", "kubectl get secrets -RA=true", "kubectl get secrets -RA", "kubectl get secrets -wA",
		// "--" is the VALUE of a value-taking flag with nothing attached
		"kubectl get secrets -L -- -n bayplan", "kubectl get secrets -L -- -A", "kubectl get secrets -l -- -A",
		"kubectl get secrets --selector -- -n bayplan", "kubectl get secrets -o -- -A",
		// the logs -f (follow) boolean must not hide the following letters
		"kubectl logs -fn bayplan deploy/bayplan",
		// the Namespace object itself
		"kubectl delete ns bayplan", "kubectl delete namespace bayplan", "kubectl delete ns/bayplan",
		"kubectl delete namespaces,pods bayplan", "kubectl label ns bayplan a=b", "kubectl annotate namespace/bayplan a=b",
		"kubectl patch ns bayplan -p {}",
		// raw paths
		"kubectl get --raw /api/v1/namespaces/bayplan/secrets", "kubectl get --raw=/api/v1/secrets",
	} {
		if _, err := ValidateProtected(bad, "bayplan", "default"); err == nil {
			t.Errorf("%q should be rejected with a protected namespace", bad)
		}
	}
	for _, ok := range []string{
		"kubectl get secrets -n default", "kubectl get ns", "kubectl get ns bayplan", "kubectl describe ns bayplan",
		"kubectl delete ns other", "kubectl exec -it api-0 -c app -- ls -A", "kubectl exec -it api-0 -- sh -c 'ls -n bayplan'",
		"kubectl logs -f deploy/api -n app", "kubectl get pods -L app -n app",
	} {
		if _, err := ValidateProtected(ok, "bayplan", "default"); err != nil {
			t.Errorf("%q must pass: %v", ok, err)
		}
	}
	if _, err := ValidateProtected("kubectl get pods", "bayplan", "bayplan"); err == nil {
		t.Error("a session whose context namespace is the protected one must be rejected")
	}
	if _, err := ValidateProtected("kubectl get pods -A", "", ""); err != nil {
		t.Fatalf("without a protected namespace -A is valid: %v", err)
	}
	if _, err := Validate("kubectl get --raw /api"); err == nil {
		t.Error("--raw is always rejected")
	}
}

func TestProtectedPathBlocked(t *testing.T) {
	cases := []struct {
		method, raw string
		blocked     bool
	}{
		{"GET", "/api/v1/namespaces/bayplan/secrets", true},
		{"GET", "/api/v1/namespaces/bayplan/secrets/db", true},
		{"GET", "/api/v1/watch/namespaces/bayplan/secrets", true},
		{"GET", "/apis/apps/v1/namespaces/bayplan/deployments", true},
		{"POST", "/api/v1/namespaces/bayplan/pods/x/exec", true},
		{"DELETE", "/api/v1/namespaces/bayplan", true},
		{"PATCH", "/api/v1/namespaces/bayplan", true},
		{"GET", "/api/v1/namespaces/bayplan", false},
		{"GET", "/api/v1/namespaces", false},
		{"GET", "/api/v1/namespaces/default/secrets", false},
		{"GET", "/apis/apps/v1/namespaces/default/deployments/", false},
		{"GET", "/api/v1/namespaces/default/../bayplan/secrets", true},
		{"GET", "/api/v1//namespaces/bayplan/secrets", true},
		{"GET", "/api/v1/namespaces/bay%70lan/secrets", true},
		{"GET", "/api/v1/namespaces%2Fbayplan%2Fsecrets", true},
		{"GET", "/api", false},
	}
	for _, c := range cases {
		u, err := url.Parse("https://127.0.0.1" + c.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := protectedPathBlocked(c.method, u, "bayplan"); got != c.blocked {
			t.Errorf("%s %s: blocked=%v want %v", c.method, c.raw, got, c.blocked)
		}
	}
	u, _ := url.Parse("https://127.0.0.1/api/v1/namespaces/bayplan/secrets")
	if protectedPathBlocked("GET", u, "") {
		t.Error("without a protected namespace nothing is blocked")
	}
}
