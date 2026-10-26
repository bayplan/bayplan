package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bayplan/bayplan/internal/audit"
)

type tool struct {
	name        string
	title       string
	description string
	schema      map[string]any
	readOnly    bool
	destructive bool
	idempotent  bool
	run         func(c *call, args map[string]any) (string, error)
}

// call carries what a tool needs to reach the API as the caller.
type call struct {
	s        *Server
	r        *http.Request
	userID   int64
	readOnly bool
	// source marks the audit entries of the API calls ("mcp", "assistant").
	source string
	// gate, when set, restricts what the tools reach: the in-panel assistant
	// uses it for the AI minimum role and the panel's own namespace, which
	// plain MCP leaves to the API (an MCP user sees what their token sees).
	gate *Gate
}

// Gate restricts an in-process caller's tools. Allow is asked before touching
// a cluster (namespace "") or one of its namespaces; Hidden names, per
// cluster, a namespace whose objects are removed from every answer, because a
// listing across all namespaces would otherwise carry it anyway.
type Gate struct {
	Allow  func(clusterID int64, namespace string) error
	Hidden func(clusterID int64) string
}

// errTool is a failure to report to the model as a tool result (isError),
// not as a protocol error: the model can read it and correct itself.
type errTool struct{ msg string }

func (e errTool) Error() string { return e.msg }

func toolErr(format string, a ...any) error { return errTool{fmt.Sprintf(format, a...)} }

func (s *Server) listTools(readOnlyToken bool) []map[string]any {
	out := []map[string]any{}
	for _, t := range s.tools {
		// A read-only token would get a 403 on every write anyway; not listing
		// them keeps the model from proposing what cannot happen.
		if readOnlyToken && !t.readOnly {
			continue
		}
		out = append(out, map[string]any{
			"name":        t.name,
			"title":       t.title,
			"description": t.description,
			"inputSchema": t.schema,
			"annotations": map[string]any{
				"title":           t.title,
				"readOnlyHint":    t.readOnly,
				"destructiveHint": t.destructive,
				"idempotentHint":  t.idempotent,
				"openWorldHint":   false,
			},
		})
	}
	return out
}

func (s *Server) callTool(r *http.Request, userID int64, readOnly bool, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid tools/call params"}
	}
	var t *tool
	for i := range s.tools {
		if s.tools[i].name == p.Name {
			t = &s.tools[i]
		}
	}
	if t == nil {
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}
	if readOnly && !t.readOnly {
		return toolResult("this API token is read-only: create a token without the read-only flag to let an AI client make changes", true), nil
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}
	text, err := t.run(&call{s: s, r: r, userID: userID, readOnly: readOnly}, p.Arguments)
	if err != nil {
		var te errTool
		if !errors.As(err, &te) {
			s.Logger.Error("mcp: tool failed", "tool", t.name, "error", err)
		}
		return toolResult(err.Error(), true), nil
	}
	return toolResult(text, false), nil
}

