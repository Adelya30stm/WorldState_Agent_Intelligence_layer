package attackplan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"pentagi/pkg/database"
	"pentagi/pkg/worldstate"

	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
)

func TestStoreConcurrencyRollbackReplayAndBinding(t *testing.T) {
	db := attackPlanPostgres(t)
	goose.SetBaseFS(os.DirFS("../../migrations"))
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	flowID, otherFlowID, taskID, subtasks := attackPlanFixture(t, db)
	store := NewStore(database.New(db))

	create := CreatePlanRequest{
		FlowID: flowID, ObjectiveKey: "task-objective", Objective: "Test objective", Status: PlanStatusActive,
		Revision: Revision{IdempotencyKey: "create", Planner: "test"}, Nodes: testGraphNodes("initial"), Edges: testGraphEdges(),
	}
	created, err := store.Create(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.Create(ctx, create)
	if err != nil || !replayed.Replayed || replayed.Run.ID != created.Run.ID {
		t.Fatalf("replay = %#v, err = %v", replayed, err)
	}
	conflicting := create
	conflicting.Objective = "different request"
	if _, err := store.Create(ctx, conflicting); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	if run, err := store.GetRun(ctx, flowID, created.Plan.ID, "create"); err != nil || run.ResultingVersion == nil || *run.ResultingVersion != 1 {
		t.Fatalf("create run = %#v, err = %v", run, err)
	}
	if _, err := store.Get(ctx, otherFlowID, created.Plan.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-flow get error = %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for index := range 2 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			nodes := testGraphNodes(fmt.Sprintf("writer-%d", index))
			_, err := store.ReplaceGraph(ctx, ReplaceGraphRequest{
				FlowID: flowID, PlanID: created.Plan.ID, Revision: Revision{
					ExpectedVersion: 1, IdempotencyKey: fmt.Sprintf("writer-%d", index), Planner: "test",
				}, Nodes: nodes, Edges: testGraphEdges(),
			})
			errs <- err
		}(index)
	}
	wg.Wait()
	close(errs)
	successes, stale := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrStaleVersion):
			stale++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("concurrent results: success=%d stale=%d", successes, stale)
	}

	transitioned, err := store.TransitionNode(ctx, TransitionNodeRequest{
		FlowID: flowID, PlanID: created.Plan.ID, NodeKey: "action", To: NodeStatusSucceeded,
		Revision: Revision{ExpectedVersion: 2, IdempotencyKey: "terminal", Planner: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionNode(ctx, TransitionNodeRequest{
		FlowID: flowID, PlanID: created.Plan.ID, NodeKey: "action", To: NodeStatusReady,
		Revision: Revision{ExpectedVersion: 3, IdempotencyKey: "reopen", Planner: "test"},
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal reopen error = %v", err)
	}

	rollbackNodes := append([]Node(nil), transitioned.Plan.Nodes...)
	rollbackNodes[0].Title = "must roll back"
	if _, err := store.ReplaceGraph(ctx, ReplaceGraphRequest{
		FlowID: flowID, PlanID: created.Plan.ID, Revision: Revision{
			ExpectedVersion: 3, WorldStateRevisionFrom: 999999999, WorldStateRevision: 999999999,
			IdempotencyKey: "rollback", Planner: "test",
		}, Nodes: rollbackNodes, Edges: transitioned.Plan.Edges,
	}); err == nil {
		t.Fatal("expected evidence constraint rollback")
	}
	afterRollback, err := store.Get(ctx, flowID, created.Plan.ID)
	if err != nil || afterRollback.Version != 3 || afterRollback.Nodes[0].Title == "must roll back" {
		t.Fatalf("rollback plan = %#v, err = %v", afterRollback, err)
	}

	errs = make(chan error, 2)
	for index, subtaskID := range subtasks {
		wg.Add(1)
		go func(index int, subtaskID int64) {
			defer wg.Done()
			_, err := store.BindAction(ctx, BindActionRequest{
				FlowID: flowID, PlanID: created.Plan.ID, NodeKey: "action", TaskID: taskID, SubtaskID: subtaskID,
				Revision: Revision{ExpectedVersion: 3, IdempotencyKey: fmt.Sprintf("bind-%d", index), Planner: "test"},
			})
			errs <- err
		}(index, subtaskID)
	}
	wg.Wait()
	close(errs)
	successes, stale = 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrStaleVersion) || errors.Is(err, ErrAlreadyBound) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("binding race: success=%d rejected=%d", successes, stale)
	}
	bound, err := store.Get(ctx, flowID, created.Plan.ID)
	if err != nil || len(bound.Bindings) != 1 || bound.Bindings[0].NodeKey != "action" || bound.Bindings[0].SubtaskID == nil {
		t.Fatalf("bound plan = %#v, err = %v", bound, err)
	}
	if _, err := store.BindAction(ctx, BindActionRequest{
		FlowID: flowID, PlanID: created.Plan.ID, NodeKey: "other-action", TaskID: taskID, SubtaskID: *bound.Bindings[0].SubtaskID,
		Revision: Revision{ExpectedVersion: 4, IdempotencyKey: "bind-same-subtask", Planner: "test"},
	}); !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("ambiguous subtask binding error = %v", err)
	}
	afterRejectedBinding, err := store.Get(ctx, flowID, created.Plan.ID)
	if err != nil || afterRejectedBinding.Version != 4 || len(afterRejectedBinding.Bindings) != 1 {
		t.Fatalf("plan after rejected binding = %#v, err = %v", afterRejectedBinding, err)
	}

	evidencePlan, err := store.Create(ctx, CreatePlanRequest{
		FlowID: flowID, ObjectiveKey: "evidence-boundaries", Objective: "Evidence boundaries", Status: PlanStatusActive,
		Revision: Revision{IdempotencyKey: "evidence-create", Planner: "test"}, Nodes: testGraphNodes("evidence"), Edges: testGraphEdges(),
	})
	if err != nil {
		t.Fatal(err)
	}
	queries := database.New(db)
	localFirst := createPlannerEvent(t, ctx, queries, flowID)
	foreignBoundary := createPlannerEvent(t, ctx, queries, otherFlowID)
	localHead := createPlannerEvent(t, ctx, queries, flowID)
	limits := worldstate.DefaultPlannerEvidenceLimits()
	limits.MaxEvents = 1
	builder, err := worldstate.NewPlannerEvidenceBuilder(queries, limits)
	if err != nil {
		t.Fatal(err)
	}
	interleaved, err := builder.Build(ctx, flowID)
	if err != nil {
		t.Fatal(err)
	}
	if interleaved.Journal.AfterRevision != foreignBoundary || interleaved.CapturedHead != localHead {
		t.Fatalf("interleaved evidence coverage = %+v", interleaved.Journal)
	}
	updated, err := store.ReplaceGraph(ctx, ReplaceGraphRequest{
		FlowID: flowID, PlanID: evidencePlan.Plan.ID, Revision: Revision{ExpectedVersion: 1,
			WorldStateRevisionFrom: interleaved.Journal.AfterRevision, WorldStateRevision: interleaved.CapturedHead,
			IdempotencyKey: "interleaved-boundary", Planner: "test"}, Nodes: testGraphNodes("interleaved"), Edges: testGraphEdges(),
	})
	if err != nil {
		t.Fatalf("persist interleaved evidence boundary: %v", err)
	}

	gapTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	gap := createPlannerEvent(t, ctx, database.New(gapTx), otherFlowID)
	if err := gapTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	gappedHead := createPlannerEvent(t, ctx, queries, flowID)
	gapped, err := builder.Build(ctx, flowID)
	if err != nil {
		t.Fatal(err)
	}
	if gapped.Journal.AfterRevision != gap || gapped.CapturedHead != gappedHead {
		t.Fatalf("gapped evidence coverage = %+v", gapped.Journal)
	}
	if _, err := store.ReplaceGraph(ctx, ReplaceGraphRequest{
		FlowID: flowID, PlanID: evidencePlan.Plan.ID, Revision: Revision{ExpectedVersion: updated.Plan.Version,
			WorldStateRevisionFrom: gapped.Journal.AfterRevision, WorldStateRevision: gapped.CapturedHead,
			IdempotencyKey: "gapped-boundary", Planner: "test"}, Nodes: testGraphNodes("gapped"), Edges: testGraphEdges(),
	}); err != nil {
		t.Fatalf("persist gapped evidence boundary: %v", err)
	}
	if _, err := store.ReplaceGraph(ctx, ReplaceGraphRequest{
		FlowID: flowID, PlanID: evidencePlan.Plan.ID, Revision: Revision{ExpectedVersion: 3,
			WorldStateRevisionFrom: localFirst, WorldStateRevision: foreignBoundary,
			IdempotencyKey: "foreign-head", Planner: "test"}, Nodes: testGraphNodes("foreign"), Edges: testGraphEdges(),
	}); err == nil {
		t.Fatal("foreign-flow evidence head was accepted")
	}
	if _, err := store.ReplaceGraph(ctx, ReplaceGraphRequest{
		FlowID: flowID, PlanID: evidencePlan.Plan.ID, Revision: Revision{ExpectedVersion: 3,
			WorldStateRevisionFrom: gappedHead, WorldStateRevision: localHead,
			IdempotencyKey: "reversed-boundary", Planner: "test"}, Nodes: testGraphNodes("reversed"), Edges: testGraphEdges(),
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reversed evidence range error = %v", err)
	}
}

func TestStoreReadSnapshotSurvivesConcurrentGraphCommit(t *testing.T) {
	db := attackPlanPostgres(t)
	goose.SetBaseFS(os.DirFS("../../migrations"))
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	flowID, _, _, _ := attackPlanFixture(t, db)
	store := NewStore(database.New(db))
	created, err := store.Create(ctx, CreatePlanRequest{
		FlowID: flowID, ObjectiveKey: "snapshot", Objective: "Snapshot", Status: PlanStatusActive,
		Revision: Revision{IdempotencyKey: "create", Planner: "test"}, Nodes: testGraphNodes("before"), Edges: testGraphEdges(),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, closeSnapshot, err := store.beginSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSnapshot()
	row, err := reader.GetAttackPlan(ctx, database.GetAttackPlanParams{ID: created.Plan.ID, FlowID: flowID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceGraph(ctx, ReplaceGraphRequest{
		FlowID: flowID, PlanID: created.Plan.ID, Revision: Revision{ExpectedVersion: 1, IdempotencyKey: "concurrent", Planner: "test"},
		Nodes: testGraphNodes("after"), Edges: testGraphEdges(),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := loadPlan(ctx, reader, row)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != 1 || snapshot.Nodes[0].Description != "before" {
		t.Fatalf("mixed PostgreSQL snapshot: %+v", snapshot)
	}
	current, err := store.GetActive(ctx, flowID, "snapshot")
	if err != nil || current.Version != 2 || current.Nodes[0].Description != "after" {
		t.Fatalf("current aggregate = %+v, err = %v", current, err)
	}
}

func TestStoreDelayedReplayReturnsRecordedVersion(t *testing.T) {
	db := attackPlanPostgres(t)
	goose.SetBaseFS(os.DirFS("../../migrations"))
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	flowID, _, _, _ := attackPlanFixture(t, db)
	store := NewStore(database.New(db))
	created, err := store.Create(ctx, CreatePlanRequest{
		FlowID: flowID, ObjectiveKey: "delayed-replay", Objective: "Delayed replay", Status: PlanStatusActive,
		Revision: Revision{IdempotencyKey: "create", Planner: "test"}, Nodes: testGraphNodes("created"), Edges: testGraphEdges(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ReplaceGraphRequest{
		FlowID: flowID, PlanID: created.Plan.ID,
		Revision: Revision{ExpectedVersion: 1, IdempotencyKey: "original", Planner: "test"},
		Nodes:    testGraphNodes("original result"), Edges: testGraphEdges(),
	}
	original, err := store.ReplaceGraph(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceGraph(ctx, ReplaceGraphRequest{
		FlowID: flowID, PlanID: created.Plan.ID,
		Revision: Revision{ExpectedVersion: 2, IdempotencyKey: "later", Planner: "test"},
		Nodes:    testGraphNodes("newer result"), Edges: testGraphEdges(),
	}); err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(ctx, flowID, created.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	queries := database.New(db)
	for _, node := range current.Nodes[:2] {
		if _, err := queries.CreateAttackPlanEvidence(ctx, database.CreateAttackPlanEvidenceParams{
			PlanID: created.Plan.ID, FlowID: flowID,
			NodeID: sql.NullInt64{Int64: node.ID, Valid: true}, RunID: sql.NullInt64{Int64: original.Run.ID, Valid: true},
			Provenance: []byte(`{}`),
		}); err != nil {
			t.Fatalf("node-scoped evidence: %v", err)
		}
	}

	const replays = 4
	results := make(chan MutationResult, replays)
	errs := make(chan error, replays)
	var wg sync.WaitGroup
	for range replays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.ReplaceGraph(ctx, request)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("delayed replay: %v", err)
		}
	}
	for replayed := range results {
		if !replayed.Replayed || replayed.Run.ID != original.Run.ID || replayed.Run.ResultingVersion == nil ||
			replayed.Plan.ID != created.Plan.ID || replayed.Plan.FlowID != flowID || replayed.Plan.Version != *original.Run.ResultingVersion {
			t.Fatalf("delayed replay = %#v", replayed)
		}
		if replayed.Plan.Status != "" || replayed.Plan.Objective != "" || len(replayed.Plan.Nodes) != 0 ||
			len(replayed.Plan.Edges) != 0 || len(replayed.Plan.Bindings) != 0 {
			t.Fatalf("delayed replay exposed mutable aggregate = %#v", replayed.Plan)
		}
	}
	if current.Version != 3 || current.Nodes[0].Description != "newer result" {
		t.Fatalf("current aggregate = %#v", current)
	}
	var repositoryEvidence int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM attack_plan_evidence WHERE run_id = $1 AND node_id IS NULL`, original.Run.ID).Scan(&repositoryEvidence); err != nil || repositoryEvidence != 1 {
		t.Fatalf("repository evidence rows = %d, err = %v", repositoryEvidence, err)
	}
}

func TestAttackPlanRunEvidenceUniqueness(t *testing.T) {
	db := attackPlanPostgres(t)
	goose.SetBaseFS(os.DirFS("../../migrations"))
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	flowID, _, _, _ := attackPlanFixture(t, db)
	planID, goalID, actionID := attackPlanBindingFixture(t, db, flowID, "evidence-unique", "draft")
	queries := database.New(db)
	run, err := queries.CreateAttackPlanRun(ctx, database.CreateAttackPlanRunParams{
		PlanID: planID, FlowID: flowID, RequestedVersion: 1, IdempotencyKey: "run", Planner: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	params := database.CreateAttackPlanEvidenceParams{
		PlanID: planID, FlowID: flowID, RunID: sql.NullInt64{Int64: run.ID, Valid: true}, Provenance: []byte(`{}`),
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := queries.CreateAttackPlanEvidence(ctx, params)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	succeeded, duplicates := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case hasPQCode(err, "23505"):
			duplicates++
		default:
			t.Fatalf("concurrent evidence error = %v", err)
		}
	}
	if succeeded != 1 || duplicates != 1 {
		t.Fatalf("concurrent evidence: success=%d duplicate=%d", succeeded, duplicates)
	}
	if _, err := queries.CreateAttackPlanEvidence(ctx, params); !hasPQCode(err, "23505") {
		t.Fatalf("retried evidence error = %v", err)
	}
	for _, nodeID := range []int64{goalID, actionID} {
		nodeParams := params
		nodeParams.NodeID = sql.NullInt64{Int64: nodeID, Valid: true}
		if _, err := queries.CreateAttackPlanEvidence(ctx, nodeParams); err != nil {
			t.Fatalf("node-scoped evidence for node %d: %v", nodeID, err)
		}
	}
	otherPlanID, _, _ := attackPlanBindingFixture(t, db, flowID, "evidence-history", "draft")
	otherRun, err := queries.CreateAttackPlanRun(ctx, database.CreateAttackPlanRunParams{
		PlanID: otherPlanID, FlowID: flowID, RequestedVersion: 1, IdempotencyKey: "run", Planner: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	params.PlanID = otherPlanID
	params.RunID.Int64 = otherRun.ID
	if _, err := queries.CreateAttackPlanEvidence(ctx, params); err != nil {
		t.Fatalf("cross-plan historical evidence: %v", err)
	}
}

func TestAttackPlanBindingDatabaseInvariants(t *testing.T) {
	db := attackPlanPostgres(t)
	goose.SetBaseFS(os.DirFS("../../migrations"))
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	flowID, _, taskID, subtasks := attackPlanFixture(t, db)
	queries := database.New(db)

	historicalPlanID, historicalGoalID, historicalActionID := attackPlanBindingFixture(t, db, flowID, "historical", "active")
	params := attackPlanBindingParams(historicalPlanID, flowID, historicalGoalID, taskID, subtasks[0])
	if _, err := queries.CreateAttackPlanBinding(ctx, params); !hasPQCode(err, "P0001") {
		t.Fatalf("goal binding error = %v", err)
	}
	params.NodeID.Int64 = historicalActionID
	if _, err := queries.CreateAttackPlanBinding(ctx, params); err != nil {
		t.Fatalf("valid action binding: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE attack_plan_nodes SET kind = 'goal' WHERE id = $1`, historicalActionID); !hasPQCode(err, "P0001") {
		t.Fatalf("bound action kind update error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE attack_plans SET status = 'completed' WHERE id = $1`, historicalPlanID); err != nil {
		t.Fatal(err)
	}

	currentPlanID, _, currentActionID := attackPlanBindingFixture(t, db, flowID, "current", "active")
	if _, err := queries.CreateAttackPlanBinding(ctx, attackPlanBindingParams(currentPlanID, flowID, currentActionID, taskID, subtasks[0])); err != nil {
		t.Fatalf("cross-plan historical binding: %v", err)
	}
	var historicalRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM attack_plan_bindings WHERE task_id = $1 AND subtask_id = $2`, taskID, subtasks[0]).Scan(&historicalRows); err != nil || historicalRows != 2 {
		t.Fatalf("historical binding rows = %d, err = %v", historicalRows, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE attack_plans SET status = 'completed' WHERE id = $1`, currentPlanID); err != nil {
		t.Fatal(err)
	}

	idempotentPlanID, _, _ := attackPlanBindingFixture(t, db, flowID, "idempotent", "active")
	store := NewStore(database.New(db))
	request := BindActionRequest{
		FlowID: flowID, PlanID: idempotentPlanID, NodeKey: "action", TaskID: taskID, SubtaskID: subtasks[1],
		Revision: Revision{ExpectedVersion: 1, IdempotencyKey: "binding-replay", Planner: "test"},
	}
	bound, err := store.BindAction(ctx, request)
	if err != nil {
		t.Fatalf("bind action: %v", err)
	}
	replayed, err := store.BindAction(ctx, request)
	if err != nil || !replayed.Replayed || replayed.Run.ID != bound.Run.ID || len(replayed.Plan.Bindings) != 1 {
		t.Fatalf("binding replay = %#v, err = %v", replayed, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE attack_plans SET status = 'completed' WHERE id = $1`, idempotentPlanID); err != nil {
		t.Fatal(err)
	}

	missingPlanID, _, _ := attackPlanBindingFixture(t, db, flowID, "guarded-insert-miss", "active")
	if _, err := db.ExecContext(ctx, `
CREATE FUNCTION force_attack_plan_action_kind_change()
RETURNS TRIGGER AS $$
BEGIN
  UPDATE attack_plan_nodes SET kind = 'goal' WHERE plan_id = NEW.plan_id AND node_key = 'action';
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER force_attack_plan_action_kind_change
  AFTER INSERT ON attack_plan_runs
  FOR EACH ROW EXECUTE FUNCTION force_attack_plan_action_kind_change();`); err != nil {
		t.Fatal(err)
	}
	_, err = store.BindAction(ctx, BindActionRequest{
		FlowID: flowID, PlanID: missingPlanID, NodeKey: "action", TaskID: taskID, SubtaskID: subtasks[1],
		Revision: Revision{ExpectedVersion: 1, IdempotencyKey: "guarded-insert-miss", Planner: "test"},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("guarded insert miss error = %v", err)
	}
	var nodeKind string
	if err := db.QueryRowContext(ctx, `SELECT kind::text FROM attack_plan_nodes WHERE plan_id = $1 AND node_key = 'action'`, missingPlanID).Scan(&nodeKind); err != nil || nodeKind != "action" {
		t.Fatalf("rolled-back action kind = %q, err = %v", nodeKind, err)
	}
	if _, err := db.ExecContext(ctx, `
DROP TRIGGER force_attack_plan_action_kind_change ON attack_plan_runs;
DROP FUNCTION force_attack_plan_action_kind_change();`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE attack_plans SET status = 'completed' WHERE id = $1`, missingPlanID); err != nil {
		t.Fatal(err)
	}

	firstPlanID, _, firstActionID := attackPlanBindingFixture(t, db, flowID, "race-first", "active")
	secondPlanID, _, secondActionID := attackPlanBindingFixture(t, db, flowID, "race-second", "active")
	firstTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.New(firstTx).CreateAttackPlanBinding(ctx, attackPlanBindingParams(firstPlanID, flowID, firstActionID, taskID, subtasks[0])); err != nil {
		_ = firstTx.Rollback()
		t.Fatalf("first concurrent binding: %v", err)
	}
	secondTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = firstTx.Rollback()
		t.Fatal(err)
	}
	secondResult := make(chan error, 1)
	secondStarted := make(chan struct{})
	secondCtx, cancelSecond := context.WithTimeout(ctx, 5*time.Second)
	defer cancelSecond()
	go func() {
		close(secondStarted)
		_, err := database.New(secondTx).CreateAttackPlanBinding(secondCtx, attackPlanBindingParams(secondPlanID, flowID, secondActionID, taskID, subtasks[0]))
		if err != nil {
			_ = secondTx.Rollback()
			secondResult <- err
			return
		}
		secondResult <- secondTx.Commit()
	}()
	<-secondStarted
	select {
	case err := <-secondResult:
		_ = firstTx.Rollback()
		t.Fatalf("conflicting writer completed before serialization lock released: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := firstTx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-secondResult:
		if !hasPQCode(err, "P0001") {
			t.Fatalf("conflicting writer error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("conflicting writer did not finish after lock release")
	}
	var committedRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM attack_plan_bindings WHERE plan_id IN ($1, $2)`, firstPlanID, secondPlanID).Scan(&committedRows); err != nil || committedRows != 1 {
		t.Fatalf("concurrent committed rows = %d, err = %v", committedRows, err)
	}
}

func TestAttackPlanMigrationDownUpSymmetry(t *testing.T) {
	db := attackPlanPostgres(t)
	goose.SetBaseFS(os.DirFS("../../migrations"))
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, "sql", 20260817); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	assertAttackPlanEvidenceRunIndex(t, db, true)
	if err := goose.DownTo(db, "sql", 20260815); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	var table sql.NullString
	if err := db.QueryRow(`SELECT to_regclass('attack_plan_bindings')`).Scan(&table); err != nil || table.Valid {
		t.Fatalf("binding table after down = %#v, err = %v", table, err)
	}
	assertAttackPlanEvidenceRunIndex(t, db, false)
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	if err := db.QueryRow(`SELECT to_regclass('attack_plan_bindings')`).Scan(&table); err != nil || !table.Valid {
		t.Fatalf("binding table after second up = %#v, err = %v", table, err)
	}
	assertAttackPlanEvidenceRunIndex(t, db, true)
}

func assertAttackPlanEvidenceRunIndex(t *testing.T, db *sql.DB, want bool) {
	t.Helper()
	var index sql.NullString
	if err := db.QueryRow(`SELECT to_regclass('attack_plan_evidence_run_unique')`).Scan(&index); err != nil || index.Valid != want {
		t.Fatalf("run evidence index exists = %t, want %t, err = %v", index.Valid, want, err)
	}
}

func attackPlanBindingFixture(t *testing.T, db *sql.DB, flowID int64, suffix, status string) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	var planID, goalID, actionID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO attack_plans(flow_id,objective_key,objective,status,planner) VALUES($1,$2,'test',$3,'test') RETURNING id`, flowID, suffix, status).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO attack_plan_nodes(plan_id,flow_id,node_key,kind,title) VALUES($1,$2,'goal','goal','Goal') RETURNING id`, planID, flowID).Scan(&goalID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO attack_plan_nodes(plan_id,flow_id,node_key,kind,title) VALUES($1,$2,'action','action','Action') RETURNING id`, planID, flowID).Scan(&actionID); err != nil {
		t.Fatal(err)
	}
	return planID, goalID, actionID
}

func attackPlanBindingParams(planID, flowID, nodeID, taskID, subtaskID int64) database.CreateAttackPlanBindingParams {
	return database.CreateAttackPlanBindingParams{
		PlanID: planID, FlowID: flowID,
		NodeID:    sql.NullInt64{Int64: nodeID, Valid: true},
		TaskID:    sql.NullInt64{Int64: taskID, Valid: true},
		SubtaskID: sql.NullInt64{Int64: subtaskID, Valid: true},
	}
}

func createPlannerEvent(t *testing.T, ctx context.Context, q *database.Queries, flowID int64) int64 {
	t.Helper()
	event, err := q.CreateWorldStateEvent(ctx, database.CreateWorldStateEventParams{
		FlowID: flowID, Kind: database.WorldStateEventKindEntityUpserted, Facts: []byte(`{"entity_id":1,"entity_key":"host:test","entity_type":"host","state":"discovered"}`),
		Actor: "test", Provenance: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return event.Revision
}

func hasPQCode(err error, code pq.ErrorCode) bool {
	var pqError *pq.Error
	return errors.As(err, &pqError) && pqError.Code == code
}

func testGraphNodes(description string) []Node {
	return []Node{
		{Key: "root", Kind: NodeKindGoal, Status: NodeStatusReady, Title: "Root", Description: description},
		{Key: "action", Kind: NodeKindAction, Status: NodeStatusPending, Title: "Action"},
		{Key: "other-action", Kind: NodeKindAction, Status: NodeStatusPending, Title: "Other action"},
	}
}

func testGraphEdges() []Edge {
	return []Edge{
		{FromKey: "root", ToKey: "action", Kind: EdgeKindAnd},
		{FromKey: "root", ToKey: "other-action", Kind: EdgeKindAnd},
	}
}

func attackPlanFixture(t *testing.T, db *sql.DB) (int64, int64, int64, []int64) {
	t.Helper()
	ctx := context.Background()
	var userID, flowID, otherFlowID, taskID int64
	mail := fmt.Sprintf("attackplan-%d@example.invalid", time.Now().UnixNano())
	if err := db.QueryRowContext(ctx, `INSERT INTO users(mail,name) VALUES($1,'test') RETURNING id`, mail).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	for _, target := range []*int64{&flowID, &otherFlowID} {
		if err := db.QueryRowContext(ctx, `INSERT INTO flows(model,model_provider_name,model_provider_type,language,tool_call_id_template,user_id) VALUES('m','p','openai','en','',$1) RETURNING id`, userID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO tasks(input,flow_id) VALUES('test',$1) RETURNING id`, flowID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	subtasks := make([]int64, 2)
	for index := range subtasks {
		if err := db.QueryRowContext(ctx, `INSERT INTO subtasks(title,description,task_id) VALUES($1,'test',$2) RETURNING id`, fmt.Sprintf("subtask-%d", index), taskID).Scan(&subtasks[index]); err != nil {
			t.Fatal(err)
		}
	}
	return flowID, otherFlowID, taskID, subtasks
}

func attackPlanPostgres(t *testing.T) *sql.DB {
	t.Helper()
	raw := os.Getenv("DATABASE_URL")
	base, err := url.Parse(raw)
	if raw == "" || err != nil || base.Scheme == "" || base.Host == "" {
		t.Skip("DATABASE_URL is required for disposable PostgreSQL tests")
	}
	name := fmt.Sprintf("pentagi_attackplan_%x", time.Now().UnixNano())
	adminURL := *base
	adminURL.Path = "/postgres"
	admin, err := sql.Open("postgres", adminURL.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Skipf("cannot create disposable database: %v", err)
	}
	testURL := *base
	testURL.Path = "/" + name
	db, err := sql.Open("postgres", testURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1`, name)
		_, _ = admin.Exec(`DROP DATABASE IF EXISTS "` + name + `"`)
		admin.Close()
	})
	return db
}
