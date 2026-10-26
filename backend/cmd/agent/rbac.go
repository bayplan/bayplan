package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// The agent keeps its own RBAC up to date: it asks the server for the current
// ClusterRoles and ClusterRoleBindings for its cluster and applies them with
// its SA via server-side apply (one application/apply-patch+yaml PATCH per
// document, without client-go: the agent does not need half of Kubernetes for
// two kinds). That way a new panel version that reads new kinds does not force
// the operator to reapply anything; what the SA may touch is bounded by the
// manifest's bayplan-agent-rbac ClusterRole (only the bayplan-* ones).

const (
	rbacSyncInterval = 6 * time.Hour
	rbacFieldManager = "bayplan-agent"
)

type rbacSyncer struct {
	serverURL, clusterID, agentKey string
	currentImage                   string // BAYPLAN_IMAGE from the manifest; empty = no self-update
	server                         *http.Client
	apiHostPort                    string
	kube                           *http.Client
	token                          func() (string, error)
	logger                         *slog.Logger
}

// run applies at startup (with retries: the server may take a while) and then
// every rbacSyncInterval, until the context is cancelled.
func (s *rbacSyncer) run(ctx context.Context) {
	backoff := 10 * time.Second
	for {
		err := s.syncOnce(ctx)
		if err == nil {
			backoff = 10 * time.Second
			select {
			case <-ctx.Done():
				return
			case <-time.After(rbacSyncInterval):
			}
			continue
		}
		s.logger.Warn("rbac: sync failed, retrying", "error", err, "in", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 10*time.Minute {
			backoff *= 2
		}
	}
}

func (s *rbacSyncer) syncOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.serverURL+"/api/agent/rbac", nil)
	req.Header.Set("X-Bayplan-Cluster", s.clusterID)
	req.Header.Set("Authorization", "Bearer "+s.agentKey)
	resp, err := s.server.Do(req)
	if err != nil {
		return fmt.Errorf("requesting the RBAC from the server: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the server responded %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:8])

	applyErr := s.apply(ctx, body)
	s.report(ctx, hash, applyErr)
	if applyErr != nil {
		return applyErr
	}
	s.logger.Info("rbac: applied", "hash", hash)

	// With the RBAC up to date, is there a new agent version?
	if s.currentImage != "" {
		target, ok, err := upgradeTarget(s.currentImage, resp.Header.Get("X-Bayplan-Agent-Image"))
		if err != nil {
			s.logger.Warn("upgrade: proposed image rejected", "error", err)
		} else if ok {
			s.logger.Info("upgrade: changing the agent image", "from", s.currentImage, "to", target)
			if err := s.upgradeSelf(ctx, target); err != nil {
				s.logger.Warn("upgrade: failed to patch the Deployment", "error", err)
			} else {
				// Until Kubernetes replaces this pod, do not patch again.
				s.currentImage = target
			}
		}
	}
	return nil
}

var rbacPlurals = map[string]string{"ClusterRole": "clusterroles", "ClusterRoleBinding": "clusterrolebindings"}

// Allowlist compiled into the binary: the ceiling of what the server can make
// the agent grant itself. It is STRUCTURAL: for each (apiGroup, resource) the
// exact verbs of the panel's RBAC with some margin (watch where there is
// list), no wildcards nor nonResourceURLs. Rejecting only "*" was not enough:
// `impersonate groups` without resourceNames amounts to system:masters, and
// unscoped `create clusterrolebindings` to cluster-admin. If a panel version
// needs something outside this, a new agent image is required: it is the
// operator who raises the ceiling, not the server.
var (
	rbacAllowedNames = map[string]bool{
		"bayplan-agent": true, "bayplan-impersonator": true,
		"bayplan-impersonated-admin": true, "bayplan-impersonated-editor": true, "bayplan-impersonated-readonly": true,
	}
	rbacAllowedSubjects = map[string]bool{
		"ServiceAccount/bayplan-agent/bayplan-agent": true,
		"Group/bayplan:admins/":                      true, "Group/bayplan:editors/": true, "Group/bayplan:readonly/": true,
	}
	// Groups the panel may impersonate: its own and no others.
	rbacImpersonableGroups = map[string]bool{"bayplan:admins": true, "bayplan:editors": true, "bayplan:readonly": true}

	rwVerbs   = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	roVerbs   = []string{"get", "list", "watch"}
	rbacTable = map[string][]string{
		"/pods": rwVerbs, "/services": rwVerbs, "/configmaps": rwVerbs, "/secrets": rwVerbs, "/serviceaccounts": rwVerbs,
		"/persistentvolumeclaims": rwVerbs, "/persistentvolumes": rwVerbs, "/namespaces": rwVerbs, "/resourcequotas": rwVerbs,
		"/limitranges": rwVerbs, "/events": roVerbs, "/endpoints": roVerbs, "/replicationcontrollers": roVerbs,
		"/nodes": {"get", "list", "watch", "patch"}, "/nodes/metrics": {"get"},
		"/pods/log": {"get"}, "/pods/exec": {"create"}, "/pods/eviction": {"create"},
		"/users": {"impersonate"}, "/groups": {"impersonate"},
		"apps/deployments": rwVerbs, "apps/statefulsets": rwVerbs, "apps/daemonsets": rwVerbs, "apps/replicasets": rwVerbs,
		"apps/deployments/scale": {"get", "update", "patch"}, "apps/statefulsets/scale": {"get", "update", "patch"},
		"batch/jobs": rwVerbs, "batch/cronjobs": rwVerbs,
		"autoscaling/horizontalpodautoscalers": rwVerbs,
		"networking.k8s.io/ingresses":          rwVerbs, "networking.k8s.io/networkpolicies": rwVerbs,
		"storage.k8s.io/storageclasses": rwVerbs,
		"policy/poddisruptionbudgets":   roVerbs,
		"metrics.k8s.io/pods":           roVerbs, "metrics.k8s.io/nodes": roVerbs,
		"discovery.k8s.io/endpointslices": roVerbs, "events.k8s.io/events": roVerbs,
		"authentication.k8s.io/tokenreviews": {"create"}, "authorization.k8s.io/subjectaccessreviews": {"create"},
		// RBAC/APIService writes: only what installing metrics-server needs, and
		// patch/update require resourceNames (see validateRBACDoc).
		"rbac.authorization.k8s.io/clusterroles": {"get", "create", "patch", "update"}, "rbac.authorization.k8s.io/clusterrolebindings": {"get", "create", "patch", "update"},
		"rbac.authorization.k8s.io/rolebindings": {"get", "create", "patch", "update"},
		"apiregistration.k8s.io/apiservices":     {"get", "create", "patch", "update"},
	}
)

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

type rbacRule struct {
	APIGroups       []string `json:"apiGroups"`
	Resources       []string `json:"resources"`
	ResourceNames   []string `json:"resourceNames"`
	Verbs           []string `json:"verbs"`
	NonResourceURLs []string `json:"nonResourceURLs"`
}

// rbacDoc is the ONLY shape that reaches the API server: apply() re-marshals
// this struct instead of forwarding the server's text, so a field the
// validator does not know (aggregationRule, labels, anything a future RBAC
// version adds) is dropped rather than applied unseen. aggregationRule is
// listed on purpose so it can be refused explicitly: with `escalate` on these
// ClusterRoles the API server would accept an empty role that aggregates
// cluster-admin, and the validator, seeing no rules, would have nothing to
// object to.
type rbacDoc struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Rules           []rbacRule       `json:"rules,omitempty"`
	AggregationRule *json.RawMessage `json:"aggregationRule,omitempty"`
	RoleRef         *struct {
		APIGroup string `json:"apiGroup"`
		Kind     string `json:"kind"`
		Name     string `json:"name"`
	} `json:"roleRef,omitempty"`
	Subjects []struct {
		APIGroup  string `json:"apiGroup,omitempty"`
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace,omitempty"`
	} `json:"subjects,omitempty"`
}

