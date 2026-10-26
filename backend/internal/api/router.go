package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/ai"
	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/crypto"
	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/mcp"
	"github.com/bayplan/bayplan/internal/metrics"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/rbac"
	"github.com/bayplan/bayplan/internal/session"
	"github.com/bayplan/bayplan/internal/settings"
	"github.com/bayplan/bayplan/internal/ws"
)

// Deps groups the dependencies shared by the API handlers.
type Deps struct {
	DB             *pgxpool.Pool
	Logger         *slog.Logger
	Sessions       *session.Manager
	LoginLimiter   *middleware.IPRateLimiter
	AccountLimiter *middleware.AccountLimiter
	Health         *HealthHandler
	Keyring        *crypto.Keyring
	K8sManager     *k8s.Manager
	OIDC           *OIDCHandler    // SSO login; its routes respond 404 while SSO is disabled
	LogsHandler    *ws.LogsHandler // built in main.go so that metrics.New can read its active streams counter
	ExecHandler    *ws.ExecHandler // same, for the active terminal sessions gauge
	Metrics        *metrics.Metrics
	Settings       *settings.Store
	Helm           *HelmHandler          // nil disables the catalog and the Helm releases
	GitOps         *GitOpsHandler        // nil disables the GitOps sources
	AgentHub       http.Handler          // WebSocket of the agents (internal/agent.Hub)
	AgentAuth      AgentAuthenticator    // the same Hub, for the agent's HTTP routes (RBAC)
	Docker         *DockerHandler        // Docker/Podman environments via agent
	Cloud          *CloudHandler         // cloud provider credentials (EKS/AKS/GKE); nil disables the connector
	Notify         *NotificationsHandler // notification channels and rules; nil disables
	Registries     *RegistriesHandler    // image registries and pull secrets per namespace; nil disables
	Backups        *BackupHandler        // configuration backups to S3; nil disables
	Images         *ImagesHandler        // image inventory and Trivy scanning
	Extensions     *ExtensionsHandler    // links to external tools per cluster
	DockerWS       *ws.DockerHandler     // logs and terminal of Docker containers
	KubectlWS      *ws.KubectlHandler    // kubectl web shell (admin over the cluster)
	Web            http.Handler          // embedded frontend, serves everything not registered explicitly
	// AgentCA returns the CA of the server's own HTTPS (self-signed mode), or
	// nil. It goes into the agent manifests; see ClusterHandler.agentCACert.
	AgentCA func() []byte
	TLSMode string // tlsserve mode, "" without built-in HTTPS
}

