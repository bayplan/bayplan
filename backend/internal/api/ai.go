package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/bayplan/bayplan/internal/ai"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/mcp"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/rbac"
	"github.com/bayplan/bayplan/internal/settings"
)

// AIHandler serves "Explain": it gathers an object's context from the cache,
// redacts it, and asks the provider the admin configured. Embeds the resource
// handler for its actor and error helpers.
type AIHandler struct {
	*ResourceHandler
	Client *ai.Client
	// MCP provides the read tools of the in-panel assistant; set once the
	// router (which it dispatches to) is built.
	MCP *mcp.Server
}

const (
	explainLogLines   = 80
	explainMaxYAML    = 40 << 10
	explainMaxPodYAML = 12 << 10
	explainMaxEvents  = 40
	explainMaxLogs    = 24 << 10
)

// explainKinds are the route kinds Explain accepts, with their Kubernetes name.
var explainKinds = map[string]string{
	"pods": "Pod", "deployments": "Deployment", "statefulsets": "StatefulSet", "daemonsets": "DaemonSet",
	"services": "Service", "ingresses": "Ingress", "persistentvolumeclaims": "PersistentVolumeClaim",
	"nodes": "Node", "jobs": "Job", "cronjobs": "CronJob",
}

var workloadKinds = map[string]bool{"deployments": true, "statefulsets": true, "daemonsets": true, "jobs": true, "cronjobs": true}

func (h *AIHandler) config(ctx context.Context) (settings.AI, error) {
	var cfg settings.AI
	err := h.Settings.Get(ctx, settings.KeyAI, &cfg)
	return cfg, err
}

func (h *AIHandler) clientConfig(cfg settings.AI) (ai.Config, error) {
	key := ""
	if cfg.APIKey.IsSet() {
		var err error
		if key, err = h.Settings.DecryptSecret(cfg.APIKey); err != nil {
			return ai.Config{}, err
		}
	}
	return ai.Config{Provider: cfg.Provider, BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: key}, nil
}

// Status: GET /api/ai/status. Lets the UI decide whether to show "Explain",
// and to whom, without exposing anything of the configuration but the names.
func (h *AIHandler) Status(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.config(r.Context())
	if err != nil {
		h.Logger.Error("ai.status: failed reading settings", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ready": cfg.Ready(), "provider": cfg.Provider, "model": cfg.Model,
		"minRole": cfg.EffectiveMinRole(), "dailyLimit": cfg.EffectiveDailyLimit(),
	})
}

// Test: POST /api/settings/ai/test (global admin). Sends a one-line prompt
// with the SAVED configuration and returns the provider's verbatim error if it
// fails: a wrong key or model name is caught here, not on someone's first click.
func (h *AIHandler) Test(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.config(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if cfg.Provider == "" || cfg.Model == "" {
		http.Error(w, "save a provider and a model first", http.StatusBadRequest)
		return
	}
	cc, err := h.clientConfig(cfg)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	start := time.Now()
	res, err := h.Client.Complete(r.Context(), cc, "You are a connectivity check.", "Reply with the single word OK.")
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "latencyMs": time.Since(start).Milliseconds(), "reply": strings.TrimSpace(res.Text)})
}

type explainRequest struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Lang      string `json:"lang"`
}

