package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/session"
)

type seenRequest struct {
	method, path, query, auth, source string
	body                              map[string]any
}

// fakeAPI stands in for Bayplan's router: it records what the tools ask for
// and answers like the real routes do.
type fakeAPI struct {
	seen []seenRequest
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization"), source: audit.Source(r.Context())}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &req.body)
	}
	f.seen = append(f.seen, req)
	switch {
	case r.URL.Path == "/api/clusters":
		_, _ = w.Write([]byte(`[{"id":1,"displayName":"prod","kind":"kubernetes"},{"id":3,"displayName":"dock","kind":"docker"}]`))
	case strings.HasSuffix(r.URL.Path, "/apply"):
		_ = json.NewEncoder(w).Encode(map[string]any{"applied": []string{"created"}, "dryRun": req.body["dryRun"]})
	case strings.HasSuffix(r.URL.Path, "/restart"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete:
		http.Error(w, "forbidden: requires the admin role on this cluster", http.StatusForbidden)
	default:
		_, _ = w.Write([]byte(`[]`))
	}
}

// calls returns the requests other than the cluster lookup.
func (f *fakeAPI) calls() []seenRequest {
	var out []seenRequest
	for _, s := range f.seen {
		if s.path != "/api/clusters" {
			out = append(out, s)
		}
	}
	return out
}

func newTestServer() (*Server, *fakeAPI) {
	api := &fakeAPI{}
	s := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	s.API = api
	return s, api
}

var tokenSession = &session.Session{UserID: 7, Username: "ana", ViaToken: true}

func rpc(t *testing.T, s *Server, sess *session.Session, method string, params any) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	r := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer bpl_test")
	w := httptest.NewRecorder()
	s.handle(w, r, sess)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func toolCall(t *testing.T, s *Server, sess *session.Session, name string, args map[string]any) (string, bool) {
	t.Helper()
	_, out := rpc(t, s, sess, "tools/call", map[string]any{"name": name, "arguments": args})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result for %s: %v", name, out)
	}
	content := result["content"].([]any)[0].(map[string]any)
	return content["text"].(string), result["isError"].(bool)
}

func TestInitializeNegotiatesVersion(t *testing.T) {
	s, _ := newTestServer()
	for requested, want := range map[string]string{"2025-06-18": "2025-06-18", "1999-01-01": supportedVersions[0]} {
		_, out := rpc(t, s, tokenSession, "initialize", map[string]any{"protocolVersion": requested})
		if got := out["result"].(map[string]any)["protocolVersion"]; got != want {
			t.Errorf("requested %s: got %v, want %s", requested, got, want)
		}
	}
}

func TestRejectsCookieSessionsBrowsersAndGET(t *testing.T) {
	s, _ := newTestServer()
	if code, _ := rpc(t, s, &session.Session{UserID: 7}, "tools/list", nil); code != http.StatusUnauthorized {
		t.Errorf("cookie session: got %d, want 401", code)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("browser origin: got %d, want 403", w.Code)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/mcp", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: got %d, want 405", w.Code)
	}
}

func TestNotificationGetsNoAnswer(t *testing.T) {
	s, _ := newTestServer()
	r := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	w := httptest.NewRecorder()
	s.handle(w, r, tokenSession)
	if w.Code != http.StatusAccepted || w.Body.Len() != 0 {
		t.Fatalf("got %d %q, want 202 and no body", w.Code, w.Body.String())
	}
}

func TestReadOnlyTokenHidesAndRefusesWrites(t *testing.T) {
	s, api := newTestServer()
	ro := &session.Session{UserID: 7, ViaToken: true, ReadOnly: true}
	_, out := rpc(t, s, ro, "tools/list", nil)
	for _, raw := range out["result"].(map[string]any)["tools"].([]any) {
		tl := raw.(map[string]any)
		if !tl["annotations"].(map[string]any)["readOnlyHint"].(bool) {
			t.Errorf("write tool %v listed for a read-only token", tl["name"])
		}
	}
	text, isErr := toolCall(t, s, ro, "restart_workload", map[string]any{"cluster": "prod", "kind": "deployments", "namespace": "shop", "name": "api"})
	if !isErr || !strings.Contains(text, "read-only") {
		t.Fatalf("got %q (isError=%v)", text, isErr)
	}
	if len(api.seen) != 0 {
		t.Fatalf("a refused write must not reach the API, saw %v", api.seen)
	}
}

