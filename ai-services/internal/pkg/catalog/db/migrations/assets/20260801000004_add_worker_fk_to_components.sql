-- +goose Up
-- +goose StatementBegin

-- Add worker_id FK to components, referencing the workers table created in
-- migration 20260801000002.
-- NULL = component is deployed on the control-plane (local Podman deploy).
-- ON DELETE SET NULL: deregistering a worker orphans its components rather
-- than cascading deletes.
ALTER TABLE components
    ADD COLUMN worker_id UUID REFERENCES workers(id) ON DELETE SET NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE components
    DROP COLUMN IF EXISTS worker_id;

-- +goose StatementEnd
