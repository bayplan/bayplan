# Changelog

All published versions are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versioning follows
[SemVer](https://semver.org/).

## [Unreleased]

### Fixed

- Helm chart: the server crash-looped on a fresh install with `localCluster.enabled` (the
  default) because the local cluster violated the `clusters_has_ca` constraint. Migration `0033`
  exempts the in-cluster connector, whose CA comes from the pod.

## [0.1.0] - 2026-10-26

### Added

- First public release: multi-cluster Kubernetes management with direct, agent, cloud
  (EKS/AKS/GKE) and local-cluster connectors; per-cluster RBAC with three roles; local login and
  OIDC SSO; Helm, lightweight GitOps, namespace quotas, image registries, Trivy scanning,
  configuration backups to S3, notifications, web kubectl shell, API tokens and OpenAPI,
  estimated cost per namespace, UI extensions and Docker environments.
- Apache 2.0 license, third-party notices, security policy and contributing guide.
- Images for linux/amd64 and linux/arm64, signed with cosign (keyless) with SBOM and provenance
  attestations; the chart is signed too. `latest` only moves with releases; `main` follows the
  branch.
- Single-container install (`docker run -v bayplan-data:/data ghcr.io/bayplan/bayplan`): without
  `DATABASE_URL` the server runs the PostgreSQL shipped in the image, generates its encryption key
  and serves HTTPS on 8443.
- HTTPS served by the binary: own certificate files, Let's Encrypt (ACME) or a private CA whose
  certificate goes into the agent manifests, so agents verify the server.
- Chart: ingress certificates from cert-manager (`ingress.tls.source`: letsEncrypt or
  selfSigned), generated initial admin password (`bootstrapPassword`), `imageRegistry` for
  private registries.
- Backups of Bayplan's own database: scheduled to S3, downloadable, and `bayplan backup|restore`.
- Air-gapped installs: image list with every release and `deploy/airgap/mirror.sh`.
- Classic Helm repository (`helm repo add bayplan https://bayplan.github.io/bayplan`) and optional
  Docker Hub mirror.
- Topology view per cluster: Ingress → Service → workload → pods → volumes, live, with zoom, pan,
  minimap and the dependency path of any object.
- AI, all optional and off until an admin configures a provider (OpenAI-compatible including
  self-hosted models, Anthropic or Gemini): "Explain" on failing objects and a read-only assistant
  panel, both with the user's permissions, redaction of env values and secrets, a minimum role, a
  daily limit per person and audit; and an MCP server (`/api/mcp`) to use Bayplan from Claude Code,
  Codex or Cursor with a personal token.
- Settings organized by section; the light theme by default.

### Security

- Dependencies at their patched versions (`golang.org/x/net`, `golang.org/x/crypto`,
  `react-router`, `follow-redirects`, k8s.io 0.37.1, Helm 3.22.1, pgx 5.11), Go 1.27.3 for the
  binaries, Node 24 LTS for the frontend build, Alpine 3.24.3, kubectl 1.37.1 and Trivy 0.76.1 in the
  image, base images and the bundled Postgres pinned to exact versions (18.6 in Compose, Quadlet
  and the chart, as in all-in-one; see "PostgreSQL 16 → 18" in `deploy/README.md`); CI actions on
  their current majors (Node 24 runtime).
- CI blocks publishing images with fixable HIGH/CRITICAL vulnerabilities, runs govulncheck,
  npm audit and gitleaks, and checks that the third-party notices are current; a weekly scan of
  the published images opens a `security` issue.
- The technical ServiceAccount's ClusterRole is read-only (plus `impersonate`): every write goes
  through an impersonated `bayplan:*` group.
- Chart: the bundled Postgres runs non-root with dropped capabilities and resource limits, and an
  opt-in `networkPolicy` restricts who reaches the server and the database. `trustedProxies` is
  empty by default (trusting every private range let anyone on the LAN write the audited IP): set
  it to the ingress controller's pod CIDR; the server logs a warning and NOTES.txt reminds it
  while it is missing.
- ValidatingAdmissionPolicies (`deploy/rbac/admission-policy.yaml`, Kubernetes 1.30+), included in
  the agent manifest and the chart; in **direct** clusters apply them next to the RBAC. They only
  match the panel's `bayplan:*` groups:
  - no workload may run as the panel's own ServiceAccounts nor get a token for them (the panel
    refuses it too before writing, also on older clusters);
  - nothing may be written in the agent's namespace;
  - in scoped mode the admin group can only create metrics-server's RBAC and APIService, so a panel
    admin cannot leave themselves access outside the panel. As a consequence, Helm charts that
    bring their own cluster RBAC cannot be installed from the panel in scoped mode (use your own
    credentials or cluster-admin mode).
- On agent clusters the agent's namespace is protected like the panel's own: no reads of its
  Secrets, writes, exec or logs through the panel.
- Login: rate limited per IP and per account, the per-account limit only counting failures; a
  browser that already logged in as that user carries a device cookie (sealed, bound to the
  current password, with its own failure limit), so failures by others cannot lock the owner out.
  Login and setup only accept JSON (login CSRF). Changing the password or the profile requires a
  browser session, not an API token.
- Secret values stay masked below admin everywhere: masking also drops the annotations that carry a
  copy of the object (`kubectl.kubernetes.io/last-applied-configuration` and similar), Helm release
  values and dry-run output, and the environment of Docker containers. Lists any user can reach no
  longer show registry usernames, S3 access key ids or endpoints.
- GitOps and Helm repository URLs with credentials inside (`https://user:token@…`) are refused: the
  credentials go in their own encrypted fields.
- Outbound calls (webhooks, git, registries, AI providers) refuse every cloud metadata endpoint,
  including AWS IMDS over IPv6, and the AI client follows no redirects. Deleting or disabling an
  agent cluster closes its tunnel at once. The working directories in `/tmp` must be private to
  the server's user.