func TestToolsCallTheAPIAsTheUser(t *testing.T) {
	s, api := newTestServer()
	if _, isErr := toolCall(t, s, tokenSession, "list_resources", map[string]any{"cluster": "PROD", "kind": "pods", "namespace": "a,b"}); isErr {
		t.Fatal("list_resources failed")
	}
	if _, isErr := toolCall(t, s, tokenSession, "restart_workload", map[string]any{"cluster": 1, "kind": "deployments", "namespace": "shop", "name": "api"}); isErr {
		t.Fatal("restart_workload failed")
	}
	got := api.calls()
	want := []seenRequest{
		{method: "GET", path: "/api/clusters/1/pods", query: "namespace=a%2Cb"},
		{method: "POST", path: "/api/clusters/1/deployments/shop/api/restart"},
	}
	if len(got) != len(want) {
		t.Fatalf("calls: %+v", got)
	}
	for i := range want {
		if got[i].method != want[i].method || got[i].path != want[i].path || got[i].query != want[i].query {
			t.Errorf("call %d: got %s %s?%s, want %s %s?%s", i, got[i].method, got[i].path, got[i].query, want[i].method, want[i].path, want[i].query)
		}
		// Same token, so the router applies this user's RBAC; marked so the
		// audit says the action came from an AI client.
		if got[i].auth != "Bearer bpl_test" || got[i].source != "mcp" {
			t.Errorf("call %d: auth %q source %q", i, got[i].auth, got[i].source)
		}
	}
}

func TestApplyRequiresAMatchingDryRun(t *testing.T) {
	s, api := newTestServer()
	manifest := "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\n"
	args := func(dryRun bool, yaml string) map[string]any {
		return map[string]any{"cluster": "prod", "namespace": "shop", "yaml": yaml, "dry_run": dryRun}
	}
	if text, isErr := toolCall(t, s, tokenSession, "apply_manifest", args(false, manifest)); !isErr || !strings.Contains(text, "dry_run=true") {
		t.Fatalf("apply without dry-run: %q", text)
	}
	if _, isErr := toolCall(t, s, tokenSession, "apply_manifest", args(true, manifest)); isErr {
		t.Fatal("dry-run failed")
	}
	// A different YAML does not ride on the first dry-run.
	if _, isErr := toolCall(t, s, tokenSession, "apply_manifest", args(false, manifest+"data: {a: b}\n")); !isErr {
		t.Fatal("a changed manifest was applied without its own dry-run")
	}
	if _, isErr := toolCall(t, s, tokenSession, "apply_manifest", args(true, manifest)); isErr {
		t.Fatal("second dry-run failed")
	}
	// Another user cannot use it either.
	other := &session.Session{UserID: 8, ViaToken: true}
	if _, isErr := toolCall(t, s, other, "apply_manifest", args(false, manifest)); !isErr {
		t.Fatal("another user's dry-run was accepted")
	}
	if _, isErr := toolCall(t, s, tokenSession, "apply_manifest", args(false, manifest)); isErr {
		t.Fatal("apply after its dry-run was refused")
	}
	if _, isErr := toolCall(t, s, tokenSession, "apply_manifest", args(false, manifest)); !isErr {
		t.Fatal("one dry-run allowed two applies")
	}
	var dryRuns []any
	for _, c := range api.calls() {
		dryRuns = append(dryRuns, c.body["dryRun"])
	}
	if len(dryRuns) != 3 || dryRuns[0] != true || dryRuns[1] != true || dryRuns[2] != false {
		t.Fatalf("apply calls reaching the API: %v", dryRuns)
	}
}

