package attackplan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"pentagi/pkg/database"

	"github.com/lib/pq"
)

func (s *Store) TransitionPlan(ctx context.Context, request TransitionPlanRequest) (MutationResult, error) {
	if request.FlowID <= 0 || request.PlanID <= 0 || !request.To.isKnown() {
		return MutationResult{}, ErrInvalidInput
	}
	if err := validateRevision(request.Revision, true); err != nil {
		return MutationResult{}, err
	}
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return MutationResult{}, err
	}
	var result MutationResult
	err = s.withTx(ctx, func(q *database.Queries) error {
		locked, err := lockPlan(ctx, q, request.FlowID, request.PlanID)
		if err != nil {
			return err
		}
		if replay, ok, err := replayMutation(ctx, q, locked, request.Revision, request.ExpectedVersion, fingerprint); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		if locked.Version != request.ExpectedVersion {
			return ErrStaleVersion
		}
		if err := validatePlanTransition(PlanStatus(locked.Status), request.To); err != nil {
			return err
		}
		run, err := createRun(ctx, q, locked, request.Revision, request.ExpectedVersion)
		if err != nil {
			return err
		}
		updated, err := updatePlan(ctx, q, locked, locked.Objective, request.To, request.Revision)
		if err != nil {
			return err
		}
		finished, err := finishRun(ctx, q, run, request.Revision, updated.Version, fingerprint)
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

func (s *Store) BindAction(ctx context.Context, request BindActionRequest) (MutationResult, error) {
	if request.FlowID <= 0 || request.PlanID <= 0 || request.NodeKey == "" || request.TaskID <= 0 || request.SubtaskID <= 0 {
		return MutationResult{}, ErrInvalidInput
	}
	if err := validateRevision(request.Revision, true); err != nil {
		return MutationResult{}, err
	}
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return MutationResult{}, err
	}
	var result MutationResult
	err = s.withTx(ctx, func(q *database.Queries) error {
		locked, err := lockPlan(ctx, q, request.FlowID, request.PlanID)
		if err != nil {
			return err
		}
		if replay, ok, err := replayMutation(ctx, q, locked, request.Revision, request.ExpectedVersion, fingerprint); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		if locked.Version != request.ExpectedVersion {
			return ErrStaleVersion
		}
		if PlanStatus(locked.Status).IsTerminal() {
			return ErrInvalidTransition
		}
		node, err := q.GetAttackPlanNodeByKey(ctx, database.GetAttackPlanNodeByKeyParams{
			PlanID: locked.ID, FlowID: locked.FlowID, NodeKey: request.NodeKey,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("attackplan: get action node: %w", err)
		}
		if node.Kind != database.AttackPlanNodeKindAction {
			return fmt.Errorf("%w: only action nodes can bind to subtasks", ErrInvalidInput)
		}
		run, err := createRun(ctx, q, locked, request.Revision, request.ExpectedVersion)
		if err != nil {
			return err
		}
		_, err = q.CreateAttackPlanActionSubtaskBinding(ctx, database.CreateAttackPlanActionSubtaskBindingParams{
			PlanID: locked.ID, FlowID: locked.FlowID, ID: node.ID,
			TaskID: sql.NullInt64{Int64: request.TaskID, Valid: true}, SubtaskID: sql.NullInt64{Int64: request.SubtaskID, Valid: true},
		})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if isUniqueViolation(err) {
			return ErrAlreadyBound
		}
		if err != nil {
			return fmt.Errorf("attackplan: bind action: %w", err)
		}
		updated, err := updatePlan(ctx, q, locked, locked.Objective, PlanStatus(locked.Status), request.Revision)
		if err != nil {
			return err
		}
		finished, err := finishRun(ctx, q, run, request.Revision, updated.Version, fingerprint)
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

func isUniqueViolation(err error) bool {
	var pqError *pq.Error
	return errors.As(err, &pqError) && pqError.Code == "23505"
}
