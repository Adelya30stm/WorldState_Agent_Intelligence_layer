package attackplan

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"pentagi/pkg/database"
)

type graphTransform func(Plan) ([]Node, []Edge, error)

func (s *Store) ReplaceGraph(ctx context.Context, request ReplaceGraphRequest) (MutationResult, error) {
	if err := validateGraph(request.Nodes, request.Edges); err != nil {
		return MutationResult{}, err
	}
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return MutationResult{}, err
	}
	return s.mutateGraph(ctx, request.FlowID, request.PlanID, request.Revision, fingerprint, func(Plan) ([]Node, []Edge, error) {
		return request.Nodes, request.Edges, nil
	})
}

func (s *Store) PatchGraph(ctx context.Context, request PatchGraphRequest) (MutationResult, error) {
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return MutationResult{}, err
	}
	return s.mutateGraph(ctx, request.FlowID, request.PlanID, request.Revision, fingerprint, func(current Plan) ([]Node, []Edge, error) {
		return applyPatch(current.Nodes, current.Edges, request.Patch)
	})
}

func (s *Store) TransitionNode(ctx context.Context, request TransitionNodeRequest) (MutationResult, error) {
	if request.NodeKey == "" || !request.To.isKnown() {
		return MutationResult{}, ErrInvalidInput
	}
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return MutationResult{}, err
	}
	return s.mutateGraph(ctx, request.FlowID, request.PlanID, request.Revision, fingerprint, func(current Plan) ([]Node, []Edge, error) {
		found := false
		for index := range current.Nodes {
			if current.Nodes[index].Key != request.NodeKey {
				continue
			}
			found = true
			if err := validateNodeTransition(current.Nodes[index].Status, request.To); err != nil {
				return nil, nil, err
			}
			current.Nodes[index].Status = request.To
		}
		if !found {
			return nil, nil, ErrNotFound
		}
		return current.Nodes, current.Edges, nil
	})
}

func (s *Store) mutateGraph(ctx context.Context, flowID, planID int64, revision Revision, fingerprint string, transform graphTransform) (MutationResult, error) {
	if flowID <= 0 || planID <= 0 {
		return MutationResult{}, ErrInvalidInput
	}
	if err := validateRevision(revision, true); err != nil {
		return MutationResult{}, err
	}
	var result MutationResult
	err := s.withTx(ctx, func(q *database.Queries) error {
		locked, err := lockPlan(ctx, q, flowID, planID)
		if err != nil {
			return err
		}
		if replay, ok, err := replayMutation(ctx, q, locked, revision, revision.ExpectedVersion, fingerprint); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		if locked.Version != revision.ExpectedVersion {
			return ErrStaleVersion
		}
		if PlanStatus(locked.Status).IsTerminal() {
			return ErrInvalidTransition
		}
		current, err := loadPlan(ctx, q, locked)
		if err != nil {
			return err
		}
		nodes, edges, err := transform(current)
		if err != nil {
			return err
		}
		if err := validateGraph(nodes, edges); err != nil {
			return err
		}
		if err := validateGraphChanges(current.Nodes, nodes, current.Bindings); err != nil {
			return err
		}
		run, err := createRun(ctx, q, locked, revision, revision.ExpectedVersion)
		if err != nil {
			return err
		}
		if err := persistGraph(ctx, q, locked, current.Nodes, nodes, edges); err != nil {
			return err
		}
		updated, err := updatePlan(ctx, q, locked, locked.Objective, PlanStatus(locked.Status), revision)
		if err != nil {
			return err
		}
		finished, err := finishRun(ctx, q, run, revision, updated.Version, fingerprint)
		if err != nil {
			return err
		}
		plan, err := loadPlan(ctx, q, updated)
		if err != nil {
			return err
		}
		result = MutationResult{Plan: plan, Run: runFromDB(finished)}
		return nil
	})
	return result, err
}

func lockPlan(ctx context.Context, q *database.Queries, flowID, planID int64) (database.AttackPlan, error) {
	if _, err := q.LockAttackPlanFlow(ctx, flowID); err != nil {
		return database.AttackPlan{}, storeError("lock flow", err)
	}
	plan, err := q.LockAttackPlan(ctx, database.LockAttackPlanParams{ID: planID, FlowID: flowID})
	if err != nil {
		return database.AttackPlan{}, storeError("lock plan", err)
	}
	return plan, nil
}

