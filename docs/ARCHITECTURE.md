# Architecture

How Bayplan is built and why: the decisions behind each part, and the ones that should not be
reverted without reading the reason first. For features see `README.md`; for deployment,
`deploy/README.md`; for the workflow, `CONTRIBUTING.md`.

## What this is

Bayplan: generic, self-hosted multi-cluster Kubernetes management. Observability from one
informer cache per cluster + audited write actions (restart, scale, rollback, cordon/drain, apply
YAML with a prior dry-run, delete, logs, terminal), local login or generic OIDC SSO, and scoped
RBAC with three roles (`admin` > `editor` > `readonly`). It does **not** provision clusters or
upgrade their Kubernetes version, nor does it try to be a full GitOps system. See `README.md` for
features and `deploy/README.md` for deployment (Podman Quadlet).

The Go backend (`backend/`) embeds the already-built React frontend (`frontend/`) via
`embed.FS` — the production artifact is a single binary.

## Commands

Everything goes through the `Makefile` (`make help`); the dev environment targets (`make dev`,
`make web`, `make restart`, `make stop`…) call `scripts/dev.sh`. There is no `run.sh`: the
Makefile is the usual entry point of Go projects.

```bash
make dev            # Postgres in Docker/Podman (CONTAINER_ENGINE) + build + backend in the background, admin/devlocal12345
make web            # Vite with hot reload on :5173 (another terminal)
make check          # what CI runs: lint + tests
make build          # build frontend + embed + build backend -> bin/bayplan
make test           # go test ./... (backend)
make vet            # go vet ./...
make lint           # gofmt -l . + go vet + frontend lint (oxlint)
make run            # build + start ./bin/bayplan

# a single Go test
cd backend && go test ./internal/rbac/... -run TestRoleAtLeast -v

# frontend
cd frontend && npm run dev      # vite server, proxies /api and /ws to :8080 (see vite.config.ts)
cd frontend && npm run build    # tsc -b && vite build
cd frontend && npm run lint     # oxlint + locale key check
cd frontend && npx tsc -b        # type check only (see note below)
```

**Do not use `npx tsc --noEmit` to type-check: it gives false greens.** `frontend/tsconfig.json`
is a solution file (`"files": []` + `references`), so `--noEmit` on it does not check a single
file under `src/`. Use `npx tsc -b`, which is what `npm run build` actually runs — a real error
can pass `--noEmit` and then break `make build`.

Local development needs a Postgres and environment variables (`DATABASE_URL`,
`ENCRYPTION_KEY_ACTIVE_ID`, `ENCRYPTION_KEY_V1`, `COOKIE_SECURE=false`) — see "Local
development" in `README.md` for the exact startup sequence (one-time setup token; a default
admin password is never generated or logged).

## Architecture

### Reads vs. writes against managed clusters

`internal/k8s.Manager` keeps one `SharedInformerFactory` per connected cluster
(`internal/k8s/manager.go`). **Every read endpoint serves from this in-memory informer cache** —
it never makes a live call to the managed cluster's API server. Mutating actions (restart, scale,
exec) go straight to the API server with their own clientset built on each call.

Mutating actions impersonate the real user (`Impersonate-User: bayplan:<username>`) so that the
managed cluster's native audit attributes the action correctly, but **authorization always comes
from an impersonated *group*** (`bayplan:admins` / `bayplan:editors` / `bayplan:readonly`,
`k8s.GroupForRole` in `internal/k8s/actions.go`, chosen from the user's EFFECTIVE role on that
cluster) — the impersonated username on its own has no RoleBinding. Writes live in
`internal/k8s/write.go` (apply with server-side apply and field manager `bayplan`, delete via the
dynamic client + RESTMapper, restart/scale/rollback, cordon/drain with the Eviction API) and their
handlers in `api/write.go`: the API server's error is returned verbatim as a 502, because it is
the real explanation (cluster RBAC, admission…). `internal/k8s/exec.go` replicates this pattern for
`pods/exec` (SPDY executor, chosen over the newer WebSocket executor for broader compatibility
across cluster versions). **It is built with `spdyExecutorFor`, not `remotecommand.NewSPDYExecutor`**:
both stock executors dial the API server with their own `net.Dialer` and ignore `rest.Config.Dial`,
so through the agent tunnel the upgrade request tried to resolve `agent.bayplan.internal` on the host
and the terminal failed only on agent clusters. The SPDY round tripper is given the transport
client-go derives from the config (`UpgradeTransport`), which carries the dialer, the TLS config and
the cloud token like every other call.

### HTTP routes and the scoped RBAC middleware chain

`internal/api/router.go` builds a handful of composable middleware chains on top of
`internal/middleware`:
- `requireSession` — valid session + resolves the user's *global* role into the context.
- `requireAdminMutation` — session + CSRF + global admin. For platform actions (create/enable/
  delete a cluster, grant/revoke roles) — not tied to a specific cluster.
- `requireClusterRead` / `requireClusterAdminMutation` — session + role resolved *for that
  specific cluster* (global role OR a `role_assignments` row scoped to that cluster, highest
  privilege wins — see `rbac.EffectiveRoleForCluster`). Used on every per-cluster resource route
  and on restart/scale.
- The WebSocket routes (logs, exec) deliberately use a variant **without** `RequireCSRF`: the
  CSRF middleware is a no-op on GET (the WS handshake), and the browser cannot add a custom
  header to `new WebSocket(...)` either — the real defense there is the `Origin` check inside the
  handler plus `SameSite=Lax` on the session cookie. Do not "fix" this by adding CSRF to a WS
  route: it would be a no-op that gives a false sense of protection.

`role_assignments.scope_ref` and `group_role_mappings.*_ref` are `TEXT`, **not a real FK** to
`clusters.id` — validated only at the application level. Anything that deletes a cluster must
also explicitly delete the `scope_type='cluster'` rows pointing at it in both tables (see
`ClusterHandler.Delete`), or they are left orphaned.

The `namespace` scope exists in the schema (`enum scope_type`) but the role-granting API
**deliberately rejects it** (`UsersHandler.GrantRole`) — no read filters by namespace, so
allowing it would be a permission that appears to restrict without restricting anything at all.
Do not enable it without also filtering every read of the informer cache by namespace.

### Adding a resource type means touching the managed cluster

Every new informer needs its verb in the `bayplan-technical` `ClusterRole`
(`deploy/rbac/serviceaccount.yaml`) **and that YAML must be reapplied on every managed cluster**.
There are two classes of informer, and the difference is deliberate:

- **Required** (pods, deployments, statefulsets, daemonsets, replicasets, services, nodes,
  namespaces, events; the `required` list in `manager.go`): `WaitForCacheSync` waits for all of
  them, and if a permission is missing **every** read on the cluster responds 503. Without them
  the panel cannot say anything useful, and a "half-synced" cluster complicates everything else.
- **Optional** (`internal/k8s/optional.go`: configmaps, secrets, ingresses, jobs…): checked one by
  one on read (`getOptionalConn`) and their list/watch error is stored **per type**, not in the
  cluster's `lastError`. If a cluster lacks the permission, only that tab responds 503 with the
  reason (`GET /api/clusters/{id}/kinds` exposes it) and the cluster stays green. A new type goes
  here: an entry in `registerOptionalInformers`, summary and lister in `read_extra.go`, a row in
  `yamlKinds` (`yaml.go`), a route, and its rule in the ClusterRole.

That is why deployment order still matters for the required ones (**RBAC first, binary
second**), but a new binary with new optional types can be deployed before reapplying the RBAC
without breaking anything.

To check permissions without deploying anything, a `SelfSubjectAccessReview` with the SA's token
answers on the spot (or `kubectl auth can-i list statefulsets
--as=system:serviceaccount:kube-system:bayplan-technical`).

