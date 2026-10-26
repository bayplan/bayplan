-- +goose Up
-- +goose StatementBegin

-- Image vulnerability scans (Trivy run on the server). One row per image
-- reference, GLOBAL and not per cluster: the same image has the same CVEs
-- wherever it is, and this way one cluster benefits from what was scanned for
-- another. The report is stored trimmed (id, package, versions, severity,
-- title, URL) in JSONB; the per-severity counters separately so they can be
-- listed without opening the JSON.
CREATE TABLE image_scans (
    id              BIGSERIAL PRIMARY KEY,
    image           TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'ok', 'error')),
    scanner_version TEXT,
    scanned_at      TIMESTAMPTZ,
    critical        INT NOT NULL DEFAULT 0,
    high            INT NOT NULL DEFAULT 0,
    medium          INT NOT NULL DEFAULT 0,
    low             INT NOT NULL DEFAULT 0,
    unknown         INT NOT NULL DEFAULT 0,
    fixable         INT NOT NULL DEFAULT 0,
    report          JSONB,
    error           TEXT,
    requested_by    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX image_scans_scanned_idx ON image_scans (scanned_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS image_scans;
-- +goose StatementEnd
