package registries

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDockerConfigJSONUsesLegacyKeyForDockerHub(t *testing.T) {
	r := Registry{Name: "hub", Server: "docker.io", Username: "u", Password: "p"}
	raw, err := r.DockerConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Auths map[string]struct {
			Username, Password, Auth string
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	a, ok := cfg.Auths["https://index.docker.io/v1/"]
	if !ok {
		t.Fatalf("Docker Hub needs the legacy key; keys: %v", cfg.Auths)
	}
	if a.Auth != "dTpw" {
		t.Fatalf("auth must be base64(user:pass): %q", a.Auth)
	}
	r2 := Registry{Name: "gh", Server: "https://ghcr.io/", Username: "u", Password: "p"}
	if err := r2.Validate(true); err != nil {
		t.Fatal(err)
	}
	if r2.Server != "ghcr.io" {
		t.Fatalf("the server is normalized without scheme or slash: %q", r2.Server)
	}
	raw, _ = r2.DockerConfigJSON()
	if !strings.Contains(string(raw), `"ghcr.io"`) {
		t.Fatalf("got %s", raw)
	}
}

func TestRegistryValidate(t *testing.T) {
	if err := (&Registry{Name: "My Registry", Server: "x", Username: "u", Password: "p"}).Validate(true); err == nil {
		t.Fatal("a name with uppercase/spaces is not valid for a Secret")
	}
	if err := (&Registry{Name: "ok", Server: "x", Username: "u"}).Validate(true); err == nil {
		t.Fatal("missing password")
	}
	if err := (&Registry{Name: "ok", Server: "x", Username: "u"}).Validate(false); err != nil {
		t.Fatalf("editing without changing the password must not require it: %v", err)
	}
}

func TestParseChallenge(t *testing.T) {
	scheme, p := parseChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:x:pull,push"`)
	if scheme != "Bearer" || p["realm"] != "https://auth.docker.io/token" || p["service"] != "registry.docker.io" {
		t.Fatalf("got %q %v", scheme, p)
	}
	if p["scope"] != "repository:x:pull,push" {
		t.Fatalf("a comma inside quotes does not separate parameters: %q", p["scope"])
	}
	scheme, _ = parseChallenge(`Basic realm="harbor"`)
	if scheme != "Basic" {
		t.Fatalf("got %q", scheme)
	}
}

func TestRegistryErrorMessageSanitizes(t *testing.T) {
	if got := registryErrorMessage([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`)); got != "authentication required" {
		t.Fatalf("got %q", got)
	}
	if got := registryErrorMessage([]byte(`{"details":"incorrect username or password"}`)); got != "incorrect username or password" {
		t.Fatalf("got %q", got)
	}
	if got := registryErrorMessage([]byte(`<html>evil</html>`)); got != "" {
		t.Fatalf("a non-JSON body is not reflected: %q", got)
	}
}
