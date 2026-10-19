-- name: CreateWorldStateEvent :one
INSERT INTO world_state_events (flow_id, kind, facts, actor, actor_msgchain_id, provenance)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetWorldStateEventHead :one
SELECT COALESCE(MAX(revision), 0)::bigint
FROM world_state_events
WHERE flow_id = $1;

-- name: GetWorldStateEventsByRevision :many
SELECT *
FROM world_state_events
WHERE flow_id = sqlc.arg(flow_id)
  AND revision > sqlc.arg(after_revision)
  AND revision <= sqlc.arg(through_revision)
ORDER BY revision ASC
LIMIT sqlc.arg(limit_rows);

-- name: GetSelectedWorldStateEvents :many
SELECT *
FROM world_state_events
WHERE flow_id = $1 AND revision = ANY($2::bigint[])
ORDER BY revision ASC;

-- name: GetWorldStateChainCursor :one
SELECT *
FROM world_state_chain_cursors
WHERE msgchain_id = $1;

-- name: AdvanceWorldStateChainCursor :one
INSERT INTO world_state_chain_cursors (msgchain_id, revision)
SELECT mc.id, sqlc.arg(revision)::bigint
FROM msgchains mc
WHERE mc.id = sqlc.arg(msgchain_id)
  AND (
    sqlc.arg(revision)::bigint = 0
    OR EXISTS (
      SELECT 1 FROM world_state_events e
      WHERE e.revision = sqlc.arg(revision)::bigint AND e.flow_id = mc.flow_id
    )
  )
ON CONFLICT (msgchain_id) DO UPDATE SET revision = EXCLUDED.revision
WHERE world_state_chain_cursors.revision <= EXCLUDED.revision
RETURNING *;

-- name: CreateAgentTaskUpdate :one
INSERT INTO agent_task_updates (
  flow_id, sender_msgchain_id, target_type, target_task_id, target_subtask_id,
  target_assistant_id, target_role, target_msgchain_id, instruction,
  selected_facts, source_revisions
)
SELECT
  sqlc.arg(flow_id), mc.id, sqlc.arg(target_type), sqlc.narg(target_task_id),
  sqlc.narg(target_subtask_id), sqlc.narg(target_assistant_id), sqlc.narg(target_role),
  sqlc.narg(target_msgchain_id), sqlc.arg(instruction), sqlc.arg(selected_facts),
  sqlc.arg(source_revisions)::bigint[]
FROM msgchains mc
WHERE mc.id = sqlc.arg(sender_msgchain_id) AND mc.flow_id = sqlc.arg(flow_id)
RETURNING agent_task_updates.*;

-- name: GetAgentTaskUpdate :one
SELECT *
FROM agent_task_updates
WHERE id = $1 AND flow_id = $2;

-- name: GetPendingAgentTaskUpdateHead :one
SELECT COALESCE(MAX(atu.id), 0)::bigint
FROM agent_task_updates atu
INNER JOIN msgchains mc ON mc.id = sqlc.arg(recipient_msgchain_id) AND mc.flow_id = atu.flow_id
WHERE atu.flow_id = sqlc.arg(flow_id) AND atu.recipient_msgchain_id = mc.id AND atu.state = 'pending';

-- name: GetPendingAgentTaskUpdatesForRecipient :many
SELECT atu.*
FROM agent_task_updates atu
INNER JOIN msgchains mc ON mc.id = sqlc.arg(recipient_msgchain_id) AND mc.flow_id = atu.flow_id
WHERE atu.flow_id = sqlc.arg(flow_id)
  AND atu.recipient_msgchain_id = mc.id
  AND atu.state = 'pending'
  AND atu.id <= sqlc.arg(through_id)
ORDER BY atu.id ASC
LIMIT sqlc.arg(limit_rows);

