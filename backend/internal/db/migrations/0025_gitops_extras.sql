-- +goose Up
-- +goose StatementBegin

-- GitOps: three things missing compared to full GitOps tools that do fit in a
-- "lightweight" GitOps:
--  - auth_type: 'ssh' uses token_encrypted for the PRIVATE KEY (PEM) instead
--    of an HTTPS token; ssh_known_hosts stores the git server's known_hosts
--    line to verify its identity (empty = unverified, and the UI flags it).
--  - prune: delete from the cluster what disappears from the repo. Applied
--    objects carry the label bayplan.io/gitops-source=<id>; after a successful
--    apply, those with the label that are not in the repo are listed.
--  - kustomize: build with Kustomize (kustomization.yaml in path) instead of
--    applying the loose YAML files.
ALTER TABLE gitops_sources ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'https' CHECK (auth_type IN ('https', 'ssh'));
ALTER TABLE gitops_sources ADD COLUMN ssh_known_hosts TEXT;
ALTER TABLE gitops_sources ADD COLUMN prune BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE gitops_sources ADD COLUMN kustomize BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE gitops_sources ADD COLUMN last_pruned JSONB;

DROP TRIGGER IF EXISTS trg_audit_db_gitops_sources_update ON gitops_sources;
CREATE TRIGGER trg_audit_db_gitops_sources_update
    AFTER UPDATE OF name, repo_url, branch, path, namespace, username, token_encrypted, interval_seconds, enabled, webhook_token_hash,
        auth_type, ssh_known_hosts, prune, kustomize
    ON gitops_sources
    FOR EACH ROW EXECUTE FUNCTION audit_db_change();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE gitops_sources DROP COLUMN last_pruned;
ALTER TABLE gitops_sources DROP COLUMN kustomize;
ALTER TABLE gitops_sources DROP COLUMN prune;
ALTER TABLE gitops_sources DROP COLUMN ssh_known_hosts;
ALTER TABLE gitops_sources DROP COLUMN auth_type;
-- +goose StatementEnd
