# Deployment

Containers only: there are no native packages. Four ways:

| Method | Where | File |
|---|---|---|
| **Single container** | try it, or a small install on one host: `docker run` with one volume | the image itself |
| **Helm chart** | in a Kubernetes cluster (bundled or external Postgres, Ingress with cert-manager, Secrets stable across upgrades) | `deploy/helm/bayplan` |
| **Docker/Podman Compose** | on a container host, with Postgres as its own container | `deploy/compose/docker-compose.yml` |
| **Podman Quadlet** | on a host with systemd (AlmaLinux/RHEL) | `deploy/*.container` |

The agent for managed clusters is **not** deployed with any of these: the server itself
generates it as a manifest when a cluster is registered.

## Single container (all-in-one)

One container with its own database and HTTPS, for trying it out or for a single host:

```bash
docker run -d --name bayplan --restart=always -p 8443:8443 -v bayplan-data:/data ghcr.io/bayplan/bayplan
docker logs bayplan 2>&1 | grep -A3 "First startup"     # one-time /setup token
# open https://<host>:8443 (self-signed certificate)
```

Without `DATABASE_URL` the server runs in **all-in-one** mode: it starts the PostgreSQL 18 shipped
in the image as a child process (Unix socket only, data in `/data/postgres`), generates the
encryption key in `/data/encryption-key` and serves HTTPS on `:8443` with its own CA
(`/data/tls`); `:8080` only redirects to it. Everything lives in the volume.

- **Back up the volume and, apart, the key** (`docker cp bayplan:/data/encryption-key .`). In
  this mode the key sits next to the data for convenience; whoever wants it apart passes
  `ENCRYPTION_KEY_V1` + `ENCRYPTION_KEY_ACTIVE_ID=v1` in the environment, which always wins.
- Agents registered against this server receive its CA in the manifest (`BAYPLAN_CA_CERT`) and
  verify the server with it: no `INSECURE_SKIP_VERIFY` needed. Set the public URL in
  Settings → Server to the address the clusters will use.
- It is a single PostgreSQL inside the container, without replicas or PITR. For production,
  the chart or Compose with a database of their own; the move is a backup and a restore
  ("Backing up Bayplan itself").
- `-e BOOTSTRAP_PASSWORD=…` (12+ characters) creates the admin with that password instead of
  the token, for scripted installs; it has to be changed on first login.

## HTTPS served by the binary

Any installation (not only all-in-one) can terminate TLS itself instead of behind a proxy:

| Variable | Meaning |
|---|---|
| `TLS_LISTEN_ADDR` | HTTPS address (`:8443`). On by default in all-in-one; `TLS_LISTEN_ADDR=` (empty) turns it off there |
| `TLS_CERT_FILE`, `TLS_KEY_FILE` | your own certificate (PEM). Re-read when the file changes: certbot/cert-manager renewals need no restart |
| `ACME_DOMAINS`, `ACME_EMAIL` | Let's Encrypt (TLS-ALPN-01): the HTTPS port must be reachable as 443 (`-p 443:8443`). `ACME_DIRECTORY` for another ACME CA or the staging one |
| neither | certificates issued on the fly by a private CA kept in `DATA_DIR/tls` |

With HTTPS on, the HTTP listener (`LISTEN_ADDR`) only redirects, except `/healthz`, `/readyz`
and `/metrics`. Behind an ingress or a proxy leave `TLS_LISTEN_ADDR` empty, as the chart and
Compose do.

## Helm chart

Published as an OCI artifact on GHCR and in a classic Helm repository by the CI on every `v*`
tag (same version as the images):

```bash
helm upgrade --install bayplan oci://ghcr.io/bayplan/charts/bayplan --version 0.1.1 -n bayplan --create-namespace \
  --set ingress.enabled=true --set ingress.host=bayplan.example.com --set ingress.tls.enabled=true \
  --set ingress.tls.source=letsEncrypt --set letsEncrypt.email=ops@example.com
kubectl -n bayplan get secret bayplan-bootstrap -o go-template='{{.data.bootstrapPassword|base64decode}}{{"\n"}}'
```

```bash
# The same chart from the classic repository:
helm repo add bayplan https://bayplan.github.io/bayplan && helm repo update
helm upgrade --install bayplan bayplan/bayplan -n bayplan --create-namespace …
```

Or from the repository (development): `helm upgrade --install bayplan deploy/helm/bayplan …`.

