-- +goose Up
-- +goose StatementBegin

-- Add model-management columns to the components table.
-- name:            human-readable label supplied by the user at deploy time (NULL for pipeline-created components)
-- created_by:      authenticated user who triggered POST /api/v1/models (NULL for pipeline-created components)
-- worker_selector: target Worker LPAR ID (NULL = control-plane Podman deploy)
ALTER TABLE components
    ADD COLUMN name            VARCHAR(100),
    ADD COLUMN created_by      VARCHAR(100),
    ADD COLUMN worker_selector VARCHAR(100);

-- Extend component_status with the async-deployment lifecycle value.
ALTER TYPE component_status ADD VALUE IF NOT EXISTS 'Deploying';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- NOTE: PostgreSQL does not support removing enum values.
-- The 'Deploying' value cannot be rolled back automatically.

ALTER TABLE components
    DROP COLUMN IF EXISTS worker_selector,
    DROP COLUMN IF EXISTS created_by,
    DROP COLUMN IF EXISTS name;

-- +goose StatementEnd

-- Made with Bob