-- name: GetPendingAgentTaskUpdatesForTarget :many
SELECT *
FROM agent_task_updates
WHERE flow_id = sqlc.arg(flow_id)
  AND state = 'pending'
  AND target_type = sqlc.arg(target_type)
  AND target_task_id IS NOT DISTINCT FROM sqlc.narg(target_task_id)
  AND target_subtask_id IS NOT DISTINCT FROM sqlc.narg(target_subtask_id)
  AND target_assistant_id IS NOT DISTINCT FROM sqlc.narg(target_assistant_id)
  AND target_role IS NOT DISTINCT FROM sqlc.narg(target_role)
  AND target_msgchain_id IS NOT DISTINCT FROM sqlc.narg(target_msgchain_id)
ORDER BY id ASC
LIMIT sqlc.arg(limit_rows);

-- name: LockPendingAgentTaskUpdatesForTarget :many
SELECT *
FROM agent_task_updates
WHERE flow_id = sqlc.arg(flow_id)
  AND state = 'pending'
  AND target_type = sqlc.arg(target_type)
  AND target_task_id IS NOT DISTINCT FROM sqlc.narg(target_task_id)
  AND target_subtask_id IS NOT DISTINCT FROM sqlc.narg(target_subtask_id)
  AND target_assistant_id IS NOT DISTINCT FROM sqlc.narg(target_assistant_id)
  AND target_role IS NOT DISTINCT FROM sqlc.narg(target_role)
  AND target_msgchain_id IS NOT DISTINCT FROM sqlc.narg(target_msgchain_id)
ORDER BY id ASC
FOR UPDATE;

-- Routing and terminal completion must call the matching lifecycle lock first,
-- then insert or run the guarded terminal update in the same transaction.
-- name: LockAgentTaskTargetLifecycle :one
SELECT t.*
FROM tasks t
WHERE t.id = sqlc.arg(target_task_id) AND t.flow_id = sqlc.arg(flow_id)
FOR UPDATE;

-- name: LockAgentSubtaskTargetLifecycle :one
SELECT s.*
FROM subtasks s
INNER JOIN tasks t ON t.id = s.task_id
WHERE s.id = sqlc.arg(target_subtask_id)
  AND t.id = sqlc.arg(target_task_id)
  AND t.flow_id = sqlc.arg(flow_id)
FOR UPDATE OF s;

-- name: LockAgentAssistantTargetLifecycle :one
SELECT a.*
FROM assistants a
WHERE a.id = sqlc.arg(target_assistant_id)
  AND a.flow_id = sqlc.arg(flow_id)
  AND a.deleted_at IS NULL
FOR UPDATE;

-- Specialist routing also locks the task/subtask lifecycle row above. When a
-- persisted invocation is selected, this additionally validates and locks it.
-- name: LockAgentSpecialistMsgchain :one
SELECT mc.*
FROM msgchains mc
WHERE mc.id = sqlc.arg(target_msgchain_id)
  AND mc.flow_id = sqlc.arg(flow_id)
  AND mc.task_id IS NOT DISTINCT FROM sqlc.narg(target_task_id)
  AND mc.subtask_id IS NOT DISTINCT FROM sqlc.narg(target_subtask_id)
  AND mc.type::text = sqlc.arg(target_role)
FOR UPDATE;

-- name: TrySetAgentTaskTargetTerminal :one
UPDATE tasks t
SET status = sqlc.arg(status)::task_status
WHERE t.id = sqlc.arg(target_task_id)
  AND t.flow_id = sqlc.arg(flow_id)
  AND sqlc.arg(status)::text IN ('finished', 'failed')
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_updates atu
    WHERE atu.flow_id = t.flow_id
      AND atu.target_task_id = t.id
      AND atu.target_subtask_id IS NULL
      AND atu.state = 'pending'
      AND atu.target_type IN ('task', 'specialist')
  )
RETURNING t.*;

