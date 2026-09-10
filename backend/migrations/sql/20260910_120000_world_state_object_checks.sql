-- +goose Up
-- +goose StatementBegin

-- Existing flows retain their original storage/delivery semantics. The default
-- changes only after the existing rows have received version 1.
ALTER TABLE flows ADD COLUMN world_state_schema_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE flows ALTER COLUMN world_state_schema_version SET DEFAULT 2;
ALTER TABLE flows ADD CONSTRAINT flows_world_state_schema_version CHECK (world_state_schema_version IN (1, 2));

ALTER TYPE WORLD_STATE_EVENT_KIND ADD VALUE IF NOT EXISTS 'object_upserted';
ALTER TYPE WORLD_STATE_EVENT_KIND ADD VALUE IF NOT EXISTS 'check_created';
ALTER TYPE WORLD_STATE_EVENT_KIND ADD VALUE IF NOT EXISTS 'check_run_started';
ALTER TYPE WORLD_STATE_EVENT_KIND ADD VALUE IF NOT EXISTS 'check_run_updated';

CREATE TABLE world_state_objects (
    id BIGSERIAL PRIMARY KEY,
    flow_id BIGINT NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
    object_key TEXT NOT NULL CHECK (btrim(object_key) <> ''),
    type TEXT NOT NULL CHECK (type ~ '^[a-z][a-z0-9_]{0,63}$'),
    locator JSONB NOT NULL CHECK (jsonb_typeof(locator) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (flow_id, object_key)
);

CREATE TABLE world_state_checks (
    id BIGSERIAL PRIMARY KEY,
    object_id BIGINT NOT NULL REFERENCES world_state_objects(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind ~ '^[a-z][a-z0-9_]{0,63}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (object_id, kind)
);

CREATE TABLE world_state_check_runs (
    id BIGSERIAL PRIMARY KEY,
    check_id BIGINT NOT NULL REFERENCES world_state_checks(id) ON DELETE CASCADE,
    tool_call_id BIGINT NULL REFERENCES toolcalls(id) ON DELETE SET NULL,
    -- Same request means retrying storage; a new request means a new experiment.
    -- It is intentionally independent of tool_call_id: one tool can run many cases.
    request_key TEXT NULL CHECK (request_key IS NULL OR btrim(request_key) <> ''),
    parameters JSONB NOT NULL CHECK (jsonb_typeof(parameters) = 'object'),
    execution_status TEXT NOT NULL CHECK (execution_status IN ('planned','running','completed','failed','cancelled')),
    verdict TEXT NULL CHECK (verdict IN ('vulnerable','not_reproduced','inconclusive')),
    evidence JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(evidence) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ NULL,
    finished_at TIMESTAMPTZ NULL,
    CONSTRAINT world_state_check_runs_completion CHECK (
        (execution_status IN ('planned','running') AND verdict IS NULL AND finished_at IS NULL)
        OR (execution_status = 'completed' AND verdict IS NOT NULL AND finished_at IS NOT NULL)
        OR (execution_status IN ('failed','cancelled') AND verdict IS NULL AND finished_at IS NOT NULL)
    ),
    CONSTRAINT world_state_check_runs_start CHECK (execution_status <> 'running' OR started_at IS NOT NULL),
    -- The store prefixes keys with flow_id. This also prevents retrying the same
    -- logical request against another check from creating a second experiment.
    UNIQUE (request_key)
);
CREATE INDEX world_state_check_runs_check_id_idx ON world_state_check_runs(check_id, id);
CREATE INDEX world_state_check_runs_tool_call_id_idx ON world_state_check_runs(tool_call_id) WHERE tool_call_id IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE world_state_check_runs;
DROP TABLE world_state_checks;
DROP TABLE world_state_objects;
ALTER TABLE flows DROP COLUMN world_state_schema_version;
-- PostgreSQL enum values cannot be removed without recreating their type.
-- Keep them so retained journal rows remain readable after a rollback.
-- +goose StatementEnd
