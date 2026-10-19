-- +goose Up
-- +goose StatementBegin

CREATE TYPE WORLD_STATE_EVENT_KIND AS ENUM (
  'entity_upserted',
  'entity_transitioned',
  'link_upserted'
);

CREATE TYPE AGENT_TASK_TARGET_TYPE AS ENUM ('task', 'subtask', 'specialist', 'assistant');
CREATE TYPE AGENT_TASK_UPDATE_STATE AS ENUM ('pending', 'delivered', 'rejected');
CREATE TYPE AGENT_CHAIN_WAIT_KIND AS ENUM ('tool', 'idle');
CREATE TYPE AGENT_CHAIN_WAIT_STATE AS ENUM ('pending', 'resolved');

CREATE SEQUENCE world_state_events_revision_seq AS BIGINT;
CREATE SEQUENCE agent_task_updates_id_seq AS BIGINT;

CREATE FUNCTION next_world_state_event_revision()
RETURNS BIGINT AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(74192001);
  RETURN nextval('world_state_events_revision_seq');
END;
$$ LANGUAGE plpgsql VOLATILE;

CREATE FUNCTION next_agent_task_update_id()
RETURNS BIGINT AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(74192002);
  RETURN nextval('agent_task_updates_id_seq');
END;
$$ LANGUAGE plpgsql VOLATILE;

CREATE TABLE world_state_events (
  revision          BIGINT                  PRIMARY KEY DEFAULT next_world_state_event_revision(),
  flow_id           BIGINT                  NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
  kind              WORLD_STATE_EVENT_KIND  NOT NULL,
  facts             JSONB                   NOT NULL,
  actor             TEXT                    NOT NULL,
  actor_msgchain_id BIGINT                  NULL REFERENCES msgchains(id) ON DELETE SET NULL,
  provenance        JSONB                   NOT NULL DEFAULT '{}'::jsonb,
  created_at        TIMESTAMPTZ              NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT world_state_events_facts_object CHECK (jsonb_typeof(facts) = 'object'),
  CONSTRAINT world_state_events_actor_not_empty CHECK (btrim(actor) <> ''),
  CONSTRAINT world_state_events_provenance_object CHECK (jsonb_typeof(provenance) = 'object')
);

CREATE INDEX world_state_events_flow_revision_idx ON world_state_events(flow_id, revision);
CREATE INDEX world_state_events_actor_msgchain_idx ON world_state_events(actor_msgchain_id)
  WHERE actor_msgchain_id IS NOT NULL;

CREATE TABLE world_state_chain_cursors (
  msgchain_id BIGINT       PRIMARY KEY REFERENCES msgchains(id) ON DELETE CASCADE,
  revision    BIGINT       NOT NULL DEFAULT 0,
  updated_at  TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT world_state_chain_cursors_revision_nonnegative CHECK (revision >= 0)
);

