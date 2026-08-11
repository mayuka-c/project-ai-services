-- +goose Up
-- +goose StatementBegin
CREATE TYPE bundle_status AS ENUM (
    'processing',
    'active',
    'failed'
);

CREATE TABLE catalog_bundles (
    id               VARCHAR(30)    PRIMARY KEY,
    -- Human-readable versioned name: <catalog_id>-<version>, e.g. "my-service-1.0.0"
    -- Used as the directory name on the bundle volume.
    name             VARCHAR(200)   NOT NULL,

    status           bundle_status  NOT NULL DEFAULT 'processing',
    -- Uncompressed on-disk size in bytes, populated after extraction completes.
    -- NULL on the immediate 202 response; set once the bundle reaches 'active' or 'failed'.
    size_bytes       BIGINT,

    -- The catalog item type declared by the uploader: "service", "component", …
    catalog_type     VARCHAR(50)    NOT NULL,
    -- The id of the catalog item: e.g. "my-service", "my-llm-provider"
    catalog_id       VARCHAR(100)   NOT NULL,
    -- Semantic version of this bundle: e.g. "1.0.0", "2.1.0"
    version          VARCHAR(50)    NOT NULL DEFAULT '',

    error            TEXT,
    uploaded_by      VARCHAR(100),
    uploaded_at      TIMESTAMPTZ    NOT NULL DEFAULT NOW()
);

-- Enforce one active bundle per catalog_id at the DB level.
-- 'processing' and 'failed' rows are exempt so that a replacement upload
-- in flight does not block itself.
CREATE UNIQUE INDEX uq_catalog_bundles_active_catalog_id
    ON catalog_bundles (catalog_id)
    WHERE status = 'active';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX  IF EXISTS uq_catalog_bundles_active_catalog_id;
DROP TABLE  IF EXISTS catalog_bundles;
DROP TYPE   IF EXISTS bundle_status;
-- +goose StatementEnd
