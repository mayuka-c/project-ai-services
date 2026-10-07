-- +goose Up
-- +goose StatementBegin

-- keys stores the per-model LiteLLM virtual key for every deployed local model.
-- Consumer service pods retrieve the key at startup via GET /api/v1/keys/:component_id.
-- The virtual_key column holds the raw sk-... bearer token and is never logged or
-- included in list responses.
CREATE TABLE keys (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    component_id UUID         NOT NULL REFERENCES components (id) ON DELETE CASCADE,
    virtual_key  TEXT         NOT NULL,   -- 'sk-...' value — treated as a secret; never logged
    route_id     VARCHAR(255) NOT NULL,   -- LiteLLM route ID, e.g. 'granite-3.3-8b-instruct-vllm-spyre'
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_keys_component_id ON keys (component_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_keys_component_id;
DROP TABLE IF EXISTS keys;

-- +goose StatementEnd

-- Made with Bob
