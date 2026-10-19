-- +goose Up
-- +goose StatementBegin

CREATE TYPE ATTACK_PLAN_STATUS AS ENUM (
  'draft',
  'active',
  'completed',
  'failed',
  'cancelled',
  'superseded'
);

CREATE TYPE ATTACK_PLAN_NODE_KIND AS ENUM ('goal', 'action');
CREATE TYPE ATTACK_PLAN_NODE_STATUS AS ENUM (
  'pending',
  'ready',
  'running',
  'succeeded',
  'failed',
  'blocked',
  'skipped',
  'cancelled'
);
CREATE TYPE ATTACK_PLAN_EDGE_KIND AS ENUM ('and', 'or', 'dependency');
CREATE TYPE ATTACK_PLAN_RUN_STATUS AS ENUM ('running', 'succeeded', 'failed', 'cancelled');

CREATE TABLE attack_plans (
  id            BIGINT                PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
  flow_id       BIGINT                NOT NULL REFERENCES flows(id) ON DELETE CASCADE,
  objective_key TEXT                  NOT NULL,
  objective     TEXT                  NOT NULL,
  status        ATTACK_PLAN_STATUS    NOT NULL DEFAULT 'draft',
  version       BIGINT                NOT NULL DEFAULT 1,
  planner       TEXT                  NOT NULL DEFAULT 'unknown',
  created_at    TIMESTAMPTZ           NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMPTZ           NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT attack_plans_id_flow_unique UNIQUE (id, flow_id),
  CONSTRAINT attack_plans_objective_key_not_empty CHECK (btrim(objective_key) <> ''),
  CONSTRAINT attack_plans_objective_not_empty CHECK (btrim(objective) <> ''),
  CONSTRAINT attack_plans_version_positive CHECK (version > 0),
  CONSTRAINT attack_plans_planner_not_empty CHECK (btrim(planner) <> '')
);

CREATE UNIQUE INDEX attack_plans_active_objective_unique
  ON attack_plans(flow_id, objective_key)
  WHERE status = 'active';
CREATE INDEX attack_plans_flow_status_idx ON attack_plans(flow_id, status);

CREATE TABLE attack_plan_nodes (
  id            BIGINT                    PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
  plan_id       BIGINT                    NOT NULL,
  flow_id       BIGINT                    NOT NULL,
  node_key      TEXT                      NOT NULL,
  kind          ATTACK_PLAN_NODE_KIND    NOT NULL,
  status        ATTACK_PLAN_NODE_STATUS  NOT NULL DEFAULT 'pending',
  title         TEXT                      NOT NULL,
  description   TEXT                      NOT NULL DEFAULT '',
  payload       JSONB                     NOT NULL DEFAULT '{}'::jsonb,
  version       BIGINT                    NOT NULL DEFAULT 1,
  created_at    TIMESTAMPTZ               NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMPTZ               NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT attack_plan_nodes_plan_fk FOREIGN KEY (plan_id, flow_id)
    REFERENCES attack_plans(id, flow_id) ON DELETE CASCADE,
  CONSTRAINT attack_plan_nodes_plan_id_flow_id_id_unique UNIQUE (plan_id, flow_id, id),
  CONSTRAINT attack_plan_nodes_key_unique UNIQUE (plan_id, node_key),
  CONSTRAINT attack_plan_nodes_key_not_empty CHECK (btrim(node_key) <> ''),
  CONSTRAINT attack_plan_nodes_title_not_empty CHECK (btrim(title) <> ''),
  CONSTRAINT attack_plan_nodes_payload_object CHECK (jsonb_typeof(payload) = 'object'),
  CONSTRAINT attack_plan_nodes_version_positive CHECK (version > 0)
);

CREATE INDEX attack_plan_nodes_flow_status_idx ON attack_plan_nodes(flow_id, status);