// Explain: POST /api/clusters/{id}/explain {kind, namespace, name, lang}.
// The role check is the configured minimum ON THIS CLUSTER (capped for
// read-only tokens by the middleware); the panel's own namespace is refused
// whole, because its objects describe the panel's secrets and database.
func (h *AIHandler) Explain(w http.ResponseWriter, r *http.Request) {
	id, err := parseClusterID(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var req explainRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSmallBodyBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	kindName, ok := explainKinds[req.Kind]
	if !ok || req.Name == "" || (req.Kind != "nodes" && req.Namespace == "") {
		http.Error(w, "kind, namespace and name are required (kind one of pods, deployments, statefulsets, daemonsets, services, ingresses, persistentvolumeclaims, nodes, jobs, cronjobs)", http.StatusBadRequest)
		return
	}
	if req.Kind == "nodes" {
		req.Namespace = ""
	}

	cfg, err := h.config(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !cfg.Ready() {
		http.Error(w, "AI is not configured on this Bayplan (Settings, AI)", http.StatusConflict)
		return
	}
	a, ok := h.resolveActor(w, r, id)
	if !ok {
		return
	}
	role, _ := middleware.RoleFromContext(r.Context())
	if !role.AtLeast(rbac.Role(cfg.EffectiveMinRole())) {
		http.Error(w, fmt.Sprintf("Explain requires the %s role on this cluster", cfg.EffectiveMinRole()), http.StatusForbidden)
		return
	}
	if req.Namespace != "" {
		if err := h.K8s.GuardNamespace(id, req.Namespace); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	sections, status, err := h.gather(r.Context(), id, req, kindName, a)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	remaining, err := ai.Reserve(r.Context(), h.DB, a.userID, cfg.EffectiveDailyLimit())
	if errors.Is(err, ai.ErrDailyLimit) {
		http.Error(w, fmt.Sprintf("you reached today's limit of %d explanations", cfg.EffectiveDailyLimit()), http.StatusTooManyRequests)
		return
	}
	if err != nil {
		// Fail closed: without a readable counter there is no limit.
		h.Logger.Error("ai.explain: usage counter unavailable", "error", err)
		http.Error(w, "the AI usage counter is unavailable, try again later", http.StatusServiceUnavailable)
		return
	}

	cc, err := h.clientConfig(cfg)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	system, user := ai.ExplainPrompt(req.Lang, kindName, req.Namespace, req.Name, sections)
	start := time.Now()
	res, callErr := h.Client.Complete(r.Context(), cc, system, user)
	if callErr == nil {
		if err := ai.Record(context.WithoutCancel(r.Context()), h.DB, a.userID, res.InputTokens, res.OutputTokens); err != nil {
			h.Logger.Warn("ai.explain: failed recording tokens", "error", err)
		}
	}

	sent := make([]string, 0, len(sections))
	for _, s := range sections {
		sent = append(sent, s.Title)
	}
	h.auditAction(r, a, "ai.explain", id, req.Namespace, req.Name, callErr, map[string]any{
		"kind": kindName, "provider": cfg.Provider, "model": cfg.Model, "sent": sent,
		"inputTokens": res.InputTokens, "outputTokens": res.OutputTokens,
	})
	if callErr != nil {
		http.Error(w, callErr.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"text": res.Text, "provider": cfg.Provider, "model": cfg.Model, "sent": sections,
		"inputTokens": res.InputTokens, "outputTokens": res.OutputTokens,
		"durationMs": time.Since(start).Milliseconds(), "remainingToday": remaining,
	})
}

// gather collects what is sent: the object's YAML, its recent events and, for
// a pod or a workload, the logs of the container that is in trouble. Every
// piece goes through the redaction in internal/ai.
func (h *AIHandler) gather(ctx context.Context, id int64, req explainRequest, kindName string, a actor) ([]ai.Section, int, error) {
	var sections []ai.Section
	manifest, err := h.K8s.ResourceYAML(id, req.Kind, req.Namespace, req.Name, true)
	if err != nil {
		switch {
		case errors.Is(err, k8s.ErrResourceNotFound):
			return nil, http.StatusNotFound, errors.New("resource not found")
		case errors.Is(err, k8s.ErrInformerNotSynced), errors.Is(err, k8s.ErrClusterNotSynced):
			return nil, http.StatusServiceUnavailable, err
		}
		return nil, http.StatusInternalServerError, err
	}
	clean, err := ai.RedactManifest(manifest)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	sections = append(sections, ai.Section{
		Title: "Manifest and status", Summary: kindName + " YAML without env values, Secret data or managedFields",
		Body: ai.Clip(clean, explainMaxYAML),
	})

	if events := h.objectEvents(id, req, kindName); len(events) > 0 {
		sections = append(sections, ai.Section{
			Title: "Recent events", Summary: fmt.Sprintf("%d events of the object and its pods", len(events)),
			Body: ai.RedactText(strings.Join(events, "\n")),
		})
	}

	pod := ""
	switch {
	case req.Kind == "pods":
		pod = req.Name
	case workloadKinds[req.Kind]:
		pod = h.troubledPod(id, req.Namespace, req.Name)
		if pod != "" {
			if podYAML, err := h.K8s.ResourceYAML(id, "pods", req.Namespace, pod, true); err == nil {
				if clean, err := ai.RedactManifest(podYAML); err == nil {
					sections = append(sections, ai.Section{
						Title: "Pod " + pod, Summary: "one of its pods, the one in worst state",
						Body: ai.Clip(clean, explainMaxPodYAML),
					})
				}
			}
		}
	}
	if pod != "" {
		sections = append(sections, h.podLogs(ctx, id, req.Namespace, pod, a))
	}
	return sections, 0, nil
}

func (h *AIHandler) objectEvents(id int64, req explainRequest, kindName string) []string {
	var namespaces []string
	if req.Namespace != "" {
		namespaces = []string{req.Namespace}
	}
	result, err := h.K8s.ListEvents(id, namespaces, k8s.EventFilter{}, 1000)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range result.Items {
		kind, name, _ := strings.Cut(e.InvolvedObject, "/")
		match := kind == kindName && name == req.Name
		// A workload's ReplicaSets and pods are named after it.
		if workloadKinds[req.Kind] && strings.HasPrefix(name, req.Name+"-") {
			match = true
		}
		if match {
			out = append(out, fmt.Sprintf("%s %s %s %s x%d: %s", e.LastSeen.UTC().Format(time.RFC3339), e.Type, e.Reason, e.InvolvedObject, e.Count, e.Message))
		}
	}
	// ListEvents is newest first; the model reads a timeline better oldest first.
	if len(out) > explainMaxEvents {
		out = out[:explainMaxEvents]
	}
	sort.Strings(out)
	return out
}

// troubledPod picks the workload's pod in the worst state: not running first,
// then the one with most restarts.
func (h *AIHandler) troubledPod(id int64, namespace, workload string) string {
	pods, err := h.K8s.ListPods(id, []string{namespace})
	if err != nil {
		return ""
	}
	best, bestScore := "", -1
	for _, p := range pods {
		if !strings.HasPrefix(p.Name, workload+"-") {
			continue
		}
		score := int(p.Restarts)
		if p.Status != "Running" {
			score += 1000
		}
		if ready, total, ok := strings.Cut(p.Ready, "/"); ok && ready != total {
			score += 100
		}
		if score > bestScore {
			best, bestScore = p.Name, score
		}
	}
	return best
}

// podLogs reads the troubled container's last lines, from the previous run if
// it restarted (that is where a crash is written). Read with the user's
// identity, so a role without pods/log gets no logs here either.
func (h *AIHandler) podLogs(ctx context.Context, id int64, namespace, pod string, a actor) ai.Section {
	container, previous := h.troubledContainer(id, namespace, pod)
	title := "Logs of pod " + pod
	if container != "" {
		title += ", container " + container
	}
	if previous {
		title += " (previous run)"
	}
	logs, _, err := h.K8s.PodLogs(ctx, id, namespace, pod, container, explainLogLines, previous, 256<<10, a.username, a.group)
	if err != nil && previous {
		// The previous run may be gone (the kubelet keeps only one); try the
		// current one before giving up.
		logs, _, err = h.K8s.PodLogs(ctx, id, namespace, pod, container, explainLogLines, false, 256<<10, a.username, a.group)
	}
	if err != nil {
		return ai.Section{Title: title, Summary: "not available: " + err.Error(), Body: "Logs not available: " + err.Error()}
	}
	if strings.TrimSpace(logs) == "" {
		logs = "(empty)"
	}
	return ai.Section{Title: title, Summary: fmt.Sprintf("last %d lines, secrets scrubbed", explainLogLines), Body: ai.Clip(ai.RedactText(logs), explainMaxLogs)}
}

func (h *AIHandler) troubledContainer(id int64, namespace, pod string) (string, bool) {
	manifest, err := h.K8s.ResourceYAML(id, "pods", namespace, pod, true)
	if err != nil {
		return "", false
	}
	var obj struct {
		Status struct {
			ContainerStatuses []struct {
				Name         string         `json:"name"`
				Ready        bool           `json:"ready"`
				RestartCount int32          `json:"restartCount"`
				LastState    map[string]any `json:"lastState"`
			} `json:"containerStatuses"`
		} `json:"status"`
	}
	if yaml.Unmarshal([]byte(manifest), &obj) != nil || len(obj.Status.ContainerStatuses) == 0 {
		return "", false
	}
	pick := obj.Status.ContainerStatuses[0]
	for _, c := range obj.Status.ContainerStatuses {
		if !c.Ready || c.RestartCount > pick.RestartCount {
			pick = c
			if !c.Ready {
				break
			}
		}
	}
	_, terminated := pick.LastState["terminated"]
	return pick.Name, pick.RestartCount > 0 && terminated
}

// --- settings section ---

type aiSettingsResponse struct {
	Enabled           bool   `json:"enabled"`
	Provider          string `json:"provider"`
	BaseURL           string `json:"baseURL"`
	Model             string `json:"model"`
	APIKeySet         bool   `json:"apiKeySet"`
	MinRole           string `json:"minRole"`
	DailyLimitPerUser int    `json:"dailyLimitPerUser"`
}

func toAISettingsResponse(a settings.AI) aiSettingsResponse {
	return aiSettingsResponse{Enabled: a.Enabled, Provider: a.Provider, BaseURL: a.BaseURL, Model: a.Model,
		APIKeySet: a.APIKey.IsSet(), MinRole: a.EffectiveMinRole(), DailyLimitPerUser: a.EffectiveDailyLimit()}
}

// aiSettingsInput: the key travels in the clear only towards the server; empty
// keeps the stored one, clearAPIKey removes it (a local model needs none).
type aiSettingsInput struct {
	Enabled           bool    `json:"enabled"`
	Provider          string  `json:"provider"`
	BaseURL           string  `json:"baseURL"`
	Model             string  `json:"model"`
	APIKey            *string `json:"apiKey"`
	ClearAPIKey       bool    `json:"clearAPIKey"`
	MinRole           string  `json:"minRole"`
	DailyLimitPerUser int     `json:"dailyLimitPerUser"`
}

func buildAISettings(in aiSettingsInput, current settings.AI, encrypt func(string) (settings.Secret, error)) (settings.AI, error) {
	out := settings.AI{
		Enabled: in.Enabled, Provider: strings.TrimSpace(in.Provider), BaseURL: strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"),
		Model: strings.TrimSpace(in.Model), APIKey: current.APIKey, MinRole: in.MinRole, DailyLimitPerUser: in.DailyLimitPerUser,
	}
	switch out.Provider {
	case "", ai.ProviderOpenAI, ai.ProviderAnthropic, ai.ProviderGemini:
	default:
		return out, &ValidationError{Msg: "provider must be openai, anthropic or gemini"}
	}
	if out.Provider != ai.ProviderOpenAI {
		out.BaseURL = ""
	}
	if err := ai.ValidateBaseURL(out.BaseURL); err != nil {
		return out, &ValidationError{Msg: err.Error()}
	}
	if out.MinRole != "" && !rbac.Role(out.MinRole).Valid() {
		return out, &ValidationError{Msg: "minRole must be readonly, editor or admin"}
	}
	if out.DailyLimitPerUser < 0 || out.DailyLimitPerUser > 10000 {
		return out, &ValidationError{Msg: "dailyLimitPerUser must be between 0 and 10000"}
	}
	if in.ClearAPIKey {
		out.APIKey = settings.Secret{}
	}
	if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
		sec, err := encrypt(strings.TrimSpace(*in.APIKey))
		if err != nil {
			return out, err
		}
		out.APIKey = sec
	}
	if out.Enabled {
		if out.Provider == "" || out.Model == "" {
			return out, &ValidationError{Msg: "to enable AI choose a provider and a model"}
		}
		if !out.APIKey.IsSet() && (out.Provider != ai.ProviderOpenAI || out.BaseURL == "") {
			return out, &ValidationError{Msg: "this provider needs an API key (only a self-hosted OpenAI-compatible server can go without one)"}
		}
	}
	return out, nil
}