// validateRBACDoc checks a document against the allowlist. It returns the
// plural of the kind for the apply URL.
func validateRBACDoc(d rbacDoc) (string, error) {
	plural, ok := rbacPlurals[d.Kind]
	if !ok || d.APIVersion != "rbac.authorization.k8s.io/v1" {
		return "", fmt.Errorf("document not allowed: %s/%s", d.APIVersion, d.Kind)
	}
	if !rbacAllowedNames[d.Metadata.Name] {
		return "", fmt.Errorf("name outside the allowlist: %q", d.Metadata.Name)
	}
	switch d.Kind {
	case "ClusterRole":
		if d.AggregationRule != nil {
			return "", errors.New("aggregationRule not allowed")
		}
		if d.RoleRef != nil || len(d.Subjects) > 0 {
			return "", errors.New("a ClusterRole carries no roleRef or subjects")
		}
		for _, r := range d.Rules {
			if len(r.NonResourceURLs) > 0 {
				return "", errors.New("nonResourceURLs not allowed")
			}
			if len(r.APIGroups) == 0 || len(r.Resources) == 0 || len(r.Verbs) == 0 {
				return "", errors.New("incomplete rule")
			}
			for _, name := range r.ResourceNames {
				if strings.HasPrefix(name, "system:masters") || name == "*" {
					return "", fmt.Errorf("resourceName not allowed: %q", name)
				}
			}
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					allowed, ok := rbacTable[g+"/"+res]
					if !ok {
						return "", fmt.Errorf("resource outside the allowlist: %s/%s", g, res)
					}
					for _, v := range r.Verbs {
						if !contains(allowed, v) {
							return "", fmt.Errorf("verb %q not allowed for %s/%s", v, g, res)
						}
						// impersonate groups only on the panel's groups: without
						// resourceNames it would amount to system:masters.
						if v == "impersonate" && res == "groups" {
							if len(r.ResourceNames) == 0 {
								return "", errors.New("impersonate groups requires resourceNames")
							}
							for _, name := range r.ResourceNames {
								if !rbacImpersonableGroups[name] {
									return "", fmt.Errorf("impersonable group not allowed: %q", name)
								}
							}
						}
						// No modifying other people's RBAC/APIServices: patch/update only by name.
						if (g == "rbac.authorization.k8s.io" || g == "apiregistration.k8s.io") && (v == "patch" || v == "update") && len(r.ResourceNames) == 0 {
							return "", fmt.Errorf("%s/%s: patch/update requires resourceNames", g, res)
						}
					}
				}
			}
		}
	case "ClusterRoleBinding":
		if len(d.Rules) > 0 || d.AggregationRule != nil {
			return "", errors.New("a ClusterRoleBinding carries no rules")
		}
		if d.RoleRef == nil || d.RoleRef.Kind != "ClusterRole" || !rbacAllowedNames[d.RoleRef.Name] || (d.RoleRef.APIGroup != "" && d.RoleRef.APIGroup != "rbac.authorization.k8s.io") {
			return "", errors.New("roleRef outside the allowlist")
		}
		for _, s := range d.Subjects {
			if s.APIGroup != "" && s.APIGroup != "rbac.authorization.k8s.io" {
				return "", fmt.Errorf("subject apiGroup not allowed: %q", s.APIGroup)
			}
			if !rbacAllowedSubjects[s.Kind+"/"+s.Name+"/"+s.Namespace] {
				return "", fmt.Errorf("subject outside the allowlist: %s %s", s.Kind, s.Name)
			}
		}
	}
	return plural, nil
}