func toolResult(text string, isError bool) map[string]any {
	if len(text) > maxToolText {
		text = text[:maxToolText] + fmt.Sprintf("\n\n[truncated: the result was longer than %d KB; narrow it with a namespace or a smaller tail]", maxToolText>>10)
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}

// api performs a request against Bayplan's own router as the caller: same
// token, same client address (for the audit) and the "mcp" source mark, so
// every write it triggers is audited as the user's and flagged as coming from
// an AI client.
func (c *call) api(method, path string, query url.Values, body any) (int, []byte, error) {
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	source := c.source
	if source == "" {
		source = "mcp"
	}
	ctx := audit.WithSource(c.r.Context(), source)
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	req.RemoteAddr = c.r.RemoteAddr
	// Cookie: the in-panel assistant runs on the browser session, not a token.
	for _, h := range []string{"Authorization", "Cookie", "User-Agent", "X-Forwarded-For", "X-Real-Ip"} {
		if v := c.r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	c.s.API.ServeHTTP(rec, req)
	return rec.status, rec.body.Bytes(), nil
}

// get is api for reads, turning an HTTP failure into a tool error with the
// API's own message (it already says why: no permission, not synced…).
func (c *call) get(path string, query url.Values) ([]byte, error) {
	status, body, err := c.api(http.MethodGet, path, query, nil)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, toolErr("HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (c *call) write(method, path string, body any) (string, error) {
	status, resp, err := c.api(method, path, nil, body)
	if err != nil {
		return "", err
	}
	if status >= 300 {
		return "", toolErr("HTTP %d: %s", status, strings.TrimSpace(string(resp)))
	}
	if len(bytes.TrimSpace(resp)) == 0 {
		return "done", nil
	}
	return string(resp), nil
}

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(status int) {
	if !r.wrote {
		r.status, r.wrote = status, true
	}
}
func (r *recorder) Write(p []byte) (int, error) {
	r.wrote = true
	// Never buffer more than a tool could show; the rest is discarded.
	if room := 4*maxToolText - r.body.Len(); room > 0 {
		if len(p) > room {
			r.body.Write(p[:room])
		} else {
			r.body.Write(p)
		}
	}
	return len(p), nil
}

// --- argument helpers ---

func str(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	}
	return ""
}

func required(args map[string]any, keys ...string) error {
	for _, k := range keys {
		if str(args, k) == "" {
			return toolErr("missing argument %q", k)
		}
	}
	return nil
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

type clusterRef struct {
	ID   int64  `json:"id"`
	Name string `json:"displayName"`
	Kind string `json:"kind"`
}

// cluster resolves the "cluster" argument (id or display name) against the
// clusters THIS user can see, so a name that belongs to a cluster they have no
// role on is simply "not found".
func (c *call) cluster(args map[string]any) (clusterRef, error) {
	want := str(args, "cluster")
	if want == "" {
		return clusterRef{}, toolErr("missing argument \"cluster\" (id or name, see list_clusters)")
	}
	body, err := c.get("/api/clusters", nil)
	if err != nil {
		return clusterRef{}, err
	}
	var clusters []clusterRef
	if err := json.Unmarshal(body, &clusters); err != nil {
		return clusterRef{}, err
	}
	names := make([]string, 0, len(clusters))
	for _, cl := range clusters {
		if strconv.FormatInt(cl.ID, 10) == want || strings.EqualFold(cl.Name, want) {
			if cl.Kind == "docker" {
				return clusterRef{}, toolErr("%q is a Docker host, not a Kubernetes cluster: these tools only cover Kubernetes", cl.Name)
			}
			if c.gate != nil {
				if err := c.gate.Allow(cl.ID, ""); err != nil {
					return clusterRef{}, toolErr("%s", err.Error())
				}
			}
			return cl, nil
		}
		names = append(names, fmt.Sprintf("%d (%s)", cl.ID, cl.Name))
	}
	return clusterRef{}, toolErr("cluster %q not found among the ones you can access: %s", want, strings.Join(names, ", "))
}

func clusterPath(cl clusterRef, rest string) string {
	return "/api/clusters/" + strconv.FormatInt(cl.ID, 10) + rest
}

// namespaces builds the namespace filter, asking the gate about each one.
func (c *call) namespaces(cl clusterRef, args map[string]any) (url.Values, error) {
	q := url.Values{}
	ns := str(args, "namespace")
	if ns == "" {
		return q, nil
	}
	for _, one := range strings.Split(ns, ",") {
		if err := c.namespaceAllowed(cl, strings.TrimSpace(one)); err != nil {
			return nil, err
		}
	}
	q.Set("namespace", ns)
	return q, nil
}

func (c *call) namespaceAllowed(cl clusterRef, ns string) error {
	if c.gate == nil || ns == "" || ns == "-" {
		return nil
	}
	if err := c.gate.Allow(cl.ID, ns); err != nil {
		return toolErr("%s", err.Error())
	}
	return nil
}

// --- schemas ---

func object(required []string, props map[string]any) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

func enumProp(description string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

var (
	clusterProp   = map[string]any{"type": []string{"string", "integer"}, "description": "Cluster id or display name (see list_clusters)"}
	namespaceProp = prop("string", "Namespace; several comma-separated; empty = all the ones you can see")
)

// listKinds are the list routes of the API, in the API's own (plural) names.
var listKinds = []string{
	"namespaces", "nodes", "pods", "deployments", "statefulsets", "daemonsets", "services", "ingresses",
	"jobs", "cronjobs", "configmaps", "secrets", "serviceaccounts", "persistentvolumeclaims",
	"persistentvolumes", "storageclasses", "networkpolicies", "horizontalpodautoscalers",
	"resourcequotas", "limitranges",
}

var clusterScopedKinds = map[string]bool{"namespaces": true, "nodes": true, "persistentvolumes": true, "storageclasses": true}

func (s *Server) toolset() []tool {
	return []tool{
		{
			name: "list_clusters", title: "List clusters", readOnly: true, idempotent: true,
			description: "Clusters you can access in Bayplan, with their connection state.",
			schema:      object(nil, map[string]any{}),
			run: func(c *call, _ map[string]any) (string, error) {
				body, err := c.get("/api/clusters", nil)
				if err != nil {
					return "", err
				}
				var clusters []map[string]any
				if err := json.Unmarshal(body, &clusters); err != nil {
					return "", err
				}
				keep := []string{"id", "displayName", "description", "kind", "connector", "enabled", "status"}
				out := make([]map[string]any, 0, len(clusters))
				for _, cl := range clusters {
					row := map[string]any{}
					for _, k := range keep {
						if v, ok := cl[k]; ok {
							row[k] = v
						}
					}
					out = append(out, row)
				}
				return compactJSON(out)
			},
		},
		{
			name: "get_attention", title: "What needs attention", readOnly: true, idempotent: true,
			description: "Problems detected right now (crashing pods, unavailable deployments, NotReady nodes…) with their stable reason codes. Without a cluster: every cluster you can see. With a cluster: its overview (counters, reserved capacity, issues, recent Warning events).",
			schema:      object(nil, map[string]any{"cluster": clusterProp}),
			run: func(c *call, args map[string]any) (string, error) {
				if str(args, "cluster") == "" {
					body, err := c.get("/api/fleet/attention", nil)
					if err != nil {
						return "", err
					}
					return c.gateFleet(body)
				}
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				body, err := c.getIn(cl, clusterPath(cl, "/overview"), nil)
				if err != nil {
					return "", err
				}
				return string(body), nil
			},
		},
		{
			name: "list_resources", title: "List resources", readOnly: true, idempotent: true,
			description: "Lists objects of one kind (summaries, not full manifests). Use get_resource for one object's YAML.",
			schema: object([]string{"cluster", "kind"}, map[string]any{
				"cluster":   clusterProp,
				"kind":      enumProp("Kind, in plural", listKinds),
				"namespace": namespaceProp,
			}),
			run: func(c *call, args map[string]any) (string, error) {
				kind := str(args, "kind")
				if !contains(listKinds, kind) {
					return "", toolErr("unsupported kind %q", kind)
				}
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				q, err := c.namespaces(cl, args)
				if err != nil {
					return "", err
				}
				body, err := c.getIn(cl, clusterPath(cl, "/"+kind), q)
				if err != nil {
					return "", err
				}
				return string(body), nil
			},
		},
		{
			name: "get_resource", title: "Get a resource's YAML", readOnly: true, idempotent: true,
			description: "Full YAML of one object. Secret values are only shown to cluster admins (masked otherwise).",
			schema: object([]string{"cluster", "kind", "name"}, map[string]any{
				"cluster":   clusterProp,
				"kind":      enumProp("Kind, in plural", listKinds),
				"namespace": prop("string", "Namespace (omit for cluster-scoped kinds)"),
				"name":      prop("string", "Object name"),
			}),
			run: func(c *call, args map[string]any) (string, error) {
				kind := str(args, "kind")
				if !contains(listKinds, kind) {
					return "", toolErr("unsupported kind %q", kind)
				}
				ns := str(args, "namespace")
				if ns == "" || clusterScopedKinds[kind] {
					ns = "-"
				}
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				if err := c.namespaceAllowed(cl, ns); err != nil {
					return "", err
				}
				if kind == "namespaces" {
					if err := c.namespaceAllowed(cl, str(args, "name")); err != nil {
						return "", err
					}
				}
				body, err := c.get(clusterPath(cl, "/resources/"+url.PathEscape(kind)+"/"+url.PathEscape(ns)+"/"+url.PathEscape(str(args, "name"))+"/yaml"), nil)
				if err != nil {
					return "", err
				}
				var y struct {
					YAML string `json:"yaml"`
				}
				if err := json.Unmarshal(body, &y); err != nil {
					return "", err
				}
				return y.YAML, nil
			},
		},
		{
			name: "list_events", title: "List events", readOnly: true, idempotent: true,
			description: "Most recent Kubernetes events, newest first.",
			schema: object([]string{"cluster"}, map[string]any{
				"cluster":   clusterProp,
				"namespace": namespaceProp,
				"type":      enumProp("Only this event type", []string{"Warning", "Normal"}),
				"since":     prop("string", "Only events seen after this RFC3339 time"),
			}),
			run: func(c *call, args map[string]any) (string, error) {
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				q, err := c.namespaces(cl, args)
				if err != nil {
					return "", err
				}
				if t := str(args, "type"); t != "" {
					q.Set("type", t)
				}
				if since := str(args, "since"); since != "" {
					q.Set("since", since)
				}
				body, err := c.getIn(cl, clusterPath(cl, "/events"), q)
				if err != nil {
					return "", err
				}
				return string(body), nil
			},
		},
		{
			name: "get_pod_logs", title: "Get pod logs", readOnly: true, idempotent: true,
			description: "Last lines of a pod container's log (no follow). previous=true reads the previous run, which is where a crashing container's error usually is. The read is audited.",
			schema: object([]string{"cluster", "namespace", "pod"}, map[string]any{
				"cluster":    clusterProp,
				"namespace":  prop("string", "Pod namespace"),
				"pod":        prop("string", "Pod name"),
				"container":  prop("string", "Container (required if the pod has several)"),
				"tail_lines": map[string]any{"type": "integer", "minimum": 1, "maximum": 2000, "description": "How many lines (default 200)"},
				"previous":   prop("boolean", "Logs of the previous container run"),
			}),
			run: func(c *call, args map[string]any) (string, error) {
				if err := required(args, "namespace", "pod"); err != nil {
					return "", err
				}
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				if err := c.namespaceAllowed(cl, str(args, "namespace")); err != nil {
					return "", err
				}
				q := url.Values{}
				if v := str(args, "container"); v != "" {
					q.Set("container", v)
				}
				if v := str(args, "tail_lines"); v != "" {
					q.Set("tailLines", v)
				}
				if b, _ := args["previous"].(bool); b {
					q.Set("previous", "true")
				}
				body, err := c.get(clusterPath(cl, "/pods/"+url.PathEscape(str(args, "namespace"))+"/"+url.PathEscape(str(args, "pod"))+"/logs"), q)
				if err != nil {
					return "", err
				}
				var l struct {
					Logs      string `json:"logs"`
					Truncated bool   `json:"truncated"`
				}
				if err := json.Unmarshal(body, &l); err != nil {
					return "", err
				}
				if l.Truncated {
					l.Logs += "\n[truncated by size: ask for fewer lines]"
				}
				if l.Logs == "" {
					return "(no log lines)", nil
				}
				return l.Logs, nil
			},
		},
		{
			name: "get_topology", title: "Get topology", readOnly: true, idempotent: true,
			description: "How objects relate in some namespaces: Ingress -> Service -> workload -> pods -> PVC, with each node's status. Configured relations only, not observed network traffic.",
			schema:      object([]string{"cluster"}, map[string]any{"cluster": clusterProp, "namespace": namespaceProp}),
			run: func(c *call, args map[string]any) (string, error) {
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				q, err := c.namespaces(cl, args)
				if err != nil {
					return "", err
				}
				body, err := c.getIn(cl, clusterPath(cl, "/topology"), q)
				if err != nil {
					return "", err
				}
				return topologyText(body)
			},
		},
		{
			name: "get_usage", title: "Get resource usage", readOnly: true, idempotent: true,
			description: "Real CPU/memory usage per node from metrics-server (available=false with the reason if it is not installed).",
			schema:      object([]string{"cluster"}, map[string]any{"cluster": clusterProp}),
			run: func(c *call, args map[string]any) (string, error) {
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				body, err := c.get(clusterPath(cl, "/metrics"), nil)
				if err != nil {
					return "", err
				}
				return string(body), nil
			},
		},

		// --- writes: editor or admin on the cluster, decided by the API ---
		{
			name: "restart_workload", title: "Restart a workload", idempotent: false,
			description: "Rolling restart of a Deployment, StatefulSet or DaemonSet (like kubectl rollout restart). Requires editor on the cluster. Confirm with the user first.",
			schema: object([]string{"cluster", "kind", "namespace", "name"}, map[string]any{
				"cluster": clusterProp, "kind": enumProp("Workload kind", []string{"deployments", "statefulsets", "daemonsets"}),
				"namespace": prop("string", "Namespace"), "name": prop("string", "Workload name"),
			}),
			run: func(c *call, args map[string]any) (string, error) {
				if err := required(args, "kind", "namespace", "name"); err != nil {
					return "", err
				}
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				_, err = c.write(http.MethodPost, clusterPath(cl, "/"+url.PathEscape(str(args, "kind"))+"/"+url.PathEscape(str(args, "namespace"))+"/"+url.PathEscape(str(args, "name"))+"/restart"), nil)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("restart of %s %s/%s requested in %s", str(args, "kind"), str(args, "namespace"), str(args, "name"), cl.Name), nil
			},
		},
		{
			name: "scale_workload", title: "Scale a workload", idempotent: true,
			description: "Sets the replicas of a Deployment or StatefulSet (0 stops it). Requires editor on the cluster. Confirm with the user first.",
			schema: object([]string{"cluster", "kind", "namespace", "name", "replicas"}, map[string]any{
				"cluster": clusterProp, "kind": enumProp("Workload kind", []string{"deployments", "statefulsets"}),
				"namespace": prop("string", "Namespace"), "name": prop("string", "Workload name"),
				"replicas": map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
			}),
			run: func(c *call, args map[string]any) (string, error) {
				if err := required(args, "kind", "namespace", "name"); err != nil {
					return "", err
				}
				replicas, ok := args["replicas"].(float64)
				if !ok || replicas != float64(int32(replicas)) {
					return "", toolErr("replicas must be an integer")
				}
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				_, err = c.write(http.MethodPost, clusterPath(cl, "/"+url.PathEscape(str(args, "kind"))+"/"+url.PathEscape(str(args, "namespace"))+"/"+url.PathEscape(str(args, "name"))+"/scale"), map[string]any{"replicas": int32(replicas)})
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%s %s/%s scaled to %d in %s", str(args, "kind"), str(args, "namespace"), str(args, "name"), int32(replicas), cl.Name), nil
			},
		},
		{
			name: "delete_resource", title: "Delete a resource", destructive: true, idempotent: true,
			description: "Deletes one object (e.g. a pod, which its controller recreates). Requires admin on the cluster. Always confirm with the user, naming the exact object and cluster.",
			schema: object([]string{"cluster", "kind", "name"}, map[string]any{
				"cluster": clusterProp, "kind": enumProp("Kind, in plural", listKinds),
				"namespace": prop("string", "Namespace (omit for cluster-scoped kinds)"), "name": prop("string", "Object name"),
			}),
			run: func(c *call, args map[string]any) (string, error) {
				kind := str(args, "kind")
				if !contains(listKinds, kind) {
					return "", toolErr("unsupported kind %q", kind)
				}
				if err := required(args, "name"); err != nil {
					return "", err
				}
				ns := str(args, "namespace")
				if ns == "" || clusterScopedKinds[kind] {
					ns = "-"
				}
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				_, err = c.write(http.MethodDelete, clusterPath(cl, "/resources/"+url.PathEscape(kind)+"/"+url.PathEscape(ns)+"/"+url.PathEscape(str(args, "name"))), nil)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%s %s deleted in %s", kind, strings.TrimPrefix(ns+"/", "-/")+str(args, "name"), cl.Name), nil
			},
		},
		{
			name: "create_namespace", title: "Create a namespace", idempotent: true,
			description: "Creates a namespace (server-side apply: nothing happens if it exists). Requires editor on the cluster and that the cluster's RBAC lets editors create namespaces.",
			schema:      object([]string{"cluster", "name"}, map[string]any{"cluster": clusterProp, "name": prop("string", "Namespace name (DNS label)")}),
			run: func(c *call, args map[string]any) (string, error) {
				name := str(args, "name")
				if !dnsLabel.MatchString(name) {
					return "", toolErr("invalid namespace name %q: lowercase letters, digits and '-', up to 63", name)
				}
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				manifest := "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + name + "\n"
				return c.write(http.MethodPost, clusterPath(cl, "/apply"), map[string]any{"yaml": manifest, "dryRun": false})
			},
		},
		{
			name: "apply_manifest", title: "Apply a manifest", destructive: true, idempotent: true,
			description: "Server-side apply of one or more YAML documents, with the user's permissions. ALWAYS call first with dry_run=true and show the user the result; a real apply (dry_run=false) is only accepted after a successful dry-run of the exact same YAML, cluster and namespace in the last 15 minutes.",
			schema: object([]string{"cluster", "yaml"}, map[string]any{
				"cluster":   clusterProp,
				"yaml":      prop("string", "One or more YAML documents separated by ---"),
				"namespace": prop("string", "Default namespace for documents without one"),
				"dry_run":   map[string]any{"type": "boolean", "default": true, "description": "Rehearse on the API server without changing anything"},
			}),
			run: func(c *call, args map[string]any) (string, error) {
				manifest, _ := args["yaml"].(string)
				if strings.TrimSpace(manifest) == "" {
					return "", toolErr("missing argument \"yaml\"")
				}
				dryRun := true
				if v, ok := args["dry_run"].(bool); ok {
					dryRun = v
				}
				cl, err := c.cluster(args)
				if err != nil {
					return "", err
				}
				ns := str(args, "namespace")
				key := dryRunKey(c.userID, cl.ID, ns, manifest)
				now := time.Now()
				if !dryRun && !c.s.dryRuns.consume(key, now) {
					return "", toolErr("refused: run apply_manifest with dry_run=true on this exact YAML first, show the user what it changes, and only then apply it")
				}
				out, err := c.write(http.MethodPost, clusterPath(cl, "/apply"), map[string]any{"yaml": manifest, "namespace": ns, "dryRun": dryRun})
				if err != nil {
					return "", err
				}
				if dryRun {
					c.s.dryRuns.record(key, now)
				}
				return out, nil
			},
		},
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func compactJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	return string(raw), err
}

// topologyText turns the graph into lines a model reads better than nested
// JSON, problems first.
func topologyText(body []byte) (string, error) {
	var t struct {
		Nodes []struct {
			ID, Kind, Namespace, Name, Status, Reason, Detail, NodeName string
			Config                                                      []string
		} `json:"nodes"`
		Edges []struct {
			Source, Target, Kind string
		} `json:"edges"`
		Total       int               `json:"total"`
		TooLarge    bool              `json:"tooLarge"`
		Unavailable map[string]string `json:"unavailable"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return "", err
	}
	if t.TooLarge {
		return fmt.Sprintf("%d objects: too many for one graph, ask again with a namespace", t.Total), nil
	}
	var b strings.Builder
	sort.SliceStable(t.Nodes, func(i, j int) bool { return (t.Nodes[i].Status != "ok") && (t.Nodes[j].Status == "ok") })
	fmt.Fprintf(&b, "%d objects, %d relations\nOBJECTS (kind namespace/name: status [reason] detail):\n", len(t.Nodes), len(t.Edges))
	for _, n := range t.Nodes {
		line := fmt.Sprintf("- %s %s/%s: %s", n.Kind, n.Namespace, n.Name, n.Status)
		if n.Reason != "" {
			line += " [" + n.Reason + "]"
		}
		if n.Detail != "" {
			line += " " + n.Detail
		}
		if n.NodeName != "" {
			line += " on " + n.NodeName
		}
		if len(n.Config) > 0 {
			line += " uses " + strings.Join(n.Config, ",")
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("RELATIONS:\n")
	for _, e := range t.Edges {
		fmt.Fprintf(&b, "- %s %s %s\n", e.Source, e.Kind, e.Target)
	}
	for kind, reason := range t.Unavailable {
		fmt.Fprintf(&b, "not included: %s (%s)\n", kind, reason)
	}
	return b.String(), nil
}

// gateFleet drops the clusters the gate refuses from the fleet answer, so a
// cluster where AI is not allowed for this user does not leak through the
// "every cluster" view.
func (c *call) gateFleet(body []byte) (string, error) {
	if c.gate == nil {
		return string(body), nil
	}
	var clusters []map[string]any
	if err := json.Unmarshal(body, &clusters); err != nil {
		return "", err
	}
	kept := clusters[:0]
	for _, cl := range clusters {
		id, _ := cl["clusterId"].(float64)
		if c.gate.Allow(int64(id), "") != nil {
			continue
		}
		if hidden := c.hidden(int64(id)); hidden != "" {
			stripNamespace(cl, hidden)
		}
		kept = append(kept, cl)
	}
	return compactJSON(kept)
}

func (c *call) hidden(clusterID int64) string {
	if c.gate == nil || c.gate.Hidden == nil {
		return ""
	}
	return c.gate.Hidden(clusterID)
}

// getIn reads a per-cluster route and removes the gate's hidden namespace
// from the answer.
func (c *call) getIn(cl clusterRef, path string, query url.Values) ([]byte, error) {
	body, err := c.get(path, query)
	if err != nil {
		return nil, err
	}
	hidden := c.hidden(cl.ID)
	if hidden == "" {
		return body, nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		// Every route read this way answers JSON; anything else is refused
		// rather than passed on unfiltered.
		return nil, toolErr("unexpected answer from %s", path)
	}
	return json.Marshal(stripNamespace(v, hidden))
}

// stripNamespace walks a decoded answer and drops, from every list, the
// objects that live in ns: summaries and issues ("namespace"), the Namespace
// itself in a namespace listing (kind-less "name" with no "namespace" next to
// a "createdAt"), topology edges ("Kind/ns/name") and events
// ("involvedObject" carries no namespace, the item does).
func stripNamespace(v any, ns string) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			t[k] = stripNamespace(child, ns)
		}
		return t
	case []any:
		kept := t[:0]
		for _, item := range t {
			if inNamespace(item, ns) {
				continue
			}
			kept = append(kept, stripNamespace(item, ns))
		}
		return kept
	}
	return v
}

func inNamespace(item any, ns string) bool {
	m, ok := item.(map[string]any)
	if !ok {
		return false
	}
	if m["namespace"] == ns {
		return true
	}
	if _, namespaced := m["namespace"]; !namespaced && m["name"] == ns && m["kind"] == nil {
		if _, isNamespaceRow := m["createdAt"]; isNamespaceRow {
			return true
		}
	}
	marker := "/" + ns + "/"
	for _, k := range []string{"source", "target", "id"} {
		if s, ok := m[k].(string); ok && strings.Contains(s, marker) {
			return true
		}
	}
	return false
}