CREATE TABLE agent_task_updates (
  id                         BIGINT                    PRIMARY KEY DEFAULT next_agent_task_update_id(),
  flow_id                    BIGINT                    NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
  sender_msgchain_id         BIGINT                    NULL REFERENCES msgchains(id) ON DELETE SET NULL,
  target_type                AGENT_TASK_TARGET_TYPE    NOT NULL,
  target_task_id             BIGINT                    NULL,
  target_subtask_id          BIGINT                    NULL,
  target_assistant_id        BIGINT                    NULL,
  target_role                TEXT                      NULL,
  target_msgchain_id         BIGINT                    NULL REFERENCES msgchains(id) ON DELETE SET NULL,
  instruction                TEXT                      NOT NULL,
  selected_facts             JSONB                     NOT NULL DEFAULT '[]'::jsonb,
  source_revisions           BIGINT[]                  NOT NULL DEFAULT '{}'::bigint[],
  state                      AGENT_TASK_UPDATE_STATE   NOT NULL DEFAULT 'pending',
  recipient_msgchain_id      BIGINT                    NULL REFERENCES msgchains(id) ON DELETE SET NULL,
  delivered_at               TIMESTAMPTZ               NULL,
  rejected_at                TIMESTAMPTZ               NULL,
  rejection_reason           TEXT                      NULL,
  created_at                 TIMESTAMPTZ               NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at                 TIMESTAMPTZ               NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT agent_task_updates_instruction_not_empty CHECK (btrim(instruction) <> ''),
  CONSTRAINT agent_task_updates_facts_array CHECK (jsonb_typeof(selected_facts) = 'array'),
  CONSTRAINT agent_task_updates_revisions_not_null CHECK (array_position(source_revisions, NULL) IS NULL),
  CONSTRAINT agent_task_updates_target_shape CHECK (
    (target_type = 'task' AND target_task_id IS NOT NULL AND target_subtask_id IS NULL
      AND target_assistant_id IS NULL AND target_role IS NULL AND target_msgchain_id IS NULL)
    OR
    (target_type = 'subtask' AND target_task_id IS NOT NULL AND target_subtask_id IS NOT NULL
      AND target_assistant_id IS NULL AND target_role IS NULL AND target_msgchain_id IS NULL)
    OR
    (target_type = 'assistant' AND target_task_id IS NULL AND target_subtask_id IS NULL
      AND target_assistant_id IS NOT NULL AND target_role IS NULL AND target_msgchain_id IS NULL)
    OR
    (target_type = 'specialist' AND target_task_id IS NOT NULL AND target_assistant_id IS NULL
      AND target_role IS NOT NULL AND btrim(target_role) <> '')
  ),
  CONSTRAINT agent_task_updates_state_shape CHECK (
    (state = 'pending' AND delivered_at IS NULL AND rejected_at IS NULL AND rejection_reason IS NULL)
    OR
    (state = 'delivered' AND recipient_msgchain_id IS NOT NULL AND delivered_at IS NOT NULL
      AND rejected_at IS NULL AND rejection_reason IS NULL)
    OR
    (state = 'rejected' AND delivered_at IS NULL AND rejected_at IS NOT NULL
      AND rejection_reason IS NOT NULL AND btrim(rejection_reason) <> '')
  )
);

CREATE INDEX agent_task_updates_flow_state_id_idx ON agent_task_updates(flow_id, state, id);
CREATE INDEX agent_task_updates_recipient_pending_idx ON agent_task_updates(recipient_msgchain_id, id)
  WHERE state = 'pending' AND recipient_msgchain_id IS NOT NULL;
CREATE INDEX agent_task_updates_target_pending_idx ON agent_task_updates(
  flow_id, target_type, target_task_id, target_subtask_id, target_assistant_id, id
) WHERE state = 'pending';
CREATE INDEX agent_task_updates_sender_idx ON agent_task_updates(sender_msgchain_id)
  WHERE sender_msgchain_id IS NOT NULL;

