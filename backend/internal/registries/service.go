package registries

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bayplan/bayplan/internal/netguard"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/bayplan/bayplan/internal/k8s"
)

const (
	// resyncInterval: re-applying the Secrets is idempotent and cheap; it
	// picks up a recreated namespace or a Secret deleted by hand.
	resyncInterval = 15 * time.Minute
	// startupDelay lets the clusters connect before the first pass.
	startupDelay = 2 * time.Minute
	// identity is the user impersonated in automatic syncs; the ones a human
	// triggers go with their own user.
	identity = "registries"
)

// Service applies the bindings in the clusters.
type Service struct {
	Store  *Store
	k8s    *k8s.Manager
	logger *slog.Logger
}

func NewService(store *Store, k8sManager *k8s.Manager, logger *slog.Logger) *Service {
	return &Service{Store: store, k8s: k8sManager, logger: logger}
}

// Start launches the periodic resync.
func (s *Service) Start(ctx context.Context) {
	go func() {
		timer := time.NewTimer(startupDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		ticker := time.NewTicker(resyncInterval)
		defer ticker.Stop()
		for {
			s.resyncAll(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (s *Service) resyncAll(ctx context.Context) {
	bindings, err := s.Store.AllBindings(ctx)
	if err != nil {
		s.logger.Error("registries: failed to list bindings", "error", err)
		return
	}
	for _, b := range bindings {
		_ = s.Sync(ctx, b, identity, k8s.GroupEditors)
	}
}

// Sync creates/updates the binding's Secret and adjusts the default SA. The
// result is stored in last_synced_at/last_error. Writes go with the given
// identity and the cluster's RBAC decides (an editor can create secrets and
// touch serviceaccounts; see bayplan-impersonated-editor).
func (s *Service) Sync(ctx context.Context, b Binding, actingUser, actingGroup string) error {
	err := s.sync(ctx, b, actingUser, actingGroup)
	if serr := s.Store.SetStatus(ctx, b.ID, err); serr != nil {
		s.logger.Error("registries: failed to store status", "error", serr)
	}
	if err != nil {
		s.logger.Warn("registries: failed to sync", "registry", b.RegistryName, "cluster", b.ClusterID, "namespace", b.Namespace, "error", err)
	}
	return err
}

func (s *Service) sync(ctx context.Context, b Binding, actingUser, actingGroup string) error {
	reg, err := s.Store.Get(ctx, b.RegistryID)
	if err != nil {
		return err
	}
	cfg, err := reg.DockerConfigJSON()
	if err != nil {
		return err
	}
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      reg.SecretName(),
			"namespace": b.Namespace,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "bayplan",
				"bayplan.io/registry":          reg.Name,
			},
			"annotations": map[string]any{"bayplan.io/registry-server": reg.Server},
		},
		"type":       "kubernetes.io/dockerconfigjson",
		"stringData": map[string]any{".dockerconfigjson": string(cfg)},
	}}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := s.k8s.Apply(ctx, b.ClusterID, []*unstructured.Unstructured{secret}, b.Namespace, actingUser, actingGroup, false); err != nil {
		return err
	}
	return s.k8s.SetImagePullSecret(ctx, b.ClusterID, b.Namespace, "default", reg.SecretName(), b.AttachDefaultSA, actingUser, actingGroup)
}

// Remove deletes the Secret from the cluster and removes it from the default
// SA. A disconnected cluster does not prevent deleting the binding: the error
// is returned to show it, and the caller decides.
func (s *Service) Remove(ctx context.Context, b Binding, actingUser, actingGroup string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var errs []error
	if err := s.k8s.SetImagePullSecret(ctx, b.ClusterID, b.Namespace, "default", b.SecretName(), false, actingUser, actingGroup); err != nil {
		errs = append(errs, err)
	}
	if err := s.k8s.DeleteResource(ctx, b.ClusterID, "secrets", b.Namespace, b.SecretName(), actingUser, actingGroup); err != nil && !errors.Is(err, k8s.ErrResourceNotFound) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// SyncRegistry resyncs every binding of a registry (after rotating the
// credential), in the background.
func (s *Service) SyncRegistry(registryID int64, actingUser, actingGroup string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		bindings, err := s.Store.BindingsForRegistry(ctx, registryID)
		if err != nil {
			s.logger.Error("registries: failed to list the registry's bindings", "error", err)
			return
		}
		for _, b := range bindings {
			_ = s.Sync(ctx, b, actingUser, actingGroup)
		}
	}()
}

// Validate checks the credential against the registry with the Distribution
// API protocol: GET /v2/ returns 401 with WWW-Authenticate; if Bearer, the
// token is requested from the realm with basic auth, if Basic, the request is
// repeated with basic auth. A registry without authentication (direct 200) is
// also fine. The registry's error is returned whole: it is the real
// explanation (expired token, 2FA…).
func Validate(ctx context.Context, r *Registry) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// The registry host is chosen by the admin and may be private (an internal
	// Harbor is the normal case), so no range is filtered except link-local
	// (cloud metadata), and it is checked at connect time so that a DNS name
	// resolving there does not slip through. No redirects: the Distribution
	// API does not need them and they would bypass the filter.
	transport := &http.Transport{DialContext: guardedDialer().DialContext, Proxy: http.ProxyFromEnvironment}
	if r.InsecureSkipTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit admin option
	}
	client := &http.Client{
		Timeout:       20 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	host := r.Server
	if host == "docker.io" || host == "index.docker.io" {
		host = "registry-1.docker.io"
	}
	base := "https://" + host + "/v2/"
	resp, err := get(ctx, client, base, "", "")
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", host, err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
	default:
		return fmt.Errorf("%s answered HTTP %d to /v2/", host, resp.StatusCode)
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	scheme, params := parseChallenge(challenge)
	switch strings.ToLower(scheme) {
	case "basic":
		resp, err = get(ctx, client, base, r.Username, r.Password)
	case "bearer":
		realm := params["realm"]
		if realm == "" {
			return fmt.Errorf("%s requests Bearer without a realm: %q", host, challenge)
		}
		// The realm is dictated by the remote registry: only https is accepted,
		// so as not to send the credential in plaintext wherever a foreign
		// response says.
		u, err := url.Parse(realm)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("invalid or non-https authentication realm: %q", realm)
		}
		q := u.Query()
		if svc := params["service"]; svc != "" {
			q.Set("service", svc)
		}
		u.RawQuery = q.Encode()
		resp, err = get(ctx, client, u.String(), r.Username, r.Password)
		if err != nil {
			return fmt.Errorf("could not reach the token server %s: %w", u.Host, err)
		}
	default:
		return fmt.Errorf("%s uses an unsupported authentication scheme: %q", host, challenge)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if msg := registryErrorMessage(body); msg != "" {
			return fmt.Errorf("the registry rejects the credential (HTTP %d): %s", resp.StatusCode, msg)
		}
		return fmt.Errorf("the registry rejects the credential (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// registryErrorMessage extracts the message from the known error formats
// ({"errors":[{"message"}]} from the Distribution API, {"details"} from Docker
// Hub) instead of reflecting the raw body of a foreign server.
func registryErrorMessage(body []byte) string {
	var e struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Details string `json:"details"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	var parts []string
	for _, x := range e.Errors {
		if x.Message != "" {
			parts = append(parts, x.Message)
		}
	}
	if e.Details != "" {
		parts = append(parts, e.Details)
	}
	msg := strings.Join(parts, "; ")
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

// guardedDialer rejects at connect time any link-local IP (cloud metadata
// service), already resolved.
func guardedDialer() *net.Dialer {
	return &net.Dialer{
		Timeout: 20 * time.Second,
		ControlContext: func(_ context.Context, _, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip != nil && netguard.IsMetadata(ip) {
				return errors.New("the address is link-local (cloud metadata); not allowed")
			}
			return nil
		},
	}
}

func get(ctx context.Context, client *http.Client, u, user, pass string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	req.Header.Set("User-Agent", "bayplan/1")
	return client.Do(req)
}

// parseChallenge splits `Bearer realm="https://…",service="x",scope="y"`.
func parseChallenge(h string) (string, map[string]string) {
	params := map[string]string{}
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	for _, part := range splitChallengeParams(rest) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		params[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return scheme, params
}

// splitChallengeParams splits on commas outside quotes (a realm may carry
// commas in its query).
func splitChallengeParams(s string) []string {
	var out []string
	var cur strings.Builder
	inQuotes := false
	for _, c := range s {
		switch {
		case c == '"':
			inQuotes = !inQuotes
			cur.WriteRune(c)
		case c == ',' && !inQuotes:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
