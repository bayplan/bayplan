// Package registries distributes private image registry credentials to the
// clusters: for each binding (registry × cluster × namespace) it creates a
// kubernetes.io/dockerconfigjson Secret and, optionally, adds it to the
// namespace's `default` ServiceAccount. It is one
// platform-level credential instead of a `kubectl create secret
// docker-registry` per namespace and per cluster, and rotating it rotates all
// of its copies.
package registries

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	nameRE      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?$`)
	namespaceRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Registry is an image_registries row. Password is only filled in when
// loading with Store.Get.
type Registry struct {
	ID              int64
	Name            string
	Server          string
	Username        string
	Password        string
	Email           string
	InsecureSkipTLS bool
	BindingCount    int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Validate checks consistency before calling the registry.
func (r *Registry) Validate(requirePassword bool) error {
	r.Name = strings.TrimSpace(r.Name)
	r.Server = normalizeServer(r.Server)
	r.Username = strings.TrimSpace(r.Username)
	r.Email = strings.TrimSpace(r.Email)
	if !nameRE.MatchString(r.Name) {
		return errors.New("the name must be a lowercase DNS label (a-z, 0-9, hyphens; max. 42): it goes into the Secret name")
	}
	if r.Server == "" {
		return errors.New("missing registry server (e.g. ghcr.io, docker.io, harbor.example.com)")
	}
	if r.Username == "" {
		return errors.New("missing username")
	}
	if requirePassword && r.Password == "" {
		return errors.New("missing password or token")
	}
	return nil
}

// normalizeServer cleans the host: no scheme and no trailing slash.
// "docker.io" and "index.docker.io" are left as is; the special key Docker Hub
// needs in config.json is set by dockerConfigKey.
func normalizeServer(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimRight(s, "/")
}

// dockerConfigKey is the server key inside .dockerconfigjson. Docker Hub is
// the only one that requires its legacy URL: with "docker.io" the kubelet does
// not find the credential.
func dockerConfigKey(server string) string {
	switch server {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return "https://index.docker.io/v1/"
	}
	return server
}

// DockerConfigJSON generates the .dockerconfigjson content for the Secret.
func (r *Registry) DockerConfigJSON() ([]byte, error) {
	auth := map[string]any{
		"username": r.Username,
		"password": r.Password,
		"auth":     base64.StdEncoding.EncodeToString([]byte(r.Username + ":" + r.Password)),
	}
	if r.Email != "" {
		auth["email"] = r.Email
	}
	return json.Marshal(map[string]any{"auths": map[string]any{dockerConfigKey(r.Server): auth}})
}

// SecretName is the name of the Secret in the cluster.
func (r *Registry) SecretName() string {
	return "bayplan-registry-" + r.Name
}

// Binding is a registry_bindings row with the registry data the UI needs.
type Binding struct {
	ID              int64
	RegistryID      int64
	RegistryName    string
	Server          string
	ClusterID       int64
	Namespace       string
	AttachDefaultSA bool
	LastSyncedAt    *time.Time
	LastError       string
	CreatedBy       string
	CreatedAt       time.Time
}

// SecretName of this binding's Secret.
func (b Binding) SecretName() string {
	return "bayplan-registry-" + b.RegistryName
}

// Validate checks the binding.
func (b *Binding) Validate() error {
	b.Namespace = strings.TrimSpace(b.Namespace)
	if b.Namespace == "" {
		return errors.New("missing namespace")
	}
	if !namespaceRE.MatchString(b.Namespace) {
		return fmt.Errorf("invalid namespace %q", b.Namespace)
	}
	return nil
}