**`deploy/rbac/admission-policy.yaml`** (three copies like the RBAC: `internal/agent/policy.yaml`,
the chart's `files/admission-policy.yaml`; `manifest_test.go` compares them) holds the
ValidatingAdmissionPolicies (1.30+) that only match the `bayplan:*` groups: no workload as the panel's
own SAs nor a token Secret for them (`k8s.GuardPanelIdentity` is the same rule in `Manager.Apply`,
so it holds on older clusters too), nothing in the agent's namespace, and, in scoped mode only, the
admin group's RBAC/APIService writes narrowed to metrics-server's objects, roleRefs and subjects
(`agent.AdmissionPolicies(clusterAdmin)` and the chart leave that one out in cluster-admin mode).
**Never scope a policy to a namespace with `namespaceSelector`**: it does not filter cluster-scoped
objects, and the chart's own-namespace policy used to deny every ClusterRole, Node and Namespace
write of the panel in the local cluster because of it; use a `matchCondition` on
`has(request.namespace) && request.namespace == '…'` (a Namespace object's request carries its name
there). The agent manifest puts them last, so on an older cluster kubectl has already created the
agent when it reports the unknown kinds.

The impersonated ClusterRoles (`bayplan-impersonated-admin`/`-readonly`) are **not** touched when
adding reads: reads go through the technical SA against the cache, not through the impersonated
identity. They only need touching when a write action is added. The `bayplan-technical` ClusterRole
is read-only plus `impersonate`: no write goes with the SA's own identity, so a new write action adds
its verbs to the impersonated ClusterRoles, never to the technical one.

### The cluster overview: thresholds, "reserved", and why the clock is a parameter

`GET /api/clusters/{id}/overview` (`api/overview.go`) composes in a single request the per-state
counters, the reserved capacity, the kubelet versions, the prioritized "needs attention" list and
the latest Warning events. All from the informer cache; it deliberately does **not** accept a
namespace filter (nodes, capacity and versions mean nothing scoped to one).

- The classification lives in `k8s/attention.go` and is **pure**: `PodIssue`/`DeploymentIssue`/
  `NodeIssue` receive the object and **the time as a parameter**. That is why they are testable
  without a DB or a cluster (`attention_test.go`) — it is the pattern this project follows in any
  non-trivial logic. Do not put `time.Now()` inside.
- There are **persistence thresholds** (`podGrace`, `rolloutGrace`, `nodeGrace`,
  `terminatingGrace`): without them any normal deployment shows up as a problem for a few seconds
  and the panel is always red, which is the fastest way to make nobody believe it. The exceptions
  without a grace period are on purpose: a `CrashLoopBackOff` or a deployment with 0 available
  replicas count from the very first moment.
- **`Capacity` is what is RESERVED (sum of requests), not consumption.** Confusing the two leads
  to errors in both directions. The UI labels it as such and must keep doing so. REAL consumption
  lives separately, in `k8s/metrics.go` (`GET /api/clusters/{id}/metrics` and `/metrics/pods`): it
  is the only thing in the panel that does not come from the informer cache, because
  `metrics.k8s.io` does not support watch; a `metricsLoop` per cluster polls every 30 s and keeps
  the latest snapshot in memory, and reads serve from there. metrics-server is **optional** in
  the cluster: without it (404) or without the SA's `metrics.k8s.io` permission (403) the response
  is 200 with `available=false` and the reason, and the UI shows only what is reserved, saying
  why. It talks to the raw API (two GETs + JSON), not with `k8s.io/metrics`. Only non-terminated
  pods count (a `Succeeded` one reserves nothing; CronJobs leave hundreds) and it applies the max
  of the initContainers, just like the scheduler.
- The `reason` values sent to the frontend are **stable codes** (`CrashLoopBackOff`,
  `RolloutIncomplete`…), never human-readable text: the UI translates them with
  `t('overview.reason.<code>', { defaultValue: code })`, so a new Kubernetes reason is shown as-is
  instead of breaking.
- The list is trimmed to `maxOverviewIssues` but `issuesTotal` is sent: **never truncate
  silently**; a panel showing 50 of 400 without saying so reads as "there are only 50".

### Topology (`k8s/topology.go`, `pages/ClusterTopology.tsx`)

`GET /api/clusters/{id}/topology` returns the graph Ingress → Service → workload → pods → PVC of the
selected namespaces, from the informer cache. `BuildTopology` is **pure** (clock as a parameter,
`topology_test.go`): node statuses reuse `attention.go`, grace periods included, so the graph and the
overview never disagree about what is broken. Edges are only what the API server knows (`owns` via
ownerReferences, `selects` with the full Service selector against the pod template — the edge goes to
the workload, not to each replica, and survives a scale to 0 —, `routes` including `defaultBackend`,
`mounts`); **observed traffic is not inferred**, it would need Hubble/a mesh and must be a separate,
labeled kind. A Deployment's ReplicaSets are folded into it; Succeeded pods are left out. Ingresses and
PVCs are optional informers: without them the graph is still served and `unavailable` says why. Above
`maxTopologyNodes` the response is empty with `tooLarge` (a half graph lies about dependencies) and the
UI asks for namespaces. The frontend uses React Flow (`@xyflow/react`: zoom, pan, minimap) with a
deterministic column layout of its own (`lib/topologyLayout.ts`), not elkjs/dagre, so nodes do not jump
on the 5-second refresh; the selection (`sel`) and pod folding (`pods`) live in the URL.

### Encryption key rotation: `bayplan reencrypt` (`internal/rotate`)

Every secret in the DB carries its `key_id`, so adding `ENCRYPTION_KEY_V2` and switching
`ENCRYPTION_KEY_ACTIVE_ID=v2` already encrypts new data with the new key while still reading the
old. What was missing was being able to **retire** v1: `bayplan reencrypt` (same binary, same
configuration; `make reencrypt` locally) re-encrypts with the active key every row whose
`key_id` is not it, table by table in transactions with `FOR UPDATE` (it can run with the server
up), and walks the `settings` JSON looking for `{keyId, ciphertext}` objects (`settings.Secret`).
It is idempotent. It prints a report and fails if any row has a key the keyring does not know: in
that case no key can be retired. `rotate.Tables` is the list of encrypted columns and
`TestTablesCoverSchema` compares it against the migrations: a new secret that is not there makes
the test fail, not the rotation silently.


Only the irreducible bootstrap goes through the environment (`internal/config`: `DATABASE_URL`,
`ENCRYPTION_KEY_*`, `LISTEN_ADDR`, `COOKIE_SECURE`, `TRUSTED_PROXIES`, timeouts). Public URL,
SSO, SMTP and prices live in the `settings` table (one JSONB row per section, migration `0008`;
`internal/settings.Store` with an in-memory cache and `Subscribe` for hot reload). Sensitive
fields are encrypted with the **same keyring** as cluster tokens (`settings.Secret`) and never
leave towards the frontend: the API returns `clientSecretSet: true`. The brand (name and tagline)
is NOT configurable: it is fixed (`lib/brand.ts`, `login.subtitle` in i18n); the `branding`
section that once lived in `settings` was removed. One instance per DB: the cache is not
coordinated across replicas, same as the login rate limiter.

### Packaging: all-in-one, built-in HTTPS, backups of Bayplan itself

Containers only (no native packages): single container, chart, Compose, Quadlet
(`deploy/README.md`). Three pieces make the single `docker run -v bayplan-data:/data` possible,
and each is opt-in so the chart/Compose path does not change:

- **All-in-one** (`internal/embeddedpg`): with `DATABASE_URL` empty the server starts the
  PostgreSQL 18 of the image as a child process (Unix socket only in a 0700 dir under `/tmp`,
  `trust` because only our UID reaches it; data in `DATA_DIR/postgres`), stops it with SIGINT on
  shutdown, and refuses a data dir of another major version with the upgrade procedure in the
  message. The key is generated in `DATA_DIR/encryption-key` (`crypto.LoadOrCreateKeyFile`)
  **only in this mode, only if the environment brings none, and only by the server**: `reencrypt`
  requires the existing file and `backup`/`restore` never create it. Subcommands (`reencrypt`,
  `backup`) attach to the running instance's socket instead of starting a second one, and log to
  **stderr** (`bayplan backup -` writes the dump to stdout). It is still Postgres: no second
  storage engine.
- **Built-in HTTPS** (`internal/tlsserve`): `TLS_LISTEN_ADDR` (default `:8443` only in
  all-in-one; explicitly empty disables it). Files (re-read on change), ACME (TLS-ALPN-01,
  autocert) or a **private CA** in `DATA_DIR/tls` issuing a leaf per SNI/IP on the fly (so any
  name or IP the users type gets a valid certificate). With HTTPS on, the HTTP listener only redirects (the cookie is Secure),
  except `/healthz`, `/readyz`, `/metrics`. Without an https public URL it redirects to the request's own
  host only if that is an IP or a valid DNS name (and one of `ACME_DOMAINS` with ACME); otherwise 400.
- **Agents pin the CA instead of skipping verification**: the manifest and the Docker `run`
  command carry `BAYPLAN_CA_CERT` (base64 PEM), ADDED to the system roots by the agent
  (`serverTLSConfig`). Source, in order: `settings.server.agentCACert` (pasted by the admin),
  the built-in self-signed CA, `AGENT_CA_FILE` (the chart mounts cert-manager's CA there with
  `ingress.tls.source=selfSigned`). With a verified CA the agent keeps self-updating its RBAC;
  `INSECURE_SKIP_VERIFY` still disables that.
- **Backups of Bayplan's own DB** (`internal/backup/server.go`): `pg_dump --format=custom` to an
  existing S3 target on a schedule (`settings.serverBackup`; history in `server_backup_runs`,
  migration `0029`, pruned to 100), download on the spot, and `bayplan backup|restore` on the
  CLI. Connection data goes to pg_dump through `PG*` variables, never in argv. **The key is never
  in the dump.** Restore only into a DB without Bayplan tables, before the server starts; in
  all-in-one it writes the given key into the new volume (and `ensureEncryptionKey` is skipped
  for `restore`, or it would generate a fresh one). A target used by the server backup cannot be
  deleted (409).
- Chart: `ingress.tls.source` = `secret | letsEncrypt | selfSigned` (cert-manager Issuers in
  `templates/certificates.yaml`), `bootstrapPassword` (Secret `<release>-bootstrap`, generated,
  `BOOTSTRAP_PASSWORD` is only read when `users` is empty and forces a change on first login),
  `imageRegistry` (rewrites server/agent/Postgres registries, `bayplan.withRegistry`). The server's
  selector carries `app.kubernetes.io/component: server` (the bundled Postgres shares name/instance and
  is `component: database`); `networkPolicy.enabled` (opt-in) restricts ingress to the server to
  `networkPolicy.ingressNamespaces` and to Postgres to the server.
  `deploy/airgap/images.sh` derives the image list from the sources (attached to each release),
  `mirror.sh` copies it keeping paths. CI also publishes a classic Helm repo on gh-pages and,
  if `DOCKERHUB_NAMESPACE` is set, mirrors the images to Docker Hub.

### Dependencies and security patches

Three layers, documented for operators in "Upgrades and security patches" (`deploy/README.md`):
Dependabot (grouped minor/patch PRs per ecosystem, majors alone, base images of the
Containerfiles, security updates immediate; the `k8s.io` family and kustomize must move
together, and react/react-dom always in the same PR); CI (`govulncheck` — only vulnerabilities
the code reaches —, `npm audit --omit=dev --audit-level=high`, and the `image-scan` job: Trivy on
both images, HIGH/CRITICAL with a fix block `images`; gitleaks over the whole history with
`.gitleaks.toml`; and a `third-party` job that fails if `THIRD_PARTY_NOTICES.md` is not what `make
third-party` generates, so run it whenever a dependency moves; the chart is linted read-only and only
`chart-publish`, on `v*` tags, gets `packages`/`id-token` write); and the weekly `security-scan` workflow
(`scripts/security-scan.sh`, also `make audit`) over the **published** `latest`/`main`, which
opens or closes a `security` issue and **never republishes** by itself. Base images and tools
carry **exact** versions (a floating `golang:1.26` once built with a cached 1.26.5); kubectl and
Trivy are ARGs Dependabot cannot read, the weekly report compares them with upstream. Accepted
findings go in `.trivyignore.yaml` with reason, `paths` and `expired_at`. Trivy always runs in
jobs without write tokens.

### Generic OIDC SSO (`internal/oidc`), with Entra as just another case

Replaces the `entra` package. `oidc.Manager` builds the `Provider` from `settings.auth.oidc` and
rebuilds it when that section or `server.publicURL` changes (the redirect URI comes from there,
`<publicURL>/api/auth/oidc/callback`). Claims with configurable names (`usernameClaim`,
`groupsClaim`); the stable subject is stored in `users.sso_subject` (in Entra it is `oid`, which
does not change between app registrations; elsewhere `sub`). The Microsoft Graph fallback for
group overage is only enabled with `graphGroupsFallback` and a Microsoft issuer. A user without a
mapped group still gets in if they have a manual (`origin='manual'`) assignment in `role_assignments`
(providers without groups, like Google); 'sso' rows from a previous login do not count, because they
are re-derived (and dropped) on every login. The OIDC state cookie lives on `/api/auth` so the
`/api/auth/entra/callback` alias receives it, and the signed state carries its issue time. `POST /api/auth/login` and `/setup` require `Content-Type: application/json` (`jsonBody`): a cross-site
`<form>` can only send text/plain or form encodings, and without a cookie the CSRF middleware has nothing
to check, so this is what stops a login-CSRF leaving the attacker's session in the victim's browser. Login
is limited per IP and, separately, per username (`middleware.AccountLimiter`): X-Forwarded-For is only
as trustworthy as `TRUSTED_PROXIES`, and a client inside those ranges can pick a fresh IP per attempt.
The account limiter is **not** a `rate.Limiter`: `Acquire` reserves the attempt while bcrypt runs (a
burst of parallel guesses cannot all pass a "tokens left?" check before any is charged) and `Release`
only charges a **failure**. A browser that already logged in as that user carries a **device cookie**
(`api/device.go`, OWASP's pattern: sealed with the keyring, `SameSite=Strict`, path `/api/auth`, 90
days) and skips **both** shared limiters, so failures by others cannot lock the owner out; only
browsers that never logged in can be throttled. The cookie does not buy unlimited guesses: it
carries a random nonce and its failures go to its own bucket in the account limiter, and it carries
an epoch derived from the password hash (`PasswordEpoch`), so changing the password (or disabling
the user) retires every cookie issued before. That matters most behind a proxy missing from
`TRUSTED_PROXIES` (the chart's default is empty): every client then shares the proxy's IP, one
client empties that bucket for all, and `clientip` logs a one-time warning when it sees
X-Forwarded-For from an untrusted address (NOTES.txt says it too). The `AZURE_*` variables are only read to migrate them ONCE into
settings (`settings.MigrateLegacyEntra`); `/api/auth/entra/*` are aliases of the callback.

### Connection strategy

(1) The cluster where the server runs is the **local cluster**: when
deployed with the chart (`localCluster.enabled`, the default), the server registers at startup a
row with `connector='incluster'` (`k8s.EnsureLocalCluster`, migration `0028`; there can only be
one) and connects with `rest.InClusterConfig()`, that is, with its own pod's ServiceAccount, to
which the chart grants the panel's RBAC (`templates/local-cluster.yaml` renders `files/rbac.yaml`,
a third copy of `serviceaccount.yaml` watched by `manifest_test.go`). No credential in the DB and
no agent; it cannot be deleted or disabled from the API (409/400).
Outside Kubernetes `EnsureLocalCluster` does nothing. **The panel's own namespace is protected**
(`k8s/protected.go`, `POD_NAMESPACE` via the downward API): its Secrets carry the encryption key
and the DB and its Deployment IS the panel, so if a panel admin could read them or write there
they would be cluster admin through the back door. **On an agent cluster the protected namespace is the agent's (`k8s.AgentNamespace`, `bayplan-agent`;
`TestAgentNamespaceMatchesProtected` keeps it equal to the manifest's):** its Secret holds the agent
key and its ServiceAccount is the identity the panel reads and impersonates through, so a workload an
editor runs under that SA would be cluster admin outside the panel's audit. `GuardNamespace` rejects (403,
`ErrProtectedNamespace`) apply/delete/restart/scale/rollback/exec/pull secrets in it,
`ResourceYAML` always masks its Secrets (**masking a Secret also drops the annotations that carry a
copy of it**: `kubectl.kubernetes.io/last-applied-configuration` holds the `stringData` in clear for
anything created with `kubectl apply`, `objectset.rio.cattle.io/applied` and kapp's
`kapp.k14s.io/original` likewise, and any annotation over 1 KB is treated as one; `k8s.IsSecretCopyAnnotation`
is shared by the YAML view, `helm.RedactSecrets` and the backup dump), `DumpResources` skips it (and notes it in `skipped`), the
Helm detail of the panel's release goes without values and with masked Secrets, and the kubectl
shell rejects `-n <that one>`, `-A`, `--raw`, a context namespace equal to it and mutating verbs on
`ns <that one>` (`kubectl.ValidateProtected`), while its local proxy refuses every request under
`/namespaces/<that one>/` and any write to that Namespace (`protectedPathBlocked`), which covers
what the line validator cannot see (`get -f -`, label selectors). Logs of its pods are refused too
(they print the first-startup setup token), and apply/delete of the Namespace object itself goes
through the same guard (`guardTarget`: namespace for namespaced kinds, name for `Namespace`,
nothing for other cluster-scoped kinds). Since admission does not see
reads, this application guard is what counts for reading; for writes made with the panel's
identity outside the panel, the chart adds a `ValidatingAdmissionPolicy` (if the cluster is 1.30+)
that denies the `bayplan:*` groups every write in that namespace. (2) The other clusters come in
**via agent** as the recommended path, and the UI says so; direct and cloud remain available. (3)
**Agent and server are published together and with the same version**: `main.version` (ldflags
in Containerfile, CI and Makefile) → `settings.ServerVersion` → `DefaultAgentImage()`
(`ghcr.io/bayplan/bayplan-agent:<version>`, `:latest` in development); `agentImage` in Settings
is only filled in if publishing to a private registry. (4) The agent **updates itself**: the
server sends the `X-Bayplan-Agent-Image` header in `/api/agent/rbac` and, if it differs from
`BAYPLAN_IMAGE` (env in the manifest), the agent patches the image of its own Deployment
and its `BAYPLAN_IMAGE` variable, so the new pod does not re-patch on every sync (`cmd/agent/upgrade.go`, Role `bayplan-agent-upgrade` scoped to that Deployment). It only accepts
**another tag of the same repository** (`upgradeTarget`, pure and tested): a compromised server
cannot make the cluster pull an arbitrary image. With that, "reapply RBAC" and "update agents"
cease to exist for the operator; the only manual thing left is cluster-admin mode (manifest).

