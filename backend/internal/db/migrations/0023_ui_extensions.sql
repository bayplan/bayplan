-- +goose Up
-- +goose StatementBegin

-- UI extensions: links to external tools (Grafana, Kibana, Harbor…)
-- shown in the sidebar of one cluster, or of all of them (cluster_id NULL).
-- `mode` decides whether it opens in another tab or embedded in an iframe
-- inside the panel; `min_role` is the minimum role on the cluster to see it (a
-- link to Grafana is seen by anyone, one to Harbor admin only by admins). It
-- is not a plugin system: the external tool knows nothing about the panel, and
-- the panel only composes the URL (placeholders {cluster}, {clusterId},
-- {namespace}).
CREATE TABLE ui_extensions (
    id         BIGSERIAL PRIMARY KEY,
    cluster_id BIGINT REFERENCES clusters(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    url        TEXT NOT NULL,
    mode       TEXT NOT NULL DEFAULT 'link' CHECK (mode IN ('link', 'embed')),
    min_role   app_role NOT NULL DEFAULT 'readonly',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ui_extensions_cluster_idx ON ui_extensions (cluster_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ui_extensions;
-- +goose StatementEnd
