package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"pentagi/pkg/database"

	_ "github.com/lib/pq"
)

func TestWorldStateModelToolRoundTrip(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := db.QueryRow(`INSERT INTO users(mail,name) VALUES($1,'test') RETURNING id`,
		fmt.Sprintf("worldstate-tools-%d@example.invalid", time.Now().UnixNano())).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id=$1`, userID) })
	newFlow := func() int64 {
		t.Helper()
		var flowID int64
		if err := db.QueryRow(`INSERT INTO flows(model,model_provider_name,model_provider_type,language,tool_call_id_template,user_id,world_state_schema_version)
			VALUES('m','p','openai','en','',$1,2) RETURNING id`, userID).Scan(&flowID); err != nil {
			t.Fatal(err)
		}
		return flowID
	}
	flowID, otherFlowID := newFlow(), newFlow()
	tool := NewWorldStateTool(flowID, database.New(db), "pentester")
	otherTool := NewWorldStateTool(otherFlowID, database.New(db), "pentester")
	call := func(target Tool, callID string, args map[string]any) (json.RawMessage, error) {
		encoded, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		ctx := context.WithValue(context.Background(), worldStateInvocationKey{}, worldStateInvocation{CallID: callID})
		result, err := target.Handle(ctx, WorldStateUpdateToolName, encoded)
		if err != nil {
			return nil, err
		}
		var response struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(result), &response); err != nil {
			return nil, err
		}
		return response.Result, nil
	}
	callID := func(target Tool, invocation string, args map[string]any) int64 {
		t.Helper()
		result, err := call(target, invocation, args)
		if err != nil {
			t.Fatal(err)
		}
		var row struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(result, &row); err != nil || row.ID <= 0 {
			t.Fatalf("missing record ID: %s err=%v", result, err)
		}
		return row.ID
	}
	objectID := callID(tool, "observe", map[string]any{
		"operation": "observe_object", "type": "parameter",
		"locator": map[string]any{"url": "http://localhost:18080/search", "method": "GET", "location": "query", "name": "q"},
	})
	checkID := callID(tool, "check", map[string]any{"operation": "ensure_check", "object_id": objectID, "kind": "xss"})
	if repeated := callID(tool, "check-repeat", map[string]any{"operation": "ensure_check", "object_id": objectID, "kind": "XSS"}); repeated != checkID {
		t.Fatal("same object and normalized kind created another check")
	}
	start := map[string]any{"operation": "start_run", "check_id": checkID,
		"parameters": map[string]any{"payload": "synthetic-payload", "auth_role": "anonymous", "target_id": json.Number("9007199254740993")}}
	runID := callID(tool, "experiment-1", start)
	if retry := callID(tool, "experiment-1", start); retry != runID {
		t.Fatal("retrying one logical invocation duplicated its run")
	}
	secondRunID := callID(tool, "experiment-2", start)
	if secondRunID == runID {
		t.Fatal("a real repeat with identical conditions must create a new run")
	}
	finish := map[string]any{"operation": "finish_run", "run_id": runID, "execution_status": "completed", "verdict": "vulnerable",
		"evidence": map[string]any{"script_executed": true, "observed_id": json.Number("9007199254740993"), "Authorization": "Bearer synthetic-evidence-secret"}}
	finished, err := call(tool, "complete", finish)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(finished), "synthetic-evidence-secret") {
		t.Fatal("evidence was not redacted")
	}
	if strings.Count(string(finished), "9007199254740993") != 2 {
		t.Fatalf("exact numeric IDs were rounded in parameters or evidence: %s", finished)
	}
	if retry := callID(tool, "complete-retry", finish); retry != runID {
		t.Fatal("resaving the same final result changed the run")
	}
	finish["verdict"] = "not_reproduced"
	if _, err := call(tool, "rewrite", finish); err == nil {
		t.Fatal("a finished run must not be silently overwritten with another verdict")
	}
	for _, args := range []map[string]any{
		{"operation": "ensure_check", "object_id": objectID, "kind": "sqli"},
		start,
		{"operation": "finish_run", "run_id": runID, "execution_status": "completed", "verdict": "vulnerable", "evidence": map[string]any{}},
	} {
		if _, err := call(otherTool, "cross-flow", args); err == nil {
			t.Fatalf("cross-flow update accepted: %v", args["operation"])
		}
	}
	var foreignToolID int64
	if err := db.QueryRow(`INSERT INTO toolcalls(call_id,status,name,args,flow_id)
		VALUES('foreign','finished','terminal','{}',$1) RETURNING id`, otherFlowID).Scan(&foreignToolID); err != nil {
		t.Fatal(err)
	}
	start["tool_call_id"] = foreignToolID
	if _, err := call(tool, "foreign-tool", start); err == nil {
		t.Fatal("cross-flow tool_call_id accepted")
	}
	query := json.RawMessage(fmt.Sprintf(`{"check_id":%d,"run_limit":1,"run_offset":1}`, checkID))
	result, err := tool.Handle(context.Background(), WorldStateQueryToolName, query)
	if err != nil || !strings.Contains(result, `"computed_status":"vulnerable"`) || !strings.Contains(result, `"run_count":2`) {
		t.Fatalf("paged query lost aggregate status/count: %s err=%v", result, err)
	}
	if _, err := otherTool.Handle(context.Background(), WorldStateQueryToolName, query); err == nil {
		t.Fatal("cross-flow query accepted a foreign check ID")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM world_state_check_runs WHERE check_id=$1`, checkID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("unexpected final run count %d err=%v", count, err)
	}
}