CREATE TABLE agent_chain_waits (
  msgchain_id          BIGINT                   PRIMARY KEY REFERENCES msgchains(id) ON DELETE CASCADE,
  flow_id              BIGINT                   NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
  wait_kind            AGENT_CHAIN_WAIT_KIND    NOT NULL,
  pending_tool_call_id TEXT                     NULL,
  state                AGENT_CHAIN_WAIT_STATE   NOT NULL DEFAULT 'pending',
  resolution_winner    TEXT                     NULL,
  resolution_ref       BIGINT                   NULL,
  resolved_at          TIMESTAMPTZ              NULL,
  lease_owner          TEXT                     NULL,
  lease_expires_at     TIMESTAMPTZ              NULL,
  retry_count          INTEGER                  NOT NULL DEFAULT 0,
  next_attempt_at      TIMESTAMPTZ              NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resume_pending       BOOLEAN                  NOT NULL DEFAULT FALSE,
  resume_intent        JSONB                    NOT NULL DEFAULT '{}'::jsonb,
  created_at           TIMESTAMPTZ              NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at           TIMESTAMPTZ              NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT agent_chain_waits_tool_shape CHECK (
    (wait_kind = 'tool' AND pending_tool_call_id IS NOT NULL AND btrim(pending_tool_call_id) <> '')
    OR (wait_kind = 'idle' AND pending_tool_call_id IS NULL)
  ),
  CONSTRAINT agent_chain_waits_winner_value CHECK (
    resolution_winner IS NULL
    OR resolution_winner IN ('user', 'world_state', 'task_update', 'cancellation')
    OR resolution_winner LIKE 'internal:%'
  ),
  CONSTRAINT agent_chain_waits_resolution_shape CHECK (
    (state = 'pending' AND resolution_winner IS NULL AND resolution_ref IS NULL
      AND resolved_at IS NULL AND resume_pending = FALSE)
    OR
    (state = 'resolved' AND resolution_winner IS NOT NULL AND resolved_at IS NOT NULL
      AND (lease_owner IS NULL OR (resume_pending = TRUE AND resolution_winner = 'world_state')))
  ),
  CONSTRAINT agent_chain_waits_reference_shape CHECK (
    resolution_winner IS NULL
    OR (resolution_winner IN ('world_state', 'task_update') AND resolution_ref > 0)
    OR (resolution_winner IN ('user', 'cancellation') AND resolution_ref IS NULL)
    OR resolution_winner LIKE 'internal:%'
  ),
  CONSTRAINT agent_chain_waits_lease_shape CHECK (
    (lease_owner IS NULL AND lease_expires_at IS NULL)
    OR (lease_owner IS NOT NULL AND btrim(lease_owner) <> '' AND lease_expires_at IS NOT NULL)
  ),
  CONSTRAINT agent_chain_waits_retry_nonnegative CHECK (retry_count >= 0),
  CONSTRAINT agent_chain_waits_resume_object CHECK (jsonb_typeof(resume_intent) = 'object')
);

CREATE INDEX agent_chain_waits_flow_idx ON agent_chain_waits(flow_id);
CREATE INDEX agent_chain_waits_lease_idx ON agent_chain_waits(next_attempt_at, lease_expires_at, msgchain_id)
  WHERE state = 'pending' AND resolution_winner IS NULL;
CREATE INDEX agent_chain_waits_resume_lease_idx ON agent_chain_waits(next_attempt_at, lease_expires_at, msgchain_id)
  WHERE state = 'resolved' AND resolution_winner = 'world_state' AND resume_pending = TRUE;

CREATE FUNCTION enforce_world_state_cursor_monotonicity()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.revision < OLD.revision THEN
    RAISE EXCEPTION 'world state cursor cannot move backward';
  END IF;
  NEW.updated_at = CURRENT_TIMESTAMP;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER world_state_chain_cursors_monotonic
  BEFORE UPDATE ON world_state_chain_cursors
  FOR EACH ROW EXECUTE FUNCTION enforce_world_state_cursor_monotonicity();