### Agent connector (`internal/agent`, `cmd/agent`)

A second connector besides the direct one: an agent deployed in the cluster opens an **outbound**
WebSocket to `/api/agent/connect` and, over it, a multiplexed tunnel (`remotedialer`). The
Manager uses `hub.Dialer(clusterID)` as `rest.Config.Dial` with `Host`
`http://agent.bayplan.internal`: informers, impersonation, SPDY exec and Helm work unchanged. The
agent **does not expose the API server**: inside the tunnel it only accepts connections to its
local proxy (`127.0.0.1:8081`), which adds the token of its own ServiceAccount (re-read from the
projected file every minute, so rotation is transparent) and forwards over TLS with the cluster's
CA. The server never sees that token. Onboarding: `POST /api/clusters {connector:"agent"}`
generates the agent key (hash to authenticate the handshake + encrypted to render the manifest)
and a 1 h manifest token; `GET /api/agent/manifest/{id}/{token}.yaml` (no session) returns
namespace + SA + **all the RBAC** (embedded copy of `deploy/rbac/serviceaccount.yaml` renamed to
`bayplan-agent`; `manifest_test.go` fails if the two copies diverge) + Secret + Deployment.
`Hub.OnConnect` → `Manager.Connect`; `OnDisconnect` → `MarkAgentOffline`; deleting or disabling the
cluster calls `Hub.Evict`, which closes the live tunnel at once (its next handshake is refused), instead
of leaving an authenticated WebSocket registered until the agent happens to reconnect. The manifest URL
is not served for a disabled cluster. Each tunnel carries a
generation: a new handshake for the same cluster closes the previous tunnel's connection
(remotedialer has no eviction API and dials through the OLDEST session), and only the current
session's end calls `OnDisconnect`, so a stale tunnel ending after a reconnect does not mark the
cluster offline. **The agent keeps its
RBAC up to date**: at startup and every 6 h it requests `GET /api/agent/rbac` (same credential as
the handshake, `Hub.Authenticate`), which returns only the ClusterRoles/ClusterRoleBindings
currently in force for its `rbac_mode` (`agent.RenderRBACOnly`), and applies them with its SA via
server-side apply with one `application/apply-patch+yaml` PATCH per document, without client-go
(`cmd/agent/rbac.go`; it rejects any kind other than those two: it is not a channel for arbitrary
manifests). This is allowed by the `bayplan-agent-rbac` ClusterRole in the manifest, scoped by
name to the `bayplan-*` ones (not to itself) with `escalate`/`bind` only on them. Since
`escalate` on its own read role would let a compromised server grant it `*`, the agent
**validates what it receives against an allowlist compiled into its binary** (`validateRBACDoc`,
**structural**: an `rbacTable` of (apiGroup, resource) → the exact verbs of the panel's RBAC,
fixed names, no wildcards or nonResourceURLs, `impersonate groups` only with `resourceNames` ⊆
the three `bayplan:*` groups (without names, `system:masters` would be valid), patch/update of
RBAC and APIServices only by name, bindings only to `bayplan-*` roles and to its own subjects),
rejects the whole pass before applying anything if something is out of bounds, **applies the
re-encoded validated struct and never the server's text** (a field the validator does not model, such as
an `aggregationRule` on an empty role that the aggregation controller would fill with `cluster-admin`,
is dropped; `aggregationRule` itself is refused explicitly), and **does not
self-update with `BAYPLAN_INSECURE_SKIP_VERIFY`**. The ceiling is thus set by the agent image,
which the operator deploys: extending the panel's RBAC with a new group or verb requires
extending the list and publishing an agent (`TestRealRBACPassesAllowlist` warns). Neither the
self-update role nor the cluster-admin bindings travel to the agent: only the manifest (switching
to cluster-admin is still applying it). It reports in `POST /api/agent/rbac/status` the applied
hash (`clusters.agent_rbac_*`, migration `0027`) and the cluster table shows "RBAC up to date"
when it matches `agent.RBACHash(RenderRBACOnly)`. So "reapply the RBAC when upgrading" is only
left for the direct connector. An agent cluster has no credentials to edit (`PATCH` rejects
them); what gets regenerated is the manifest URL, never the key. The agent image comes from
`settings.server.agentImage` (`deploy/Containerfile.agent`, `make images`); `BAYPLAN_SA_DIR`
allows running the agent outside a pod. It requires a configured public URL: it goes inside the
manifest.

### Permission mode `rbac_mode` (scoped or cluster-admin)

`clusters.rbac_mode` (migration `0026`: `scoped` by default, `cluster-admin`) decides which RBAC
the panel generates for agent and cloud (`agent.RenderManifest{ClusterAdmin}`,
`agent.RenderRBAC(s, clusterAdmin)`); on a direct cluster it does not apply, the operator applies
the RBAC. The cluster-admin mode **adds** two ClusterRoleBindings (`agent.ClusterAdminBindings`:
the subject that reads and the `bayplan:admins` group → `cluster-admin`) without removing the
scoped roles: going back is deleting two objects, and editors/readonly never escalate. It exists
because some operators prefer
"install and forget" over reapplying the RBAC with every version; the scoped mode is still the
recommended one and the one the UI shows by default, and the cluster table marks with a badge the
ones running as cluster-admin. It can be changed with `PATCH` and the manifest or the cloud RBAC
regenerated afterwards.

### Installing metrics-server from the panel (`k8s/metricsserver.go`)

`metrics-server.yaml` is the **official** `components.yaml` of the pinned release
(`MetricsServerVersion`), unedited: `TestMetricsServerManifestPinned` compares its sha256 with the
constant, so bringing in a new version means replacing the file and the two constants.
`MetricsServerManifest(kubeletInsecureTLS)` parses it with `ParseManifests` and, if requested,
adds `--kubelet-insecure-tls` to the Deployment (kubeadm/kind/labs; without it it starts and
collects nothing). `POST /api/clusters/{id}/metrics-server` applies it with the user's identity
(admin on the cluster, prior dry-run, audited as `metrics_server.install`); `GET
.../metrics-server/manifest` serves it as YAML for `kubectl apply`. In a dry-run, a binding whose
role comes in the same manifest cannot be rehearsed (the escalation check loads the role and it
does not exist yet: "not found"): `SplitBindingsToNewRoles` sets them aside, they are assumed
`created` and the response marks it with `partialDryRun`. `ClusterUsage.ReasonCode`
(`NotInstalled`/`Forbidden`/`Unavailable`/`Error`) is a stable code: the UI only offers to install
with `NotInstalled`. For the apply to work in scoped mode, the impersonated admin's ClusterRole
carries what the manifest creates (ClusterRoles, bindings, APIService; `create` unscoped because
it does not support `resourceNames`, and get/patch/update **only by name** on the metrics-server
objects, without `delete`: a panel admin cannot rewrite anyone else's RBAC) **and** what those
roles grant (watch of pods/nodes/configmaps, `nodes/metrics`, tokenreviews and
subjectaccessreviews): Kubernetes rejects with "escalate" creating a role with permissions one
does not have.

### Cloud connector (`internal/cloud`, connector=cloud)

A third connector: a **provider credential** (`cloud_credentials`, migration `0017`; AWS access
key, Azure service principal or JSON key of a Google SA; the secret encrypted with the keyring as
JSON, the rest in `config` JSONB) serves to **discover** clusters (EKS/AKS/GKE) and to
**authenticate** against their API server with short-lived tokens issued by the provider. The
cluster stores `cloud_credential_id` + `cloud_ref` (name and region / resource group /
project+location) and also endpoint and CA, but `Manager.Connect` **refreshes them by describing
the cluster on each connection** (`k8s.CloudConnector`, implemented by `cloud.Service.RestConfig`;
`k8s` does not import `cloud` so as not to drag in the SDKs). Deleting a credential with clusters
hanging off it is rejected (FK `ON DELETE RESTRICT`, 409).

- The token goes in a `rest.Config.WrapTransport` with `oauth2.Transport` over an
  `oauth2.ReuseTokenSource` captured in the closure: copies of the config (impersonation, Helm)
  share the cache and the renewal. `WrapTransport` is client-go's innermost wrapper, so the
  `Impersonate-*` headers it adds on the outside arrive intact (`cloud_test.go` checks it with a
  fake server). EKS: STS `GetCallerIdentity` pre-signed with `x-k8s-aws-id`, format
  `k8s-aws-v1.<base64url>` (the one from aws-iam-authenticator; it is signed locally, the test
  verifies the format). AKS: Entra ID token for the fixed app of the AKS API server
  (`6dae42f8-…`, the same as kubelogin) with `clientcredentials`; if the cluster does not have
  Entra ID integrated, the kubeconfig from `listClusterUserCredential` brings a client certificate
  and that one is used. GKE: the same OAuth2 token (cloud-platform scope) works for the GKE API
  and for the API server. Azure and GCP go through raw REST with `x/oauth2`; only AWS uses its SDK
  (SigV4 by hand is not worth it). The `aws.Config` is built **only** with the stored credential:
  never `LoadDefaultConfig`, which would read the server's environment and change the identity
  without anyone noticing.
- The API server sees the **provider's** identity, not a ServiceAccount, so the panel's RBAC
  has to be granted to it: `agent.RenderRBAC(Subject)` reuses the embedded copy of
  `serviceaccount.yaml` replacing the subject of the SA's two bindings (Group `bayplan:cloud` in
  EKS, which the operator maps with an *access entry*; User `<oid>` in AKS; User `<email>` in
  GKE). Onboarding responds with that YAML (`cloudOnboarding`) and
  `GET /api/clusters/{id}/cloud/rbac` serves it again. Until it is applied the cluster is
  "connected" (/version requires no permissions) but reads give 403; the UI warns about it.
