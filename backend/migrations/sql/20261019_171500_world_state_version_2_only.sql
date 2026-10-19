-- +goose Up
-- +goose StatementBegin

UPDATE flows SET world_state_schema_version = 2 WHERE world_state_schema_version <> 2;
ALTER TABLE flows DROP CONSTRAINT flows_world_state_schema_version;
ALTER TABLE flows ADD CONSTRAINT flows_world_state_schema_version CHECK (world_state_schema_version = 2);
ALTER TABLE flows ALTER COLUMN world_state_schema_version SET DEFAULT 2;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE flows DROP CONSTRAINT flows_world_state_schema_version;
ALTER TABLE flows ADD CONSTRAINT flows_world_state_schema_version CHECK (world_state_schema_version IN (1, 2));

-- +goose StatementEnd
