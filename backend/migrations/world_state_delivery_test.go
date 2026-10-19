package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"pentagi/pkg/database"

	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"
)

type deliveryFixture struct {
	flowID, taskID, senderID, recipientID int64
}

var errTaskTargetTerminal = errors.New("task target is terminal")

func TestWorldStateDeliveryPersistence(t *testing.T) {
	db := disposablePostgres(t)
	goose.SetBaseFS(EmbedMigrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, "sql", 20260815); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if err := goose.DownTo(db, "sql", 20260525); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	assertObjectExists(t, db, "flows", true)
	assertObjectExists(t, db, "world_state_events", false)
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}

	ctx := context.Background()
	q := database.New(db)
	first := createDeliveryFixture(t, db, "first")
	second := createDeliveryFixture(t, db, "second")

	t.Run("ordering and flow isolation", func(t *testing.T) {
		testOrderingAndIsolation(t, ctx, q, first, second)
	})
	t.Run("monotonic cursor and exact acknowledgement", func(t *testing.T) {
		testCursorAndAcknowledgement(t, ctx, db, q, first, second)
	})
	t.Run("leases and immutable wait winner", func(t *testing.T) {
		testLeasesAndWinner(t, ctx, db, q, first)
	})
	t.Run("routing and terminalization arbitration", func(t *testing.T) {
		testRoutingAndTerminalization(t, ctx, db, q)
	})
	t.Run("constraints and cascades", func(t *testing.T) {
		testConstraintsAndCascades(t, ctx, db, q)
	})
}