func updatePlan(ctx context.Context, q *database.Queries, plan database.AttackPlan, objective string, status PlanStatus, revision Revision) (database.AttackPlan, error) {
	updated, err := q.UpdateAttackPlanVersion(ctx, database.UpdateAttackPlanVersionParams{
		ID: plan.ID, FlowID: plan.FlowID, Objective: objective, Status: database.AttackPlanStatus(status),
		Planner: revision.Planner, Version: revision.ExpectedVersion,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return database.AttackPlan{}, ErrStaleVersion
	}
	if err != nil {
		return database.AttackPlan{}, fmt.Errorf("attackplan: update plan: %w", err)
	}
	return updated, nil
}

func validateGraphChanges(current, desired []Node, bindings []Binding) error {
	existing := make(map[string]Node, len(current))
	for _, node := range current {
		existing[node.Key] = node
	}
	for _, node := range desired {
		old, ok := existing[node.Key]
		if !ok {
			continue
		}
		delete(existing, node.Key)
		if old.Kind != node.Kind {
			return fmt.Errorf("%w: node %q kind is immutable", ErrInvalidInput, node.Key)
		}
		if err := validateNodeTransition(old.Status, node.Status); err != nil {
			return err
		}
	}
	for _, removed := range existing {
		if removed.Status.IsTerminal() {
			return fmt.Errorf("%w: terminal node %q cannot be removed", ErrInvalidTransition, removed.Key)
		}
		for _, binding := range bindings {
			if binding.NodeKey == removed.Key {
				return fmt.Errorf("%w: bound node %q cannot be removed", ErrInvalidInput, removed.Key)
			}
		}
	}
	return nil
}

func persistGraph(ctx context.Context, q *database.Queries, plan database.AttackPlan, current, desired []Node, edges []Edge) error {
	if err := q.DeleteAttackPlanEdges(ctx, database.DeleteAttackPlanEdgesParams{PlanID: plan.ID, FlowID: plan.FlowID}); err != nil {
		return fmt.Errorf("attackplan: delete edges: %w", err)
	}
	existing := make(map[string]Node, len(current))
	for _, node := range current {
		existing[node.Key] = node
	}
	ids := make(map[string]int64, len(desired))
	for _, node := range desired {
		old, ok := existing[node.Key]
		if !ok {
			created, err := q.CreateAttackPlanNode(ctx, database.CreateAttackPlanNodeParams{
				PlanID: plan.ID, FlowID: plan.FlowID, NodeKey: node.Key, Kind: database.AttackPlanNodeKind(node.Kind),
				Status: database.AttackPlanNodeStatus(node.Status), Title: node.Title, Description: node.Description, Payload: normalizedPayload(node.Payload),
			})
			if err != nil {
				return fmt.Errorf("attackplan: create node %q: %w", node.Key, err)
			}
			ids[node.Key] = created.ID
			continue
		}
		delete(existing, node.Key)
		ids[node.Key] = old.ID
		if old.Status == node.Status && old.Title == node.Title && old.Description == node.Description && bytes.Equal(normalizedPayload(old.Payload), normalizedPayload(node.Payload)) {
			continue
		}
		if _, err := q.UpdateAttackPlanNodeVersion(ctx, database.UpdateAttackPlanNodeVersionParams{
			ID: old.ID, PlanID: plan.ID, FlowID: plan.FlowID, Status: database.AttackPlanNodeStatus(node.Status),
			Title: node.Title, Description: node.Description, Payload: normalizedPayload(node.Payload), Version: old.Version,
		}); errors.Is(err, sql.ErrNoRows) {
			return ErrStaleVersion
		} else if err != nil {
			return fmt.Errorf("attackplan: update node %q: %w", node.Key, err)
		}
	}
	for _, removed := range existing {
		if err := q.DeleteAttackPlanNode(ctx, database.DeleteAttackPlanNodeParams{ID: removed.ID, PlanID: plan.ID, FlowID: plan.FlowID}); err != nil {
			return fmt.Errorf("attackplan: delete node %q: %w", removed.Key, err)
		}
	}
	for _, edge := range edges {
		if _, err := q.CreateAttackPlanEdge(ctx, database.CreateAttackPlanEdgeParams{
			PlanID: plan.ID, FlowID: plan.FlowID, FromNodeID: ids[edge.FromKey], ToNodeID: ids[edge.ToKey], Kind: database.AttackPlanEdgeKind(edge.Kind),
		}); err != nil {
			return fmt.Errorf("attackplan: create edge %q -> %q: %w", edge.FromKey, edge.ToKey, err)
		}
	}
	return nil
}
