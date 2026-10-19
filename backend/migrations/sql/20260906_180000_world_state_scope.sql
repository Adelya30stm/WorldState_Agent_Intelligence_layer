-- +goose Up
-- +goose StatementBegin

CREATE TABLE world_state_scope (
  flow_id    BIGINT        PRIMARY KEY REFERENCES flows(id) ON DELETE CASCADE,
  domains    JSONB         NOT NULL DEFAULT '[]'::jsonb,
  cidrs      JSONB         NOT NULL DEFAULT '[]'::jsonb,
  updated_at TIMESTAMPTZ   NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE world_state_candidates (
  id          BIGINT        PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
  flow_id     BIGINT        NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
  entity_key  TEXT          NOT NULL,
  type        TEXT          NOT NULL,
  properties  JSONB         NOT NULL DEFAULT '{}'::jsonb,
  reason      TEXT          NOT NULL DEFAULT '',
  status      TEXT          NOT NULL DEFAULT 'pending',
  created_at  TIMESTAMPTZ   NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT world_state_candidates_flow_key_unique UNIQUE (flow_id, entity_key),
  CONSTRAINT world_state_candidates_status_check CHECK (status IN ('pending', 'accepted', 'rejected'))
);

CREATE INDEX world_state_candidates_flow_status_idx ON world_state_candidates(flow_id, status);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS world_state_candidates;
DROP TABLE IF EXISTS world_state_scope;

-- +goose StatementEnd