-- name: TrySetAgentSubtaskTargetTerminal :one
UPDATE subtasks s
SET status = sqlc.arg(status)::subtask_status
FROM tasks t
WHERE s.id = sqlc.arg(target_subtask_id)
  AND t.id = s.task_id
  AND t.id = sqlc.arg(target_task_id)
  AND t.flow_id = sqlc.arg(flow_id)
  AND sqlc.arg(status)::text IN ('finished', 'failed')
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_updates atu
    WHERE atu.flow_id = t.flow_id
      AND atu.target_task_id = t.id
      AND atu.target_subtask_id = s.id
      AND atu.state = 'pending'
      AND atu.target_type IN ('subtask', 'specialist')
  )
RETURNING s.*;

-- name: TrySetAgentAssistantTargetTerminal :one
UPDATE assistants a
SET status = sqlc.arg(status)::assistant_status
WHERE a.id = sqlc.arg(target_assistant_id)
  AND a.flow_id = sqlc.arg(flow_id)
  AND a.deleted_at IS NULL
  AND sqlc.arg(status)::text IN ('finished', 'failed')
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_updates atu
    WHERE atu.flow_id = a.flow_id
      AND atu.target_assistant_id = a.id
      AND atu.state = 'pending'
      AND atu.target_type = 'assistant'
  )
RETURNING a.*;

-- name: BindAgentTaskUpdateRecipient :one
UPDATE agent_task_updates atu
SET recipient_msgchain_id = mc.id
FROM msgchains mc
WHERE atu.id = sqlc.arg(update_id)
  AND atu.flow_id = sqlc.arg(flow_id)
  AND atu.state = 'pending'
  AND (atu.recipient_msgchain_id IS NULL OR atu.recipient_msgchain_id = mc.id)
  AND mc.id = sqlc.arg(recipient_msgchain_id)
  AND mc.flow_id = atu.flow_id
RETURNING atu.*;

-- name: AcknowledgeAgentTaskUpdatesExact :many
WITH requested AS (
  SELECT DISTINCT unnest(sqlc.arg(update_ids)::bigint[]) AS id
), eligible AS (
  SELECT atu.id
  FROM agent_task_updates atu
  INNER JOIN requested r ON r.id = atu.id
  INNER JOIN msgchains mc ON mc.id = sqlc.arg(recipient_msgchain_id) AND mc.flow_id = atu.flow_id
  WHERE atu.flow_id = sqlc.arg(flow_id)
    AND atu.state = 'pending'
    AND (atu.recipient_msgchain_id IS NULL OR atu.recipient_msgchain_id = mc.id)
    AND mc.updated_at = sqlc.arg(delivered_at)
  FOR UPDATE OF atu
), valid AS (
  SELECT COUNT(*) = cardinality(sqlc.arg(update_ids)::bigint[])
    AND COUNT(*) = (SELECT COUNT(*) FROM eligible) AS ok
  FROM requested
), acknowledged AS (
  UPDATE agent_task_updates atu
  SET state = 'delivered',
      recipient_msgchain_id = sqlc.arg(recipient_msgchain_id),
      delivered_at = sqlc.arg(delivered_at)
  FROM eligible e
  WHERE atu.id = e.id AND (SELECT ok FROM valid)
  RETURNING atu.*
)
SELECT * FROM acknowledged ORDER BY id ASC;

-- name: RejectAgentTaskUpdate :one
UPDATE agent_task_updates
SET state = 'rejected', rejected_at = sqlc.arg(rejected_at), rejection_reason = sqlc.arg(rejection_reason)
WHERE id = sqlc.arg(update_id) AND flow_id = sqlc.arg(flow_id) AND state = 'pending'
RETURNING *;

-- name: RejectPendingAgentTaskUpdatesForTarget :many
UPDATE agent_task_updates
SET state = 'rejected', rejected_at = sqlc.arg(rejected_at), rejection_reason = sqlc.arg(rejection_reason)
WHERE flow_id = sqlc.arg(flow_id)
  AND state = 'pending'
  AND target_type = sqlc.arg(target_type)
  AND target_task_id IS NOT DISTINCT FROM sqlc.narg(target_task_id)
  AND target_subtask_id IS NOT DISTINCT FROM sqlc.narg(target_subtask_id)
  AND target_assistant_id IS NOT DISTINCT FROM sqlc.narg(target_assistant_id)
  AND target_role IS NOT DISTINCT FROM sqlc.narg(target_role)
  AND target_msgchain_id IS NOT DISTINCT FROM sqlc.narg(target_msgchain_id)
