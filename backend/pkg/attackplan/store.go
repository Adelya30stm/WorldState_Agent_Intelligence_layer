package attackplan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"pentagi/pkg/database"
)

type transactionalQuerier interface {
	database.Querier
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	WithTx(*sql.Tx) *database.Queries
}

type planReader interface {
	ListAttackPlanNodes(context.Context, database.ListAttackPlanNodesParams) ([]database.AttackPlanNode, error)
	ListAttackPlanEdges(context.Context, database.ListAttackPlanEdgesParams) ([]database.AttackPlanEdge, error)
	ListAttackPlanBindings(context.Context, database.ListAttackPlanBindingsParams) ([]database.AttackPlanBinding, error)
}

type planSnapshotReader interface {
	planReader
	GetAttackPlan(context.Context, database.GetAttackPlanParams) (database.AttackPlan, error)
	GetActiveAttackPlanByObjective(context.Context, database.GetActiveAttackPlanByObjectiveParams) (database.AttackPlan, error)
}

type planSnapshotter interface {
	beginAttackPlanSnapshot(context.Context) (planSnapshotReader, func() error, error)
}

func (s *Store) GetRun(ctx context.Context, flowID, planID int64, idempotencyKey string) (Run, error) {
	row, err := s.db.GetAttackPlanRunByIdempotencyKey(ctx, database.GetAttackPlanRunByIdempotencyKeyParams{
		PlanID: planID, FlowID: flowID, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return Run{}, storeError("get run", err)
	}
	return runFromDB(row), nil
}

// Store is the transactional PostgreSQL implementation of Repository.
type Store struct{ db database.Querier }

var _ Repository = (*Store)(nil)

func NewStore(db database.Querier) *Store { return &Store{db: db} }

func (s *Store) Get(ctx context.Context, flowID, planID int64) (Plan, error) {
	return s.readSnapshot(ctx, func(q planSnapshotReader) (Plan, error) {
		row, err := q.GetAttackPlan(ctx, database.GetAttackPlanParams{ID: planID, FlowID: flowID})
		if err != nil {
			return Plan{}, storeError("get plan", err)
		}
		return loadPlan(ctx, q, row)
	})
}

func (s *Store) GetActive(ctx context.Context, flowID int64, objectiveKey string) (Plan, error) {
	return s.readSnapshot(ctx, func(q planSnapshotReader) (Plan, error) {
		row, err := q.GetActiveAttackPlanByObjective(ctx, database.GetActiveAttackPlanByObjectiveParams{
			FlowID: flowID, ObjectiveKey: objectiveKey,
		})
		if err != nil {
			return Plan{}, storeError("get active plan", err)
		}
		return loadPlan(ctx, q, row)
	})
}

func (s *Store) readSnapshot(ctx context.Context, load func(planSnapshotReader) (Plan, error)) (plan Plan, err error) {
	reader, closeSnapshot, err := s.beginSnapshot(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer func() {
		if closeErr := closeSnapshot(); err == nil && closeErr != nil {
			plan = Plan{}
			err = fmt.Errorf("attackplan: close read snapshot: %w", closeErr)
		}
	}()
	return load(reader)
}

func (s *Store) beginSnapshot(ctx context.Context) (planSnapshotReader, func() error, error) {
	if snapshotter, ok := s.db.(planSnapshotter); ok {
		return snapshotter.beginAttackPlanSnapshot(ctx)
	}
	db, ok := s.db.(transactionalQuerier)
	if !ok {
		return nil, nil, errors.New("attackplan: database does not provide a consistent snapshot")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("attackplan: begin read snapshot: %w", err)
	}
	return db.WithTx(tx), tx.Rollback, nil
}

func (s *Store) Create(ctx context.Context, request CreatePlanRequest) (MutationResult, error) {
	if request.FlowID <= 0 || strings.TrimSpace(request.ObjectiveKey) == "" || strings.TrimSpace(request.Objective) == "" ||
		(request.Status != PlanStatusDraft && request.Status != PlanStatusActive) {
		return MutationResult{}, ErrInvalidInput
	}
	if err := validateRevision(request.Revision, false); err != nil {
		return MutationResult{}, err
	}
	if err := validateGraph(request.Nodes, request.Edges); err != nil {
		return MutationResult{}, err
	}
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return MutationResult{}, err
	}

	var result MutationResult
	err = s.withTx(ctx, func(q *database.Queries) error {
		if _, err := q.LockAttackPlanFlow(ctx, request.FlowID); err != nil {
			return storeError("lock flow", err)
		}
		plans, err := q.ListAttackPlans(ctx, request.FlowID)
		if err != nil {
			return fmt.Errorf("attackplan: list plans: %w", err)
		}
		for _, candidate := range plans {
			if candidate.ObjectiveKey != request.ObjectiveKey {
				continue
			}
			if replay, ok, err := replayMutation(ctx, q, candidate, request.Revision, 1, fingerprint); err != nil {
				return err
			} else if ok {
				result = replay
				return nil
			}
			if candidate.Status == database.AttackPlanStatusActive && request.Status == PlanStatusActive {
				return ErrIdempotencyConflict
			}
		}

		created, err := q.CreateAttackPlan(ctx, database.CreateAttackPlanParams{
			FlowID: request.FlowID, ObjectiveKey: request.ObjectiveKey, Objective: request.Objective,
			Status: database.AttackPlanStatus(request.Status), Planner: request.Revision.Planner,
		})
		if err != nil {
			return fmt.Errorf("attackplan: create plan: %w", err)
		}
		run, err := createRun(ctx, q, created, request.Revision, 1)
		if err != nil {
			return err
		}
		if err := createGraph(ctx, q, created, request.Nodes, request.Edges); err != nil {
			return err
		}
		finished, err := finishRun(ctx, q, run, request.Revision, created.Version, fingerprint)
		if err != nil {
			return err
		}
		plan, err := loadPlan(ctx, q, created)
		if err != nil {
			return err
		}
		result = MutationResult{Plan: plan, Run: runFromDB(finished)}
		return nil
	})
	return result, err
}

func (s *Store) withTx(ctx context.Context, fn func(*database.Queries) error) error {
	db, ok := s.db.(transactionalQuerier)
	if !ok {
		return errors.New("attackplan: database does not support transactions")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("attackplan: begin transaction: %w", err)
	}
	if err := fn(db.WithTx(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("attackplan: commit transaction: %w", err)
	}
	return nil
}

func loadPlan(ctx context.Context, q planReader, row database.AttackPlan) (Plan, error) {
	nodes, err := q.ListAttackPlanNodes(ctx, database.ListAttackPlanNodesParams{PlanID: row.ID, FlowID: row.FlowID})
	if err != nil {
		return Plan{}, fmt.Errorf("attackplan: list nodes: %w", err)
	}
	edges, err := q.ListAttackPlanEdges(ctx, database.ListAttackPlanEdgesParams{PlanID: row.ID, FlowID: row.FlowID})
	if err != nil {
		return Plan{}, fmt.Errorf("attackplan: list edges: %w", err)
	}
	bindings, err := q.ListAttackPlanBindings(ctx, database.ListAttackPlanBindingsParams{PlanID: row.ID, FlowID: row.FlowID})
	if err != nil {
		return Plan{}, fmt.Errorf("attackplan: list bindings: %w", err)
	}
	result := planFromDB(row)
	byID := make(map[int64]string, len(nodes))
	for _, node := range nodes {
		result.Nodes = append(result.Nodes, nodeFromDB(node))
		byID[node.ID] = node.NodeKey
	}
	for _, edge := range edges {
		from, fromOK := byID[edge.FromNodeID]
		to, toOK := byID[edge.ToNodeID]
		if !fromOK || !toOK {
			return Plan{}, fmt.Errorf("%w: edge %d has a missing endpoint", ErrInvalidInput, edge.ID)
		}
		result.Edges = append(result.Edges, Edge{ID: edge.ID, FromKey: from, ToKey: to, Kind: EdgeKind(edge.Kind)})
	}
	for _, binding := range bindings {
		item := Binding{ID: binding.ID, CreatedAt: binding.CreatedAt}
		if binding.NodeID.Valid {
			item.NodeKey = byID[binding.NodeID.Int64]
		}
		if binding.TaskID.Valid {
			item.TaskID = &binding.TaskID.Int64
		}
		if binding.SubtaskID.Valid {
			item.SubtaskID = &binding.SubtaskID.Int64
		}
		result.Bindings = append(result.Bindings, item)
	}
	return result, nil
}

func planFromDB(row database.AttackPlan) Plan {
	return Plan{ID: row.ID, FlowID: row.FlowID, ObjectiveKey: row.ObjectiveKey, Objective: row.Objective,
		Status: PlanStatus(row.Status), Version: row.Version, Planner: row.Planner, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func nodeFromDB(row database.AttackPlanNode) Node {
	return Node{ID: row.ID, Key: row.NodeKey, Kind: NodeKind(row.Kind), Status: NodeStatus(row.Status), Title: row.Title,
		Description: row.Description, Payload: row.Payload, Version: row.Version}
}

func runFromDB(row database.AttackPlanRun) Run {
	result := Run{ID: row.ID, PlanID: row.PlanID, FlowID: row.FlowID, Status: RunStatus(row.Status),
		RequestedVersion: row.RequestedVersion, WorldStateRevision: row.WorldStateRevision,
		IdempotencyKey: row.IdempotencyKey, Planner: row.Planner, StartedAt: row.StartedAt}
	if row.ResultingVersion.Valid {
		result.ResultingVersion = &row.ResultingVersion.Int64
	}
	if row.FinishedAt.Valid {
		result.FinishedAt = &row.FinishedAt.Time
	}
	return result
}

func storeError(operation string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, operation)
	}
	return fmt.Errorf("attackplan: %s: %w", operation, err)
}