CREATE TABLE attack_plan_edges (
  id            BIGINT                  PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
  plan_id       BIGINT                  NOT NULL,
  flow_id       BIGINT                  NOT NULL,
  from_node_id  BIGINT                  NOT NULL,
  to_node_id    BIGINT                  NOT NULL,
  kind          ATTACK_PLAN_EDGE_KIND  NOT NULL,
  created_at    TIMESTAMPTZ             NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT attack_plan_edges_plan_fk FOREIGN KEY (plan_id, flow_id)
    REFERENCES attack_plans(id, flow_id) ON DELETE CASCADE,
  CONSTRAINT attack_plan_edges_from_node_fk FOREIGN KEY (plan_id, flow_id, from_node_id)
    REFERENCES attack_plan_nodes(plan_id, flow_id, id) ON DELETE CASCADE,
  CONSTRAINT attack_plan_edges_to_node_fk FOREIGN KEY (plan_id, flow_id, to_node_id)
    REFERENCES attack_plan_nodes(plan_id, flow_id, id) ON DELETE CASCADE,
  CONSTRAINT attack_plan_edges_not_self CHECK (from_node_id <> to_node_id),
  CONSTRAINT attack_plan_edges_unique UNIQUE (plan_id, from_node_id, to_node_id, kind)
);

CREATE INDEX attack_plan_edges_flow_idx ON attack_plan_edges(flow_id, plan_id);

CREATE TABLE attack_plan_runs (
  id                     BIGINT                   PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
  plan_id                BIGINT                   NOT NULL,
  flow_id                BIGINT                   NOT NULL,
  status                 ATTACK_PLAN_RUN_STATUS  NOT NULL DEFAULT 'running',
  requested_version      BIGINT                   NOT NULL,
  resulting_version      BIGINT                   NULL,
  world_state_revision   BIGINT                   NOT NULL DEFAULT 0,
  idempotency_key        TEXT                     NOT NULL,
  planner                TEXT                     NOT NULL DEFAULT 'unknown',
  error                  JSONB                    NOT NULL DEFAULT '{}'::jsonb,
  started_at             TIMESTAMPTZ              NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at            TIMESTAMPTZ              NULL,

  CONSTRAINT attack_plan_runs_plan_fk FOREIGN KEY (plan_id, flow_id)
    REFERENCES attack_plans(id, flow_id) ON DELETE CASCADE,
  CONSTRAINT attack_plan_runs_plan_flow_id_unique UNIQUE (plan_id, flow_id, id),
  CONSTRAINT attack_plan_runs_plan_key_unique UNIQUE (plan_id, idempotency_key),
  CONSTRAINT attack_plan_runs_requested_version_positive CHECK (requested_version > 0),
  CONSTRAINT attack_plan_runs_resulting_version_positive CHECK (resulting_version IS NULL OR resulting_version > 0),
  CONSTRAINT attack_plan_runs_revision_nonnegative CHECK (world_state_revision >= 0),
  CONSTRAINT attack_plan_runs_idempotency_key_not_empty CHECK (btrim(idempotency_key) <> ''),
  CONSTRAINT attack_plan_runs_error_object CHECK (jsonb_typeof(error) = 'object'),
  CONSTRAINT attack_plan_runs_terminal_timestamp CHECK (
    (status = 'running' AND finished_at IS NULL) OR
    (status <> 'running' AND finished_at IS NOT NULL)
  )
);

CREATE INDEX attack_plan_runs_flow_status_idx ON attack_plan_runs(flow_id, status);

CREATE TABLE attack_plan_evidence (
  id                    BIGINT        PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
  plan_id               BIGINT        NOT NULL,
  flow_id               BIGINT        NOT NULL,
  node_id               BIGINT        NULL,
  run_id                BIGINT        NULL,
  revision_from         BIGINT        NOT NULL DEFAULT 0,
  revision_to           BIGINT        NOT NULL DEFAULT 0,
  provenance            JSONB         NOT NULL DEFAULT '{}'::jsonb,
  created_at            TIMESTAMPTZ   NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT attack_plan_evidence_plan_fk FOREIGN KEY (plan_id, flow_id)
    REFERENCES attack_plans(id, flow_id) ON DELETE CASCADE,
  CONSTRAINT attack_plan_evidence_node_fk FOREIGN KEY (plan_id, flow_id, node_id)
    REFERENCES attack_plan_nodes(plan_id, flow_id, id) ON DELETE CASCADE,
  CONSTRAINT attack_plan_evidence_run_fk FOREIGN KEY (plan_id, flow_id, run_id)
    REFERENCES attack_plan_runs(plan_id, flow_id, id) ON DELETE CASCADE,
  CONSTRAINT attack_plan_evidence_revision_nonnegative CHECK (revision_from >= 0 AND revision_to >= 0),
  CONSTRAINT attack_plan_evidence_revision_order CHECK (revision_from <= revision_to),
  CONSTRAINT attack_plan_evidence_provenance_object CHECK (jsonb_typeof(provenance) = 'object'),
  CONSTRAINT attack_plan_evidence_unique UNIQUE (plan_id, node_id, run_id, revision_from, revision_to)
);

