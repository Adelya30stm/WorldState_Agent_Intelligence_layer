-- name: GetWorldStateEntityByID :one
SELECT *
FROM world_state_entities
WHERE id = $1;

-- name: GetWorldStateEntityByKey :one
SELECT *
FROM world_state_entities
WHERE flow_id = $1 AND entity_key = $2;

-- name: LockWorldStateEntityByID :one
SELECT *
FROM world_state_entities
WHERE id = $1
FOR UPDATE;

-- name: LockWorldStateEntityByKey :one
SELECT *
FROM world_state_entities
WHERE flow_id = $1 AND entity_key = $2
FOR UPDATE;

-- name: GetWorldStateEntitiesByFlow :many
SELECT *
FROM world_state_entities
WHERE flow_id = $1
ORDER BY updated_at DESC;

-- name: GetWorldStateLinksByFlow :many
SELECT *
FROM world_state_links
WHERE flow_id = $1
ORDER BY id ASC;

-- name: GetWorldStateTransitionsByFlow :many
SELECT
  t.id,
  t.entity_id,
  t.from_state,
  t.to_state,
  t.agent,
  t.evidence,
  t.created_at,
  e.entity_key,
  e.type AS entity_type
FROM world_state_transitions t
INNER JOIN world_state_entities e ON e.id = t.entity_id
WHERE e.flow_id = $1
ORDER BY t.created_at DESC
LIMIT $2;

-- name: GetWorldStateTransitionsByEntity :many
SELECT *
FROM world_state_transitions
WHERE entity_id = $1
ORDER BY created_at ASC;

-- name: InsertWorldStateEntity :one
INSERT INTO world_state_entities (
  flow_id,
  entity_key,
  type,
  state,
  properties,
  annotations
) VALUES (
  $1, $2, $3, $4, $5, $6
)
ON CONFLICT (flow_id, entity_key) DO NOTHING
RETURNING *;

-- name: MergeWorldStateEntityProperties :one
UPDATE world_state_entities
SET
  properties = properties || sqlc.arg(properties)::jsonb,
  updated_at = CURRENT_TIMESTAMP,
  version = version + 1
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: UpdateWorldStateEntityState :one
UPDATE world_state_entities
SET
  state = $1,
  updated_at = CURRENT_TIMESTAMP,
  version = version + 1
WHERE id = $2
RETURNING *;

-- name: CreateWorldStateTransition :one
INSERT INTO world_state_transitions (
  entity_id,
  from_state,
  to_state,
  agent,
  evidence
) VALUES (
  $1, $2, $3, $4, $5
)
RETURNING *;

-- name: LockWorldStateLink :one
SELECT *
FROM world_state_links
WHERE flow_id = $1 AND source_id = $2 AND target_id = $3 AND type = $4
FOR UPDATE;

-- name: InsertWorldStateLink :one
INSERT INTO world_state_links (
  flow_id,
  source_id,
  target_id,
  type,
  properties
) VALUES (
  $1, $2, $3, $4, $5
)
ON CONFLICT (flow_id, source_id, target_id, type) DO NOTHING
RETURNING *;

-- name: MergeWorldStateLinkProperties :one
UPDATE world_state_links
SET properties = properties || sqlc.arg(properties)::jsonb
WHERE id = sqlc.arg(id)
RETURNING *;
