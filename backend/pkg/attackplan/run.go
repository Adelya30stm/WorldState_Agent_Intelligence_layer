package attackplan

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"pentagi/pkg/database"
)

func validateRevision(revision Revision, requireVersion bool) error {
	if requireVersion && revision.ExpectedVersion <= 0 {
		return ErrInvalidInput
	}
	if revision.WorldStateRevisionFrom < 0 || revision.WorldStateRevision < revision.WorldStateRevisionFrom ||
		strings.TrimSpace(revision.IdempotencyKey) == "" || strings.TrimSpace(revision.Planner) == "" {
		return ErrInvalidInput
	}
	return nil
}

func createRun(ctx context.Context, q *database.Queries, plan database.AttackPlan, revision Revision, requestedVersion int64) (database.AttackPlanRun, error) {
	run, err := q.CreateAttackPlanRun(ctx, database.CreateAttackPlanRunParams{
		PlanID: plan.ID, FlowID: plan.FlowID, RequestedVersion: requestedVersion,
		WorldStateRevision: revision.WorldStateRevision, IdempotencyKey: revision.IdempotencyKey, Planner: revision.Planner,
	})
	if err != nil {
		return database.AttackPlanRun{}, fmt.Errorf("attackplan: create run: %w", err)
	}
	if run.RequestedVersion != requestedVersion || run.WorldStateRevision != revision.WorldStateRevision || run.Planner != revision.Planner {
		return database.AttackPlanRun{}, ErrIdempotencyConflict
	}
	return run, nil
}

func finishRun(ctx context.Context, q *database.Queries, run database.AttackPlanRun, revision Revision, version int64, fingerprint string) (database.AttackPlanRun, error) {
	provenance, err := json.Marshal(map[string]string{"fingerprint": fingerprint})
	if err != nil {
		return database.AttackPlanRun{}, fmt.Errorf("attackplan: encode run provenance: %w", err)
	}
	if _, err := q.CreateAttackPlanEvidence(ctx, database.CreateAttackPlanEvidenceParams{
		PlanID: run.PlanID, FlowID: run.FlowID, RunID: sql.NullInt64{Int64: run.ID, Valid: true},
		RevisionFrom: revision.WorldStateRevisionFrom, RevisionTo: revision.WorldStateRevision, Provenance: provenance,
	}); err != nil {
		return database.AttackPlanRun{}, fmt.Errorf("attackplan: record revision evidence: %w", err)
	}
	finished, err := q.TransitionAttackPlanRun(ctx, database.TransitionAttackPlanRunParams{
		ID: run.ID, PlanID: run.PlanID, FlowID: run.FlowID, Status: database.AttackPlanRunStatusSucceeded,
		ResultingVersion: sql.NullInt64{Int64: version, Valid: true}, Error: []byte(`{}`),
	})
	if err != nil {
		return database.AttackPlanRun{}, fmt.Errorf("attackplan: finish run: %w", err)
	}
	return finished, nil
}

func replayMutation(ctx context.Context, q *database.Queries, plan database.AttackPlan, revision Revision, requestedVersion int64, fingerprint string) (MutationResult, bool, error) {
	run, err := q.GetAttackPlanRunByIdempotencyKey(ctx, database.GetAttackPlanRunByIdempotencyKeyParams{
		PlanID: plan.ID, FlowID: plan.FlowID, IdempotencyKey: revision.IdempotencyKey,
	})
	if err == sql.ErrNoRows {
		return MutationResult{}, false, nil
	}
	if err != nil {
		return MutationResult{}, false, fmt.Errorf("attackplan: read idempotency key: %w", err)
	}
	if run.RequestedVersion != requestedVersion || run.WorldStateRevision != revision.WorldStateRevision || run.Planner != revision.Planner ||
		run.Status != database.AttackPlanRunStatusSucceeded || !run.ResultingVersion.Valid {
		return MutationResult{}, false, ErrIdempotencyConflict
	}
	evidence, err := q.ListAttackPlanEvidenceForRun(ctx, database.ListAttackPlanEvidenceForRunParams{
		PlanID: plan.ID, FlowID: plan.FlowID, RunID: sql.NullInt64{Int64: run.ID, Valid: true},
	})
	if err != nil {
		return MutationResult{}, false, fmt.Errorf("attackplan: read run provenance: %w", err)
	}
	var provenance struct {
		Fingerprint string `json:"fingerprint"`
	}
	if len(evidence) != 1 || evidence[0].RevisionFrom != revision.WorldStateRevisionFrom ||
		evidence[0].RevisionTo != revision.WorldStateRevision || json.Unmarshal(evidence[0].Provenance, &provenance) != nil ||
		provenance.Fingerprint != fingerprint {
		return MutationResult{}, false, ErrIdempotencyConflict
	}
	resultPlan := Plan{ID: plan.ID, FlowID: plan.FlowID, Version: run.ResultingVersion.Int64}
	if plan.Version == run.ResultingVersion.Int64 {
		resultPlan, err = loadPlan(ctx, q, plan)
		if err != nil {
			return MutationResult{}, false, err
		}
	}
	return MutationResult{Plan: resultPlan, Run: runFromDB(run), Replayed: true}, true, nil
}

func requestFingerprint(request any) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("attackplan: fingerprint request: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func createGraph(ctx context.Context, q *database.Queries, plan database.AttackPlan, nodes []Node, edges []Edge) error {
	ids := make(map[string]int64, len(nodes))
	for _, node := range nodes {
		payload := normalizedPayload(node.Payload)
		created, err := q.CreateAttackPlanNode(ctx, database.CreateAttackPlanNodeParams{
			PlanID: plan.ID, FlowID: plan.FlowID, NodeKey: node.Key, Kind: database.AttackPlanNodeKind(node.Kind),
			Status: database.AttackPlanNodeStatus(node.Status), Title: node.Title, Description: node.Description, Payload: payload,
		})
		if err != nil {
			return fmt.Errorf("attackplan: create node %q: %w", node.Key, err)
		}
		ids[node.Key] = created.ID
	}
	for _, edge := range edges {
		if _, err := q.CreateAttackPlanEdge(ctx, database.CreateAttackPlanEdgeParams{
			PlanID: plan.ID, FlowID: plan.FlowID, FromNodeID: ids[edge.FromKey], ToNodeID: ids[edge.ToKey],
			Kind: database.AttackPlanEdgeKind(edge.Kind),
		}); err != nil {
			return fmt.Errorf("attackplan: create edge %q -> %q: %w", edge.FromKey, edge.ToKey, err)
		}
	}
	return nil
}

func normalizedPayload(payload []byte) []byte {
	if len(payload) == 0 {
		return []byte(`{}`)
	}
	return payload
}