CREATE INDEX attack_plan_evidence_flow_revision_idx
  ON attack_plan_evidence(flow_id, revision_from, revision_to);
CREATE UNIQUE INDEX attack_plan_evidence_run_unique
  ON attack_plan_evidence(plan_id, run_id)
  WHERE node_id IS NULL AND run_id IS NOT NULL;

CREATE TABLE attack_plan_bindings (
  id            BIGINT        PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
  plan_id       BIGINT        NOT NULL,
  flow_id       BIGINT        NOT NULL,
  node_id       BIGINT        NULL,
  task_id       BIGINT        NULL REFERENCES tasks(id) ON DELETE CASCADE,
  subtask_id    BIGINT        NULL REFERENCES subtasks(id) ON DELETE CASCADE,
  created_at    TIMESTAMPTZ   NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT attack_plan_bindings_plan_fk FOREIGN KEY (plan_id, flow_id)
    REFERENCES attack_plans(id, flow_id) ON DELETE CASCADE,
  CONSTRAINT attack_plan_bindings_node_fk FOREIGN KEY (plan_id, flow_id, node_id)
    REFERENCES attack_plan_nodes(plan_id, flow_id, id) ON DELETE CASCADE,
  CONSTRAINT attack_plan_bindings_target_shape CHECK (
    task_id IS NOT NULL OR subtask_id IS NOT NULL
  ),
  CONSTRAINT attack_plan_bindings_unique UNIQUE (plan_id, node_id, task_id, subtask_id)
);

CREATE INDEX attack_plan_bindings_flow_task_idx ON attack_plan_bindings(flow_id, task_id)
  WHERE task_id IS NOT NULL;
CREATE INDEX attack_plan_bindings_flow_subtask_idx ON attack_plan_bindings(flow_id, subtask_id)
  WHERE subtask_id IS NOT NULL;
CREATE UNIQUE INDEX attack_plan_bindings_logical_unique
  ON attack_plan_bindings(
    plan_id,
    COALESCE(node_id, 0),
    COALESCE(task_id, 0),
    COALESCE(subtask_id, 0)
  );
CREATE UNIQUE INDEX attack_plan_bindings_node_unique
  ON attack_plan_bindings(plan_id, node_id)
  WHERE node_id IS NOT NULL;
CREATE UNIQUE INDEX attack_plan_bindings_subtask_unique
  ON attack_plan_bindings(plan_id, subtask_id)
  WHERE subtask_id IS NOT NULL;

CREATE FUNCTION validate_attack_plan_binding()
RETURNS TRIGGER AS $$
DECLARE
  node_kind ATTACK_PLAN_NODE_KIND;
  task_flow_id BIGINT;
  subtask_task_id BIGINT;
BEGIN
  IF NEW.task_id IS NULL AND NEW.subtask_id IS NULL THEN
    RAISE EXCEPTION 'attack plan binding must target a task or subtask';
  END IF;

  IF NEW.node_id IS NOT NULL THEN
    SELECT kind INTO node_kind
    FROM attack_plan_nodes
    WHERE id = NEW.node_id AND plan_id = NEW.plan_id AND flow_id = NEW.flow_id
    FOR UPDATE;
    IF node_kind IS DISTINCT FROM 'action'::ATTACK_PLAN_NODE_KIND THEN
      RAISE EXCEPTION 'attack plan binding node % must be an action node', NEW.node_id;
    END IF;
  END IF;

  IF NEW.task_id IS NOT NULL THEN
    SELECT flow_id INTO task_flow_id FROM tasks WHERE id = NEW.task_id;
    IF task_flow_id IS NULL THEN
      RAISE EXCEPTION 'attack plan binding task % does not exist', NEW.task_id;
    END IF;
    IF task_flow_id <> NEW.flow_id THEN
      RAISE EXCEPTION 'attack plan binding task % belongs to flow %, not %', NEW.task_id, task_flow_id, NEW.flow_id;
    END IF;
  END IF;

  IF NEW.subtask_id IS NOT NULL THEN
    SELECT t.id, t.flow_id INTO subtask_task_id, task_flow_id
    FROM subtasks s JOIN tasks t ON t.id = s.task_id
    WHERE s.id = NEW.subtask_id;
    IF subtask_task_id IS NULL THEN
      RAISE EXCEPTION 'attack plan binding subtask % does not exist', NEW.subtask_id;
    END IF;
    IF task_flow_id <> NEW.flow_id THEN
      RAISE EXCEPTION 'attack plan binding subtask % belongs to flow %, not %', NEW.subtask_id, task_flow_id, NEW.flow_id;
    END IF;
    IF NEW.task_id IS NULL OR NEW.task_id <> subtask_task_id THEN
      RAISE EXCEPTION 'attack plan binding subtask % must use its owning task %', NEW.subtask_id, subtask_task_id;
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER attack_plan_binding_validation
  BEFORE INSERT OR UPDATE ON attack_plan_bindings
  FOR EACH ROW EXECUTE FUNCTION validate_attack_plan_binding();