- **Ingress certificate** (`ingress.tls.source`): `secret`
  (you create `ingress.tls.secretName`), `letsEncrypt` (cert-manager + HTTP-01, needs
  `letsEncrypt.email`; `letsEncrypt.environment=staging` to test) or `selfSigned` (cert-manager
  creates a private CA for this install; the server mounts it and puts it in every agent
  manifest so agents keep verifying). The last two need cert-manager in the cluster.
- **Initial password**: generated into the Secret `<release>-bootstrap` (or `bootstrapPassword`)
  and only used on the first start; the admin must change it on first login. Without it the
  one-time setup token in the log still works as before.
- The encryption key and the Postgres password are generated on the first install and
  **preserved** across upgrades (`lookup` of the Secret; `helm.sh/resource-policy: keep`). Keep
  the `bayplan-encryption` Secret separate from the database backup.
- `postgresql.enabled=false` + `externalDatabaseURL` (or `existingDatabaseSecret`) for a managed
  database. `replicaCount` must stay at 1: the settings cache, the rate limiter and the agent
  tunnels live in memory.
- `publicURL` is derived from the ingress if not given. `trustedProxies` is **empty** by default:
  until it is set, the audit log records the ingress controller's IP instead of the client's.
  Set it to the controller's pod CIDR (e.g. `--set trustedProxies=10.42.0.0/16`), not to every
  private range: whatever it trusts can write `X-Forwarded-For`, the IP the audit log records and
  the per-IP login limit keys on (the per-account limit does not depend on it). Pair it with
  `networkPolicy.enabled=true` (`networkPolicy.ingressNamespaces`, default `ingress-nginx`),
  which only lets the controller's namespace reach the server and only the server reach the
  bundled Postgres; it needs a CNI that enforces NetworkPolicy.
- The chart sets the ingress-nginx WebSocket timeouts (agents, logs, terminals); with another
  controller configure its own idle/tunnel timeout (see `ingress.annotations` in `values.yaml`).
- **GitOps tools that render with `helm template`**: `lookup` always returns empty there, so the
  generated key would change on every sync and the stored credentials could no longer be
  decrypted. Create the Secrets outside the chart (Sealed Secrets, External Secrets…) and pass
  `existingEncryptionSecret` (key `encryptionKey`) and `existingDatabaseSecret` (keys
  `databaseURL` and, with the bundled Postgres, `password`). Tools that run a real Helm
  install are not affected.
- The chart is validated when running `make test` (`internal/helm/chart_test.go` renders it with
  the Helm engine), so there is no need to have `helm` installed to develop.

## Compose

```bash
cd deploy/compose && cp ../env.example bayplan.env   # edit DATABASE_URL, ENCRYPTION_KEY_V1, PUBLIC_URL…
POSTGRES_PASSWORD=<the same one as in DATABASE_URL> docker compose up -d
```

## Upgrades and security patches

**Versions.** SemVer: `X.Y.Z`. A patch (`Z`) only fixes bugs and vulnerabilities, a minor (`Y`)
adds features, and both upgrade in place. Only the latest release receives fixes (see
`SECURITY.md`). Migrations only go forward: **there is no downgrade** of the database, so take a
backup ("Backing up Bayplan itself") before a minor upgrade.

**How to upgrade**, by installation method:

| Method | Command |
|---|---|
| Single container | `docker pull ghcr.io/bayplan/bayplan:<ver>` and recreate the container with the same volume |
| Helm | `helm upgrade bayplan oci://ghcr.io/bayplan/charts/bayplan --version <ver> --reuse-values` |
| Compose | change `BAYPLAN_IMAGE` (or keep `latest`) and `docker compose pull && docker compose up -d` |
| Quadlet | `podman auto-update` (with `AutoUpdate=registry`) or change `Image=` and restart |

The agents of the managed clusters follow on their own: the server tells them its version and
they patch their own Deployment (`Image` of the same repository only). Clusters with the direct
connector need `deploy/rbac/serviceaccount.yaml` reapplied when the release notes say so.

**Tags that receive patches.** `latest` and `X.Y` move with every patch release; `X.Y.Z` never
moves. Pin `X.Y` to get fixes without features, `X.Y.Z` to decide every upgrade by hand.

**How vulnerabilities are handled** (what the repository does, so you know what to expect):