RETURNING *;

-- name: UpsertAgentChainWait :one
INSERT INTO agent_chain_waits (flow_id, msgchain_id, wait_kind, pending_tool_call_id)
SELECT mc.flow_id, mc.id, sqlc.arg(wait_kind), sqlc.narg(pending_tool_call_id)
FROM msgchains mc
WHERE mc.id = sqlc.arg(msgchain_id) AND mc.flow_id = sqlc.arg(flow_id)
ON CONFLICT (msgchain_id) DO UPDATE SET
  msgchain_id = agent_chain_waits.msgchain_id
WHERE agent_chain_waits.state = 'pending'
  AND agent_chain_waits.resolution_winner IS NULL
  AND agent_chain_waits.wait_kind = EXCLUDED.wait_kind
  AND agent_chain_waits.pending_tool_call_id IS NOT DISTINCT FROM EXCLUDED.pending_tool_call_id
RETURNING *;

-- name: GetAgentChainWait :one
SELECT * FROM agent_chain_waits WHERE msgchain_id = $1;

-- name: LockAgentChainWait :one
SELECT * FROM agent_chain_waits WHERE msgchain_id = $1 FOR UPDATE;

-- name: LeaseAgentChainWaits :many
WITH candidates AS (
  SELECT msgchain_id
  FROM agent_chain_waits
  WHERE state = 'pending'
    AND resolution_winner IS NULL
    AND next_attempt_at <= CURRENT_TIMESTAMP
    AND (lease_expires_at IS NULL OR lease_expires_at <= CURRENT_TIMESTAMP)
  ORDER BY next_attempt_at ASC, msgchain_id ASC
  FOR UPDATE SKIP LOCKED
  LIMIT sqlc.arg(limit_rows)
)
UPDATE agent_chain_waits w
SET lease_owner = sqlc.arg(lease_owner), lease_expires_at = sqlc.arg(lease_expires_at)
FROM candidates c
WHERE w.msgchain_id = c.msgchain_id
RETURNING w.*;

-- name: LeasePrimaryWorldStateWaits :many
WITH candidates AS (
  SELECT w.msgchain_id
  FROM agent_chain_waits w
  INNER JOIN msgchains mc ON mc.id = w.msgchain_id AND mc.flow_id = w.flow_id
  INNER JOIN tasks t ON t.id = mc.task_id AND t.flow_id = w.flow_id
  INNER JOIN subtasks s ON s.id = mc.subtask_id AND s.task_id = t.id
  WHERE w.wait_kind = 'tool'
    AND w.state = 'pending'
    AND w.resolution_winner IS NULL
    AND mc.type = 'primary_agent'
    AND t.status NOT IN ('finished', 'failed')
    AND s.status NOT IN ('finished', 'failed')
    AND w.next_attempt_at <= CURRENT_TIMESTAMP
    AND (w.lease_expires_at IS NULL OR w.lease_expires_at <= CURRENT_TIMESTAMP)
    AND COALESCE((SELECT c.revision FROM world_state_chain_cursors c WHERE c.msgchain_id = w.msgchain_id), 0)
      < COALESCE((SELECT MAX(e.revision) FROM world_state_events e WHERE e.flow_id = w.flow_id), 0)
  ORDER BY w.next_attempt_at ASC, w.msgchain_id ASC
  FOR UPDATE OF w SKIP LOCKED
  LIMIT sqlc.arg(limit_rows)
)
UPDATE agent_chain_waits w
SET lease_owner = sqlc.arg(lease_owner), lease_expires_at = sqlc.arg(lease_expires_at)
FROM candidates c
WHERE w.msgchain_id = c.msgchain_id
RETURNING w.*;