func testOrderingAndIsolation(t *testing.T, ctx context.Context, q *database.Queries, a, b deliveryFixture) {
	const count = 12
	revisions := make(chan int64, count)
	updateIDs := make(chan int64, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		fixture := a
		if i%2 == 1 {
			fixture = b
		}
		wg.Add(1)
		go func(i int, f deliveryFixture) {
			defer wg.Done()
			event, err := q.CreateWorldStateEvent(ctx, database.CreateWorldStateEventParams{
				FlowID: f.flowID, Kind: database.WorldStateEventKindEntityUpserted,
				Facts: json.RawMessage(fmt.Sprintf(`{"sequence":%d}`, i)), Actor: "test",
				ActorMsgchainID: sql.NullInt64{Int64: f.senderID, Valid: true}, Provenance: json.RawMessage(`{}`),
			})
			if err != nil {
				t.Errorf("create event: %v", err)
				return
			}
			update, err := q.CreateAgentTaskUpdate(ctx, taskUpdateParams(f, event.Revision, i))
			if err != nil {
				t.Errorf("create update: %v", err)
				return
			}
			revisions <- event.Revision
			updateIDs <- update.ID
		}(i, fixture)
	}
	wg.Wait()
	close(revisions)
	close(updateIDs)
	assertStrictlyOrdered(t, collectSorted(revisions), count)
	assertStrictlyOrdered(t, collectSorted(updateIDs), count)

	head, err := q.GetWorldStateEventHead(ctx, a.flowID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := q.GetWorldStateEventsByRevision(ctx, database.GetWorldStateEventsByRevisionParams{
		FlowID: a.flowID, AfterRevision: 0, ThroughRevision: head, LimitRows: count,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != count/2 {
		t.Fatalf("flow event leak: got %d, want %d", len(events), count/2)
	}
	for _, event := range events {
		if event.FlowID != a.flowID {
			t.Fatalf("foreign flow event returned: %d", event.FlowID)
		}
	}
}

func testCursorAndAcknowledgement(t *testing.T, ctx context.Context, db *sql.DB, q *database.Queries, a, b deliveryFixture) {
	e1 := createEvent(t, ctx, q, a)
	e2 := createEvent(t, ctx, q, a)
	foreign := createEvent(t, ctx, q, b)
	if _, err := q.AdvanceWorldStateChainCursor(ctx, database.AdvanceWorldStateChainCursorParams{Revision: e2, MsgchainID: a.senderID}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.AdvanceWorldStateChainCursor(ctx, database.AdvanceWorldStateChainCursorParams{Revision: e1, MsgchainID: a.senderID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("backward cursor returned %v", err)
	}
	if _, err := q.AdvanceWorldStateChainCursor(ctx, database.AdvanceWorldStateChainCursorParams{Revision: foreign, MsgchainID: a.senderID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign cursor returned %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE world_state_chain_cursors SET revision=$1 WHERE msgchain_id=$2`, e1, a.senderID); err == nil {
		t.Fatal("direct backward cursor update succeeded")
	}
	var cursorWG sync.WaitGroup
	for _, revision := range []int64{e1, e2} {
		cursorWG.Add(1)
		go func(revision int64) {
			defer cursorWG.Done()
			_, err := q.AdvanceWorldStateChainCursor(ctx, database.AdvanceWorldStateChainCursorParams{Revision: revision, MsgchainID: a.recipientID})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("concurrent cursor: %v", err)
			}
		}(revision)
	}
	cursorWG.Wait()
	cursor, err := q.GetWorldStateChainCursor(ctx, a.recipientID)
	if err != nil || cursor.Revision != e2 {
		t.Fatalf("concurrent cursor revision=%d want=%d err=%v", cursor.Revision, e2, err)
	}

	u1, err := q.CreateAgentTaskUpdate(ctx, taskUpdateParams(a, e1, 101))
	if err != nil {
		t.Fatal(err)
	}
	u2, err := q.CreateAgentTaskUpdate(ctx, taskUpdateParams(a, e2, 102))
	if err != nil {
		t.Fatal(err)
	}
	foreignUpdate, err := q.CreateAgentTaskUpdate(ctx, taskUpdateParams(b, foreign, 103))
	if err != nil {
		t.Fatal(err)
	}
	for _, updateID := range []int64{u1.ID, u2.ID} {
		if _, err := q.BindAgentTaskUpdateRecipient(ctx, database.BindAgentTaskUpdateRecipientParams{
			UpdateID: updateID, FlowID: a.flowID, RecipientMsgchainID: a.recipientID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	head, err := q.GetPendingAgentTaskUpdateHead(ctx, database.GetPendingAgentTaskUpdateHeadParams{
		FlowID: a.flowID, RecipientMsgchainID: a.recipientID,
	})
	if err != nil || head != u2.ID {
		t.Fatalf("inbox head=%d want=%d err=%v", head, u2.ID, err)
	}
	pending, err := q.GetPendingAgentTaskUpdatesForRecipient(ctx, database.GetPendingAgentTaskUpdatesForRecipientParams{
		FlowID: a.flowID, RecipientMsgchainID: a.recipientID, ThroughID: head, LimitRows: 10,
	})
	if err != nil || len(pending) != 2 {
		t.Fatalf("recipient inbox rows=%d err=%v", len(pending), err)
	}
	chainBeforeDelivery, err := q.GetMsgChain(ctx, a.recipientID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := q.AcknowledgeAgentTaskUpdatesExact(ctx, database.AcknowledgeAgentTaskUpdatesExactParams{
		UpdateIds: []int64{u1.ID, foreignUpdate.ID}, RecipientMsgchainID: a.recipientID,
		FlowID: a.flowID, DeliveredAt: chainBeforeDelivery.UpdatedAt,
	})
	if err != nil || len(rows) != 0 {
		t.Fatalf("partial exact acknowledgement: rows=%d err=%v", len(rows), err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	txq := q.WithTx(tx)
	chain, err := txq.UpdateMsgChain(ctx, database.UpdateMsgChainParams{Chain: json.RawMessage(`[]`), ID: a.recipientID})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	rows, err = txq.AcknowledgeAgentTaskUpdatesExact(ctx, database.AcknowledgeAgentTaskUpdatesExactParams{
		UpdateIds: []int64{u1.ID, u2.ID}, RecipientMsgchainID: a.recipientID,
		FlowID: a.flowID, DeliveredAt: chain.UpdatedAt,
	})
	if err != nil || len(rows) != 2 {
		_ = tx.Rollback()
		t.Fatalf("exact acknowledgement: rows=%d err=%v", len(rows), err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RejectAgentTaskUpdate(ctx, database.RejectAgentTaskUpdateParams{
		UpdateID: u1.ID, FlowID: a.flowID, RejectedAt: sql.NullTime{Time: time.Now(), Valid: true},
		RejectionReason: sql.NullString{String: "late", Valid: true},
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("terminal arbitration returned %v", err)
	}
}

func testLeasesAndWinner(t *testing.T, ctx context.Context, db *sql.DB, q *database.Queries, f deliveryFixture) {
	if _, err := q.UpsertAgentChainWait(ctx, database.UpsertAgentChainWaitParams{
		WaitKind: database.AgentChainWaitKindTool, PendingToolCallID: sql.NullString{String: "call-1", Valid: true},
		MsgchainID: f.recipientID, FlowID: f.flowID,
	}); err != nil {
		t.Fatal(err)
	}
	lease := func(owner string) ([]database.AgentChainWait, error) {
		return q.LeaseAgentChainWaits(ctx, database.LeaseAgentChainWaitsParams{
			LeaseOwner:     sql.NullString{String: owner, Valid: true},
			LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true}, LimitRows: 1,
		})
	}
	if rows, err := lease("owner-a"); err != nil || len(rows) != 1 {
		t.Fatalf("first lease: rows=%d err=%v", len(rows), err)
	}
	registrationResults := make(chan error, 2)
	go func() {
		_, err := q.UpsertAgentChainWait(ctx, database.UpsertAgentChainWaitParams{
			WaitKind: database.AgentChainWaitKindTool, PendingToolCallID: sql.NullString{String: "call-1", Valid: true},
			MsgchainID: f.recipientID, FlowID: f.flowID,
		})
		registrationResults <- err
	}()
	go func() {
		_, err := q.UpsertAgentChainWait(ctx, database.UpsertAgentChainWaitParams{
			WaitKind: database.AgentChainWaitKindTool, PendingToolCallID: sql.NullString{String: "call-conflict", Valid: true},
			MsgchainID: f.recipientID, FlowID: f.flowID,
		})
		registrationResults <- err
	}()
	var successes, conflicts int
	for i := 0; i < 2; i++ {
		err := <-registrationResults
		switch {
		case err == nil:
			successes++
		case errors.Is(err, sql.ErrNoRows):
			conflicts++
		default:
			t.Fatalf("duplicate registration: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("registrations successes=%d conflicts=%d", successes, conflicts)
	}
	registered, err := q.GetAgentChainWait(ctx, f.recipientID)
	if err != nil {
		t.Fatal(err)
	}
	if registered.PendingToolCallID.String != "call-1" || registered.LeaseOwner.String != "owner-a" || !registered.LeaseExpiresAt.Valid {
		t.Fatalf("duplicate registration changed exact wait or lease: %+v", registered)
	}
	if rows, err := lease("owner-b"); err != nil || len(rows) != 0 {
		t.Fatalf("active lease reclaimed: rows=%d err=%v", len(rows), err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE agent_chain_waits SET lease_expires_at=NOW()-INTERVAL '1 second' WHERE msgchain_id=$1`, f.recipientID); err != nil {
		t.Fatal(err)
	}
	if rows, err := lease("owner-b"); err != nil || len(rows) != 1 {
		t.Fatalf("expired lease not reclaimed: rows=%d err=%v", len(rows), err)
	}

	params := []database.ResolveAgentChainWaitParams{
		{ResolutionWinner: sql.NullString{String: "user", Valid: true}, ResolvedAt: sql.NullTime{Time: time.Now(), Valid: true}, ResumePending: true, ResumeIntent: json.RawMessage(`{"reason":"user"}`), MsgchainID: f.recipientID},
		{ResolutionWinner: sql.NullString{String: "cancellation", Valid: true}, ResolvedAt: sql.NullTime{Time: time.Now(), Valid: true}, ResumeIntent: json.RawMessage(`{}`), MsgchainID: f.recipientID},
	}
	var wg sync.WaitGroup
	wins := make(chan string, 2)
	for _, p := range params {
		wg.Add(1)
		go func(p database.ResolveAgentChainWaitParams) {
			defer wg.Done()
			wait, err := q.ResolveAgentChainWait(ctx, p)
			if err == nil {
				wins <- wait.ResolutionWinner.String
			} else if !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("resolve wait: %v", err)
			}
		}(p)
	}
	wg.Wait()
	close(wins)
	if len(wins) != 1 {
		t.Fatalf("winner count=%d", len(wins))
	}
	if _, err := db.ExecContext(ctx, `UPDATE agent_chain_waits SET resolution_winner='internal:other' WHERE msgchain_id=$1`, f.recipientID); err == nil {
		t.Fatal("resolved wait winner changed")
	}
}

func testRoutingAndTerminalization(t *testing.T, ctx context.Context, db *sql.DB, q *database.Queries) {
	completionFirst := createDeliveryFixture(t, db, "completion-first")
	completionTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	completionQ := q.WithTx(completionTx)
	if _, err := completionQ.LockAgentTaskTargetLifecycle(ctx, database.LockAgentTaskTargetLifecycleParams{
		TargetTaskID: completionFirst.taskID, FlowID: completionFirst.flowID,
	}); err != nil {
		_ = completionTx.Rollback()
		t.Fatal(err)
	}
	if _, err := completionQ.TrySetAgentTaskTargetTerminal(ctx, database.TrySetAgentTaskTargetTerminalParams{
		Status: database.TaskStatusFinished, TargetTaskID: completionFirst.taskID, FlowID: completionFirst.flowID,
	}); err != nil {
		_ = completionTx.Rollback()
		t.Fatal(err)
	}
	routingStarted := make(chan struct{})
	routingResult := make(chan error, 1)
	go func() { routingResult <- routeTaskUpdate(ctx, db, completionFirst, routingStarted) }()
	<-routingStarted
	select {
	case err := <-routingResult:
		_ = completionTx.Rollback()
		t.Fatalf("routing did not wait for completion lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := completionTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-routingResult; !errors.Is(err, errTaskTargetTerminal) {
		t.Fatalf("completion-first routing was not rejected: %v", err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM agent_task_updates WHERE flow_id=$1`, 0, completionFirst.flowID)

	insertionFirst := createDeliveryFixture(t, db, "insertion-first")
	routeTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	routeQ := q.WithTx(routeTx)
	if _, err := routeQ.LockAgentTaskTargetLifecycle(ctx, database.LockAgentTaskTargetLifecycleParams{
		TargetTaskID: insertionFirst.taskID, FlowID: insertionFirst.flowID,
	}); err != nil {
		_ = routeTx.Rollback()
		t.Fatal(err)
	}
	event := createEvent(t, ctx, routeQ, insertionFirst)
	update, err := routeQ.CreateAgentTaskUpdate(ctx, taskUpdateParams(insertionFirst, event, 300))
	if err != nil {
		_ = routeTx.Rollback()
		t.Fatal(err)
	}
	completionStarted := make(chan struct{})
	completionResult := make(chan error, 1)
	go func() { completionResult <- completeTaskTarget(ctx, db, insertionFirst, completionStarted) }()
	<-completionStarted
	select {
	case err := <-completionResult:
		_ = routeTx.Rollback()
		t.Fatalf("terminalization did not wait for routing lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := routeTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-completionResult; !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("pending accepted update did not guard completion: %v", err)
	}
	if _, err := q.RejectAgentTaskUpdate(ctx, database.RejectAgentTaskUpdateParams{
		UpdateID: update.ID, FlowID: insertionFirst.flowID,
		RejectedAt:      sql.NullTime{Time: time.Now(), Valid: true},
		RejectionReason: sql.NullString{String: "explicit arbitration", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := completeTaskTarget(ctx, db, insertionFirst, nil); err != nil {
		t.Fatalf("terminalization after handled update: %v", err)
	}
}

func routeTaskUpdate(ctx context.Context, db *sql.DB, f deliveryFixture, started chan<- struct{}) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := database.New(tx)
	if started != nil {
		close(started)
	}
	task, err := q.LockAgentTaskTargetLifecycle(ctx, database.LockAgentTaskTargetLifecycleParams{
		TargetTaskID: f.taskID, FlowID: f.flowID,
	})
	if err != nil {
		return err
	}
	if task.Status == database.TaskStatusFinished || task.Status == database.TaskStatusFailed {
		return errTaskTargetTerminal
	}
	event, err := q.CreateWorldStateEvent(ctx, database.CreateWorldStateEventParams{
		FlowID: f.flowID, Kind: database.WorldStateEventKindEntityUpserted, Facts: json.RawMessage(`{"ok":true}`),
		Actor: "test", ActorMsgchainID: sql.NullInt64{Int64: f.senderID, Valid: true}, Provenance: json.RawMessage(`{}`),
	})
	if err != nil {
		return err
	}
	if _, err := q.CreateAgentTaskUpdate(ctx, taskUpdateParams(f, event.Revision, 301)); err != nil {
		return err
	}
	return tx.Commit()
}

func completeTaskTarget(ctx context.Context, db *sql.DB, f deliveryFixture, started chan<- struct{}) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := database.New(tx)
	if started != nil {
		close(started)
	}
	if _, err := q.LockAgentTaskTargetLifecycle(ctx, database.LockAgentTaskTargetLifecycleParams{
		TargetTaskID: f.taskID, FlowID: f.flowID,
	}); err != nil {
		return err
	}
	if _, err := q.TrySetAgentTaskTargetTerminal(ctx, database.TrySetAgentTaskTargetTerminalParams{
		Status: database.TaskStatusFinished, TargetTaskID: f.taskID, FlowID: f.flowID,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func testConstraintsAndCascades(t *testing.T, ctx context.Context, db *sql.DB, q *database.Queries) {
	f := createDeliveryFixture(t, db, "cascade")
	event := createEvent(t, ctx, q, f)
	if _, err := db.ExecContext(ctx, `INSERT INTO agent_task_updates(flow_id,sender_msgchain_id,target_type,instruction) VALUES($1,$2,'task','bad')`, f.flowID, f.senderID); err == nil {
		t.Fatal("ambiguous target accepted")
	}
	update, err := q.CreateAgentTaskUpdate(ctx, taskUpdateParams(f, event, 200))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.BindAgentTaskUpdateRecipient(ctx, database.BindAgentTaskUpdateRecipientParams{UpdateID: update.ID, FlowID: f.flowID, RecipientMsgchainID: f.recipientID}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.AdvanceWorldStateChainCursor(ctx, database.AdvanceWorldStateChainCursorParams{Revision: event, MsgchainID: f.recipientID}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertAgentChainWait(ctx, database.UpsertAgentChainWaitParams{WaitKind: database.AgentChainWaitKindIdle, MsgchainID: f.recipientID, FlowID: f.flowID}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM msgchains WHERE id=$1`, f.recipientID); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM world_state_chain_cursors WHERE msgchain_id=$1`, 0, f.recipientID)
	assertCount(t, db, `SELECT COUNT(*) FROM agent_chain_waits WHERE msgchain_id=$1`, 0, f.recipientID)
	assertCount(t, db, `SELECT COUNT(*) FROM agent_task_updates WHERE id=$1 AND recipient_msgchain_id IS NULL`, 1, update.ID)
	if _, err := db.ExecContext(ctx, `DELETE FROM flows WHERE id=$1`, f.flowID); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM world_state_events WHERE flow_id=$1`, 0, f.flowID)
	assertCount(t, db, `SELECT COUNT(*) FROM agent_task_updates WHERE flow_id=$1`, 0, f.flowID)
}

func disposablePostgres(t *testing.T) *sql.DB {
	t.Helper()
	raw := os.Getenv("DATABASE_URL")
	base, err := url.Parse(raw)
	if raw == "" || err != nil || base.Scheme == "" || base.Host == "" {
		host := os.Getenv("POSTGRES_TEST_HOST")
		if host == "" {
			host = "127.0.0.1:5432"
		}
		user := os.Getenv("REDSCOPE_POSTGRES_USER")
		password := os.Getenv("REDSCOPE_POSTGRES_PASSWORD")
		databaseName := os.Getenv("REDSCOPE_POSTGRES_DB")
		if user == "" || databaseName == "" {
			t.Skip("DATABASE_URL or REDSCOPE_POSTGRES_* is required for disposable PostgreSQL tests")
		}
		base = &url.URL{Scheme: "postgres", User: url.UserPassword(user, password), Host: host, Path: "/" + databaseName}
		base.RawQuery = "sslmode=disable"
	}
	name := fmt.Sprintf("pentagi_delivery_%x", time.Now().UnixNano())
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

func createDeliveryFixture(t *testing.T, db *sql.DB, suffix string) deliveryFixture {
	t.Helper()
	ctx := context.Background()
	var userID, flowID, taskID, senderID, recipientID int64
	mail := fmt.Sprintf("delivery-%s-%d@example.invalid", suffix, time.Now().UnixNano())
	mustScan(t, db.QueryRowContext(ctx, `INSERT INTO users(mail,name) VALUES($1,'test') RETURNING id`, mail), &userID)
	mustScan(t, db.QueryRowContext(ctx, `INSERT INTO flows(model,model_provider_name,model_provider_type,language,tool_call_id_template,user_id) VALUES('m','p','openai','en','',$1) RETURNING id`, userID), &flowID)
	mustScan(t, db.QueryRowContext(ctx, `INSERT INTO tasks(input,flow_id) VALUES('test',$1) RETURNING id`, flowID), &taskID)
	mustScan(t, db.QueryRowContext(ctx, `INSERT INTO msgchains(model,model_provider,chain,flow_id,task_id) VALUES('m','p','[]',$1,$2) RETURNING id`, flowID, taskID), &senderID)
	mustScan(t, db.QueryRowContext(ctx, `INSERT INTO msgchains(type,model,model_provider,chain,flow_id,task_id) VALUES('coder','m','p','[]',$1,$2) RETURNING id`, flowID, taskID), &recipientID)
	return deliveryFixture{flowID: flowID, taskID: taskID, senderID: senderID, recipientID: recipientID}
}

func createEvent(t *testing.T, ctx context.Context, q *database.Queries, f deliveryFixture) int64 {
	t.Helper()
	e, err := q.CreateWorldStateEvent(ctx, database.CreateWorldStateEventParams{
		FlowID: f.flowID, Kind: database.WorldStateEventKindEntityUpserted, Facts: json.RawMessage(`{"ok":true}`),
		Actor: "test", ActorMsgchainID: sql.NullInt64{Int64: f.senderID, Valid: true}, Provenance: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e.Revision
}

func taskUpdateParams(f deliveryFixture, revision int64, sequence int) database.CreateAgentTaskUpdateParams {
	return database.CreateAgentTaskUpdateParams{
		FlowID: f.flowID, SenderMsgchainID: f.senderID, TargetType: database.AgentTaskTargetTypeTask,
		TargetTaskID: sql.NullInt64{Int64: f.taskID, Valid: true}, Instruction: fmt.Sprintf("update %d", sequence),
		SelectedFacts: json.RawMessage(`[]`), SourceRevisions: []int64{revision},
	}
}

func assertObjectExists(t *testing.T, db *sql.DB, name string, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil || exists != want {
		t.Fatalf("object %s exists=%v want=%v err=%v", name, exists, want, err)
	}
}

func assertCount(t *testing.T, db *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRow(query, args...).Scan(&got); err != nil || got != want {
		t.Fatalf("count=%d want=%d err=%v", got, want, err)
	}
}

func mustScan(t *testing.T, row *sql.Row, dest ...any) {
	t.Helper()
	if err := row.Scan(dest...); err != nil {
		t.Fatal(err)
	}
}

func collectSorted(values <-chan int64) []int64 {
	var result []int64
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func assertStrictlyOrdered(t *testing.T, values []int64, want int) {
	t.Helper()
	if len(values) != want {
		t.Fatalf("got %d ordered IDs, want %d", len(values), want)
	}
	for i := 1; i < len(values); i++ {
		if values[i] <= values[i-1] {
			t.Fatalf("IDs not strictly ordered: %v", values)
		}
	}
}