- Dependabot proposes the updates of Go modules, npm packages, base images (Alpine, Go, Node)
  and CI actions every week, grouped; security updates come immediately.
- Every push and pull request runs `govulncheck` (only vulnerabilities the code actually
  reaches), `npm audit` of the production dependencies and Trivy on both images: an image with a
  HIGH or CRITICAL vulnerability that has a fix **is not published**.
- Every Monday the `security-scan` workflow scans the **already published** images (`latest`
  and `main`), the dependencies and the pinned tools (kubectl, Trivy), and opens or updates a
  GitHub issue labeled `security` if something has a fix; it closes it when everything is clean
  again. Nothing is rebuilt or republished automatically: a person decides the patch release.
- A patch release is a normal tag (`vX.Y.Z+1`) with the updated dependencies; the release notes
  name the CVEs it fixes, and a GitHub security advisory is published when the vulnerability is
  in Bayplan's own code.
- `make audit IMAGES="ghcr.io/bayplan/bayplan:latest"` runs the same checks locally (needs
  `trivy`). Accepted findings go in `.trivyignore.yaml`, each with its reason, the binary and an expiry date.

## Air-gapped installation

Every release carries `bayplan-images.txt` (server, agent, Postgres, metrics-server and Trivy's
vulnerability databases), generated by `deploy/airgap/images.sh`. To copy them into your
registry, keeping their paths:

```bash
deploy/airgap/mirror.sh registry.example.com 0.1.1     # crane (preferred) or skopeo
helm upgrade --install bayplan oci://registry.example.com/charts/bayplan … \
  --set imageRegistry=registry.example.com             # server, agent and Postgres images
```

- `imageRegistry` rewrites the registry of the server, Postgres and the agent image the server
  writes into the manifests (Compose/Quadlet: `BAYPLAN_IMAGE`, `AGENT_IMAGE`). The chart itself
  goes with `helm pull oci://ghcr.io/bayplan/charts/bayplan --version 0.1.1` + `helm push`.
- Image scanning: `TRIVY_DB_REPOSITORY=registry.example.com/aquasecurity/trivy-db:2` and
  `TRIVY_JAVA_DB_REPOSITORY=registry.example.com/aquasecurity/trivy-java-db:1` in the server's
  environment. Without them the scan fails and says so; nothing else depends on it.
- metrics-server: installing it from the panel uses the official manifest unedited
  (`registry.k8s.io`); offline, download it from the cluster's overview, change the image and
  apply it with kubectl.
- Agents update themselves to another tag **of the same repository**: mirror each new version
  before upgrading the server.

## Backing up Bayplan itself

Users, permissions, clusters, settings and the audit log live in Bayplan's database. Under
**Administration → Backup targets → Backup of Bayplan itself** a `pg_dump` goes to one of the S3
targets on a schedule (interval and copies kept), or is downloaded on the spot. From the command
line, the same dump:

```bash
docker exec bayplan bayplan backup - > bayplan.dump                 # all-in-one / Compose
kubectl -n bayplan exec deploy/bayplan -- bayplan backup - > bayplan.dump
```

The **encryption key is not in the dump**, on purpose: a leaked backup without the key is
useless. Keep the key apart; a restore needs both.

Restoring goes into an **empty** database, before starting the server on it:

```bash
# all-in-one: a new volume, the old key, the dump readable by UID 10001
docker run --rm -v bayplan-new:/data -v "$PWD":/backup:ro \
  -e ENCRYPTION_KEY_ACTIVE_ID=v1 -e ENCRYPTION_KEY_V1="$(cat encryption-key)" \
  ghcr.io/bayplan/bayplan restore /backup/bayplan.dump
docker run -d --name bayplan -p 8443:8443 -v bayplan-new:/data ghcr.io/bayplan/bayplan

# external database: same DATABASE_URL and keys as the server, with the server stopped
bayplan restore bayplan.dump
```

In all-in-one the restore writes the key into the new volume, so the following starts use it.

### Upgrading the embedded PostgreSQL

The image pins the PostgreSQL major version (18). If a future image brings another one, the
server refuses to start on the old data and points here: take a backup with the previous image
(`docker exec bayplan bayplan backup - > bayplan.dump`), then restore it into a new volume with
the new image as above. Moving from all-in-one to an external database is the same procedure
with `DATABASE_URL` set.

### PostgreSQL 16 → 18 in Compose, Quadlet and the chart

