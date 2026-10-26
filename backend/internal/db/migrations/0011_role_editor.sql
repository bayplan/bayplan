-- +goose NO TRANSACTION
-- +goose Up

-- Intermediate role between readonly and admin. Until now whoever could
-- restart a deployment could also open a terminal, delete resources and read
-- secrets: too much for the "operator" profile that only deploys and scales.
-- The privilege order is resolved by the code (rbac.rank), not by the position
-- in the enum.
--
-- ALTER TYPE ... ADD VALUE cannot run inside a transaction (hence the NO
-- TRANSACTION above) and has no way back: Postgres does not allow removing a
-- value from an enum. The Down is left empty on purpose.
ALTER TYPE app_role ADD VALUE IF NOT EXISTS 'editor';

-- +goose Down
-- A value cannot be removed from an enum in Postgres; rows with 'editor' would
-- have to be reassigned by hand before going back to an earlier version.