CREATE FUNCTION enforce_agent_task_update_lifecycle()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.flow_id IS DISTINCT FROM OLD.flow_id
    OR NEW.target_type IS DISTINCT FROM OLD.target_type
    OR NEW.target_task_id IS DISTINCT FROM OLD.target_task_id
    OR NEW.target_subtask_id IS DISTINCT FROM OLD.target_subtask_id
    OR NEW.target_assistant_id IS DISTINCT FROM OLD.target_assistant_id
    OR NEW.target_role IS DISTINCT FROM OLD.target_role
    OR NEW.instruction IS DISTINCT FROM OLD.instruction
    OR NEW.selected_facts IS DISTINCT FROM OLD.selected_facts
    OR NEW.source_revisions IS DISTINCT FROM OLD.source_revisions THEN
    RAISE EXCEPTION 'task update immutable fields cannot change';
  END IF;
  IF OLD.state <> 'pending' OR NEW.state NOT IN ('pending', 'delivered', 'rejected') THEN
    IF NEW.state IS DISTINCT FROM OLD.state THEN
      RAISE EXCEPTION 'task update terminal state cannot change';
    END IF;
  END IF;
  IF NEW.sender_msgchain_id IS NOT NULL
    AND NEW.sender_msgchain_id IS DISTINCT FROM OLD.sender_msgchain_id THEN
    RAISE EXCEPTION 'task update sender cannot be rebound';
  END IF;
  IF NEW.target_msgchain_id IS NOT NULL
    AND NEW.target_msgchain_id IS DISTINCT FROM OLD.target_msgchain_id THEN
    RAISE EXCEPTION 'task update target selector cannot be rebound';
  END IF;
  IF OLD.recipient_msgchain_id IS NOT NULL AND NEW.recipient_msgchain_id IS NOT NULL
    AND NEW.recipient_msgchain_id <> OLD.recipient_msgchain_id THEN
    RAISE EXCEPTION 'task update recipient cannot be rebound';
  END IF;
  IF OLD.state <> 'pending' AND (
    NEW.delivered_at IS DISTINCT FROM OLD.delivered_at
    OR NEW.rejected_at IS DISTINCT FROM OLD.rejected_at
    OR NEW.rejection_reason IS DISTINCT FROM OLD.rejection_reason
  ) THEN
    RAISE EXCEPTION 'task update terminal receipt cannot change';
  END IF;
  NEW.updated_at = CURRENT_TIMESTAMP;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER agent_task_updates_lifecycle
  BEFORE UPDATE ON agent_task_updates
  FOR EACH ROW EXECUTE FUNCTION enforce_agent_task_update_lifecycle();

CREATE FUNCTION enforce_agent_chain_wait_winner()
RETURNS TRIGGER AS $$
BEGIN
  IF OLD.resolution_winner IS NOT NULL AND (
    NEW.resolution_winner IS DISTINCT FROM OLD.resolution_winner
    OR NEW.resolution_ref IS DISTINCT FROM OLD.resolution_ref
    OR NEW.resolved_at IS DISTINCT FROM OLD.resolved_at
  ) THEN
    RAISE EXCEPTION 'agent chain wait winner cannot change';
  END IF;
  NEW.updated_at = CURRENT_TIMESTAMP;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER agent_chain_waits_winner
  BEFORE UPDATE ON agent_chain_waits
  FOR EACH ROW EXECUTE FUNCTION enforce_agent_chain_wait_winner();

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS agent_chain_waits;
DROP TABLE IF EXISTS agent_task_updates;
DROP TABLE IF EXISTS world_state_chain_cursors;
DROP TABLE IF EXISTS world_state_events;

DROP FUNCTION IF EXISTS enforce_agent_chain_wait_winner();
DROP FUNCTION IF EXISTS enforce_agent_task_update_lifecycle();
DROP FUNCTION IF EXISTS enforce_world_state_cursor_monotonicity();
DROP FUNCTION IF EXISTS next_agent_task_update_id();
DROP FUNCTION IF EXISTS next_world_state_event_revision();

DROP SEQUENCE IF EXISTS agent_task_updates_id_seq;
DROP SEQUENCE IF EXISTS world_state_events_revision_seq;

DROP TYPE IF EXISTS AGENT_CHAIN_WAIT_STATE;
DROP TYPE IF EXISTS AGENT_CHAIN_WAIT_KIND;
DROP TYPE IF EXISTS AGENT_TASK_UPDATE_STATE;
DROP TYPE IF EXISTS AGENT_TASK_TARGET_TYPE;
DROP TYPE IF EXISTS WORLD_STATE_EVENT_KIND;

-- +goose StatementEnd