Compose, the Quadlet unit and the chart's bundled database moved from PostgreSQL 16 to 18, the
same major as all-in-one. A new major does not start on the old data directory: Postgres refuses
it and nothing is lost. Compose and Quadlet also mount the volume at `/var/lib/postgresql` instead
of `/var/lib/postgresql/data`, which is where the 18 image keeps its data. An installation created
before this change either pins `postgresql.image` (chart) / the image line back to
`postgres:16.15-alpine3.24`, or moves over with `bayplan backup -` on the old database and
`bayplan restore` into a fresh volume with the new one, before starting the server.

## Images, tags and signatures

`ghcr.io/bayplan/bayplan` (server) and `ghcr.io/bayplan/bayplan-agent` (agent), for
`linux/amd64` and `linux/arm64`:

| Tag | Moves with |
|---|---|
| `0.1.1` | never (one release) |
| `0.1` | the 0.1.x patches |
| `1` (from 1.0 on) | every 1.x release |
| `latest` | the last published release; never `main` |
| `main` | every commit to `main` (unreleased, for testing) |

Both images are mirrored to Docker Hub with the same tags and signatures:
`docker.io/bayplan/bayplan` and `docker.io/bayplan/bayplan-agent` (the CI does it when the
repository variable `DOCKERHUB_NAMESPACE` is set; forks publish only to GHCR). The chart is published
as OCI (`oci://ghcr.io/bayplan/charts/bayplan`) and in the classic repository
`https://bayplan.github.io/bayplan`.