CREATE FUNCTION validate_attack_plan_bound_node()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.kind::text <> 'action' AND EXISTS (
    SELECT 1 FROM attack_plan_bindings
    WHERE plan_id = NEW.plan_id AND flow_id = NEW.flow_id AND node_id = NEW.id
  ) THEN
    RAISE EXCEPTION 'bound attack plan node % must remain an action node', NEW.id;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER attack_plan_bound_node_validation
  BEFORE UPDATE OF kind ON attack_plan_nodes
  FOR EACH ROW EXECUTE FUNCTION validate_attack_plan_bound_node();

CREATE FUNCTION enforce_attack_plan_active_binding()
RETURNS TRIGGER AS $$
DECLARE
  candidate_plan_id BIGINT;
  candidate_flow_id BIGINT;
  candidate_task_id BIGINT;
  candidate_subtask_id BIGINT;
BEGIN
  IF TG_TABLE_NAME = 'attack_plans' THEN
    IF NEW.status::text <> 'active' THEN
      RETURN NEW;
    END IF;
    candidate_plan_id := NEW.id;
    candidate_flow_id := NEW.flow_id;
  ELSE
    candidate_plan_id := NEW.plan_id;
    candidate_flow_id := NEW.flow_id;
    candidate_task_id := NEW.task_id;
    candidate_subtask_id := NEW.subtask_id;
  END IF;

  PERFORM 1 FROM flows WHERE id = candidate_flow_id FOR UPDATE;

  IF TG_TABLE_NAME = 'attack_plans' THEN
    IF EXISTS (
      SELECT 1
      FROM attack_plan_bindings candidate
      JOIN attack_plan_bindings existing
        ON (candidate.task_id IS NOT NULL AND existing.task_id = candidate.task_id)
        OR (candidate.subtask_id IS NOT NULL AND existing.subtask_id = candidate.subtask_id)
      JOIN attack_plans p ON p.id = existing.plan_id AND p.flow_id = existing.flow_id
      WHERE candidate.plan_id = candidate_plan_id
        AND p.flow_id = candidate_flow_id
        AND p.status = 'active'
        AND p.id <> candidate_plan_id
    ) THEN
      RAISE EXCEPTION 'flow % already has an active attack plan for this task or subtask', candidate_flow_id;
    END IF;
    RETURN NEW;
  END IF;

  IF candidate_task_id IS NULL AND candidate_subtask_id IS NULL THEN
    RETURN NEW;
  END IF;

  IF EXISTS (
    SELECT 1
    FROM attack_plan_bindings b
    JOIN attack_plans p ON p.id = b.plan_id AND p.flow_id = b.flow_id
    WHERE p.flow_id = candidate_flow_id
      AND p.status = 'active'
      AND p.id <> candidate_plan_id
      AND (
        (candidate_task_id IS NOT NULL AND b.task_id = candidate_task_id)
        OR
        (candidate_subtask_id IS NOT NULL AND b.subtask_id = candidate_subtask_id)
      )
  ) THEN
    RAISE EXCEPTION 'flow % already has an active attack plan for this task or subtask', candidate_flow_id;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER attack_plan_active_binding_validation
  BEFORE INSERT OR UPDATE ON attack_plan_bindings
  FOR EACH ROW EXECUTE FUNCTION enforce_attack_plan_active_binding();

CREATE TRIGGER attack_plan_active_binding_transition
  BEFORE INSERT OR UPDATE OF status ON attack_plans
  FOR EACH ROW EXECUTE FUNCTION enforce_attack_plan_active_binding();