-- name: LeasePrimaryWorldStateResumeWaits :many
WITH candidates AS (
  SELECT w.msgchain_id
  FROM agent_chain_waits w
  INNER JOIN msgchains mc ON mc.id = w.msgchain_id AND mc.flow_id = w.flow_id
  INNER JOIN tasks t ON t.id = mc.task_id AND t.flow_id = w.flow_id
  INNER JOIN subtasks s ON s.id = mc.subtask_id AND s.task_id = t.id
  WHERE w.state = 'resolved'
    AND w.resolution_winner = 'world_state'
    AND w.resume_pending = TRUE
    AND mc.type = 'primary_agent'
    AND t.status NOT IN ('finished', 'failed')
    AND s.status NOT IN ('finished', 'failed')
    AND w.next_attempt_at <= CURRENT_TIMESTAMP
    AND (w.lease_expires_at IS NULL OR w.lease_expires_at <= CURRENT_TIMESTAMP)
  ORDER BY w.resolved_at ASC, w.msgchain_id ASC
  FOR UPDATE OF w SKIP LOCKED
  LIMIT sqlc.arg(limit_rows)
)
UPDATE agent_chain_waits w
SET lease_owner = sqlc.arg(lease_owner), lease_expires_at = sqlc.arg(lease_expires_at)
FROM candidates c
WHERE w.msgchain_id = c.msgchain_id
RETURNING w.*;

-- name: GetClaimedPrimaryWorldStateResume :one
SELECT * FROM agent_chain_waits
WHERE msgchain_id = sqlc.arg(msgchain_id)
  AND state = 'resolved'
  AND resolution_winner = 'world_state'
  AND pending_tool_call_id = sqlc.arg(pending_tool_call_id)
  AND resolution_ref = sqlc.arg(resolution_ref)
  AND resume_pending = TRUE
  AND resume_intent = sqlc.arg(resume_intent)
  AND lease_owner = sqlc.arg(lease_owner)
  AND lease_expires_at > CURRENT_TIMESTAMP;

-- name: ClaimPrimaryWorldStateResumeForController :one
UPDATE agent_chain_waits
SET lease_owner = sqlc.arg(lease_owner), lease_expires_at = sqlc.arg(lease_expires_at)
WHERE msgchain_id = sqlc.arg(msgchain_id)
  AND state = 'resolved'
  AND resolution_winner = 'world_state'
  AND resume_pending = TRUE
RETURNING *;

-- name: RenewAgentChainWaitLease :one
UPDATE agent_chain_waits
SET lease_expires_at = sqlc.arg(lease_expires_at)
WHERE msgchain_id = sqlc.arg(msgchain_id)
  AND state = 'pending'
  AND resolution_winner IS NULL
  AND lease_owner = sqlc.arg(lease_owner)
  AND lease_expires_at > CURRENT_TIMESTAMP
RETURNING *;

-- name: ReleaseAgentChainWaitLease :one
UPDATE agent_chain_waits
SET lease_owner = NULL,
    lease_expires_at = NULL,
    retry_count = retry_count + 1,
    next_attempt_at = sqlc.arg(next_attempt_at)
WHERE msgchain_id = sqlc.arg(msgchain_id)
  AND state = 'pending'
  AND resolution_winner IS NULL
  AND lease_owner = sqlc.arg(lease_owner)
RETURNING *;

-- name: ReleasePrimaryWorldStateResumeLease :one
UPDATE agent_chain_waits
SET lease_owner = NULL,
    lease_expires_at = NULL,
    retry_count = retry_count + 1,
    next_attempt_at = sqlc.arg(next_attempt_at)
WHERE msgchain_id = sqlc.arg(msgchain_id)
  AND state = 'resolved'
  AND resolution_winner = 'world_state'
  AND resume_pending = TRUE
  AND lease_owner = sqlc.arg(lease_owner)