// apply walks the documents and does a server-side apply of each one. It only
// accepts the two RBAC kinds: anything else coming from the server is
// rejected, the agent is not a channel for applying arbitrary manifests.
func (s *rbacSyncer) apply(ctx context.Context, body []byte) error {
	type item struct {
		plural, name, kind, doc string
	}
	var items []item
	for _, doc := range strings.Split(string(body), "\n---\n") {
		doc = strings.TrimSpace(doc)
		if doc == "" || doc == "---" {
			continue
		}
		var d rbacDoc
		if err := yaml.Unmarshal([]byte(doc), &d); err != nil {
			return fmt.Errorf("invalid document: %w", err)
		}
		plural, err := validateRBACDoc(d)
		if err != nil {
			// A document outside the allowlist invalidates the whole pass,
			// BEFORE applying anything: from a server that sends what it should
			// not, not even what looked fine gets applied.
			return fmt.Errorf("rejected %s/%s: %w", d.Kind, d.Metadata.Name, err)
		}
		// What gets applied is the validated struct, never the text the server
		// sent: see rbacDoc.
		clean, err := json.Marshal(d)
		if err != nil {
			return fmt.Errorf("re-encoding %s/%s: %w", d.Kind, d.Metadata.Name, err)
		}
		items = append(items, item{plural, d.Metadata.Name, d.Kind, string(clean)})
	}
	var errs []string
	for _, it := range items {
		if err := s.applyOne(ctx, it.plural, it.name, it.doc); err != nil {
			errs = append(errs, fmt.Sprintf("%s/%s: %v", it.kind, it.name, err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (s *rbacSyncer) applyOne(ctx context.Context, plural, name, doc string) error {
	tok, err := s.token()
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://%s/apis/rbac.authorization.k8s.io/v1/%s/%s?fieldManager=%s&force=true", s.apiHostPort, plural, name, rbacFieldManager)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader([]byte(doc)))
	req.Header.Set("Content-Type", "application/apply-patch+yaml")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := s.kube.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var st struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &st)
		if st.Message == "" {
			st.Message = strings.TrimSpace(string(b))
		}
		return fmt.Errorf("%d %s", resp.StatusCode, st.Message)
	}
	return nil
}

// report tells the server what was applied (or what failed); if the report
// fails it is only logged: the next pass repeats it.
func (s *rbacSyncer) report(ctx context.Context, hash string, applyErr error) {
	payload := map[string]string{"hash": hash}
	if applyErr != nil {
		payload["error"] = applyErr.Error()
		payload["hash"] = ""
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.serverURL+"/api/agent/rbac/status", bytes.NewReader(b))
	req.Header.Set("X-Bayplan-Cluster", s.clusterID)
	req.Header.Set("Authorization", "Bearer "+s.agentKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.server.Do(req)
	if err != nil {
		s.logger.Warn("rbac: could not report the status", "error", err)
		return
	}
	resp.Body.Close()
}
