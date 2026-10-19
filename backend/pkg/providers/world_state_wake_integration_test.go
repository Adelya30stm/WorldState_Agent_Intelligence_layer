package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"pentagi/migrations"
	"pentagi/pkg/database"
	providerpkg "pentagi/pkg/providers/provider"
	providermock "pentagi/pkg/providers/tester/mock"
	"pentagi/pkg/worldstate"

	_ "github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/vxcontrol/langchaingo/llms"
)

func TestWorldStateEventWinnerLateUserThroughProvider(t *testing.T) {
	db := providerWakePostgres(t)
	goose.SetBaseFS(migrations.EmbedMigrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := database.New(db)

	var userID, flowID, taskID, subtaskID, chainID int64
	mail := fmt.Sprintf("provider-wake-%d@example.invalid", time.Now().UnixNano())
	mustProviderWakeScan(t, db.QueryRowContext(ctx, `INSERT INTO users(mail,name) VALUES($1,'test') RETURNING id`, mail), &userID)
	mustProviderWakeScan(t, db.QueryRowContext(ctx, `INSERT INTO flows(status,model,model_provider_name,model_provider_type,language,tool_call_id_template,user_id) VALUES('waiting','m','p','openai','en','',$1) RETURNING id`, userID), &flowID)
	mustProviderWakeScan(t, db.QueryRowContext(ctx, `INSERT INTO tasks(status,input,flow_id) VALUES('waiting','test',$1) RETURNING id`, flowID), &taskID)
	mustProviderWakeScan(t, db.QueryRowContext(ctx, `INSERT INTO subtasks(status,title,description,task_id) VALUES('waiting','test','test',$1) RETURNING id`, taskID), &subtaskID)
	chain := []llms.MessageContent{
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{llms.ToolCall{ID: "ask-1", FunctionCall: &llms.FunctionCall{Name: "ask", Arguments: `{}`}}}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{llms.ToolCallResponse{ToolCallID: "ask-1", Name: "ask", Content: "pending"}}},
	}
	chainBlob, _ := json.Marshal(chain)
	mustProviderWakeScan(t, db.QueryRowContext(ctx, `INSERT INTO msgchains(type,model,model_provider,chain,flow_id,task_id,subtask_id) VALUES('primary_agent','m','p',$1,$2,$3,$4) RETURNING id`, chainBlob, flowID, taskID, subtaskID), &chainID)
	if err := worldstate.RegisterPrimaryAskWait(ctx, q, flowID, chainID, "ask-1", chain, 0); err != nil {
		t.Fatal(err)
	}
	event, err := q.CreateWorldStateEvent(ctx, database.CreateWorldStateEventParams{
		FlowID: flowID, Kind: database.WorldStateEventKindEntityUpserted,
		Facts: json.RawMessage(`{"ok":true}`), Actor: "test", Provenance: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	waits, err := q.LeasePrimaryWorldStateWaits(ctx, database.LeasePrimaryWorldStateWaitsParams{
		LeaseOwner:     sql.NullString{String: "provider-test", Valid: true},
		LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true}, LimitRows: 1,
	})
	if err != nil || len(waits) != 1 {
		t.Fatalf("lease waits=%d err=%v", len(waits), err)
	}
	if won, err := worldstate.ResolveLeasedPrimaryWorldStateWait(ctx, q, waits[0], "provider-test", event.Revision); err != nil || !won {
		t.Fatalf("event winner won=%v err=%v", won, err)
	}

	fp := &flowProvider{db: q, mx: &sync.RWMutex{}, Provider: providermock.NewProvider(providerpkg.ProviderCustom, "test-model")}
	if err := fp.PutInputToAgentChain(ctx, chainID, "late-human"); err != nil {
		t.Fatalf("late provider input rejected: %v", err)
	}
	row, err := q.GetMsgChain(ctx, chainID)
	if err != nil {
		t.Fatal(err)
	}
	var got []llms.MessageContent
	if err := json.Unmarshal(row.Chain, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("late input was not appended as new chain input: %#v", got)
	}
	if got[2].Parts[0].(llms.TextContent).Text != "late-human" {
		t.Fatalf("late input=%#v", got[2].Parts[0])
	}
	response := got[1].Parts[0].(llms.ToolCallResponse).Content
	if response == "late-human" || response == "pending" {
		t.Fatalf("synthetic response was overwritten: %q", response)
	}
	wait, err := q.GetAgentChainWait(ctx, chainID)
	if err != nil || wait.ResolutionWinner.String != "world_state" {
		t.Fatalf("event winner tombstone changed: %+v err=%v", wait, err)
	}
}

func TestWorldStateDeliveryPersistencePreservesLateInput(t *testing.T) {
	db := providerWakePostgres(t)
	goose.SetBaseFS(migrations.EmbedMigrations)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "sql"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := database.New(db)

	var userID, flowID, taskID, subtaskID, chainID int64
	mail := fmt.Sprintf("provider-delivery-race-%d@example.invalid", time.Now().UnixNano())
	mustProviderWakeScan(t, db.QueryRowContext(ctx, `INSERT INTO users(mail,name) VALUES($1,'test') RETURNING id`, mail), &userID)
	mustProviderWakeScan(t, db.QueryRowContext(ctx, `INSERT INTO flows(status,model,model_provider_name,model_provider_type,language,tool_call_id_template,user_id) VALUES('waiting','m','p','openai','en','',$1) RETURNING id`, userID), &flowID)
	mustProviderWakeScan(t, db.QueryRowContext(ctx, `INSERT INTO tasks(status,input,flow_id) VALUES('waiting','test',$1) RETURNING id`, flowID), &taskID)
	mustProviderWakeScan(t, db.QueryRowContext(ctx, `INSERT INTO subtasks(status,title,description,task_id) VALUES('waiting','test','test',$1) RETURNING id`, taskID), &subtaskID)
	waitingChain := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		llms.TextParts(llms.ChatMessageTypeHuman, "request"),
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{llms.ToolCall{ID: "ask-race", FunctionCall: &llms.FunctionCall{Name: "ask", Arguments: `{}`}}}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{llms.ToolCallResponse{ToolCallID: "ask-race", Name: "ask", Content: "pending"}}},
	}
	waitingBlob, _ := json.Marshal(waitingChain)
	mustProviderWakeScan(t, db.QueryRowContext(ctx, `INSERT INTO msgchains(type,model,model_provider,chain,flow_id,task_id,subtask_id) VALUES('primary_agent','m','p',$1,$2,$3,$4) RETURNING id`, waitingBlob, flowID, taskID, subtaskID), &chainID)
	if err := worldstate.RegisterPrimaryAskWait(ctx, q, flowID, chainID, "ask-race", waitingChain, 0); err != nil {
		t.Fatal(err)
	}
	event, err := q.CreateWorldStateEvent(ctx, database.CreateWorldStateEventParams{
		FlowID: flowID, Kind: database.WorldStateEventKindEntityUpserted,
		Facts: json.RawMessage(`{"ok":true}`), Actor: "test", Provenance: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	waits, err := q.LeasePrimaryWorldStateWaits(ctx, database.LeasePrimaryWorldStateWaitsParams{
		LeaseOwner:     sql.NullString{String: "delivery-race", Valid: true},
		LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true}, LimitRows: 1,
	})
	if err != nil || len(waits) != 1 {
		t.Fatalf("lease waits=%d err=%v", len(waits), err)
	}
	if won, err := worldstate.ResolveLeasedPrimaryWorldStateWait(ctx, q, waits[0], "delivery-race", event.Revision); err != nil || !won {
		t.Fatalf("event winner won=%v err=%v", won, err)
	}
	staleRow, err := q.GetMsgChain(ctx, chainID)
	if err != nil {
		t.Fatal(err)
	}
	var staleChain []llms.MessageContent
	if err := json.Unmarshal(staleRow.Chain, &staleChain); err != nil {
		t.Fatal(err)
	}
	if handled, err := worldstate.ResolvePrimaryWaitWithUser(ctx, q, chainID, "late-human"); err != nil || !handled {
		t.Fatalf("late input handled=%v err=%v", handled, err)
	}
	lateRow, err := q.GetMsgChain(ctx, chainID)
	if err != nil {
		t.Fatal(err)
	}
	var lateChain []llms.MessageContent
	if err := json.Unmarshal(lateRow.Chain, &lateChain); err != nil {
		t.Fatal(err)
	}
	if !chainContains(lateChain, "late-human") {
		t.Fatalf("late human input was not committed before stale persistence: %#v", lateChain)
	}

	store := &databasePrimaryWorldStateDeliveryStore{queries: q}
	persisted, delivered, err := store.Persist(ctx, chainID, staleChain, `<world_state>{"through_revision":1}</world_state>`, event.Revision)
	if err != nil || !delivered {
		t.Fatalf("persist delivery delivered=%v err=%v", delivered, err)
	}
	row, err := q.GetMsgChain(ctx, chainID)
	if err != nil {
		t.Fatal(err)
	}
	var got []llms.MessageContent
	if err := json.Unmarshal(row.Chain, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, persisted) || !chainContains(got, "late-human") || !chainContains(got, "<world_state>") {
		t.Fatalf("stale delivery did not preserve exact late-input chain: %#v", got)
	}
	cursor, err := q.GetWorldStateChainCursor(ctx, chainID)
	if err != nil || cursor.Revision != event.Revision {
		t.Fatalf("cursor revision=%d want=%d err=%v", cursor.Revision, event.Revision, err)
	}
}

func providerWakePostgres(t *testing.T) *sql.DB {
	t.Helper()
	raw := os.Getenv("DATABASE_URL")
	base, err := url.Parse(raw)
	if raw == "" || err != nil || base.Scheme == "" || base.Host == "" {
		host := os.Getenv("POSTGRES_TEST_HOST")
		if host == "" {
			host = "127.0.0.1:5432"
		}
		user, password, name := os.Getenv("REDSCOPE_POSTGRES_USER"), os.Getenv("REDSCOPE_POSTGRES_PASSWORD"), os.Getenv("REDSCOPE_POSTGRES_DB")
		if user == "" || name == "" {
			t.Skip("DATABASE_URL or REDSCOPE_POSTGRES_* is required for disposable PostgreSQL tests")
		}
		base = &url.URL{Scheme: "postgres", User: url.UserPassword(user, password), Host: host, Path: "/" + name, RawQuery: "sslmode=disable"}
	}
	name := fmt.Sprintf("pentagi_provider_wake_%x", time.Now().UnixNano())
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

func mustProviderWakeScan(t *testing.T, row *sql.Row, dest ...any) {
	t.Helper()
	if err := row.Scan(dest...); err != nil {
		t.Fatal(err)
	}
}
