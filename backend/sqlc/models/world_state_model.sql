-- name: GetWorldStateModelVersion :one
SELECT world_state_schema_version FROM flows WHERE id = $1;

-- Serialize mutations within a flow, including duplicate starts, before locking
-- individual records. This keeps journal and aggregate observations coherent.
-- name: LockWorldStateModelFlow :one
SELECT world_state_schema_version FROM flows WHERE id = $1 FOR UPDATE;

-- name: GetWorldStateModelObjectByKey :one
SELECT * FROM world_state_objects WHERE flow_id = $1 AND object_key = $2;

-- name: GetWorldStateModelObject :one
SELECT * FROM world_state_objects WHERE flow_id = $1 AND id = $2;

-- name: InsertWorldStateModelObject :one
INSERT INTO world_state_objects(flow_id, object_key, type, locator)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: UpdateWorldStateModelObject :one
UPDATE world_state_objects SET locator = $3, updated_at = CURRENT_TIMESTAMP
WHERE flow_id = $1 AND id = $2 RETURNING *;

-- name: GetWorldStateModelCheckByKind :one
SELECT c.* FROM world_state_checks c
JOIN world_state_objects o ON o.id = c.object_id
WHERE o.flow_id = $1 AND c.object_id = $2 AND c.kind = $3;

-- name: GetWorldStateModelCheck :one
SELECT c.* FROM world_state_checks c
JOIN world_state_objects o ON o.id = c.object_id
WHERE o.flow_id = $1 AND c.id = $2;

-- name: InsertWorldStateModelCheck :one
INSERT INTO world_state_checks(object_id, kind) VALUES ($1, $2) RETURNING *;

-- name: TouchWorldStateModelCheck :exec
UPDATE world_state_checks SET updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: GetWorldStateModelRunByRequest :one
SELECT r.* FROM world_state_check_runs r
JOIN world_state_checks c ON c.id = r.check_id
JOIN world_state_objects o ON o.id = c.object_id
WHERE o.flow_id = $1 AND r.request_key = $2;

-- name: GetWorldStateModelRun :one
SELECT r.* FROM world_state_check_runs r
JOIN world_state_checks c ON c.id = r.check_id
JOIN world_state_objects o ON o.id = c.object_id
WHERE o.flow_id = $1 AND r.id = $2;

-- name: InsertWorldStateModelRun :one
INSERT INTO world_state_check_runs(check_id, tool_call_id, request_key, parameters, execution_status, started_at)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: UpdateWorldStateModelRun :one
UPDATE world_state_check_runs
SET tool_call_id = $2, execution_status = $3, verdict = $4, evidence = $5, started_at = $6, finished_at = $7
WHERE id = $1 RETURNING *;

-- name: ValidateWorldStateModelToolCall :one
SELECT id FROM toolcalls WHERE flow_id = $1 AND id = $2;

-- name: ListWorldStateModelObjects :many
SELECT * FROM world_state_objects WHERE flow_id = $1 ORDER BY id;

-- name: ListWorldStateModelChecks :many
SELECT c.* FROM world_state_checks c
JOIN world_state_objects o ON o.id = c.object_id
WHERE o.flow_id = $1 ORDER BY c.id;

-- name: ListWorldStateModelRuns :many
SELECT r.* FROM world_state_check_runs r
JOIN world_state_checks c ON c.id = r.check_id
JOIN world_state_objects o ON o.id = c.object_id
WHERE o.flow_id = $1 ORDER BY r.id;

-- name: ListWorldStateModelObjectChecks :many
SELECT c.* FROM world_state_checks c
JOIN world_state_objects o ON o.id = c.object_id
WHERE o.flow_id = $1 AND o.id = $2 ORDER BY c.id;

-- name: ListWorldStateModelCheckRuns :many
SELECT r.* FROM world_state_check_runs r
JOIN world_state_checks c ON c.id = r.check_id
JOIN world_state_objects o ON o.id = c.object_id
WHERE o.flow_id = $1 AND c.id = $2 ORDER BY r.id;
