package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pentagi/pkg/database"
	"pentagi/pkg/worldstate"

	"github.com/pressly/goose/v3"
	"github.com/vxcontrol/langchaingo/llms"
)

type wakeFixture struct {
	flowID, taskID, subtaskID, chainID int64
}

func TestPrimaryWorldStateWake(t *testing.T) {
	db := disposablePostgres(t)
	goose.SetBaseFS(EmbedMigrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := database.New(db)

	t.Run("user and event first races", func(t *testing.T) {
		for i := 0; i < 12; i++ {
			f := createWakeFixture(t, db, fmt.Sprintf("race-%d", i))
			registerWake(t, ctx, q, f, "ask-1")
			head := createWakeEvent(t, ctx, q, f)
			waits := leaseWake(t, ctx, q, "dispatcher", time.Minute)
			if len(waits) != 1 {
				t.Fatalf("leased %d waits", len(waits))
			}
			var wg sync.WaitGroup
			var eventWon atomic.Bool
			wg.Add(2)
			go func() {
				defer wg.Done()
				handled, err := worldstate.ResolvePrimaryWaitWithUser(ctx, q, f.chainID, "human")
				if !handled || err != nil {
					t.Errorf("user resolution: handled=%v err=%v", handled, err)
				}
			}()
			go func() {
				defer wg.Done()
				won, err := worldstate.ResolveLeasedPrimaryWorldStateWait(ctx, q, waits[0], "dispatcher", head)
				if err != nil {
					t.Errorf("event resolution: %v", err)
				}
				eventWon.Store(won)
			}()
			wg.Wait()
			_, err := q.GetAgentChainWait(ctx, f.chainID)
			userWon := errors.Is(err, sql.ErrNoRows)
			if err != nil && !userWon {
				t.Fatal(err)
			}
			if userWon == eventWon.Load() {
				t.Fatalf("winner user=%v event=%v", userWon, eventWon.Load())
			}
			assertWakeResponse(t, ctx, q, f.chainID, userWon)
			if err := q.DeleteAgentChainWait(ctx, f.chainID); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("stale head duplicate lease and reclaim", func(t *testing.T) {
		f := createWakeFixture(t, db, "lease")
		registerWake(t, ctx, q, f, "ask-1")
		if got := leaseWake(t, ctx, q, "a", time.Minute); len(got) != 0 {
			t.Fatalf("stale head leased %d waits", len(got))
		}
		createWakeEvent(t, ctx, q, f)
		type leaseResult struct {
			count int
			err   error
		}
		results := make(chan leaseResult, 2)
		for _, owner := range []string{"a", "b"} {
			go func(owner string) {
				waits, err := q.LeasePrimaryWorldStateWaits(ctx, database.LeasePrimaryWorldStateWaitsParams{
					LeaseOwner:     sql.NullString{String: owner, Valid: true},
					LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(25 * time.Millisecond), Valid: true},
					LimitRows:      32,
				})
				results <- leaseResult{count: len(waits), err: err}
			}(owner)
		}
		first, second := <-results, <-results
		if first.err != nil || second.err != nil {
			t.Fatalf("duplicate lease errors: %v, %v", first.err, second.err)
		}
		if total := first.count + second.count; total != 1 {
			t.Fatalf("duplicate claim count=%d", total)
		}
		time.Sleep(35 * time.Millisecond)
		if got := leaseWake(t, ctx, q, "reclaimer", time.Minute); len(got) != 1 {
			t.Fatalf("reclaimed %d waits", len(got))
		}
		if err := q.DeleteAgentChainWait(ctx, f.chainID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rollback late input and re-ask", func(t *testing.T) {
		f := createWakeFixture(t, db, "rollback")
		registerWake(t, ctx, q, f, "ask-1")
		head := createWakeEvent(t, ctx, q, f)
		wait := leaseWake(t, ctx, q, "dispatcher", time.Minute)[0]
		if _, err := q.UpdateMsgChain(ctx, database.UpdateMsgChainParams{Chain: json.RawMessage(`[]`), ID: f.chainID}); err != nil {
			t.Fatal(err)
		}
		if won, err := worldstate.ResolveLeasedPrimaryWorldStateWait(ctx, q, wait, "dispatcher", head); err == nil || won {
			t.Fatalf("invalid chain resolution won=%v err=%v", won, err)
		}
		if current, err := q.GetAgentChainWait(ctx, f.chainID); err != nil || current.State != database.AgentChainWaitStatePending {
			t.Fatalf("rollback wait=%+v err=%v", current, err)
		}

		registerWake(t, ctx, q, f, "ask-1")
		wait = mustWait(t, q, ctx, f.chainID)
		if _, err := q.ReleaseAgentChainWaitLease(ctx, database.ReleaseAgentChainWaitLeaseParams{
			NextAttemptAt: time.Now(), MsgchainID: f.chainID, LeaseOwner: wait.LeaseOwner,
		}); err != nil {
			t.Fatal(err)
		}
		wait = leaseWake(t, ctx, q, "dispatcher-2", time.Minute)[0]
		if won, err := worldstate.ResolveLeasedPrimaryWorldStateWait(ctx, q, wait, "dispatcher-2", head); err != nil || !won {
			t.Fatalf("event resolution won=%v err=%v", won, err)
		}
		if handled, err := worldstate.ResolvePrimaryWaitWithUser(ctx, q, f.chainID, "late-before-resume"); !handled || err != nil {
			t.Fatalf("late input before resume handled=%v err=%v", handled, err)
		}
		assertLateInput(t, ctx, q, f.chainID, "ask-1", []string{"late-before-resume"})
		current := mustWait(t, q, ctx, f.chainID)
		if !current.ResumePending || current.ResolutionWinner.String != "world_state" || current.ResolutionRef.Int64 != head {
			t.Fatalf("late input changed event winner: %+v", current)
		}
		claimed := leaseResume(t, ctx, q, "resume-dispatcher", time.Minute)
		if len(claimed) != 1 {
			t.Fatalf("claimed %d resumes", len(claimed))
		}
		if _, err := q.AcceptClaimedPrimaryWorldStateResume(ctx, acceptResumeParams(claimed[0])); err != nil {
			t.Fatal(err)
		}
		if handled, err := worldstate.ResolvePrimaryWaitWithUser(ctx, q, f.chainID, "late-after-resume"); !handled || err != nil {
			t.Fatalf("late input after resume handled=%v err=%v", handled, err)
		}
		assertLateInput(t, ctx, q, f.chainID, "ask-1", []string{"late-before-resume", "late-after-resume"})
		chain := getWakeChain(t, ctx, q, f.chainID)
		chain = append(chain, wakeChain("ask-2")...)
		if err := worldstate.RegisterPrimaryAskWait(ctx, q, f.flowID, f.chainID, "ask-2", chain, 0); err != nil {
			t.Fatal(err)
		}
		if got := mustWait(t, q, ctx, f.chainID); got.PendingToolCallID.String != "ask-2" || got.State != database.AgentChainWaitStatePending {
			t.Fatalf("re-ask wait=%+v", got)
		}
		if handled, err := worldstate.ResolvePrimaryWaitWithUser(ctx, q, f.chainID, "re-ask-answer"); !handled || err != nil {
			t.Fatalf("re-ask input handled=%v err=%v", handled, err)
		}
		assertLateInput(t, ctx, q, f.chainID, "ask-1", []string{"late-before-resume", "late-after-resume"})
		if response := toolResponse(t, getWakeChain(t, ctx, q, f.chainID), "ask-2"); response != "re-ask-answer" {
			t.Fatalf("re-ask response=%q", response)
		}
	})

	t.Run("lost hint restart reconciliation and shutdown", func(t *testing.T) {
		f := createWakeFixture(t, db, "restart")
		registerWake(t, ctx, q, f, "ask-1")
		createWakeEvent(t, ctx, q, f)
		failed := make(chan struct{}, 1)
		first := worldstate.NewPrimaryWaitDispatcher(q, func(context.Context, database.AgentChainWait) error {
			select {
			case failed <- struct{}{}:
			default:
			}
			return errors.New("enqueue failed")
		})

		firstCtx, cancelFirst := context.WithCancel(ctx)
		go first.Run(firstCtx)
		select {
		case <-failed:
			cancelFirst()
		case <-time.After(3 * time.Second):
			t.Fatal("lost-hint polling did not resolve wait")
		}
		wait := mustWait(t, q, ctx, f.chainID)
		if !wait.ResumePending {
			t.Fatal("enqueue failure cleared resume intent")
		}
		accepted := make(chan struct{}, 1)
		second := worldstate.NewPrimaryWaitDispatcher(q, func(ctx context.Context, wait database.AgentChainWait) error {
			_, err := q.AcceptClaimedPrimaryWorldStateResume(ctx, acceptResumeParams(wait))
			if err == nil {
				accepted <- struct{}{}
			}
			return err
		})
		secondCtx, cancelSecond := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { second.Run(secondCtx); close(done) }()
		select {
		case <-accepted:
			cancelSecond()
		case <-time.After(3 * time.Second):
			t.Fatal("restart reconciliation did not accept resume")
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("dispatcher did not shut down")
		}
	})

	t.Run("two dispatchers claim one resolved resume", func(t *testing.T) {
		f := createWakeFixture(t, db, "resume-claim")
		registerWake(t, ctx, q, f, "ask-1")
		head := createWakeEvent(t, ctx, q, f)
		wait := leaseWake(t, ctx, q, "resolver", time.Minute)[0]
		if won, err := worldstate.ResolveLeasedPrimaryWorldStateWait(ctx, q, wait, "resolver", head); err != nil || !won {
			t.Fatalf("event resolution won=%v err=%v", won, err)
		}

		callbacks := make(chan database.AgentChainWait, 2)
		newDispatcher := func() (context.CancelFunc, chan struct{}) {
			dispatcherCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			dispatcher := worldstate.NewPrimaryWaitDispatcher(q, func(callCtx context.Context, wait database.AgentChainWait) error {
				callbacks <- wait
				<-callCtx.Done()
				return callCtx.Err()
			})
			go func() { dispatcher.Run(dispatcherCtx); close(done) }()
			return cancel, done
		}
		cancelFirst, firstDone := newDispatcher()
		cancelSecond, secondDone := newDispatcher()
		select {
		case <-callbacks:
		case <-time.After(3 * time.Second):
			t.Fatal("no dispatcher claimed resolved resume")
		}
		select {
		case duplicate := <-callbacks:
			t.Fatalf("second dispatcher claimed same resume: %+v", duplicate)
		case <-time.After(250 * time.Millisecond):
		}
		cancelFirst()
		cancelSecond()
		for _, done := range []chan struct{}{firstDone, secondDone} {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("dispatcher did not shut down")
			}
		}
	})

	t.Run("stale resolved callback cannot accept re-ask generation", func(t *testing.T) {
		f := createWakeFixture(t, db, "stale-resume")
		registerWake(t, ctx, q, f, "ask-1")
		head1 := createWakeEvent(t, ctx, q, f)
		wait := leaseWake(t, ctx, q, "resolver-1", time.Minute)[0]
		if won, err := worldstate.ResolveLeasedPrimaryWorldStateWait(ctx, q, wait, "resolver-1", head1); err != nil || !won {
			t.Fatalf("first event resolution won=%v err=%v", won, err)
		}
		oldClaim := leaseResume(t, ctx, q, "resume-1", time.Minute)[0]
		if handled, err := worldstate.ResolvePrimaryWaitWithUser(ctx, q, f.chainID, "late-human"); !handled || err != nil {
			t.Fatalf("late input handled=%v err=%v", handled, err)
		}
		controllerClaim, err := q.ClaimPrimaryWorldStateResumeForController(ctx, database.ClaimPrimaryWorldStateResumeForControllerParams{
			LeaseOwner:     sql.NullString{String: "controller", Valid: true},
			LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true}, MsgchainID: f.chainID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := q.AcceptClaimedPrimaryWorldStateResume(ctx, acceptResumeParams(controllerClaim)); err != nil {
			t.Fatal(err)
		}
		if _, err := q.AcceptClaimedPrimaryWorldStateResume(ctx, acceptResumeParams(oldClaim)); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("displaced callback acceptance error=%v", err)
		}

		chain := append(getWakeChain(t, ctx, q, f.chainID), wakeChain("ask-2")...)
		if err := worldstate.RegisterPrimaryAskWait(ctx, q, f.flowID, f.chainID, "ask-2", chain, 0); err != nil {
			t.Fatal(err)
		}
		head2 := createWakeEvent(t, ctx, q, f)
		wait = leaseWake(t, ctx, q, "resolver-2", time.Minute)[0]
		if won, err := worldstate.ResolveLeasedPrimaryWorldStateWait(ctx, q, wait, "resolver-2", head2); err != nil || !won {
			t.Fatalf("second event resolution won=%v err=%v", won, err)
		}
		newClaim := leaseResume(t, ctx, q, "resume-2", time.Minute)[0]
		if _, err := q.AcceptClaimedPrimaryWorldStateResume(ctx, acceptResumeParams(oldClaim)); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("stale callback acceptance error=%v", err)
		}
		current := mustWait(t, q, ctx, f.chainID)
		if !current.ResumePending || current.PendingToolCallID.String != "ask-2" || current.LeaseOwner != newClaim.LeaseOwner {
			t.Fatalf("stale callback changed re-ask intent: %+v", current)
		}
		assertLateInput(t, ctx, q, f.chainID, "ask-1", []string{"late-human"})
	})

	t.Run("terminal waits are ignored", func(t *testing.T) {
		f := createWakeFixture(t, db, "terminal")
		registerWake(t, ctx, q, f, "ask-1")
		createWakeEvent(t, ctx, q, f)
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET status='finished' WHERE id=$1`, f.taskID); err != nil {
			t.Fatal(err)
		}
		if got := leaseWake(t, ctx, q, "dispatcher", time.Minute); len(got) != 0 {
			t.Fatalf("terminal wait leased %d rows", len(got))
		}
	})
}

func createWakeFixture(t *testing.T, db *sql.DB, suffix string) wakeFixture {
	t.Helper()
	var f wakeFixture
	var userID int64
	mail := fmt.Sprintf("wake-%s-%d@example.invalid", suffix, time.Now().UnixNano())
	mustScan(t, db.QueryRow(`INSERT INTO users(mail,name) VALUES($1,'test') RETURNING id`, mail), &userID)
	mustScan(t, db.QueryRow(`INSERT INTO flows(status,model,model_provider_name,model_provider_type,language,tool_call_id_template,user_id) VALUES('waiting','m','p','openai','en','',$1) RETURNING id`, userID), &f.flowID)
	mustScan(t, db.QueryRow(`INSERT INTO tasks(status,input,flow_id) VALUES('waiting','test',$1) RETURNING id`, f.flowID), &f.taskID)
	mustScan(t, db.QueryRow(`INSERT INTO subtasks(status,title,description,task_id) VALUES('waiting','test','test',$1) RETURNING id`, f.taskID), &f.subtaskID)
	mustScan(t, db.QueryRow(`INSERT INTO msgchains(type,model,model_provider,chain,flow_id,task_id,subtask_id) VALUES('primary_agent','m','p','[]',$1,$2,$3) RETURNING id`, f.flowID, f.taskID, f.subtaskID), &f.chainID)
	return f
}

func wakeChain(callID string) []llms.MessageContent {
	return []llms.MessageContent{
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{llms.ToolCall{ID: callID, FunctionCall: &llms.FunctionCall{Name: "ask", Arguments: `{}`}}}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{llms.ToolCallResponse{ToolCallID: callID, Name: "ask", Content: "pending"}}},
	}
}

func registerWake(t *testing.T, ctx context.Context, q *database.Queries, f wakeFixture, callID string) {
	t.Helper()
	if err := worldstate.RegisterPrimaryAskWait(ctx, q, f.flowID, f.chainID, callID, wakeChain(callID), 0); err != nil {
		t.Fatal(err)
	}
}

func createWakeEvent(t *testing.T, ctx context.Context, q *database.Queries, f wakeFixture) int64 {
	t.Helper()
	event, err := q.CreateWorldStateEvent(ctx, database.CreateWorldStateEventParams{
		FlowID: f.flowID, Kind: database.WorldStateEventKindEntityUpserted,
		Facts: json.RawMessage(`{"ok":true}`), Actor: "test", Provenance: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return event.Revision
}

func leaseWake(t *testing.T, ctx context.Context, q *database.Queries, owner string, duration time.Duration) []database.AgentChainWait {
	t.Helper()
	waits, err := q.LeasePrimaryWorldStateWaits(ctx, database.LeasePrimaryWorldStateWaitsParams{
		LeaseOwner: sql.NullString{String: owner, Valid: true}, LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(duration), Valid: true}, LimitRows: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	return waits
}

func leaseResume(t *testing.T, ctx context.Context, q *database.Queries, owner string, duration time.Duration) []database.AgentChainWait {
	t.Helper()
	waits, err := q.LeasePrimaryWorldStateResumeWaits(ctx, database.LeasePrimaryWorldStateResumeWaitsParams{
		LeaseOwner: sql.NullString{String: owner, Valid: true}, LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(duration), Valid: true}, LimitRows: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	return waits
}

func acceptResumeParams(wait database.AgentChainWait) database.AcceptClaimedPrimaryWorldStateResumeParams {
	return database.AcceptClaimedPrimaryWorldStateResumeParams{
		MsgchainID: wait.MsgchainID, PendingToolCallID: wait.PendingToolCallID,
		ResolutionRef: wait.ResolutionRef, ResumeIntent: wait.ResumeIntent, LeaseOwner: wait.LeaseOwner,
	}
}

func mustWait(t *testing.T, q *database.Queries, ctx context.Context, chainID int64) database.AgentChainWait {
	t.Helper()
	wait, err := q.GetAgentChainWait(ctx, chainID)
	if err != nil {
		t.Fatal(err)
	}
	return wait
}

func assertWakeResponse(t *testing.T, ctx context.Context, q *database.Queries, chainID int64, userWon bool) {
	t.Helper()
	chainRow, err := q.GetMsgChain(ctx, chainID)
	if err != nil {
		t.Fatal(err)
	}
	var chain []llms.MessageContent
	if err := json.Unmarshal(chainRow.Chain, &chain); err != nil {
		t.Fatal(err)
	}
	response := chain[1].Parts[0].(llms.ToolCallResponse).Content
	if userWon && response != "human" {
		t.Fatalf("user response=%q", response)
	}
	if !userWon && response == "human" {
		t.Fatal("event winner was overwritten by user")
	}
}

func assertLateInput(t *testing.T, ctx context.Context, q *database.Queries, chainID int64, callID string, inputs []string) {
	t.Helper()
	chain := getWakeChain(t, ctx, q, chainID)
	if response := toolResponse(t, chain, callID); response == "" || response == inputs[len(inputs)-1] {
		t.Fatalf("synthetic response was overwritten: %q", response)
	}
	var got []string
	for _, message := range chain {
		if message.Role != llms.ChatMessageTypeHuman {
			continue
		}
		for _, part := range message.Parts {
			if text, ok := part.(llms.TextContent); ok {
				got = append(got, text.Text)
			}
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(inputs) {
		t.Fatalf("human inputs=%v want=%v", got, inputs)
	}
}

func getWakeChain(t *testing.T, ctx context.Context, q *database.Queries, chainID int64) []llms.MessageContent {
	t.Helper()
	chainRow, err := q.GetMsgChain(ctx, chainID)
	if err != nil {
		t.Fatal(err)
	}
	var chain []llms.MessageContent
	if err := json.Unmarshal(chainRow.Chain, &chain); err != nil {
		t.Fatal(err)
	}
	return chain
}

func toolResponse(t *testing.T, chain []llms.MessageContent, callID string) string {
	t.Helper()
	for _, message := range chain {
		for _, part := range message.Parts {
			if response, ok := part.(llms.ToolCallResponse); ok && response.ToolCallID == callID {
				return response.Content
			}
		}
	}
	t.Fatalf("tool response %s not found", callID)
	return ""
}