- `PATCH` of a cloud cluster refuses to touch the connection: the credential is edited (and that
  rotates all its clusters at the next renewal). `Config.Identity` (ARN/oid/email) is resolved
  on validation and stored to display it; `Validate` also checks that the credential **sees** the
  subscription/project, because an empty listing without an error is worse than a 422 with the
  reason. Provider errors are returned verbatim (422 on validate, 502 on discover): they are the
  real explanation (missing IAM permission, nonexistent region).

### UI extensions (`internal/extensions`)

Links to external tools (Grafana, Kibana, Harbor…) in each cluster's sidebar, "Tools"
section (`ui_extensions`, migration `0023`). **It is not a plugin system** and that was ruled out
on purpose: the tool already has its UI and what is missing is reaching it from the cluster's
context; a real plugin would require a stable API for third parties that does not exist today. An
extension is name + URL + mode (`link`: another tab; `embed`: iframe at
`/clusters/:id/ext/:extId`) + minimum role on the cluster + cluster (or `NULL` = all). The URL
accepts `{cluster}`, `{clusterId}` and `{namespace}` (first namespace of the current filter,
empty if none), substituted by the frontend (`expandExtensionUrl`; `extensions.Expand` is the Go
copy for tests and API consumers). `Validate` is pure: only absolute http(s) (a `javascript:` in
the sidebar would be XSS served by the panel) and placeholders from the list. Management is
global-admin (`/api/extensions`, audited); `GET /api/clusters/{id}/extensions` filters by the
user's role on THAT cluster, the one `RequireClusterRole` leaves in the context. The iframe goes
with `sandbox` and `referrerPolicy=no-referrer`; if the tool sends
`X-Frame-Options`/`frame-ancestors` the browser leaves it blank without warning, so the "open in
another tab" button is always visible and the UI explains it. The panel sends `X-Frame-Options: DENY`,
`nosniff`, `Referrer-Policy` (and HSTS on the built-in TLS listener) via `web.SecurityHeaders`, but
deliberately no CSP; do not add one with `frame-src` without thinking about this. X-Frame-Options only
stops others framing the panel; it does not affect the extension iframes.

### Image scanning with Trivy (`internal/scan`, `k8s/images.go`)

`k8s.ClusterImages` groups by reference the images of non-terminated pods (containers and
initContainers) with the owning workload resolved via ReplicaSet and the digest reported by the
kubelet. `scan.Runner` executes the server's `trivy` binary (`TRIVY_PATH`, optional like kubectl;
the image includes it with a pinned version and checksum) with `image --scanners vuln --format
json`, cache in `$TMPDIR/bayplan-trivy` (the CVE DB is downloaded from ghcr.io the first time)
and, if the image's host matches a registry from `internal/registries`, its credentials via
`TRIVY_USERNAME/PASSWORD`. The result is stored **per image, globally** (`image_scans`, migration
`0022`): the same image has the same CVEs in any cluster. The report is trimmed to what the UI
shows (id, severity, package, versions, title, URL) and the counters go in columns.

- Queue in the DB (`status queued/running/ok/error`, `NextQueued` with `FOR UPDATE SKIP LOCKED`)
  and a single worker: Trivy does not cope well with two simultaneous scans on the same cache
  while downloading the DB. `Enqueue` does not re-scan what was scanned less than 1 h ago unless
  `force`; what is already scanned is re-queued weekly (the CVE database changes even if the image
  does not); orphaned `running` entries from a restart go back to the queue at startup, and a scan interrupted
  by shutdown is re-queued on the spot.
- Only images **in use in the route's cluster** are queued: the queue is not a generic internet
  scanner. The reference is validated (characters of an OCI reference) before passing it to the
  subprocess, and it goes after `--`. The report is read with a session (`/api/images/report`):
  it is not tied to a cluster because the scan is not either.