RETURNING *;

-- name: ResolveAgentChainWait :one
UPDATE agent_chain_waits w
SET state = 'resolved',
    resolution_winner = sqlc.arg(resolution_winner),
    resolution_ref = sqlc.narg(resolution_ref),
    resolved_at = sqlc.arg(resolved_at),
    lease_owner = NULL,
    lease_expires_at = NULL,
    resume_pending = sqlc.arg(resume_pending),
    resume_intent = sqlc.arg(resume_intent)
WHERE w.msgchain_id = sqlc.arg(msgchain_id)
  AND w.state = 'pending'
  AND w.resolution_winner IS NULL
  AND (
    sqlc.arg(resolution_winner)::text <> 'world_state'
    OR EXISTS (
      SELECT 1 FROM world_state_events e
      WHERE e.revision = sqlc.narg(resolution_ref) AND e.flow_id = w.flow_id
    )
  )
  AND (
    sqlc.arg(resolution_winner)::text <> 'task_update'
    OR EXISTS (
      SELECT 1 FROM agent_task_updates atu
      WHERE atu.id = sqlc.narg(resolution_ref) AND atu.flow_id = w.flow_id AND atu.state = 'pending'
    )
  )
RETURNING w.*;

-- name: ResolveLeasedPrimaryWorldStateWait :one
UPDATE agent_chain_waits w
SET state = 'resolved',
    resolution_winner = 'world_state',
    resolution_ref = sqlc.arg(resolution_ref),
    resolved_at = sqlc.arg(resolved_at),
    lease_owner = NULL,
    lease_expires_at = NULL,
    resume_pending = TRUE,
    resume_intent = sqlc.arg(resume_intent)
FROM msgchains mc, tasks t, subtasks s
WHERE w.msgchain_id = sqlc.arg(msgchain_id)
  AND w.state = 'pending'
  AND w.resolution_winner IS NULL
  AND w.lease_owner = sqlc.arg(lease_owner)
  AND mc.id = w.msgchain_id
  AND mc.flow_id = w.flow_id
  AND mc.type = 'primary_agent'
  AND t.id = mc.task_id
  AND t.flow_id = w.flow_id
  AND t.status NOT IN ('finished', 'failed')
  AND s.id = mc.subtask_id
  AND s.task_id = t.id
  AND s.status NOT IN ('finished', 'failed')
  AND EXISTS (
    SELECT 1 FROM world_state_events e
    WHERE e.flow_id = w.flow_id AND e.revision = sqlc.arg(resolution_ref)
  )
  AND COALESCE((SELECT c.revision FROM world_state_chain_cursors c WHERE c.msgchain_id = w.msgchain_id), 0)
    < sqlc.arg(resolution_ref)
RETURNING w.*;

-- name: AcceptClaimedPrimaryWorldStateResume :one
UPDATE agent_chain_waits
SET resume_pending = FALSE,
    resume_intent = '{}'::jsonb,
    lease_owner = NULL,
    lease_expires_at = NULL
WHERE msgchain_id = sqlc.arg(msgchain_id)
  AND state = 'resolved'
  AND resolution_winner = 'world_state'
  AND pending_tool_call_id = sqlc.arg(pending_tool_call_id)
  AND resolution_ref = sqlc.arg(resolution_ref)
  AND resume_pending = TRUE
  AND resume_intent = sqlc.arg(resume_intent)
  AND lease_owner = sqlc.arg(lease_owner)
  AND lease_expires_at > CURRENT_TIMESTAMP
RETURNING *;

-- name: DeleteAcceptedPrimaryWorldStateWait :one
DELETE FROM agent_chain_waits
WHERE msgchain_id = sqlc.arg(msgchain_id)
  AND state = 'resolved'
  AND resolution_winner = 'world_state'
  AND resume_pending = FALSE
RETURNING *;

-- name: DeleteAgentChainWait :exec
DELETE FROM agent_chain_waits WHERE msgchain_id = $1;