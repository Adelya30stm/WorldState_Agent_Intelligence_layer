-- name: CreateAttackPlan :one
INSERT INTO attack_plans (flow_id, objective_key, objective, status, planner)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;


-- name: GetAttackPlan :one
SELECT * FROM attack_plans
WHERE id = $1 AND flow_id = $2;

-- name: LockAttackPlanFlow :one
SELECT id FROM flows
WHERE id = $1
FOR UPDATE;

-- name: LockAttackPlan :one
SELECT * FROM attack_plans
WHERE id = $1 AND flow_id = $2
FOR UPDATE;

-- name: GetActiveAttackPlanByObjective :one
SELECT * FROM attack_plans
WHERE flow_id = $1 AND objective_key = $2 AND status = 'active';

-- name: ListAttackPlans :many
SELECT * FROM attack_plans
WHERE flow_id = $1
ORDER BY created_at ASC, id ASC;

-- name: UpdateAttackPlanVersion :one
UPDATE attack_plans
SET objective = $3, status = $4, planner = $5, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND flow_id = $2 AND version = $6
RETURNING *;

-- name: CreateAttackPlanNode :one
INSERT INTO attack_plan_nodes (plan_id, flow_id, node_key, kind, status, title, description, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetAttackPlanNode :one
SELECT * FROM attack_plan_nodes
WHERE id = $1 AND plan_id = $2 AND flow_id = $3;

-- name: GetAttackPlanNodeByKey :one
SELECT * FROM attack_plan_nodes
WHERE plan_id = $1 AND flow_id = $2 AND node_key = $3;

-- name: ListAttackPlanNodes :many
SELECT * FROM attack_plan_nodes
WHERE plan_id = $1 AND flow_id = $2
ORDER BY id ASC;

-- name: DeleteAttackPlanNodes :exec
DELETE FROM attack_plan_nodes
WHERE plan_id = $1 AND flow_id = $2;

-- name: DeleteAttackPlanNode :exec
DELETE FROM attack_plan_nodes
WHERE id = $1 AND plan_id = $2 AND flow_id = $3;

-- name: UpdateAttackPlanNodeVersion :one
UPDATE attack_plan_nodes
SET status = $4, title = $5, description = $6, payload = $7,
    version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND plan_id = $2 AND flow_id = $3 AND version = $8
RETURNING *;

-- name: CreateAttackPlanEdge :one
INSERT INTO attack_plan_edges (plan_id, flow_id, from_node_id, to_node_id, kind)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListAttackPlanEdges :many
SELECT * FROM attack_plan_edges
WHERE plan_id = $1 AND flow_id = $2
ORDER BY id ASC;

-- name: DeleteAttackPlanEdges :exec
DELETE FROM attack_plan_edges
WHERE plan_id = $1 AND flow_id = $2;

-- name: DeleteAttackPlanEdge :exec
DELETE FROM attack_plan_edges
WHERE id = $1 AND plan_id = $2 AND flow_id = $3;

-- name: CreateAttackPlanRun :one
INSERT INTO attack_plan_runs (
  plan_id, flow_id, requested_version, world_state_revision, idempotency_key, planner
)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (plan_id, idempotency_key) DO UPDATE
SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;

-- name: GetAttackPlanRunByIdempotencyKey :one
SELECT * FROM attack_plan_runs
WHERE plan_id = $1 AND flow_id = $2 AND idempotency_key = $3;

-- name: LockAttackPlanRunByIdempotencyKey :one
SELECT * FROM attack_plan_runs
WHERE plan_id = $1 AND flow_id = $2 AND idempotency_key = $3
FOR UPDATE;

-- name: TransitionAttackPlanRun :one
UPDATE attack_plan_runs
SET status = $4, resulting_version = $5,
    finished_at = CASE WHEN $4 = 'running'::attack_plan_run_status THEN NULL ELSE CURRENT_TIMESTAMP END,
    error = $6
WHERE id = $1 AND plan_id = $2 AND flow_id = $3
RETURNING *;

-- name: CreateAttackPlanEvidence :one
INSERT INTO attack_plan_evidence (
  plan_id, flow_id, node_id, run_id, revision_from, revision_to, provenance
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListAttackPlanEvidence :many
SELECT * FROM attack_plan_evidence
WHERE plan_id = $1 AND flow_id = $2
ORDER BY revision_from ASC, revision_to ASC, id ASC;

-- name: ListAttackPlanEvidenceForRun :many
SELECT * FROM attack_plan_evidence
WHERE plan_id = $1 AND flow_id = $2 AND run_id = $3 AND node_id IS NULL
ORDER BY revision_from ASC, revision_to ASC, id ASC;

-- name: CreateAttackPlanBinding :one
INSERT INTO attack_plan_bindings (plan_id, flow_id, node_id, task_id, subtask_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListAttackPlanBindings :many
SELECT * FROM attack_plan_bindings
WHERE plan_id = $1 AND flow_id = $2
ORDER BY id ASC;

-- name: CreateAttackPlanActionSubtaskBinding :one
INSERT INTO attack_plan_bindings (plan_id, flow_id, node_id, task_id, subtask_id)
SELECT $1, $2, n.id, $4, $5
FROM attack_plan_nodes n
WHERE n.id = $3 AND n.plan_id = $1 AND n.flow_id = $2 AND n.kind = 'action'
RETURNING *;