func TestDryRunExpires(t *testing.T) {
	l := newDryRunLedger(time.Minute)
	start := time.Now()
	l.record("k", start)
	if l.consume("k", start.Add(2*time.Minute)) {
		t.Fatal("an expired dry-run was accepted")
	}
}

func TestClusterResolution(t *testing.T) {
	s, _ := newTestServer()
	text, isErr := toolCall(t, s, tokenSession, "list_events", map[string]any{"cluster": "staging"})
	if !isErr || !strings.Contains(text, "1 (prod)") {
		t.Errorf("unknown cluster: %q", text)
	}
	text, isErr = toolCall(t, s, tokenSession, "list_events", map[string]any{"cluster": "dock"})
	if !isErr || !strings.Contains(text, "Docker host") {
		t.Errorf("docker host: %q", text)
	}
}

func TestAPIErrorsComeBackAsToolErrors(t *testing.T) {
	s, _ := newTestServer()
	text, isErr := toolCall(t, s, tokenSession, "delete_resource", map[string]any{"cluster": "prod", "kind": "pods", "namespace": "shop", "name": "api-1"})
	if !isErr || !strings.Contains(text, "HTTP 403") || !strings.Contains(text, "admin role") {
		t.Fatalf("got %q", text)
	}
}

func TestLongResultsSayTheyWereCut(t *testing.T) {
	text := toolResult(strings.Repeat("x", maxToolText+10), false)["content"].([]map[string]any)[0]["text"].(string)
	if !strings.Contains(text, "[truncated") || len(text) > maxToolText+200 {
		t.Fatalf("truncation: len %d", len(text))
	}
}

