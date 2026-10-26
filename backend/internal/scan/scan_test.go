package scan

import "testing"

func TestRegistryHost(t *testing.T) {
	cases := map[string]string{
		"nginx":                                  "docker.io",
		"library/nginx:1.25":                     "docker.io",
		"bitnami/postgresql":                     "docker.io",
		"ghcr.io/org/app:1.2":                    "ghcr.io",
		"harbor.example.com:8443/p/i@sha256:abc": "harbor.example.com:8443",
		"localhost:5000/x":                       "localhost:5000",
		"quay.io/minio/minio":                    "quay.io",
	}
	for in, want := range cases {
		if got := RegistryHost(in); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestValidateImageRef(t *testing.T) {
	for _, ok := range []string{"nginx", "ghcr.io/a/b:1.0", "x@sha256:0123abcd", "registry:5000/a_b-c.d:tag"} {
		if err := validateImageRef(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-f", "nginx; rm -rf /", "a b", "x$(y)", "img\n"} {
		if err := validateImageRef(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
