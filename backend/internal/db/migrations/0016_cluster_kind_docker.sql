-- +goose Up
-- +goose StatementBegin

-- Docker/Podman environments in addition to Kubernetes clusters. The clusters
-- table is reused (and with it all the 'cluster'-scoped RBAC, the assignments
-- and the audit trail) instead of creating another one: for the permission
-- model a Docker host is "an environment" just like a cluster. A Docker
-- environment always goes through an agent: the agent runs on the host with
-- the Docker socket mounted and exposes it to the server through the tunnel
-- (clusters_docker_uses_agent constraint).
ALTER TABLE clusters ADD COLUMN kind TEXT NOT NULL DEFAULT 'kubernetes' CHECK (kind IN ('kubernetes', 'docker'));
ALTER TABLE clusters ADD CONSTRAINT clusters_docker_uses_agent CHECK (kind <> 'docker' OR connector = 'agent');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM clusters WHERE kind = 'docker';
ALTER TABLE clusters DROP CONSTRAINT IF EXISTS clusters_docker_uses_agent;
ALTER TABLE clusters DROP COLUMN kind;
-- +goose StatementEnd