CREATE FUNCTION validate_attack_plan_evidence()
RETURNS TRIGGER AS $$
DECLARE
  revision_flow_id BIGINT;
BEGIN
  -- revision_from is an exclusive global cursor boundary. It may be a sequence
  -- gap or an event interleaved from another flow; revision_to is the owned head.
  IF NEW.revision_to > 0 THEN
    SELECT flow_id INTO revision_flow_id FROM world_state_events WHERE revision = NEW.revision_to;
    IF revision_flow_id IS NULL OR revision_flow_id <> NEW.flow_id THEN
      RAISE EXCEPTION 'attack plan evidence revision_to % is not in flow %', NEW.revision_to, NEW.flow_id;
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER attack_plan_evidence_validation
  BEFORE INSERT OR UPDATE ON attack_plan_evidence
  FOR EACH ROW EXECUTE FUNCTION validate_attack_plan_evidence();

CREATE FUNCTION enforce_attack_plan_terminal_transition()
RETURNS TRIGGER AS $$
BEGIN
  IF (
       (TG_TABLE_NAME = 'attack_plans' AND OLD.status::text IN ('completed', 'failed', 'cancelled', 'superseded'))
       OR
       (TG_TABLE_NAME = 'attack_plan_nodes' AND OLD.status::text IN ('succeeded', 'failed', 'skipped', 'cancelled'))
       OR
       (TG_TABLE_NAME = 'attack_plan_runs' AND OLD.status::text IN ('succeeded', 'failed', 'cancelled'))
     )
     AND NEW.status::text <> OLD.status::text THEN
    RAISE EXCEPTION '% % is terminal and cannot transition from % to %',
      TG_TABLE_NAME, OLD.id, OLD.status, NEW.status;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER attack_plan_terminal_transition
  BEFORE UPDATE OF status ON attack_plans
  FOR EACH ROW EXECUTE FUNCTION enforce_attack_plan_terminal_transition();

CREATE TRIGGER attack_plan_node_terminal_transition
  BEFORE UPDATE OF status ON attack_plan_nodes
  FOR EACH ROW EXECUTE FUNCTION enforce_attack_plan_terminal_transition();

CREATE TRIGGER attack_plan_run_terminal_transition
  BEFORE UPDATE OF status ON attack_plan_runs
  FOR EACH ROW EXECUTE FUNCTION enforce_attack_plan_terminal_transition();

-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS attack_plan_run_terminal_transition ON attack_plan_runs;
DROP TRIGGER IF EXISTS attack_plan_node_terminal_transition ON attack_plan_nodes;
DROP TRIGGER IF EXISTS attack_plan_terminal_transition ON attack_plans;
DROP TRIGGER IF EXISTS attack_plan_evidence_validation ON attack_plan_evidence;
DROP TRIGGER IF EXISTS attack_plan_binding_validation ON attack_plan_bindings;
DROP TRIGGER IF EXISTS attack_plan_active_binding_validation ON attack_plan_bindings;
DROP TRIGGER IF EXISTS attack_plan_active_binding_transition ON attack_plans;
DROP TRIGGER IF EXISTS attack_plan_bound_node_validation ON attack_plan_nodes;

DROP FUNCTION IF EXISTS enforce_attack_plan_terminal_transition();
DROP FUNCTION IF EXISTS validate_attack_plan_evidence();
DROP FUNCTION IF EXISTS validate_attack_plan_binding();
DROP FUNCTION IF EXISTS validate_attack_plan_bound_node();
DROP FUNCTION IF EXISTS enforce_attack_plan_active_binding();

DROP TABLE IF EXISTS attack_plan_bindings;
DROP TABLE IF EXISTS attack_plan_evidence;
DROP TABLE IF EXISTS attack_plan_runs;
DROP TABLE IF EXISTS attack_plan_edges;
DROP TABLE IF EXISTS attack_plan_nodes;
DROP TABLE IF EXISTS attack_plans;

DROP TYPE IF EXISTS ATTACK_PLAN_RUN_STATUS;
DROP TYPE IF EXISTS ATTACK_PLAN_EDGE_KIND;
DROP TYPE IF EXISTS ATTACK_PLAN_NODE_STATUS;
DROP TYPE IF EXISTS ATTACK_PLAN_NODE_KIND;
DROP TYPE IF EXISTS ATTACK_PLAN_STATUS;

-- +goose StatementEnd