- Scanning requires editor on the cluster (it consumes the server's CPU and network) and is
  audited as `image.scan`. There is no "policy" that blocks deployments: the panel is not an
  admission controller; the UI colors by the worst severity and allows filtering by minimum
  severity and "only with a fix".

### Configuration backups to S3 (`internal/backup`)

Backups of a cluster's **configuration** (manifests), not of volumes: `k8s.DumpResources` exports
from the informer cache the types in `dumpKinds` (namespaces, quotas, SAs, configmaps, secrets,
PVCs, networkpolicies, services, workloads, jobs, HPAs, ingresses; excluding pods, replicasets,
events and nodes) stripped of state (`cleanedYAML`: status, uid, resourceVersion, managedFields,
clusterIP, volumeName, Job selector…), they are packed into a tar.gz with `manifest.json` first
(to read the index without decompressing everything) and uploaded to an S3-compatible bucket
(`backup_targets`, encrypted secret; `backup_schedules`, `backup_runs`, migration `0021`). An
optional type without permission is **skipped and noted** in `skipped`, it does not abort the
backup. The UI says everywhere that PVCs are not backed up: that is what Velero is for.

- Key in the bucket: `<prefix>/cluster-<id>/<schedule>/<dateUTC>-<runID>.tar.gz`. The sortable
  date is what retention uses (list by prefix, sort, delete the surplus) and the `runID` prevents
  two backups in the same second from overwriting each other (it happened in testing). The
  scheduler (`tick` every minute) launches the due schedules; `StartRun` marks `last_run_at`
  before executing so that two ticks do not launch it twice, and a mutex per schedule prevents
  manual and scheduled runs from overlapping.
- Secrets exported **without** values keep their keys in `stringData` with `<redacted>` and the
  `bayplan.io/redacted` annotation; `Restore` rejects them by that annotation (in `data` the
  marker would be base64 and would pass as a real value, which is the bug there was). Restoring is
  server-side apply with the user's identity (admins group) and a prior dry-run, like the YAML
  editor.
- The S3 client is built **only** with the target's credential (never `LoadDefaultConfig`),
  path-style if there is a custom endpoint, and `Validate` creates the bucket if it does not
  exist (and says so if it cannot). `backup_test.go` tests Pack/Unpack, validation, upload,
  retention and download against a fake S3 in `httptest`. Routes: targets are global admin;
  schedules, runs, download and restore require **admin on the cluster** also on GETs
  (`requireClusterExec`): a backup can contain Secrets and the download is audited.

### Personal API tokens and OpenAPI

`must_change_password` is enforced in `RequireSession`, not only in the frontend: until the password
is changed only `GET /api/auth/me`, `POST /api/auth/change-password`, `POST /api/auth/logout` and
`PATCH /api/auth/preferences` answer (`middleware.AllowedWhilePasswordChangePending`); the rest is
403. A DB error while resolving a role is a 500, never a 403/`access.denied`.


`api_tokens` (migration `0020`): a user creates `bpl_…` tokens in their profile (value shown
once, SHA-256 hash in the DB, like sessions) and uses them in `Authorization: Bearer`. A token
resolves to the **same `session.Session`** as a cookie (`Validate` looks at the header first),
with two flags: `ViaToken` and `ReadOnly`. All authorization stays in the same middlewares:
`RequireCSRF` is skipped with `ViaToken` (there is no cookie a foreign site could make the
browser send, and requiring it would break scripts) and `capRole` caps the role to readonly in
`RequireSession`, `RequireClusterRole` and, via `middleware.CapRoleFromContext`, in the handlers
that recompute the role on their own (Secret values, `resolveActor`, Helm, `/api/auth/me`). When
authorizing a cluster, `RequireClusterRole` leaves in the context the role **for that cluster**
already capped, which is the one handlers must read. Managing tokens, changing the password and editing the profile require a **cookie**
session (`requireCookieSession`: a leaked token does not become permanent or multiply, and
change-password hands out a full session, which is exactly what a read-only token must never obtain;
it is also behind the login limiter, or it would be an unthrottled oracle for the current password); disabling the user or changing
their password also revokes their tokens (`RevokeAllForUser`). `last_used_at` is written at most
once per minute. The OpenAPI specification lives in `api/openapi.yaml` **by hand** (the mux does
not expose the routes) and is served as JSON at `GET /api/openapi.json`; the `/api-docs` page
renders it without Swagger UI so as not to depend on a CDN (installations without internet
access). When adding a route meant for scripts, add it to the YAML.

### AI "Explain" (`internal/ai`, `api/ai.go`)

One LLM provider per instance in `settings.ai` (global admin; the key encrypted like any
`settings.Secret` and write-only towards the UI): `openai` (any OpenAI-compatible server, Chat
Completions because it is what Ollama/vLLM/LiteLLM/Azure implement; Azure also gets `api-key`),
`anthropic` (Messages API, `fallbacks: "default"` only on the models that accept it) or `gemini`
(key in `x-goog-api-key`, never in the query string). Raw HTTP, no SDKs, guarded dialer against
link-local like webhooks. `POST /api/clusters/{id}/explain` gathers from the cache the object's
YAML, its events (and its pods' for a workload) and the troubled container's last log lines (read
with the user's impersonated identity, previous run if it restarted), runs it all through
`ai.RedactManifest`/`ai.RedactText` (env values, Secret data, last-applied, managedFields,
secret-looking text: a tested heuristic, and the UI says so) and fences it between
`<cluster_data>` tags the system prompt calls untrusted. Rules: the configured `minRole` on THAT
cluster (editor by default: it sends data out and costs money), the panel's own namespace refused
whole, a per-user daily limit reserved atomically in `ai_usage` (migration `0032`) BEFORE the call
and refused if the counter cannot be read (fail closed), and `ai.explain` audited with provider,
model, what was sent and tokens, never the answer. The UI renders the answer as text nodes (no
Markdown library, no HTML from the model) and lists what was sent; the call is a mutation fired
once per open, never a query that could refetch.

**In-panel assistant** (`POST /api/assistant/chat`, `api/assistant.go`, `components/AssistantPanel.tsx`):
a bottom panel under `<main>` (it survives navigation because the Layout does) that answers with
the same provider. It is styled as a terminal (`term-*` tokens, dark in both themes on purpose: it
talks to a third party and must not look like the app's own screens); it resizes from its top edge
(height saved in `preferences.assistantHeight`), minimizes keeping the conversation and maximizes
over the page. **Read-only and stateless by design**: the browser keeps the conversation and
sends it whole, closing the panel forgets it, nothing is stored. The model gets only the MCP
server's read tools (`mcp.Server.ReadTools`/`RunReadTool`), dispatched through the API with the
browser session (the `Cookie` travels like `Authorization` does for MCP) and marked `via:
assistant`; an `mcp.Gate` checks the AI `minRole` on each cluster it touches (also filtering the
fleet view), refuses the panel's namespace as a scope AND strips its objects from every answer
(`stripNamespace`: listings across all namespaces, overview issues and events, topology nodes and
edges, the Namespace object itself), the YAML of Secrets and ConfigMaps is refused outright
(`assistantRefusal`; `RedactManifest` also masks ConfigMap values), the cluster the browser names as
context only reaches the prompt if the gate allows it, and every tool output goes through Explain's
redaction.
Tool calling lives in `ai.Chat`, which keeps each provider's NATIVE conversation and replays the
model's turns unchanged (Anthropic thinking blocks and Gemini thought signatures are rejected if
altered) and flattens schemas to what all three accept (`portableSchema`: no type unions, no
`additionalProperties`). At most 10 provider calls per question; the answer streams as NDJSON
(`tool`, `note`, `answer`, `done`, `error`) read with `fetch`, since axios cannot stream in the
browser. Each question counts once against the daily limit and is audited as `ai.assistant`
(tools, tokens; never the text).

### MCP server for AI clients (`internal/mcp`)

`POST /api/mcp` speaks the Model Context Protocol (Streamable HTTP, JSON-RPC 2.0, plain JSON
answers: no SSE, no sessions) so a user can drive Bayplan from their own Claude Code, Codex or
Cursor with a `bpl_…` token (the token modal shows the `claude mcp add` line). **It is a client of
Bayplan's own API, not a second implementation**: each tool builds a request to the existing route
and serves it through the same mux, in process, with the caller's `Authorization` header and
address. Per-cluster RBAC, the read-only token cap, `GuardNamespace`, Secret masking and the audit
therefore apply exactly as in the UI; do not add tools that call `k8s.Manager` directly, add the
route first. `audit.WithSource(ctx, "mcp")` travels in that request's context and every entry the
handlers write gets `detail.via = "mcp"`. Rules that are deliberate: tokens only (a cookie session
would make a CSRF-able endpoint that can delete things), requests with `Origin` are refused (DNS
rebinding; no page of ours calls it), a read-only token does not even list the write tools, and
`apply_manifest` with `dry_run=false` is refused unless the same user rehearsed the same YAML on the
same cluster and namespace in the last 15 minutes (`dryRunLedger`, in memory, one use): the tool
description asks the model for it, the ledger enforces it, because text inside the cluster (logs,
annotations) can steer a model past the step the user relies on. Tool results are capped at 100 KB
and say when they were cut; the topology goes as text lines, problems first. Logs needed an HTTP
route (`GET .../pods/{ns}/{name}/logs`, no follow, audited as `pod.logs.read`) because the UI only
had the WebSocket. Tested with a fake router (`server_test.go`).

### kubectl web shell (`internal/kubectl`, `ws/kubectl.go`)

`GET /ws/clusters/{id}/kubectl` (admin on the cluster, same defense as the pod terminal) runs the
**real** kubectl binary on the server with the user's impersonated identity. It is not a system
shell: the browser carries the line editor (`pages/KubectlShell.tsx`) and sends each line; the
server parses it (`kubectl.Validate`, pure and tested) and only accepts `kubectl`/`k` followed by
a subcommand from an **allowlist** (the first word, with no flags before it: "-n x cp …" would
sneak in "x" as the subcommand), with no pipes or redirections (there is no shell behind it),
rejecting what would touch the SERVER instead of the cluster: `proxy`, `port-forward`, `cp`,
`edit`, `config`, `plugin`, `kustomize`; the credential/target flags (`--kubeconfig`, `--server`,
`--as`, `--token`…), `--profile` and `--output-directory`; the `-o *-file` formats; and
`-f`/`--from-file`/`-k`… except `-` (stdin): neither files (`/proc/self/environ` is the obvious
attack) **nor URLs**, because kubectl would download them from the server (SSRF to the internal
network or to the cloud metadata). Short flags with the value attached (`-f/etc/passwd`,
`-ojsonpath-file=x`) are normalized before checking. Short groups are walked letter by letter like
pflag (`-A=true`, `-RA`), and a `--` right after a flag still waiting for its value is that value,
not the end of the flags (`-L -- -A`). In `logs`, `-f` is `--follow`. kubectl runs
in a PTY (`creack/pty`) with PATH set to a directory containing only a symlink to kubectl
(`kubectl-*` plugins are looked up in the PATH), HOME and cwd in a per-session temp directory
that is deleted on close (and orphans from a restart, at startup).

- kubectl talks to an **ephemeral local HTTPS proxy** (127.0.0.1, free port, self-signed
  certificate per process) that requires a random per-session token and forwards with
  `rest.TransportFor(ImpersonatedRestConfig)`: TLS, cluster token, impersonation, agent tunnel
  and cloud token are all resolved by client-go, so it works the same with the three connectors
  and the binary never sees the cluster credential. It has to be **https** even though it is
  loopback: client-go does not send bearer tokens to `http://`. And towards the API server
  **HTTP/1.1** is forced (`NextProtos`): `exec`/`attach` are Upgrade (SPDY) and that does not
  exist over h2; `proxy_test.go` covers it with a fake API server and the real kubectl if it is
  in the PATH.
- Each command is audited with its arguments (`kubectl.command`), in addition to open and close.
  The binary comes from `KUBECTL_PATH` (by default `kubectl` in the PATH; the image includes it
  with a pinned version and verified checksum); if it does not exist, the route responds 404 and
  the UI says so.

### Estimated cost per namespace (`internal/costs`)

`GET /api/clusters/{id}/costs` spreads two instance prices (`settings.costs`: core·hour and
GiB·hour, with currency) over what each non-terminated pod **reserves** (requests, from the
informer cache via `k8s.PodUsage`) and, if metrics-server responds, what it **consumes**.
`Aggregate` is pure and tested: it groups by namespace, computes reserved, consumed and
**chargeable = max(reserved, consumed) per resource** (the usual cost-allocation criterion: a pod
that requests 2 cores and uses 0.1 costs 2, one that requests nothing and uses 1 costs 1) at
730 h/month, and sorts by chargeable. Without prices the response carries `configured=false` and
the UI shows resources without amounts and points to Settings; without metrics-server,
`available=false` and only the reserved amount is charged, saying so. It is an allocation
estimate, not billing: it knows nothing of spot, discounts or node types, and the page repeats
that. A per-cluster or per-node rate is the territory of dedicated cost tools.

### Namespace quotas ("projects")

There is no project concept of our own: the "project" is a namespace with a ResourceQuota and a
LimitRange, which is what Kubernetes already knows how to enforce and what a `kubectl` understands
afterwards. `resourcequotas` and `limitranges` are two more **optional** informers (listed with
used/hard from the `status`, which is what actually does the accounting). The "Namespace quota"
form (`POST /api/clusters/{id}/namespaces/{ns}/quota`, `api/projects.go`) applies in one go the
Namespace (if asked to create it), the `bayplan-quota` ResourceQuota and the `bayplan-limits`
LimitRange with server-side apply, with a prior dry-run like the YAML editor (if the namespace
does not exist yet only the Namespace is rehearsed: a dry-run does not create it and the quota
would fail with "not found"; the response marks it with `partialDryRun` and the UI warns about
it); other quotas in the namespace are not touched, and the fixed names are what allow the UI to
offer "edit" only on the panel's own (`managedByPanel`). The LimitRange goes whenever there is a
CPU/memory quota because without default values pods that do not declare resources are rejected:
it is the most common mistake when first using a quota. Permission: editor on the cluster, and
the editor/admin ClusterRole carries `resourcequotas`/`limitranges` (reapply RBAC when
upgrading).

### Image registries (`internal/registries`)

Credentials for private registries at the platform level (`image_registries`, migration `0019`,
password encrypted with the keyring) and their **distribution** to namespaces
(`registry_bindings`: registry × cluster × namespace, FKs with CASCADE). For each binding the
service applies a `kubernetes.io/dockerconfigjson` Secret named `bayplan-registry-<name>` (which
is why the registry name is a DNS label and cannot be renamed) and, if requested, adds it to the
`imagePullSecrets` of the `default` ServiceAccount. Rotating the credential resyncs all its
bindings; there is also a pass every 15 min (idempotent: it picks up a Secret deleted by hand).

- The Secret goes with server-side apply, but the ServiceAccount does **not**: `imagePullSecrets`
  is an atomic list and an apply with Force would clobber the secrets of other managers (Helm,
  operators). `k8s.SetImagePullSecret` does get+update with retry on conflict and only touches
  its own entry.
- Docker Hub requires the legacy key `https://index.docker.io/v1/` inside `.dockerconfigjson`
  (`dockerConfigKey`); with `docker.io` the kubelet does not find the credential.
- Onboarding validates against the registry with the Distribution API protocol (`/v2/` → 401 →
  Basic or Bearer with realm), so an expired token is rejected on save and not on the first
  failed pull. ECR has no integration of its own: user `AWS` + 12 h token, stated in the UI.
- Permissions: registries are **global admin**; binding/unbinding is **editor on the cluster**
  (creating a pull secret is operating a workload), and the write goes impersonated with the
  user's role; the automatic resync goes as `bayplan:registries` + editors group. The handler
  checks that the binding belongs to the route's cluster: the middleware authorized the `{id}` in
  the URL, not the one in the row.

### Notifications (`internal/notify`)

Rules on what `k8s/attention.go` already detects plus the loss of a cluster's connection
(synthetic issue `Kind=Cluster`, `Reason=Disconnected`), sent to Slack, Teams, generic webhook or
email channels (migration `0018`: `notification_channels`, `notification_rules`,
`notification_deliveries`). **It does not evaluate metrics**: it reports the "Needs attention"
list without having to look at it; for alerts on metrics there is Alertmanager.

- `k8s.Manager.ClusterIssues` returns ALL of a cluster's issues; the fleet view trims them to 10
  but a rule cannot miss number 11 (both use `collectIssues`).
- `notify.Engine` evaluates every minute. The state (what has been reported and when) lives in
  memory and is **persisted after each tick** in `notification_state` (migration `0024`: one row
  per issue × rule, deleted when resolved; it is not history). On startup it is reloaded: what is
  recovered keeps its cooldown and generates "resolved" if it is no longer there (what got fixed
  while the server was down used to be lost). What was NOT in the DB goes through the startup
  grace: the first tick only records it and does NOT send, so that a restart does not re-send at
  once everything that was already broken. Then: `MinDuration` (a pod that restarts once is not an
  alert), `Cooldown` per rule+issue and `resolved` only if it had been reported. It is pure with
  respect to Store/Sender/clusters (injectable interfaces, `engine_test.go` covers duration,
  cooldown, resolved, disconnected cluster and recovery after restart).
- The webhook URL goes **encrypted** (in Slack/Teams the URL is the secret); the SMTP password
  lives in `settings.smtp` (an instance section, not per channel). Outgoing webhooks go wherever
  the admin says (SSRF "by design"), except the cloud metadata endpoints: `netguard.IsMetadata`
  (link-local, plus AWS IMDS over IPv6 `fd00:ec2::254` and Alibaba's `100.100.100.200`), checked on the
  RESOLVED address in the dialer's control hook, shared by notify, gitops, registries and the AI client. Teams receives
  an Adaptive Card (works for classic webhooks and Power Automate flows); the generic webhook a
  flat, stable JSON (`event, severity, cluster, kind, namespace, name, reason, detail, title,
  text, url, timestamp`).
- The rules' `channel_ids` is JSONB, not an FK: `Store.DeleteChannel` removes it from the rules in
  the same transaction (the jsonb `-` operator with an integer deletes by INDEX, which is why the
  array is rebuilt). The delivery history stores denormalized names, is pruned to 30 days at
  startup and is **not** audit; actions on channels/rules are audited.

### Docker/Podman environments (`internal/dockerenv`, kind=docker)

A Docker host is just another row in `clusters` with `kind='docker'` (migration `0016`), always
via agent (`BAYPLAN_MODE=docker`: the agent mounts the socket and exposes it through the tunnel;
constraint `clusters_docker_uses_agent`). Reusing the table gives scoped RBAC, assignments and
audit for free: for permissions a host is "an environment" like a cluster. `dockerenv.Manager` is
the cache-less equivalent of `k8s.Manager` (every read goes to the daemon through the tunnel) and speaks Engine API 1.41 with raw HTTP and its own types, not with the moby SDK
(half a module for a dozen endpoints). Logs demultiplex the 8-byte format when the container has
no TTY; `inspect` masks `Config.Env` for anyone below admin on the host (`dockerenv.MaskInspect`): on a
Docker host the credentials live in the environment, there is no Secret object to mask instead; exec uses a "hijacked" connection opened by hand through the tunnel. The `k8s.Manager`
returns `ErrNotKubernetes` for these rows and the Hub dispatches by `Kind`. Routes hang off
`/api/clusters/{id}/docker/...` with the same RBAC (readonly lists/logs; editor
start/stop/restart/pull; admin deletes and opens a terminal) and actions go in
`.../containers/{cid}/action/{action}`: a literal segment, because `{cid}/{action}` collides with
the mux's generic `{kind}/{namespace}/{name}/restart` pattern. Frontend: `pages/DockerPages.tsx`,
`LogsPanel`/`TerminalPanel` accept `openSocket` to be reused; `App.tsx` decides the view by `kind`
and does not mount `ClusterView` until it has the list (it would redirect unknown tabs to pods).

### Cluster credentials and connection state

A cluster is onboarded with a **kubeconfig** (embedded data only; `k8s.ParseKubeconfig` rejects
`exec`/files with a message saying what to do), a **token** or a **client certificate**
(`k8s.Credentials`, migration `0010`: `ca_cert` optional only with `insecure_skip_tls_verify`;
private key encrypted with the keyring). `PATCH /api/clusters/{id}` edits name, description,
enabled and **rotates credentials** without deleting the cluster (before, that was the only way,
and it lost the `role_assignments`). The state travels in `/api/clusters` (`k8s.ClusterStatus`):
a `healthLoop` per cluster calls `/version` every 30 s with the stored credential and detects
401/403/network with the real error; `SetWatchErrorHandler` captures the list/watch errors of the
required informers. The base `rest.Config` carries **no `Timeout`**: it is `http.Client.Timeout`,
which would cut watches and `logs -f` (it did, every 10 s). Short calls are bounded by contexts, and
the clients that take no context (discovery, Helm) use `boundedConfig`. `/readyz` no longer depends
on the clusters. The frontend stopped
cross-referencing by name with `/readyz` (`lib/status.ts: clusterConnection(cluster)`).

### Three roles: `admin` > `editor` > `readonly`

The order is defined by `rbac.Role.rank()`, the single place (`AtLeast`, `highest`,
`EffectiveAccess` use it). `editor` (migration `0011`) operates workloads (restart, scale,
rollback, apply, cordon); delete, drain, terminal and reading secret values are `admin` only. Each
role has its impersonated group and its ClusterRole in `deploy/rbac/serviceaccount.yaml`; the
middleware requires the minimum (`requireClusterEditorMutation` /
`requireClusterAdminMutation`) and the handler resolves the exact role for the group. Extending
what an editor can apply (custom CRDs) means adding rules to the ClusterRole: the panel passes the
YAML as-is and the API server decides.

### Helm (`internal/helm`): Helm 3 SDK without a kubeconfig on disk

The SDK expects a kubeconfig; here cluster access comes from the Manager, so `restGetter` adapts a
`rest.Config` to `genericclioptions.RESTClientGetter`. **Reads** (list releases, detail, history)
use the technical SA's `rest.Config` (`k8s.Manager.BaseRestConfig`), like the rest of the panel's
reads: a readonly sees what is installed without their impersonated group needing `list
secrets`; that is why a release's detail masks the Secrets in its manifest except for admin
(`helm.RedactSecrets`, same rule as a Secret's YAML, and audited as `secret.read`), hides the user
values below admin (they are where the chart's passwords come from), and the server dry-run of an
install/upgrade goes through the same masking (a chart may `lookup` Secrets with the editor's identity).
Repo and GitOps URLs with userinfo (`https://user:token@host`) are refused on create: the URL is listed to
every reader and written to the immutable audit log. **Writes**
(install, upgrade, rollback, uninstall) impersonate the user (`ImpersonatedRestConfig`) and it is
the cluster's RBAC that decides what each role can deploy: a chart that creates ClusterRoles or
CRDs fails for an editor with the API server's 403, which is returned verbatim (502). Install and
upgrade go through a **mandatory server dry-run** in the UI before applying, like the YAML editor.
Release state lives in Secrets in the cluster (`secrets` driver): nothing to persist in our DB
except the repositories (`helm_repos`, migration `0012`, password encrypted with the keyring).
Each HTTP repo's index is cached 10 min in memory and downloaded charts in `$TMPDIR/bayplan-helm`
(like `bayplan-gitops` and `bayplan-trivy`, created through `privdir.Ensure`, which refuses a directory
or symlink another user planted in a shared `/tmp`) (archives older than 7 days are pruned at startup and every 6 h; chart
names are validated by `helm.ValidateChartName` before reaching `LocateChart`, which prefers a local path over the repo)
(the container is read-only except `/tmp`). OCI repos have no index: the chart is named by
reference and the versions are the registry's tags.

### Lightweight GitOps (`internal/gitops`)

A source = repo + branch + path → one cluster (`gitops_sources`, migration `0013`, real FK to
`clusters` with CASCADE). `Reconciler` keeps a goroutine per enabled source that clones with
go-git (depth 1; HTTPS with an encrypted token, or SSH with an encrypted private key in the same
column and `auth_type='ssh'`, migration `0025`) and applies all the `*.yaml`/`*.yml` in the path
with `k8s.Manager.Apply` (server-side apply) under the identity `bayplan:gitops:<source>` + group
`bayplan:editors`: what it can deploy is decided by the cluster's RBAC, just like for a human
editor. A YAML that is not a manifest (values.yaml, CI config) is ignored, it does not abort the
sync. With `kustomize` the path is built with krusty (`LoadRestrictionsRootOnly`, no plugins:
nothing outside the directory and nothing that executes code on the server) instead of reading
loose YAML files.

- **SSH**: `ssh_known_hosts` stores the `ssh-keyscan` line of the git server and is
  **mandatory** (the API rejects onboarding without it and `sshAuth` fails if it is missing):
  without verifying the host, a MITM of the git server would serve manifests that would be
  applied to the cluster. go-git only reads it from a file, so it is written to the sync's temp
  directory. The "unverified host" badge in the UI can only appear on old rows without
  known_hosts, which no longer sync.
- **SSRF**: the repo URL is chosen by an editor and cloned by the server, so the link-local range
  (cloud metadata) is rejected on create (`RejectLinkLocal`) and on connect: go-git resolves the
  transport by scheme in a global registry, and `installGuardedTransports` installs http/https
  clients with the same guarded dialer as notify/registries; in SSH the block goes in the
  `HostKeyCallback`, which sees the already-resolved IP. Kustomize treats any nonexistent path as
  a git URL and would clone it from the server: `rejectRemoteKustomize` walks the kustomizations:
  first a sweep of ALL the file's scalars (`findRemoteRef`: scheme, `git@`, `?ref=`, `git::` and
  the hosts kustomize understands without a scheme; not "any domain", which is the shape of a
  label key) and then it requires that `bases`/`resources`/`components` exist as a local file or
  directory under the path, recursively. The rest of the private networks are allowed: an
  internal git repo is the normal case. And the clone's content is hostile: `loadManifests`
  neither follows nor reads **symlinks** (a `secret.yaml -> /proc/self/environ` would be read
  and applied), `resolveInside` compares the path against the already-resolved links, separator
  included, and with Kustomize the whole clone is rejected if it contains any symbolic link (its
  generators read arbitrary files from the tree).
- **Prune** (`prune=true`): every applied object carries the label
  `bayplan.io/gitops-source=<id>` (`labelObjects`; not in the pod template). After a
  **complete** successful apply, `k8s.ListByLabel` (technical SA identity, all the types in
  `yamlKinds` except nodes) lists what is labeled and `PruneCandidates` (pure, tested) subtracts
  what was just applied; the deletion goes with `DeleteResource` impersonating `bayplan:admins`,
  because the editors group does not have `delete`. That is why **enabling prune requires admin
  on the cluster** (`GitOpsHandler.Create`): an editor could, with forced SSA, label someone
  else's object and delete it via GitOps without having `delete`. What is deleted is recorded in
  `last_pruned`. A half-failed apply never prunes: what was not applied is not in the list and
  would be deleted. The inventory is not stored in our DB: the cluster is the source of truth.
Triggers: interval, `POST .../sync` (manual, synchronous) and `POST /api/gitops/webhook/{id}` (no
session; authenticates with the per-source token in the `X-Webhook-Token`/`X-Gitlab-Token`
header, or with the HMAC-SHA256 signature of the body `X-Hub-Signature-256` using the token as
the secret for GitHub/Gitea/Bitbucket, which do not allow custom headers; **never via query
string**, it would end up in the access logs. The token is stored hashed for the header and
encrypted for the HMAC, and is shown ONCE on creation; it responds 202 and syncs in the
background because GitHub/GitLab cut off at 10 s). The
state (`last_*`) is written by the reconciler and is outside the UPDATE audit trigger so as not
to flood `audit_log`. **There is no health or sync waves** on purpose: it is lightweight GitOps
with optional prune, not a full GitOps controller; the UI says so.

### `group_role_mappings`: the SSO gate

It already has a UI (`/admin/group-mappings`, `api/groupmappings.go`, global admin only). It is
the main source of roles for SSO: without a row covering one of the user's groups **or a manual
assignment** in `role_assignments`, login fails with `no_role_mapped`, so the screen warns when
SSO is configured and the table is empty. Every OIDC login replaces the user's `role_assignments`
rows with `origin='sso'` (migration `0031`) with what their groups map to today, global and **per
cluster** (`rbac.GrantsFromGroups` + `SyncSSORoleAssignments`): leaving the group removes the role at
the next login. `origin='manual'` rows are never touched; granting by hand a row identical to an
'sso' one turns it into 'manual'. The last active global admin's 'sso' admin row is kept and
audited (`rbac.sync`, `last_admin`).

Two things to respect when touching it:
- **The original `UNIQUE` does NOT deduplicate `global`-scoped rows**: `scope_ref` is NULL and in
  Postgres every NULL is distinct from any other. Migration `0030` adds partial unique indexes
  `WHERE scope_ref IS NULL` on `group_role_mappings` and `role_assignments`, so duplicates are
  rejected atomically: `GrantRole` uses `ON CONFLICT DO NOTHING` (idempotent) and the mapping
  Create maps 23505 to 409. Keep both constraints in mind when adding columns to the key.
- The `ad_group_id` is **not validated as a GUID** even though it usually is one: Azure can be
  configured to emit in the `groups` claim the `sAMAccountName` of a group synced from on-prem,
  and rejecting anything that is not a GUID would leave legitimate configurations out. It is
  trimmed, though: an id with spaces would never match the claim and the failure would be
  invisible.

### Effective permissions: a single rule, in `rbac.EffectiveAccess`

`GET /api/users/{id}/access` answers what a user can really do in each cluster and **where it
comes from**. It exists because `/api/users` returns the raw rows of `role_assignments`, and what
someone can do cannot be read from those: global readonly + admin on one cluster are two loose
rows that have to be combined mentally by applying a rule that lives somewhere else.

- The resolution is done by `rbac.EffectiveAccess`, **the same rule** that
  `EffectiveRoleForCluster` applies on each request. Do not duplicate the logic in the handler: a
  permissions screen that disagrees with the middleware is worse than having no screen. It is
  pure and tested (`rbac/access_test.go`).
- The **provenance** (`global` / `cluster` / `both`) is the actionable datum, not decoration:
  revoking the cluster assignment removes nothing if the permission came from the global role,
  and `both` warns that both have to be revoked.
- **All** clusters are returned, including the ones they do not have and the disabled ones: a
  matrix that only shows what is granted does not let you check at a glance that someone does
  **not** reach production, nor see that they keep permissions on a switched-off cluster.
- Assignments with `scope_type='namespace'` are **ignored**: they exist in the enum but no read
  filters by namespace, so displaying them would suggest a permission that does not exist.

### User profile and preferences

`users.display_name` / `users.email` / `users.preferences` (migration `0007`). Four decisions
that should not be reverted without thinking:

- **Preferences are ONE JSONB document per user, not one row per preference.** Modeling
  this as a resource per preference does not age well; with a blob,
  adding a preference touches neither Go nor the database. `PATCH /api/auth/preferences` does a
  **partial merge** (`preferences || $2::jsonb`), so an old version of the UI does not delete
  keys it does not know.
- **The backend does not interpret the content**: it stores it and returns it as-is. Validation
  and normalization are done by `hooks/usePreferences.tsx`, field by field with a fallback to
  the default value, because the document can carry keys from a previous version or garbage
  written via SQL. There is a size cap, though (`maxPreferencesBytes`): it is client JSON stored
  without looking.
- **The server is the source of truth; `localStorage` is only a startup cache** so that the page
  does not flicker before `/api/auth/me` responds. With everything in the browser no default value
  could be enforced.
- **An `entra` user's profile is not edited here** (409): name and email are refreshed by
  `upsertEntraUser` on **every** login, so an editable field would overwrite itself.

Watch out for the audit: `trg_audit_db_users` wrote one row for EVERY update of `users`, and with
preferences in that table that would be one entry per keystroke. Migration `0007` splits the
trigger in two and the UPDATE one carries a **column list** (`AFTER UPDATE OF username,
password_hash, …`), which only fires if the statement mentions one of them in its `SET`.
`updated_at` is left out of the list on purpose: if it were there, any update would mention it
and the exclusion would be useless.

### The fleet view and permission filtering without scope middleware

`GET /api/fleet/attention` (`api/fleet.go` + `k8s/fleet.go`) returns the state of **all** the
clusters the user can see. It reuses the whole classification from `k8s/attention.go`, so a
change of criterion applies at once to a cluster's overview and to the fleet.

- **It has no cluster-scope middleware, and that is correct**: the request does not target a
  specific cluster, so there is no id to check — the *set* has to be bounded. That is done with
  the same query as `ClusterHandler.List` (global role sees all; without a global role, only the
  ones held through a `scope_type='cluster'` assignment). If another fleet endpoint is ever
  added, it has to repeat that filtering: `requireSession` by itself bounds nothing.
- Only **enabled** clusters: a disabled one has no informer and would always show as "not
  connected", which is permanent noise.
- A disconnected cluster **is still returned** with `connected: false` and its `lastError`,
  instead of being omitted: if it disappeared from the list, its lack of issues would read as
  "it is fine". The UI highlights it separately because not being able to look inside is not the
  same as having nothing.
- `counts` is empty if not connected. Sending zeros would say "there is nothing" when what is
  happening is that it is not known.

### Migrations are forward-only, never edited once applied

`internal/db/migrations/*.sql`, embedded and run with `goose`. If an already-applied migration
has a bug, it is fixed with a **new** migration — never by editing the already-applied file.
(`0005_fix_audit_log_partition_parsing.sql` fixes a real data-loss bug in the date parsing of
`0004_audit_log_partitioning.sql`; the fix is a separate file, and the file with the bug is left
as a historical record with a comment explaining what went wrong.)

### `audit_log`

Append-only: a `BEFORE UPDATE OR DELETE` trigger (`audit_log_immutable`) blocks every row
mutation, even from the app's own DB role. It is partitioned by month; the backend only
**creates** future partitions at startup (`db.EnsureAuditLogPartitions`) — dropping old
partitions (the destructive part of retention) is deliberately kept out of the binary, invoked
only from an external scheduled job (see `deploy/README.md`). `audit_log.action` is free text
(`"resource.verb"`, e.g. `"pod.exec"`, `"cluster.delete"`) without an enum/CHECK — no migration
is needed to add a new action. `audit_log.cluster` stores the cluster's **display name**, not its
id (it survives the cluster itself being deleted later). `TRUNCATE` is also blocked, but it
needed a separate trigger (`trg_audit_log_no_truncate`, migration `0006`): `FOR EACH ROW`
triggers do not fire on `TRUNCATE`, and `TRUNCATE` triggers are **not** cloned from the parent to
the partitions — which is why it exists on the parent, on each partition, and
`audit_log_ensure_future_partitions()` creates it on new partitions.

**Reading it: `GET /api/audit` (`api/audit.go`), global admin only.** It is the only reader of
`audit_log` in the whole backend. Four things that cannot be changed lightly:
- **Keyset pagination on `(ts, id)`, never `OFFSET`.** The table is partitioned by month and a
  large OFFSET forces reading and discarding whole partitions. It is a tuple and not just `ts`
  because `ts` is not unique. The real plan is a `Merge Append` over per-partition indexes.
- **One extra row** is requested to know whether there is a next page, instead of an unbounded
  `COUNT`, which is exactly what keyset avoids.
- **Only the first page is audited** (`audit.query`), not every cursor advance: reading the audit
  must leave a trace, but recording every "load more" would fill it with noise generated by the
  very screen that reads it. The **CSV export is always** audited (`audit.export`).
- The CSV is cut at `maxAuditExportRows` and, if cut, **it says so in the last line of the
  file**: the recipient opens a spreadsheet and will never see an HTTP header.

`audit_log.user_id` **no longer has an FK** to `users` (migration `0006`): it had one, and it
made deleting a user impossible (`ON DELETE CASCADE`/`SET NULL` are internally a row DELETE/UPDATE
and the immutability trigger blocks them). It can be left orphaned on purpose;
`audit_log.actor_username` stores the denormalized username so that attribution survives the
deletion, same criterion as `audit_log.cluster`. Nothing in Go reads `audit_log` — it is only
queried via SQL.

There are **two sources** of entries, deliberately redundant:
- The app (`audit.Log`): successful actions, and also denials —
  `middleware.AuditDenied` audits every authorization 403 on mutating routes as
  `access.denied` (not the 409s: some 409s are just a duplicate name; the 409s from the guards
  are audited by each handler with its own `reason`), and the WebSocket routes are left out
  because their 403 is emitted before the upgrade.
- DB triggers (`db.<table>.<verb>`) on `users`, `role_assignments` and
  `group_role_mappings`, so that a change made by hand via `psql` also leaves a trace. It
  duplicates the app's trace on purpose. `sessions` does **not** carry a trigger: `Validate`
  updates `last_seen_at` on every request and would flood the table.

### Frontend

- Server state always goes through TanStack Query (`useQuery`/`useMutation`) — never
  Redux/Context for remote data. The session is the only exception
  (`hooks/useAuth.tsx`).
- `components/ui/` holds the shared design primitives: `Button`, `Card`, `DataTable`, `Badge`,
  `Tabs`, `PageHeader`, `Toolbar`, `Input`/`Select`/`Textarea`/`Field`/`Checkbox`/`Switch`, `Alert`,
  `Modal` (with `title`/`footer`/`size`), `ConfirmDialog` + `useConfirm`, `Menu`/`Popover`/`Tooltip`/
  `RowActions` (on top of `radix-ui`: focus, keyboard and aria solved; do not reimplement a popover
  with a global `pointerdown`), `EmptyState`, `Spinner`/`Skeleton`, `DescriptionList`,
  `SegmentedControl`. Reuse them instead of putting `<input className=…>` or `window.confirm` in a
  new page: there are no `INPUT_CLASS` constants or native `confirm/prompt/alert` left, and
  `lib/errors.ts` holds the only error helpers (`apiErrorText`, plus `httpStatus`/`unavailableReason`
  for the 503 of an informer read); links built from server or third-party data go through
  `lib/url.ts: safeHttpUrl`, clipboard copies through `hooks/useCopy`, and user-visible dates through
  `lib/dates.ts` with `i18n.resolvedLanguage`. Classes are composed with `lib/cn.ts`
  (clsx + tailwind-merge), which resolves the conflicts between what the component sets and what
  the caller passes.
- **Every view follows the same skeleton**: `PageHeader` (breadcrumb `Panel › cluster › type` or
  `Panel › Administration › section`, title = the type or the section, primary action on the
  right) + `Toolbar` (search in URL `q`, filters, "clear") + `DataTable` with `error` (a failed
  query is NOT painted as "no data"), `actions` (a `RowActions` kebab per row, not text links) and
  `primary` columns (the only ones visible on mobile). "New resource" only on namespaced types
  creatable via YAML. Polling with two values: 15 s for resources, 60 s for configuration.
- **A resource's detail is a page, not a drawer**: `pages/ResourceDetail.tsx`, route
  `/clusters/:id/:kind/:namespace/:name/:view?` (`-` as the namespace for cluster-scoped types),
  Overview/YAML/Events/Logs/Terminal tabs in the URL, per-role actions in the header. It was
  chosen over drawers for having one URL per object that can be shared and opened in another
  tab. `LogsPanel`/`TerminalPanel` accept `inline` to live inside a tab.
- `DataTable` sorts and paginates in a **controlled** way (`state` + `onStateChange`, filled in
  with `hooks/useTableState.ts`), not with internal state: the order and the page live in the
  query string like the filters. A column is sortable **only if it declares `sortValue`** —
  `render` returns a `ReactNode` and cannot be compared, so sorting is declared, not guessed; and
  there are columns that are deliberately not sortable (`ready` on pods arrives from the backend
  already formatted as `"2/2"`). The three states of an empty table are resolved by the
  primitive, not by each page: `isLoading` → `common.loading`, `filtered` → `common.noResults`,
  and otherwise `emptyMessage` or `common.noData`. Without passing `state` it behaves as before:
  no sorting and no pagination.
- Tailwind v4, CSS-first config (`@theme`/`@custom-variant` in `src/index.css`, dark theme via the
  `.dark` class on `<html>`) — there is no `tailwind.config.*` file. **The tokens are in
  `@theme`**, so they are utilities: `bg-surface`, `text-fg-muted`, `border-border`, `bg-primary`,
  `rounded-md`, `z-(--z-popover)`…; do not write `bg-[var(--color-*)]`. **The palette is neutral
  grays in both themes, a deliberate choice**: light with white surfaces, pale gray sidebar
  (`sidebar` + `sidebar-hover`), dark gray topbar and a medium gray as `primary` (buttons, links,
  active item, bars); dark in grayscale with the accent in light gray. No blues or purples in
  backgrounds, buttons or links. The brand kit (`docs/brand/`) is drawn in the topbar gray
  (`#2f2f2f`), which is the `brand` token (logo on a light background and the login panel); the
  kit's original Deep Purple is no longer used. **Container Orange is `accent`, fill only (logo,
  active item indicator), never text on a light background (it does not pass AA) nor a state** (it
  gets confused with `warning`). There is a global focus ring via `:focus-visible` in
  `index.css`: do not add `outline-none`. lucide icons measure 16 px by default (base rule), 14 in
  `Button sm`.
- The WebSocket panels (`LogsPanel`, `TerminalPanel`) reopen the whole socket every time their
  identity props change (cluster/namespace/pod/container/shell) instead of trying to reuse the
  connection — a simpler lifecycle, consistent with how cheap it is to reopen these streams.
- **Navigation lives in `components/Layout.tsx` + `components/nav/`**, not inside the pages: the
  active cluster and the namespace filter are in the topbar (`ClusterPicker`, `NamespacePicker`)
  and the cluster's resources in the sidebar, grouped by section. The shell is `h-dvh` with
  **`<main>` as the only scroll** (topbar and sidebar fixed; a full-height view uses `h-full` +
  `flex-1 min-h-0`, never `calc(100vh - …)`); the sidebar has **two modes that do not mix**
  (global: fleet + administration; cluster: header with name/state and "← All clusters" +
  resources), collapses to icons and folds sections with state in `preferences`
  (`sidebarCollapsed`, `navCollapsed`). The namespace picker is rendered except on the
  `NAMESPACE_FREE_TABS` tabs: a new tab gets it by default. On mobile (<768 px) the sidebar is a
  drawer under the topbar with language and theme at the bottom, and the Ctrl+K search is not
  shown. The active cluster is derived from the **route** with `useMatch` — `Layout` is the
  element of a route without a `path`, so `useParams` does not see the child routes' params. When
  adding a resource type, it goes in the sidebar; tabs are **not** reintroduced inside
  `ClusterView`, which would duplicate the same navigation in two places to keep in sync
  (`ui/Tabs` is still there for sub-tabs within a view).
- **The namespace filter is multi-valued and does NOT classify namespaces.** It travels as a
  comma-separated list (`?ns=a,b` in the UI, `?namespace=a,b` in the API; helpers in
  `lib/namespaces.ts`, `parseNamespaces` in `api/resources.go`). The comma is safe because a name
  is a DNS-1123 label; it was chosen over the repeated parameter so as not to have to support
  multi-value in `useUrlState` or configure axios's array serializer, which generates
  `namespace[]=a`. **There is no "user namespaces only" filter, and it is not an
  oversight**: there is no reliable criterion to separate the team's from the platform's — in a
  real cluster, `kasten-io` was created by the team and `node-feature-discovery` by the platform,
  and neither the `-system` suffix nor the labels tell them apart. Since the failure is
  asymmetric (a heuristic that hides a team namespace makes the table say "0 pods" and the user
  believes it), the picker instead shows the **age** of each namespace and sorts the newest
  first: the ones created by the cluster's deployment are all born in the same minute. The
  selection is remembered **per cluster** in `preferences.namespacesByCluster` and only decides
  the **initial** value — the URL rules, so a shared link carries the
  filter. Careful when touching that effect: on a full
  load it runs **before** `/api/auth/me` responds, so it cannot mark itself as "already applied"
  while the preferences are empty (see the comment in `NamespacePicker`).
- **The command palette (`Ctrl/Cmd+K`, `nav/CommandPalette.tsx`) only reads from the TanStack
  Query cache**, it never fires requests: it searches clusters, namespaces and the active
  cluster's resources that were already loaded. Two traps if touched: `getQueriesData` with a
  partial key returns **all** the namespace variants in the cache and the same pod appears at
  once in the "all namespaces" entry and in its own, so it has to be **deduplicated** (otherwise
  it shows up as many times as listings have been visited); and the shortcut's `preventDefault`
  is essential, because `Ctrl+K` focuses the address bar in Chrome and Firefox. Searching
  instances across **all** clusters, without depending on what is in the cache, requires a search
  endpoint in the backend.
- **YAML is edited with CodeMirror 6** (`components/CodeEditor.tsx`: `@uiw/react-codemirror` +
  `@codemirror/lang-yaml`), shared by the resource editor and the Helm values. It follows the
  theme by observing the `.dark` class on `<html>` (CodeMirror does not read CSS variables). It is
  not Monaco on purpose: highlighting, line numbers and folding for ~300 KB instead of several MB.
- **Every overlay goes through `ui/Modal`** (Escape, click on the backdrop, focus trap, focus
  return and `<main>` scroll lock; its focus effect runs once per mount and reads `onClose` from a ref,
  because callers pass inline arrows and with `[onClose]` every re-render stole focus from the field
  being typed in; initial focus goes to the first focusable element of the body, not the close button); confirmations via `useConfirm` (Radix AlertDialog;
  `typeToConfirm` for "type the name", `noticeOnly` for notices). The only deliberate exception:
  `TerminalPanel`. Escape is a legitimate key inside a terminal and closing the panel with it
  would break the user's session mid-task; besides, focus has to be managed by xterm. It is
  commented in the file itself — do not "fix" it.
- **State colors are decided in `lib/status.ts`, never inline in a page** (`podTone`,
  `deploymentTone`, `nodeTone`, `clusterConnection`, `ageParts`). It is pure logic and it is where
  precedence lives: what is broken wins, and **an unknown state falls into `warning`, never into
  `success`** — Kubernetes's list of `waiting.reason` grows with every version and painting green
  something we do not know how to interpret is the only unacceptable failure here. `Unknown` on a
  node is amber, not red, because it is usually a spot/scale-down and not a failure. If you add a
  tone, do it with **literal** classes (see `ui/StatusDot.tsx`): Tailwind v4 discovers arbitrary
  values by scanning the code, so a class composed with a template at runtime is not generated
  and the element comes out without color.
- **Reads from the informer cache use `getSyncedConn`, not `getConn`.** An unsynced informer has
  an empty cache and its listers return zero items **without an error**: serving that makes an
  unreachable cluster look identical to a cluster with no resources ("0 pods, 0 deployments, 0
  nodes"). With `ErrClusterNotSynced` the API responds **503** (the cluster exists and this fixes
  itself) instead of an empty list. Mutating actions and WebSockets stay with `getConn`: they go
  straight to the API server, do not depend on the cache, and if the cluster is down they fail on
  their own with the real error.
- **A down cluster cannot look the same as an empty cluster.** `/readyz` exposes
  `informer_synced` per cluster (`k8s.ClusterReadiness`; `last_error` is stripped there because the
  probe is unauthenticated and the text quotes the API server's hostname) and the frontend
  consumes them (`api/health.ts`, query `['readiness']`) to distinguish connected / connecting /
  not connected / disabled in the Dashboard and in the cluster header. Watch out for two details:
  `/readyz` responds **503** when something is not ready (it has to be accepted as a valid
  response, not as an axios error) and it lives **outside `/api`**, so the call overrides the
  `baseURL`. The cross-reference is by **display name**, which is what that endpoint exposes.
- **What identifies what is being viewed goes in the URL, not in `useState`**: the resource in
  the route (`/clusters/:id/:tab`) and the filters in the query string via `hooks/useUrlState.ts`
  (`ns`, `q`, and `type`/`since`/`from`/`to` in events). That way a view is a shareable link and
  survives a reload. Two consequences that should not be reverted without thinking: filter
  changes navigate with `replace` (with `push`, every keystroke in the search box would leave an
  entry in the history) while changing tabs does `push`; and an empty value is **removed** from
  the parameter instead of being written as `?q=`, so that "no filter" has a single
  representation. Every value read from the URL is user input and has to be validated before
  use — `?since=foo` without normalizing reached `new Date(NaN).toISOString()`, which throws. In
  `useState` only the ephemeral interaction state stays (which panel is open, the draft of a
  replicas input), which makes no sense to link to.

### i18n

The locales live under `frontend/src/i18n/locales`: `en.json` is the source of truth and the
fallback, plus `es.json`, `ca.json`, `de.json`, `fr.json`, `pt-BR.json`, `zh-CN.json` and `ja.json`.
They always carry the exact same key structure — when adding a UI text, add the key to `en.json`
and to EVERY other locale, never to just some of them. `npm run lint` runs
`scripts/check-locales.mjs`, which fails if any locale's key set differs from `en.json`. The
default UI language is English (`fallbackLng 'en'`, preferences default `'en'`). Each language is
loaded in its own chunk (`i18next-resources-to-backend`, `load: 'currentOnly'` + fallback `en`),
and each page in its own (`React.lazy` in `App.tsx`): a new page is registered there with
`page(() => import(...), 'Name')`. Do not import a page statically from `App.tsx` to pull a
function out of it: it drags its chunk (CodeMirror included) into the main one.

The backend's user-facing messages, logs and notifications are in English.

### Test coverage is uneven on purpose, not by neglect

Packages with real unit tests today: `clientip`, `cloud` (validation, EKS token format,
transport with a fake API server), `notify` (engine with fake store/sender, including state
recovery after restart), `registries` (dockerconfigjson, validation, WWW-Authenticate parsing),
`kubectl` (line validation and local proxy with the real kubectl), `backup` (archive, fake S3,
retention, server backup schedule and `PG*` environment), `scan` (registry host and reference validation), `extensions` (URL/placeholder
validation), `rotate` (JSON re-encryption and coverage of encrypted columns against the
migrations), `costs` (allocation), `gitops` (manifest loading, labeling, prune candidates,
Kustomize build, SSH key), `session` (only the Bearer parsing), `config`, `crypto` (also the generated key file), `embeddedpg` (version checks), `tlsserve`
(self-signed CA verified by a client that only trusts it, file reload), `oidc`,
`metrics`, `rbac` (pure logic, easy to test without a real DB/cluster), `k8s` only in its pure
part (`attention_test.go`: issue classification, capacity and kubelet versions; `write_test.go`:
`ParseManifests`, `KindKeyFor`; `topology_test.go`: graph edges and statuses), `dockerenv` (only the log demultiplexing), `agent` (manifest,
rendered RBAC, keys and handshake rejection before touching the DB), `api` (only
`parseNamespaces`, `quantity` and the GitOps regexes) and `middleware`, but only the pure part of
`AuditDenied` (`shouldAudit` and the status/reason recorder, `middleware/audit_test.go`) and the
token role cap (`token_test.go`), the login rate limiter eviction and the must-change-password
allowlist; `auth` (username validation), `rbac` also `ReduceSSOGrants`, `api` also `csvSafeCell`,
`oidc` also the state cookie expiry/path and group cache eviction, `web` (security headers, cache
headers, no directory listing), `helm` (chart name validation, cache pruning), `mcp` (protocol, token rules, dispatch as the caller, dry-run ledger), `ai` (the three provider wire formats and their tool-calling replay against fakes, redaction, prompt fencing). What remains without tests (the `api` handlers, the `k8s`
writes against the API server, `ws`, `session`, the rest of `auth`, `db`, the `agent` tunnel, the `dockerenv`
client) is verified through manual end-to-end testing against a real cluster, because it is thin
glue around `client-go`/`pgx`/remotedialer — keep that pattern (extract the pure logic into a
testable helper when it is not trivial, instead of resorting to heavy mocks of the Kubernetes/DB
clients).

### Comment style

Comments are written in English and explain the *why*, not the *what* (a hidden constraint, a
past bug, a non-obvious decision) — see almost any existing file for reference. Do not add
comments that merely repeat what the code already says.
