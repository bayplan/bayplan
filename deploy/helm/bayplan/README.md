# Bayplan

Lightweight, self-hosted multi-cluster Kubernetes management: one panel for every cluster, with
per-cluster RBAC (admin, editor, read-only), audited actions, local login or OIDC SSO, Helm,
lightweight GitOps, image scanning, configuration backups and AI troubleshooting.

The cluster the chart is installed in shows up as the **local** cluster with no configuration;
other clusters join through an agent that opens an outbound connection to the server.

## Install

```bash
helm upgrade --install bayplan oci://ghcr.io/bayplan/charts/bayplan -n bayplan --create-namespace \
  --set ingress.enabled=true --set ingress.host=bayplan.example.com --set ingress.tls.enabled=true \
  --set ingress.tls.source=letsEncrypt --set letsEncrypt.email=ops@example.com

# initial password of the "admin" user (changed on first login)
kubectl -n bayplan get secret bayplan-bootstrap -o go-template='{{.data.bootstrapPassword|base64decode}}{{"\n"}}'
```

Or from the classic repository:

```bash
helm repo add bayplan https://bayplan.github.io/bayplan && helm repo update
helm upgrade --install bayplan bayplan/bayplan -n bayplan --create-namespace
```

Without an ingress: `kubectl -n bayplan port-forward svc/bayplan 8080:80` and open
http://localhost:8080.

## Important

- **Keep the `bayplan-encryption` Secret apart from the database backup**: without the key the
  stored cluster credentials cannot be decrypted.
- With tools that render the chart with `helm template` (`lookup` returns nothing there), create
  the Secrets outside the chart and pass `existingEncryptionSecret` and `existingDatabaseSecret`.
- Images are signed with cosign (keyless) and ship with SBOM and provenance attestations.

Every value is documented in `values.yaml`. The full guide (certificates, agents, air-gapped
installs, backups, upgrades) is in the
[deployment guide](https://github.com/bayplan/bayplan/blob/main/deploy/README.md).

Apache-2.0 licensed.