Images and chart are signed keyless with cosign (the CI's GitHub OIDC identity) and the images
carry SBOM and provenance attestations:

```bash
cosign verify ghcr.io/bayplan/bayplan:0.1.1 \
  --certificate-identity-regexp 'https://github.com/bayplan/bayplan/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## CI

`.github/workflows/ci.yml`: backend and frontend lint and tests on every push/PR, and on `main`
and `v*` tags it publishes `ghcr.io/<repo>` (server) and `ghcr.io/<repo>-agent` (agent), and on
`v*` tags also the chart, the SBOM and the GitHub release.

## Podman Quadlet (AlmaLinux 9)

Only the **irreducible bootstrap** goes through the environment: database, encryption key and
port. Everything else (public URL, SSO/OIDC, clusters) is configured from the UI under
**Administration → Settings** and stored in the database, encrypted where appropriate.

## Building images by hand

```bash
make images VERSION=0.1.1      # localhost/bayplan:0.1.1 and localhost/bayplan-agent:0.1.1
```

Only needed for a private registry or an offline installation: the published images are the
normal path.

The **agent** image has to be published to a registry reachable from the managed clusters, and
its reference set in **Settings → Server → Agent image**: that is what gets written into the
registration manifest of a cluster with the agent connector.

## Local cluster and agents

With the chart, the cluster where Bayplan is installed shows up on its own as **local** (value
`localCluster.enabled=true`): the server connects with its ServiceAccount and the chart grants it
the panel's RBAC (`localCluster.rbacMode`: `scoped` or `cluster-admin`). There is nothing to
register and no credentials to paste. The remaining clusters are added through an **agent** from
the UI (generated manifest).

The server proposes to the agents **its own image version**
(`ghcr.io/bayplan/bayplan-agent:<version>`, published by the CI together with the server and the
chart) and the agents upgrade themselves when the server changes version. Leave `agentImage`
empty unless you publish the image to your own registry. If the GHCR package is private, the
cluster needs an `imagePullSecret` in the `bayplan-agent` namespace, or make the package public
(GitHub → Packages → bayplan-agent → Change visibility).

## Panel permissions in the managed cluster

Two modes, chosen when registering a cluster with agent or cloud (editable afterwards):

- **Scoped** (default): the panel only reads what it shows (ClusterRole `bayplan-technical`)
  and writes through three impersonated groups (`bayplan:admins`/`editors`/`readonly`), each
  with its own ClusterRole. It is real least privilege, at a cost: when a panel version adds
  resource types, `deploy/rbac/serviceaccount.yaml` has to be reapplied in **direct** clusters
  and the cloud RBAC regenerated; **agent** clusters update themselves (the agent applies its
  RBAC on start and every 6 h, and the clusters table marks "RBAC up to date").
- **cluster-admin** ("install and forget"): in addition to the scoped RBAC, the agent (or the
  cloud identity) and the `bayplan:admins` group are bound to `cluster-admin`. Editors and read-only stay scoped. Nothing ever
  needs reapplying. Going back to scoped mode means deleting two ClusterRoleBindings
  (`bayplan-cluster-admin` and `bayplan-impersonated-admin-cluster-admin`).

**metrics-server** is optional (real CPU and memory consumption; without it only what is
reserved is shown). GKE, AKS, RKE2 and K3s ship it; EKS and kubeadm do not. From a cluster's
overview an admin can install it (official components.yaml embedded, pinned version, with
dry-run) or download the manifest and apply it with kubectl. In scoped mode installing from the
panel requires the current RBAC (the admin's ClusterRole carries what is needed); otherwise
download and apply by hand.

**Admission policies** (`deploy/rbac/admission-policy.yaml`, Kubernetes 1.30+) are the API
server's half of the panel's limits; they only match requests made with the `bayplan:*` groups:

- No workload may run as the panel's own ServiceAccounts (`bayplan-technical`, `bayplan-agent`)
  nor get a token Secret for them: those identities can impersonate the panel's groups, so a pod
  running as one would turn an editor into a panel admin outside its audit.
- Nothing may be written in the agent's namespace (`bayplan-agent`).
- In scoped mode, the admin group may only create the RBAC and APIService of metrics-server, with
  their real role references and subjects: otherwise a panel admin could bind a role to an
  identity of their own and keep access after losing it in the panel. This also means a Helm
  chart that brings its own ClusterRoles or bindings cannot be installed from the panel in
  scoped mode; install it with your own credentials or use cluster-admin mode, which leaves this
  one policy out.

The agent manifest and the chart (local cluster) include them; in a **direct** cluster apply
them next to the RBAC. The panel applies the first rule itself before writing, so it still holds
on older clusters, where kubectl reports these kinds as unknown and the rest is applied.

## Three ways to connect a cluster

- **Agent (recommended):** create the cluster in the UI with just a name and run the
  command it gives you (`kubectl apply -f <URL>`; with the built-in self-signed certificate or a
  CA pasted in Settings, a `curl` that verifies the download against that CA, piped to
  `kubectl apply -f -`, which needs bash or zsh). The agent opens an outbound connection to the server;
  the server does not need to reach the API server and there is no CA/token to paste. Works for
  clusters behind NAT or with a private endpoint. The manifest includes all the RBAC.
- **Docker/Podman (through the agent):** create the environment with type "Docker" and run the
  `docker run` it gives you on the host (it mounts `/var/run/docker.sock`). Containers, images,
  volumes, networks, logs and terminal. Works for Podman with its compatible socket.
- **Cloud credential (EKS/AKS/GKE):** under Administration → Cloud credentials a provider
  credential is stored (AWS access key, Azure service principal or JSON key of a Google service
  account). When adding a cluster, the "Cloud" tab lists the clusters that credential can see
  and the server connects with short-lived tokens from the provider (STS, Entra ID, OAuth2): no
  token or CA to paste or rotate. The registration returns the RBAC to apply for the provider's
  identity (on EKS it first has to be mapped to a group with an *access entry*; the modal gives
  the command). Requires the server to reach the API server endpoint: for a private cluster,
  use the agent.
- **Direct:** kubeconfig, ServiceAccount token or client certificate, with
  `deploy/rbac/serviceaccount.yaml` (and, on 1.30+, `deploy/rbac/admission-policy.yaml`) applied
  in the cluster beforehand. For when the server does
  reach the API server.

## Web kubectl shell and image scanning

The image includes `kubectl` and `trivy` (versions pinned in `deploy/Containerfile`, checksums
verified). For other binaries, `KUBECTL_PATH` and `TRIVY_PATH`. The container remains read-only:
the shell uses a per-session directory under `/tmp` and Trivy keeps its vulnerability database in
`/tmp/bayplan-trivy` (downloaded from ghcr.io the first time; without internet access the scan
fails and says so). If `/tmp` is not persistent, the DB is downloaded again on every start
(~60 MB).

## Secrets and environment file

```bash
openssl rand -base64 32 | podman secret create bayplan_encryption_key_v1 -
podman secret create bayplan_db_password -            # Postgres password (used by bayplan-db)
# Full DATABASE_URL with the same password inside: the binary does not assemble it from pieces.
printf 'postgres://bayplan:<PASSWORD>@bayplan-db:5432/bayplan?sslmode=disable' | podman secret create bayplan_database_url -

mkdir -p ~/.config/bayplan
cp deploy/env.example ~/.config/bayplan/bayplan.env     # edit: TRUSTED_PROXIES, timeouts…
```

Keep a copy of `bayplan_encryption_key_v1` **separate** from the database backup: without the
key, a restore cannot decrypt the cluster tokens or the OIDC client secret.

## Encryption key rotation

1. Generate the new key and add it to the environment **without removing the old one**:
   `ENCRYPTION_KEY_V2=…` and `ENCRYPTION_KEY_ACTIVE_ID=v2`. Restart: new data is encrypted with
   v2, old data keeps being read with v1.
2. Re-encrypt everything with the active key (it can be done with the server running):
   ```bash
   podman exec bayplan bayplan reencrypt                   # Quadlet / Compose (docker exec)
   kubectl -n bayplan exec deploy/bayplan -- bayplan reencrypt   # Helm
   ./bin/bayplan reencrypt                                  # from the repository
   ```
   It prints how many rows it re-encrypted per table. If it warns about "skipped" rows (a key
   the keyring does not know), do NOT remove any key: some row still depends on it.
3. Only then remove `ENCRYPTION_KEY_V1` from the environment and restart.

## Installing the units

```bash
mkdir -p ~/.config/containers/systemd
cp deploy/*.container deploy/*.network deploy/*.volume ~/.config/containers/systemd/
systemctl --user daemon-reload
systemctl --user start bayplan-db.service
systemctl --user start bayplan.service
journalctl --user -u bayplan.service | grep -A3 "First startup"   # /setup token (30 min; reissued on every start until used)
```

The unit pulls `ghcr.io/bayplan/bayplan:latest` and carries `AutoUpdate=registry`: with
`systemctl --user enable --now podman-auto-update.timer` Podman pulls a new release of that tag
and restarts the service. Pin `Image=` to a version (or to `0.1` to only take patches) if you
prefer to upgrade by hand.

The binary serves plain HTTP on `:8080`: a reverse proxy with TLS goes in front. Put its network
in `TRUSTED_PROXIES` so that auditing and the login attempt limit see the client's real IP.
`/readyz` only depends on the database: a managed cluster that is down does not take the panel
out of the load balancer (its state is visible in the UI).

## Interactive terminal in containers (pods/exec)

The exec terminal needs `pods/exec` (`create`) in the managed cluster's RBAC, in addition to
what logs/restart/scale already required. It is included in
`deploy/rbac/serviceaccount.yaml` (`bayplan-technical` and `bayplan-impersonated-admin`,
deliberately **not** in `bayplan-impersonated-readonly` — exec is a write). If the
`ClusterRole` was already applied in a cluster before this feature existed, the updated YAML has
to be reapplied:

```bash
kubectl apply -f deploy/rbac/serviceaccount.yaml
```

Without this permission, an attempt to open a terminal fails with 403 on the first command
executed (not when opening the WebSocket connection, which only depends on Bayplan's internal
RBAC).

## SSO (OIDC)

Configured under **Administration → Settings → Single sign-on**, with no environment variables
and no restart: issuer, client id, client secret (encrypted), scopes, user claim and groups
claim. Any OpenID Connect provider works (Entra ID, Keycloak, Okta, Google…); there are templates
that fill in the usual values. The **redirect URI** to register with the provider is computed
by the panel from the public URL (`<public URL>/api/auth/oidc/callback`) and shown on the same
screen. On save, the issuer is checked to answer OIDC discovery before persisting.

**Entra ID:** single-tenant App Registration, "Web" redirect URI with the path above, delegated
permission `GroupMember.Read.All` with admin consent if the Microsoft Graph fallback is enabled
(needed with more than ~200 groups: Azure does not include the list in the token). The
"Microsoft Entra ID" template adds the Graph scope.

**Earlier deployments with `AZURE_*`:** on start, if the four variables exist and there is no
SSO in Settings yet, they are migrated once (the old redirect URI `/api/auth/entra/callback` is
still served as an alias). Afterwards they can be removed from the environment.

**Roles:** `group_role_mappings` translates IdP groups into roles (`admin`, `editor`,
`readonly`) from **SSO groups**. A user without a mapped group can still log in if an admin
assigned them a role by hand under **Users and roles** (needed with providers that do not emit
groups, such as Google: the user tries to log in once, appears in the list and is granted the
role). Without either, the login fails with `no_role_mapped`.

Beware if inserting by hand into `group_role_mappings`: the `UNIQUE` does **not** deduplicate
mappings with `global` scope (`scope_ref` NULL). The UI checks it; a repeated direct `INSERT`
creates twin rows.

## AI features (optional)

Nothing related to AI runs until a global admin configures a provider in **Settings → AI**:
OpenAI or any OpenAI-compatible server (Azure OpenAI, Ollama, vLLM, LiteLLM…), Anthropic or Gemini.
The key is stored encrypted and never returns to the browser.

- **Network:** the SERVER calls the provider (browsers never do), so it needs egress to the
  provider's API, or to the self-hosted model's URL. With a model inside your network nothing
  leaves it. The cloud metadata range is refused as a target.
- **What is sent:** an object's YAML without env values, Secret data, ConfigMap values or
  managedFields, its recent events and the last log lines of the troubled container, with
  secret-looking text scrubbed (a heuristic: review workloads that log credentials). Never
  anything from the panel's own namespace. The assistant only reads, with the user's
  permissions.
- **Control:** a minimum role per cluster (editor by default), a daily limit per person counted
  before the call (refused if the counter cannot be read), and audit entries `ai.explain` and
  `ai.assistant` (provider, model, tools, tokens; never the text).
- **MCP:** `/api/mcp` lets a user drive Bayplan from their own AI client with a personal API
  token (the token dialog shows the `claude mcp add` line). It needs no provider configured and
  goes through the same RBAC and audit (`detail.via = "mcp"`); use read-only tokens unless the
  client must make changes.

## Notes

- `bayplan-db` does not publish a port: only reachable from `bayplan-internal.network`.
- `bayplan` runs with `ReadOnly=true`, `NoNewPrivileges=true` and a non-root user.
- A single instance of the binary per database: the settings cache, the OIDC provider and the
  login attempt limit live in memory and are not coordinated across replicas.

## `audit_log` retention (1 month)

`audit_log` is partitioned by month (migration `0004_audit_log_partitioning.sql`).
The backend itself, on every start, makes sure the partitions for the current and the
next month exist (`db.EnsureAuditLogPartitions`) — this is additive and never deletes
anything.

Volume to keep in mind since migration `0006`: the DB audit triggers write a `db.*` row for
every change in `users`/`role_assignments`/`group_role_mappings`, and that includes what the
app itself does. An SSO login generates **~3 extra rows** (the user upsert does an
`ON CONFLICT DO UPDATE` on every login and the role sync a DELETE+INSERT of the global
assignment). All of it goes away with the same monthly partition purge, but it is worth
knowing when sizing the volume.

Watch out for one detail of the purge: `audit_log_drop_old_partitions` only looks at the
monthly partitions (`relname LIKE 'audit_log_y%'`), so **`audit_log_default` is never
purged**. Only rows whose `ts` falls in no monthly partition land there (odd clocks, or a
month without a partition because the external job failed and the binary was not
restarted), but if it accumulates rows it has to be emptied by hand — and the immutability
trigger blocks both `DELETE` and `TRUNCATE`, so the way is `DROP TABLE audit_log_default`
(after a `DETACH` if you want to keep it) and recreating it.

The **deletion** of old partitions (the destructive part of retention) lives outside the
binary on purpose, in a scheduled job that has to be set up separately, for example with a
systemd timer next to the Quadlet units:

```bash
# /etc/systemd/system/bayplan-audit-retention.service
[Unit]
Description=Purge old audit_log partitions (1 month retention)

[Service]
Type=oneshot
ExecStart=podman exec bayplan-db psql -U bayplan -d bayplan -c "SELECT audit_log_drop_old_partitions('1 month');"
```

```bash
# /etc/systemd/system/bayplan-audit-retention.timer
[Unit]
Description=Monthly audit_log purge

[Timer]
OnCalendar=monthly
Persistent=true

[Install]
WantedBy=timers.target
```

```bash
systemctl enable --now bayplan-audit-retention.timer
```

If the target Postgres has the `pg_cron` extension available, it is a simpler alternative to
a systemd timer:

```sql
SELECT cron.schedule('audit-log-retention', '0 3 1 * *', $$SELECT audit_log_drop_old_partitions('1 month')$$);
```
