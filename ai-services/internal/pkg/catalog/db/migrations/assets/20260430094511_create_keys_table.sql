-- +goose Up
-- +goose StatementBegin

-- keys stores the per-model LiteLLM virtual key for every managed model:
--   dependency_type = 'component' → dependency_id = components.id (locally deployed model)
--   dependency_type = 'connector' → dependency_id = connectors.id (remote model connector)
-- Consumer service pods retrieve the key at startup via GET /api/v1/models/keys.
-- The virtual_key column holds the raw sk-... bearer token and is never logged or
-- included in list responses.
-- dependency_id is polymorphic, so there is no FK; rows are removed explicitly when the
-- owning component or connector is deleted.
CREATE TABLE keys (
    id              UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    dependency_id   UUID            NOT NULL,
    dependency_type dependency_type NOT NULL CHECK (dependency_type IN ('component', 'connector')),
    virtual_key     TEXT            NOT NULL,   -- 'sk-...' value — treated as a secret; never logged
    route_id        VARCHAR(255)    NOT NULL,   -- LiteLLM route ID, e.g. 'granite-3.3-8b-instruct--vllm-spyre'
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_keys_dependency ON keys (dependency_type, dependency_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_keys_dependency;
DROP TABLE IF EXISTS keys;

-- +goose StatementEnd

-- Made with Bob