func TestTopologyTextPutsProblemsFirst(t *testing.T) {
	text, err := topologyText([]byte(`{"nodes":[{"kind":"Pod","namespace":"a","name":"ok-1","status":"ok"},{"kind":"Pod","namespace":"a","name":"bad-1","status":"error","reason":"CrashLoopBackOff"}],"edges":[{"source":"Deployment/a/x","target":"Pod/a/bad-1","kind":"owns"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(text, "bad-1") > strings.Index(text, "ok-1") || !strings.Contains(text, "[CrashLoopBackOff]") || !strings.Contains(text, "owns Pod/a/bad-1") {
		t.Fatalf("got:\n%s", text)
	}
}

func TestReadToolGate(t *testing.T) {
	s, api := newTestServer()
	r := httptest.NewRequest(http.MethodPost, "/api/assistant/chat", nil)
	r.Header.Set("Cookie", "bayplan_session=abc")
	gate := &Gate{Allow: func(clusterID int64, namespace string) error {
		if namespace == "bayplan" {
			return errors.New("namespace bayplan is the panel's own")
		}
		return nil
	}}
	text, isErr := s.RunReadTool(r, 7, "get_pod_logs", map[string]any{"cluster": "prod", "namespace": "bayplan", "pod": "server-0"}, "assistant", gate)
	if !isErr || !strings.Contains(text, "panel's own") {
		t.Fatalf("protected namespace: %q", text)
	}
	if len(api.calls()) != 0 {
		t.Fatalf("a gated call reached the API: %v", api.calls())
	}
	if _, isErr := s.RunReadTool(r, 7, "list_resources", map[string]any{"cluster": "prod", "kind": "pods", "namespace": "shop"}, "assistant", gate); isErr {
		t.Fatal("allowed call refused")
	}
	got := api.calls()[0]
	// The assistant runs on the browser session and is audited as such.
	if got.source != "assistant" {
		t.Fatalf("source %q", got.source)
	}
	// Write tools are not runnable through this door at all.
	if _, isErr := s.RunReadTool(r, 7, "delete_resource", map[string]any{"cluster": "prod", "kind": "pods", "name": "x"}, "assistant", gate); !isErr {
		t.Fatal("a write tool ran through RunReadTool")
	}
	refuseAll := &Gate{Allow: func(int64, string) error { return errors.New("the AI assistant requires the editor role") }}
	text, isErr = s.RunReadTool(r, 7, "list_events", map[string]any{"cluster": "prod"}, "assistant", refuseAll)
	if !isErr || !strings.Contains(text, "editor role") {
		t.Fatalf("cluster gate: %q", text)
	}
}

// hiddenAPI answers like the real routes with objects in the panel's own
// namespace mixed in, the shape a listing across all namespaces has.
type hiddenAPI struct{ fakeAPI }

func (h *hiddenAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/clusters":
		_, _ = w.Write([]byte(`[{"id":1,"displayName":"prod","kind":"kubernetes"}]`))
	case "/api/clusters/1/pods":
		_, _ = w.Write([]byte(`[{"name":"api-1","namespace":"shop"},{"name":"bayplan-0","namespace":"bayplan"}]`))
	case "/api/clusters/1/namespaces":
		_, _ = w.Write([]byte(`[{"name":"shop","createdAt":"2026-01-01T00:00:00Z"},{"name":"bayplan","createdAt":"2026-01-01T00:00:00Z"}]`))
	case "/api/clusters/1/overview":
		_, _ = w.Write([]byte(`{"issues":[{"kind":"Pod","namespace":"bayplan","name":"bayplan-0","reason":"CrashLoopBackOff"},{"kind":"Node","name":"bayplan","reason":"NotReady"}],"warningEvents":[{"namespace":"bayplan","message":"setup token printed"}]}`))
	case "/api/clusters/1/topology":
		_, _ = w.Write([]byte(`{"nodes":[{"id":"Pod/bayplan/bayplan-0","kind":"Pod","namespace":"bayplan","name":"bayplan-0","status":"ok"},{"id":"Pod/shop/api-1","kind":"Pod","namespace":"shop","name":"api-1","status":"ok"}],"edges":[{"source":"Deployment/bayplan/bayplan","target":"Pod/bayplan/bayplan-0","kind":"owns"}]}`))
	case "/api/fleet/attention":
		_, _ = w.Write([]byte(`[{"clusterId":1,"cluster":"prod","issues":[{"kind":"Pod","namespace":"bayplan","name":"bayplan-0"},{"kind":"Pod","namespace":"shop","name":"api-1"}]}]`))
	default:
		http.NotFound(w, r)
	}
}

func TestHiddenNamespaceNeverReachesTheModel(t *testing.T) {
	s, _ := newTestServer()
	s.API = &hiddenAPI{}
	r := httptest.NewRequest(http.MethodPost, "/api/assistant/chat", nil)
	gate := &Gate{
		Allow: func(_ int64, ns string) error {
			if ns == "bayplan" {
				return errors.New("namespace bayplan is the panel's own")
			}
			return nil
		},
		Hidden: func(int64) string { return "bayplan" },
	}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"list_resources", map[string]any{"cluster": "prod", "kind": "pods"}},
		{"list_resources", map[string]any{"cluster": "prod", "kind": "namespaces"}},
		{"get_attention", map[string]any{"cluster": "prod"}},
		{"get_attention", map[string]any{}},
		{"get_topology", map[string]any{"cluster": "prod"}},
	} {
		text, isErr := s.RunReadTool(r, 7, tc.tool, tc.args, "assistant", gate)
		if isErr {
			t.Fatalf("%s %v: %s", tc.tool, tc.args, text)
		}
		if strings.Contains(text, "bayplan-0") || strings.Contains(text, "setup token") || strings.Contains(text, `"name":"bayplan","createdAt"`) {
			t.Errorf("%s %v leaked the hidden namespace: %s", tc.tool, tc.args, text)
		}
	}
	// A node that happens to share the name is not a namespace object.
	text, _ := s.RunReadTool(r, 7, "get_attention", map[string]any{"cluster": "prod"}, "assistant", gate)
	if !strings.Contains(text, `"kind":"Node"`) {
		t.Errorf("unrelated object dropped: %s", text)
	}
	// The Namespace object itself, read directly.
	if text, isErr := s.RunReadTool(r, 7, "get_resource", map[string]any{"cluster": "prod", "kind": "namespaces", "name": "bayplan"}, "assistant", gate); !isErr || !strings.Contains(text, "panel's own") {
		t.Errorf("namespace object: %q", text)
	}
}
