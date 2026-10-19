package attackplan

import (
	"context"
	"sync"
	"testing"

	"pentagi/pkg/database"
)

func TestGetAndGetActiveUseOneAggregateSnapshot(t *testing.T) {
	for _, test := range []struct {
		name string
		get  func(context.Context, *Store) (Plan, error)
	}{
		{name: "get", get: func(ctx context.Context, store *Store) (Plan, error) { return store.Get(ctx, 7, 1) }},
		{name: "get active", get: func(ctx context.Context, store *Store) (Plan, error) { return store.GetActive(ctx, 7, "objective") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := newAggregateSnapshotStub()
			result := make(chan Plan, 1)
			errs := make(chan error, 1)
			go func() {
				plan, err := test.get(context.Background(), NewStore(stub))
				result <- plan
				errs <- err
			}()
			<-stub.planRead
			stub.replace(2, "new graph")
			close(stub.continueRead)
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
			plan := <-result
			if plan.Version != 1 || len(plan.Nodes) != 1 || plan.Nodes[0].Description != "old graph" {
				t.Fatalf("mixed aggregate snapshot: %+v", plan)
			}
		})
	}
}

type aggregateSnapshotStub struct {
	database.Querier
	mu           sync.Mutex
	state        aggregateSnapshotState
	planRead     chan struct{}
	continueRead chan struct{}
}

type aggregateSnapshotState struct {
	plan     database.AttackPlan
	nodes    []database.AttackPlanNode
	edges    []database.AttackPlanEdge
	bindings []database.AttackPlanBinding
}

func newAggregateSnapshotStub() *aggregateSnapshotStub {
	stub := &aggregateSnapshotStub{planRead: make(chan struct{}), continueRead: make(chan struct{})}
	stub.replace(1, "old graph")
	return stub
}

func (s *aggregateSnapshotStub) replace(version int64, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = aggregateSnapshotState{
		plan: database.AttackPlan{ID: 1, FlowID: 7, ObjectiveKey: "objective", Objective: "test", Status: database.AttackPlanStatusActive, Version: version},
		nodes: []database.AttackPlanNode{{ID: 10, PlanID: 1, FlowID: 7, NodeKey: "root", Kind: database.AttackPlanNodeKindGoal,
			Status: database.AttackPlanNodeStatusReady, Title: "Root", Description: description, Version: version}},
	}
}

func (s *aggregateSnapshotStub) beginAttackPlanSnapshot(context.Context) (planSnapshotReader, func() error, error) {
	s.mu.Lock()
	snapshot := s.state
	snapshot.nodes = append([]database.AttackPlanNode(nil), s.state.nodes...)
	s.mu.Unlock()
	return &aggregateSnapshotReader{state: snapshot, planRead: s.planRead, continueRead: s.continueRead}, func() error { return nil }, nil
}

type aggregateSnapshotReader struct {
	state        aggregateSnapshotState
	planRead     chan struct{}
	continueRead chan struct{}
}

func (r *aggregateSnapshotReader) readPlan() database.AttackPlan {
	close(r.planRead)
	<-r.continueRead
	return r.state.plan
}

func (r *aggregateSnapshotReader) GetAttackPlan(context.Context, database.GetAttackPlanParams) (database.AttackPlan, error) {
	return r.readPlan(), nil
}

func (r *aggregateSnapshotReader) GetActiveAttackPlanByObjective(context.Context, database.GetActiveAttackPlanByObjectiveParams) (database.AttackPlan, error) {
	return r.readPlan(), nil
}

func (r *aggregateSnapshotReader) ListAttackPlanNodes(context.Context, database.ListAttackPlanNodesParams) ([]database.AttackPlanNode, error) {
	return r.state.nodes, nil
}

func (r *aggregateSnapshotReader) ListAttackPlanEdges(context.Context, database.ListAttackPlanEdgesParams) ([]database.AttackPlanEdge, error) {
	return r.state.edges, nil
}

func (r *aggregateSnapshotReader) ListAttackPlanBindings(context.Context, database.ListAttackPlanBindingsParams) ([]database.AttackPlanBinding, error) {
	return r.state.bindings, nil
}