// NewRouter builds the backend's full mux: liveness/readiness, auth (public
// login/setup/status, the rest protected), cluster management and their
// resources/actions, log streaming over WebSocket, and the embedded frontend
// as fallback.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", d.Health.Healthz)
	mux.HandleFunc("GET /readyz", d.Health.Readyz)
	mux.Handle("GET /metrics", d.Metrics.Handler())

	// resolveRole returns the user's GLOBAL role to inject it into the session
	// context. A user without a global role but with some 'cluster'-scoped
	// assignment is NOT blocked here (empty role, not an error): it would be
	// wrong to treat them as having no permissions when they can indeed operate
	// on some specific cluster — that is resolved further down, per cluster,
	// with resolveClusterRole. Only a user with NO assignment at all (neither
	// global nor cluster) returns rbac.ErrNoRole.
	resolveRole := func(ctx context.Context, userID int64) (rbac.Role, error) {
		role, err := rbac.EffectiveGlobalRole(ctx, d.DB, userID)
		if err == nil {
			return role, nil
		}
		if !errors.Is(err, rbac.ErrNoRole) {
			return "", err
		}
		hasAny, err := rbac.HasAnyAssignment(ctx, d.DB, userID)
		if err != nil {
			return "", err
		}
		if !hasAny {
			return "", rbac.ErrNoRole
		}
		return "", nil // authenticated, no global role, but with some cluster-scoped assignment
	}
	resolveClusterRole := func(ctx context.Context, userID, clusterID int64) (rbac.Role, error) {
		return rbac.EffectiveRoleForCluster(ctx, d.DB, userID, clusterID)
	}
	// Auditing of authorization denials (403) on mutating routes: until now
	// audit_log only collected successful actions and a probe of admin
	// endpoints left no trace. auditNoRole covers the 403 emitted by
	// RequireSession, which happens before auditDenied can see it.
	auditDenied := middleware.AuditDenied(d.DB, d.Logger)
	auditNoRole := func(r *http.Request, sess *session.Session, reason string) {
		if !middleware.IsMutating(r.Method) {
			return
		}
		audit.Log(context.WithoutCancel(r.Context()), d.DB, d.Logger, audit.Entry{
			UserID: &sess.UserID, ActorUsername: sess.Username, IP: middleware.ClientIP(r),
			Action: "access.denied", Result: audit.ResultDenied,
			Detail: map[string]any{"method": r.Method, "path": r.URL.Path, "reason": reason},
		})
	}
	requireSession := middleware.RequireSession(d.Sessions, resolveRole, auditNoRole)
	// Short chain for global mutating routes (not of a specific cluster): valid
	// session + denial auditing + CSRF + GLOBAL admin role. Cluster management
	// and user/role management use this one; restart/scale use the
	// cluster-scope chain, below.
	requireAdminMutation := func(h http.HandlerFunc) http.Handler {
		return requireSession(auditDenied(middleware.RequireCSRF(middleware.RequireAdmin(h))))
	}
	// Admin-only reads that mutate nothing (e.g. user listing).
	requireAdmin := func(h http.HandlerFunc) http.Handler {
		return requireSession(middleware.RequireAdmin(h))
	}
	// Reading the resources of a specific cluster: requires at least readonly
	// over THAT cluster (global or 'cluster'-scoped for its id).
	requireClusterRead := func(h http.HandlerFunc) http.Handler {
		return requireSession(middleware.RequireClusterRole(resolveClusterRole, rbac.RoleReadonly)(h))
	}
	// Mutations on workloads (restart/scale/rollback/apply/cordon): requires at
	// least EDITOR over THAT cluster, plus CSRF. The handler resolves the exact
	// role to pick the impersonated group.
	requireClusterEditorMutation := func(h http.HandlerFunc) http.Handler {
		return requireSession(auditDenied(middleware.RequireCSRF(middleware.RequireClusterRole(resolveClusterRole, rbac.RoleEditor)(h))))
	}
	// Destructive or high-privilege mutations (delete, drain): admin over THAT
	// cluster, plus CSRF.
	requireClusterAdminMutation := func(h http.HandlerFunc) http.Handler {
		return requireSession(auditDenied(middleware.RequireCSRF(middleware.RequireClusterRole(resolveClusterRole, rbac.RoleAdmin)(h))))
	}
	// Interactive terminal (exec): same minimum role as
	// requireClusterAdminMutation (admin over THAT cluster — exec is a write)
	// but WITHOUT RequireCSRF. RequireCSRF is a no-op on GET (csrf.go), and the
	// exec WS handshake is a GET that the browser cannot accompany with
	// X-CSRF-Token either (`new WebSocket(...)` does not accept headers) —
	// chaining it here would give a false sense of protection. The real
	// defense is originAllowed() + SameSite=Lax, same as on the logs route
	// (requireClusterRead).
	requireClusterExec := func(h http.HandlerFunc) http.Handler {
		return requireSession(middleware.RequireClusterRole(resolveClusterRole, rbac.RoleAdmin)(h))
	}

	// --- Auth ---
	authHandler := &AuthHandler{
		DB: d.DB, Sessions: d.Sessions, Logger: d.Logger, LoginLimiter: d.LoginLimiter, AccountLimiter: d.AccountLimiter, Keyring: d.Keyring,
		SSO: d.OIDC.Manager, Metrics: d.Metrics,
	}

	mux.HandleFunc("POST /api/auth/login", authHandler.Login)
	mux.HandleFunc("POST /api/auth/setup", authHandler.Setup)
	mux.HandleFunc("GET /api/auth/status", authHandler.Status)

	mux.Handle("GET /api/auth/me", requireSession(http.HandlerFunc(authHandler.Me)))
	mux.Handle("POST /api/auth/logout", requireSession(middleware.RequireCSRF(http.HandlerFunc(authHandler.Logout))))
	mux.Handle("POST /api/auth/change-password", requireSession(middleware.RequireCSRF(http.HandlerFunc(authHandler.ChangePassword))))
	// Profile and preferences of the session's user: no permissions needed
	// beyond having a session, because only one's own can be modified (the id
	// comes from the session, never from the request body).
	mux.Handle("PATCH /api/auth/profile", requireSession(middleware.RequireCSRF(http.HandlerFunc(authHandler.UpdateProfile))))
	mux.Handle("PATCH /api/auth/preferences", requireSession(middleware.RequireCSRF(http.HandlerFunc(authHandler.UpdatePreferences))))
	// Personal API tokens: only from the cookie session (the handler checks
	// it); the resulting token is valid for any route with a session.
	mux.Handle("GET /api/auth/tokens", requireSession(http.HandlerFunc(authHandler.ListTokens)))
	mux.Handle("POST /api/auth/tokens", requireSession(middleware.RequireCSRF(http.HandlerFunc(authHandler.CreateToken))))
	mux.Handle("DELETE /api/auth/tokens/{id}", requireSession(middleware.RequireCSRF(http.HandlerFunc(authHandler.RevokeToken))))
	mux.Handle("GET /api/openapi.json", requireSession(http.HandlerFunc(OpenAPI)))

	// --- SSO OIDC ---
	// Always registered: the provider is enabled and disabled on the fly from
	// Settings, and the handler responds 404 while there is none. The /entra/
	// routes are aliases so as not to break the app registrations created with
	// the old redirect URI.
	mux.HandleFunc("GET /api/auth/oidc/login", d.OIDC.Login)
	mux.HandleFunc("GET /api/auth/oidc/callback", d.OIDC.Callback)
	mux.HandleFunc("GET /api/auth/entra/login", d.OIDC.Login)
	mux.HandleFunc("GET /api/auth/entra/callback", d.OIDC.Callback)

	// --- Clusters (management) ---
	// Creating/enabling clusters is a global action (it does not depend on an
	// already existing cluster), which is why it stays in requireAdminMutation.
	// The listing is filtered inside the handler itself according to the
	// user's role (see ClusterHandler.List): global sees all, 'cluster' scope
	// sees only its own.
	clusterHandler := &ClusterHandler{DB: d.DB, Logger: d.Logger, Keyring: d.Keyring, K8s: d.K8sManager, Settings: d.Settings, AgentAuth: d.AgentAuth, ServerCA: d.AgentCA}
	if d.Docker != nil {
		clusterHandler.Docker = d.Docker.Docker
	}
	if d.Cloud != nil {
		clusterHandler.Cloud = d.Cloud.Cloud
	}

	mux.Handle("GET /api/clusters", requireSession(http.HandlerFunc(clusterHandler.List)))
	mux.Handle("POST /api/clusters", requireAdminMutation(clusterHandler.Create))
	mux.Handle("PATCH /api/clusters/{id}", requireAdminMutation(clusterHandler.Update))
	mux.Handle("DELETE /api/clusters/{id}", requireAdminMutation(clusterHandler.Delete))
	// Agent connector: regenerate the manifest URL (admin) and serve it (no
	// session: kubectl downloads it with a 1 h token). The agent's WebSocket
	// authenticates on its own with the cluster key (see agent.Hub).
	mux.Handle("POST /api/clusters/{id}/agent/manifest", requireAdminMutation(clusterHandler.RegenerateManifest))
	mux.HandleFunc("GET /api/agent/manifest/{id}/{token}", clusterHandler.AgentManifest)
	if d.AgentHub != nil {
		mux.Handle("GET /api/agent/connect", d.AgentHub)
		// The agent keeps its RBAC up to date: asks for the current one and reports what it applied.
		mux.HandleFunc("GET /api/agent/rbac", clusterHandler.AgentRBAC)
		mux.HandleFunc("POST /api/agent/rbac/status", clusterHandler.AgentRBACStatus)
	}
	// Cloud credentials and discovery: global admin only, like the rest of
	// cluster management. The RBAC of a cloud cluster is served again from the
	// cluster row.
	if d.Cloud != nil {
		mux.Handle("GET /api/cloud/credentials", requireAdmin(d.Cloud.List))
		mux.Handle("POST /api/cloud/credentials", requireAdminMutation(d.Cloud.Create))
		mux.Handle("PATCH /api/cloud/credentials/{id}", requireAdminMutation(d.Cloud.Update))
		mux.Handle("DELETE /api/cloud/credentials/{id}", requireAdminMutation(d.Cloud.Delete))
		mux.Handle("GET /api/cloud/credentials/{id}/clusters", requireAdmin(d.Cloud.Discover))
		mux.Handle("GET /api/clusters/{id}/cloud/rbac", requireAdmin(clusterHandler.CloudRBAC))
	}

	// --- Fleet (every cluster visible to the user) ---
	// No cluster-scope middleware because the request is not against a
	// specific one: the handler narrows the set according to permissions, same
	// as ClusterHandler.List.
	fleetHandler := &FleetHandler{DB: d.DB, Logger: d.Logger, K8s: d.K8sManager}
	mux.Handle("GET /api/fleet/attention", requireSession(http.HandlerFunc(fleetHandler.Attention)))

	// --- Cluster resources (read from the informer cache) ---
	// Each route requires at least the readonly role OVER THAT specific
	// CLUSTER (global or 'cluster'-scoped), so that a permission scoped to one
	// cluster means something real and not just window dressing.
	resourceHandler := &ResourceHandler{DB: d.DB, Logger: d.Logger, K8s: d.K8sManager, Settings: d.Settings}

	mux.Handle("GET /api/clusters/{id}/namespaces", requireClusterRead(resourceHandler.Namespaces))
	mux.Handle("GET /api/clusters/{id}/pods", requireClusterRead(resourceHandler.Pods))
	mux.Handle("GET /api/clusters/{id}/deployments", requireClusterRead(resourceHandler.Deployments))
	mux.Handle("GET /api/clusters/{id}/statefulsets", requireClusterRead(resourceHandler.StatefulSets))
	mux.Handle("GET /api/clusters/{id}/daemonsets", requireClusterRead(resourceHandler.DaemonSets))
	mux.Handle("GET /api/clusters/{id}/deployments/{namespace}/{name}/pods", requireClusterRead(resourceHandler.DeploymentPods))
	mux.Handle("GET /api/clusters/{id}/services", requireClusterRead(resourceHandler.Services))
	mux.Handle("GET /api/clusters/{id}/nodes", requireClusterRead(resourceHandler.Nodes))
	mux.Handle("GET /api/clusters/{id}/events", requireClusterRead(resourceHandler.Events))
	mux.Handle("GET /api/clusters/{id}/summary", requireClusterRead(resourceHandler.Summary))
	mux.Handle("GET /api/clusters/{id}/overview", requireClusterRead(resourceHandler.Overview))
	mux.Handle("GET /api/clusters/{id}/topology", requireClusterRead(resourceHandler.Topology))
	// Real consumption (metrics-server): same read as the rest, served from the
	// last polled snapshot, not from the informer cache (metrics.k8s.io does not support watch).
	mux.Handle("GET /api/clusters/{id}/metrics", requireClusterRead(resourceHandler.Usage))
	mux.Handle("GET /api/clusters/{id}/metrics/pods", requireClusterRead(resourceHandler.PodUsage))
	mux.Handle("GET /api/clusters/{id}/costs", requireClusterRead(resourceHandler.Costs))
	mux.Handle("GET /api/clusters/{id}/metrics-server/manifest", requireClusterExec(resourceHandler.MetricsServerManifest))
	mux.Handle("POST /api/clusters/{id}/metrics-server", requireClusterAdminMutation(resourceHandler.InstallMetricsServer))

	// --- Secondary kinds (optional informers: if the cluster lacks the RBAC
	// for one, only that listing responds 503) ---
	mux.Handle("GET /api/clusters/{id}/kinds", requireClusterRead(resourceHandler.Kinds))
	mux.Handle("GET /api/clusters/{id}/configmaps", requireClusterRead(resourceHandler.ConfigMaps))
	mux.Handle("GET /api/clusters/{id}/secrets", requireClusterRead(resourceHandler.Secrets))
	mux.Handle("GET /api/clusters/{id}/serviceaccounts", requireClusterRead(resourceHandler.ServiceAccounts))
	mux.Handle("GET /api/clusters/{id}/ingresses", requireClusterRead(resourceHandler.Ingresses))
	mux.Handle("GET /api/clusters/{id}/networkpolicies", requireClusterRead(resourceHandler.NetworkPolicies))
	mux.Handle("GET /api/clusters/{id}/persistentvolumeclaims", requireClusterRead(resourceHandler.PVCs))
	mux.Handle("GET /api/clusters/{id}/persistentvolumes", requireClusterRead(resourceHandler.PVs))
	mux.Handle("GET /api/clusters/{id}/storageclasses", requireClusterRead(resourceHandler.StorageClasses))
	mux.Handle("GET /api/clusters/{id}/jobs", requireClusterRead(resourceHandler.Jobs))
	mux.Handle("GET /api/clusters/{id}/cronjobs", requireClusterRead(resourceHandler.CronJobs))
	mux.Handle("GET /api/clusters/{id}/horizontalpodautoscalers", requireClusterRead(resourceHandler.HPAs))
	mux.Handle("GET /api/clusters/{id}/resourcequotas", requireClusterRead(resourceHandler.ResourceQuotas))
	mux.Handle("GET /api/clusters/{id}/limitranges", requireClusterRead(resourceHandler.LimitRanges))
	// Namespace quota ("project"): Namespace + ResourceQuota + LimitRange in one go.
	mux.Handle("POST /api/clusters/{id}/namespaces/{namespace}/quota", requireClusterEditorMutation(resourceHandler.ApplyNamespaceQuota))

	// --- View YAML of any cached kind ("-" as namespace for the non-namespaced ones) ---
	mux.Handle("GET /api/clusters/{id}/resources/{kind}/{namespace}/{name}/yaml", requireClusterRead(resourceHandler.ResourceYAML))
	// Old per-kind aliases.
	mux.Handle("GET /api/clusters/{id}/pods/{namespace}/{name}/yaml", requireClusterRead(resourceHandler.PodYAML))
	mux.Handle("GET /api/clusters/{id}/pods/{namespace}/{name}/logs", requireClusterRead(resourceHandler.PodLogs))
	mux.Handle("GET /api/clusters/{id}/deployments/{namespace}/{name}/yaml", requireClusterRead(resourceHandler.DeploymentYAML))
	mux.Handle("GET /api/clusters/{id}/services/{namespace}/{name}/yaml", requireClusterRead(resourceHandler.ServiceYAML))
	mux.Handle("GET /api/clusters/{id}/nodes/{name}/yaml", requireClusterRead(resourceHandler.NodeYAML))

	// --- Mutating actions, impersonated against K8s with the effective role's group ---
	// Editor: operate workloads. Admin: additionally delete and drain.
	mux.Handle("POST /api/clusters/{id}/apply", requireClusterEditorMutation(resourceHandler.Apply))
	mux.Handle("POST /api/clusters/{id}/{kind}/{namespace}/{name}/restart", requireClusterEditorMutation(resourceHandler.RestartWorkload))
	mux.Handle("POST /api/clusters/{id}/{kind}/{namespace}/{name}/scale", requireClusterEditorMutation(resourceHandler.ScaleWorkload))
	mux.Handle("POST /api/clusters/{id}/deployments/{namespace}/{name}/rollback", requireClusterEditorMutation(resourceHandler.RollbackDeployment))
	mux.Handle("POST /api/clusters/{id}/nodes/{name}/cordon", requireClusterEditorMutation(resourceHandler.CordonNode))
	mux.Handle("POST /api/clusters/{id}/nodes/{name}/drain", requireClusterAdminMutation(resourceHandler.DrainNode))
	mux.Handle("DELETE /api/clusters/{id}/resources/{kind}/{namespace}/{name}", requireClusterAdminMutation(resourceHandler.DeleteResource))

	// --- Streaming pod logs ---
	mux.Handle("GET /ws/clusters/{id}/namespaces/{namespace}/pods/{pod}/logs", requireClusterRead(d.LogsHandler.ServeHTTP))

	// --- Interactive terminal in containers (pods/exec) ---
	// Only if the binary was started with an ExecHandler (always, see main.go)
	// — defensive check in case it is ever made optional.
	if d.ExecHandler != nil {
		mux.Handle("GET /ws/clusters/{id}/namespaces/{namespace}/pods/{pod}/exec", requireClusterExec(d.ExecHandler.ServeHTTP))
	}

	// --- User and role management (scoped RBAC; global admin only) ---
	usersHandler := &UsersHandler{DB: d.DB, Logger: d.Logger}
	mux.Handle("GET /api/users", requireAdmin(usersHandler.List))
	mux.Handle("POST /api/users", requireAdminMutation(usersHandler.Create))
	mux.Handle("PATCH /api/users/{id}", requireAdminMutation(usersHandler.SetDisabled))
	mux.Handle("DELETE /api/users/{id}", requireAdminMutation(usersHandler.Delete))
	mux.Handle("GET /api/users/{id}/access", requireAdmin(usersHandler.Access))
	mux.Handle("POST /api/users/{id}/roles", requireAdminMutation(usersHandler.GrantRole))
	mux.Handle("DELETE /api/users/{id}/roles/{assignmentId}", requireAdminMutation(usersHandler.RevokeRole))

	// --- IdP group to role mapping (global admin only) ---
	// Main source of roles for SSO: without a row covering one of the user's
	// groups (nor a manual assignment), the login fails with no_role_mapped.
	groupMappingHandler := &GroupMappingHandler{DB: d.DB, Logger: d.Logger}
	mux.Handle("GET /api/group-mappings", requireAdmin(groupMappingHandler.List))
	mux.Handle("POST /api/group-mappings", requireAdminMutation(groupMappingHandler.Create))
	mux.Handle("DELETE /api/group-mappings/{id}", requireAdminMutation(groupMappingHandler.Delete))

	// --- Helm: repos (platform, global admin), catalog (session) and releases per cluster ---
	if d.Helm != nil {
		mux.Handle("GET /api/helm/repos", requireSession(http.HandlerFunc(d.Helm.ListRepos)))
		mux.Handle("POST /api/helm/repos", requireAdminMutation(d.Helm.CreateRepo))
		mux.Handle("DELETE /api/helm/repos/{id}", requireAdminMutation(d.Helm.DeleteRepo))
		mux.Handle("GET /api/helm/repos/{repoId}/charts", requireSession(http.HandlerFunc(d.Helm.SearchCharts)))
		mux.Handle("GET /api/helm/repos/{repoId}/charts/{chart}", requireSession(http.HandlerFunc(d.Helm.ChartInfo)))
		mux.Handle("GET /api/clusters/{id}/helm/releases", requireClusterRead(d.Helm.ListReleases))
		mux.Handle("GET /api/clusters/{id}/helm/releases/{namespace}/{name}", requireClusterRead(d.Helm.GetRelease))
		mux.Handle("POST /api/clusters/{id}/helm/releases", requireClusterEditorMutation(d.Helm.Install))
		mux.Handle("PUT /api/clusters/{id}/helm/releases/{namespace}/{name}", requireClusterEditorMutation(d.Helm.Upgrade))
		mux.Handle("POST /api/clusters/{id}/helm/releases/{namespace}/{name}/rollback", requireClusterEditorMutation(d.Helm.Rollback))
		mux.Handle("DELETE /api/clusters/{id}/helm/releases/{namespace}/{name}", requireClusterAdminMutation(d.Helm.Uninstall))
	}

	// --- GitOps: git sources per cluster (editor creates/pauses/syncs; admin deletes) ---
	if d.GitOps != nil {
		mux.Handle("GET /api/clusters/{id}/gitops", requireClusterRead(d.GitOps.List))
		mux.Handle("POST /api/clusters/{id}/gitops", requireClusterEditorMutation(d.GitOps.Create))
		mux.Handle("PATCH /api/clusters/{id}/gitops/{sid}", requireClusterEditorMutation(d.GitOps.SetEnabled))
		mux.Handle("POST /api/clusters/{id}/gitops/{sid}/sync", requireClusterEditorMutation(d.GitOps.Sync))
		mux.Handle("DELETE /api/clusters/{id}/gitops/{sid}", requireClusterAdminMutation(d.GitOps.Delete))
		// No session or CSRF: the git provider calls it; it authenticates the source's token.
		mux.HandleFunc("POST /api/gitops/webhook/{sid}", d.GitOps.Webhook)
	}

	// --- Docker/Podman environments (kind=docker): same per-cluster RBAC ---
	if d.Docker != nil {
		mux.Handle("GET /api/clusters/{id}/docker/info", requireClusterRead(d.Docker.Info))
		mux.Handle("GET /api/clusters/{id}/docker/containers", requireClusterRead(d.Docker.Containers))
		mux.Handle("GET /api/clusters/{id}/docker/containers/{cid}/inspect", requireClusterRead(d.Docker.ContainerInspect))
		mux.Handle("POST /api/clusters/{id}/docker/containers/{cid}/action/{action}", requireClusterEditorMutation(d.Docker.ContainerAction))
		mux.Handle("DELETE /api/clusters/{id}/docker/containers/{cid}", requireClusterAdminMutation(d.Docker.RemoveContainer))
		mux.Handle("GET /api/clusters/{id}/docker/images", requireClusterRead(d.Docker.Images))
		mux.Handle("POST /api/clusters/{id}/docker/images/pull", requireClusterEditorMutation(d.Docker.PullImage))
		mux.Handle("DELETE /api/clusters/{id}/docker/images/{iid}", requireClusterAdminMutation(d.Docker.RemoveImage))
		mux.Handle("GET /api/clusters/{id}/docker/volumes", requireClusterRead(d.Docker.Volumes))
		mux.Handle("DELETE /api/clusters/{id}/docker/volumes/{name}", requireClusterAdminMutation(d.Docker.RemoveVolume))
		mux.Handle("GET /api/clusters/{id}/docker/networks", requireClusterRead(d.Docker.Networks))
		mux.Handle("DELETE /api/clusters/{id}/docker/networks/{nid}", requireClusterAdminMutation(d.Docker.RemoveNetwork))
	}
	if d.KubectlWS != nil {
		// Same role and same defense as the pod terminal (Origin + SameSite, no CSRF).
		mux.Handle("GET /ws/clusters/{id}/kubectl", requireClusterExec(d.KubectlWS.ServeHTTP))
	}
	if d.DockerWS != nil {
		mux.Handle("GET /ws/clusters/{id}/docker/containers/{cid}/logs", requireClusterRead(d.DockerWS.Logs))
		mux.Handle("GET /ws/clusters/{id}/docker/containers/{cid}/exec", requireClusterExec(d.DockerWS.Terminal))
	}

	// --- Image registries: platform (admin) and bindings per cluster (editor) ---
	if d.Extensions != nil {
		mux.Handle("GET /api/extensions", requireAdmin(http.HandlerFunc(d.Extensions.List)))
		mux.Handle("POST /api/extensions", requireAdminMutation(d.Extensions.Create))
		mux.Handle("PUT /api/extensions/{id}", requireAdminMutation(d.Extensions.Update))
		mux.Handle("DELETE /api/extensions/{id}", requireAdminMutation(d.Extensions.Delete))
		mux.Handle("GET /api/clusters/{id}/extensions", requireClusterRead(d.Extensions.ListForCluster))
	}
	if d.Registries != nil {
		mux.Handle("GET /api/registries", requireSession(http.HandlerFunc(d.Registries.List)))
		mux.Handle("POST /api/registries", requireAdminMutation(d.Registries.Create))
		mux.Handle("PATCH /api/registries/{id}", requireAdminMutation(d.Registries.Update))
		mux.Handle("DELETE /api/registries/{id}", requireAdminMutation(d.Registries.Delete))
		mux.Handle("GET /api/clusters/{id}/registries", requireClusterRead(d.Registries.ListBindings))
		mux.Handle("POST /api/clusters/{id}/registries", requireClusterEditorMutation(d.Registries.CreateBinding))
		mux.Handle("POST /api/clusters/{id}/registries/{bid}/sync", requireClusterEditorMutation(d.Registries.SyncBinding))
		mux.Handle("DELETE /api/clusters/{id}/registries/{bid}", requireClusterEditorMutation(d.Registries.DeleteBinding))
	}

	// --- Backups: targets (global admin) and schedules per cluster (cluster admin) ---
	// Reads also require admin over the cluster (requireClusterExec: admin
	// without CSRF, which is what fits a GET): a backup may contain Secrets and
	// its download is audited.
	if d.Backups != nil {
		mux.Handle("GET /api/backup/targets", requireSession(http.HandlerFunc(d.Backups.ListTargets)))
		if d.Backups.Server != nil {
			mux.Handle("GET /api/admin/server-backup", requireAdmin(d.Backups.GetServerBackup))
			mux.Handle("PUT /api/admin/server-backup", requireAdminMutation(d.Backups.UpdateServerBackup))
			mux.Handle("POST /api/admin/server-backup/run", requireAdminMutation(d.Backups.RunServerBackup))
			mux.Handle("GET /api/admin/server-backup/download", requireAdmin(d.Backups.DownloadServerBackup))
		}
		mux.Handle("POST /api/backup/targets", requireAdminMutation(d.Backups.CreateTarget))
		mux.Handle("PATCH /api/backup/targets/{id}", requireAdminMutation(d.Backups.UpdateTarget))
		mux.Handle("DELETE /api/backup/targets/{id}", requireAdminMutation(d.Backups.DeleteTarget))
		mux.Handle("GET /api/clusters/{id}/backups", requireClusterExec(d.Backups.ListSchedules))
		mux.Handle("POST /api/clusters/{id}/backups", requireClusterAdminMutation(d.Backups.CreateSchedule))
		mux.Handle("PATCH /api/clusters/{id}/backups/{sid}", requireClusterAdminMutation(d.Backups.UpdateSchedule))
		mux.Handle("DELETE /api/clusters/{id}/backups/{sid}", requireClusterAdminMutation(d.Backups.DeleteSchedule))
		mux.Handle("POST /api/clusters/{id}/backups/{sid}/run", requireClusterAdminMutation(d.Backups.RunNow))
		mux.Handle("GET /api/clusters/{id}/backups/runs", requireClusterExec(d.Backups.ListRuns))
		mux.Handle("GET /api/clusters/{id}/backups/runs/{rid}/download", requireClusterExec(d.Backups.Download))
		mux.Handle("GET /api/clusters/{id}/backups/runs/{rid}/index", requireClusterExec(d.Backups.Index))
		mux.Handle("POST /api/clusters/{id}/backups/runs/{rid}/restore", requireClusterAdminMutation(d.Backups.Restore))
	}

	// --- Images and vulnerability scanning ---
	if d.Images != nil {
		mux.Handle("GET /api/scan/status", requireSession(http.HandlerFunc(d.Images.Status)))
		mux.Handle("GET /api/images/report", requireSession(http.HandlerFunc(d.Images.Report)))
		mux.Handle("GET /api/clusters/{id}/images", requireClusterRead(d.Images.List))
		mux.Handle("POST /api/clusters/{id}/images/scan", requireClusterEditorMutation(d.Images.Enqueue))
	}

	// --- Notifications (global admin only) ---
	if d.Notify != nil {
		mux.Handle("GET /api/notifications/options", requireAdmin(d.Notify.Options))
		mux.Handle("GET /api/notifications/channels", requireAdmin(d.Notify.ListChannels))
		mux.Handle("POST /api/notifications/channels", requireAdminMutation(d.Notify.CreateChannel))
		mux.Handle("PATCH /api/notifications/channels/{id}", requireAdminMutation(d.Notify.UpdateChannel))
		mux.Handle("DELETE /api/notifications/channels/{id}", requireAdminMutation(d.Notify.DeleteChannel))
		mux.Handle("POST /api/notifications/channels/{id}/test", requireAdminMutation(d.Notify.TestChannel))
		mux.Handle("GET /api/notifications/rules", requireAdmin(d.Notify.ListRules))
		mux.Handle("POST /api/notifications/rules", requireAdminMutation(d.Notify.CreateRule))
		mux.Handle("PATCH /api/notifications/rules/{id}", requireAdminMutation(d.Notify.UpdateRule))
		mux.Handle("DELETE /api/notifications/rules/{id}", requireAdminMutation(d.Notify.DeleteRule))
		mux.Handle("GET /api/notifications/deliveries", requireAdmin(d.Notify.Deliveries))
	}

	// --- Instance configuration (global admin only) ---
	settingsHandler := &SettingsHandler{DB: d.DB, Logger: d.Logger, Store: d.Settings, OIDC: d.OIDC, TLSMode: d.TLSMode, AgentCA: d.AgentCA}
	mux.Handle("GET /api/settings", requireAdmin(settingsHandler.Get))
	// --- AI "Explain" (internal/ai) ---
	// Explain is a POST that spends money and sends data out: CSRF and denial
	// auditing like a mutation, readonly as the floor here and the configured
	// minimum role checked by the handler (it lives in settings).
	aiHandler := &AIHandler{ResourceHandler: resourceHandler, Client: ai.NewClient()}
	mux.Handle("GET /api/ai/status", requireSession(http.HandlerFunc(aiHandler.Status)))
	mux.Handle("POST /api/settings/ai/test", requireAdminMutation(aiHandler.Test))
	// The assistant is not tied to one cluster: the gate inside checks the
	// AI minimum role per cluster as the model touches them.
	mux.Handle("POST /api/assistant/chat", requireSession(auditDenied(middleware.RequireCSRF(http.HandlerFunc(aiHandler.Assistant)))))
	mux.Handle("POST /api/clusters/{id}/explain", requireSession(auditDenied(middleware.RequireCSRF(middleware.RequireClusterRole(resolveClusterRole, rbac.RoleReadonly)(http.HandlerFunc(aiHandler.Explain))))))
	mux.Handle("PUT /api/settings/{key}", requireAdminMutation(settingsHandler.Update))

	// --- Audit (read-only, global admin only) ---
	// They are GET and therefore carry no CSRF (it would be a no-op), but they
	// do requireAdmin: the audit log holds what each person did and from which
	// IP.
	auditHandler := &AuditHandler{DB: d.DB, Logger: d.Logger}
	mux.Handle("GET /api/audit", requireAdmin(auditHandler.List))
	mux.Handle("GET /api/audit/export", requireAdmin(auditHandler.ExportCSV))

	// --- MCP server for AI clients (internal/mcp) ---
	// API tokens only (checked inside). Every tool is dispatched to this same
	// mux with the caller's token, so it inherits all the middlewares above;
	// that is why it gets the router once it is complete.
	mcpServer := mcp.NewServer(d.Logger, settings.ServerVersion)
	mux.Handle("/api/mcp", requireSession(mcpServer))

	mux.Handle("/", d.Web)

	mcpServer.API = mux
	aiHandler.MCP = mcpServer
	return mux
}
