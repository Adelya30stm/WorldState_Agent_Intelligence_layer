package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pentagi/pkg/database"
	"pentagi/pkg/worldstate"
)

type modelToolVersionDB struct{ database.Querier }

func (*modelToolVersionDB) GetWorldStateModelVersion(context.Context, int64) (int32, error) {
	return 2, nil
}

func TestWorldStateModelRejectsInvalidUpdatesBeforeWriting(t *testing.T) {
	tool := NewWorldStateTool(1, &modelToolVersionDB{}, "pentester")
	for _, args := range []string{
		`null`, `[]`, `"text"`,
		`{"operation":"unknown"}`,
		`{"operation":"observe_object","type":"host","locator":null}`,
		`{"operation":"observe_object","type":"host","locator":[]}`,
		`{"operation":"observe_object","type":"host","locator":"{}"}`,
		`{"operation":"ensure_check","object_id":-1,"kind":"xss"}`,
		`{"operation":"start_run","check_id":1,"parameters":null}`,
		`{"operation":"start_run","check_id":1,"parameters":[]}`,
		`{"operation":"start_run","check_id":1,"parameters":{},"verdict":"vulnerable"}`,
		`{"operation":"start_run","check_id":1,"parameters":{},"execution_status":"completed"}`,
		`{"operation":"finish_run","run_id":1,"execution_status":"completed","evidence":{}}`,
		`{"operation":"finish_run","run_id":1,"execution_status":"completed","verdict":"vulnerable","evidence":[]}`,
		`{"operation":"finish_run","run_id":1,"execution_status":"failed","verdict":"vulnerable","evidence":{}}`,
		`{"operation":"finish_run","run_id":1,"execution_status":"completed","verdict":"safe","evidence":{}}`,
		`{"operation":"finish_run","run_id":1,"execution_status":"completed","verdict":"inconclusive","evidence":{},"parameters":{}}`,
		`{"entity_key":"host:example.invalid","type":"host","to_state":"vulnerable"}`,
	} {
		t.Run(args, func(t *testing.T) {
			if _, err := tool.Handle(context.Background(), WorldStateUpdateToolName, json.RawMessage(args)); err == nil {
				t.Fatal("invalid update was accepted")
			}
		})
	}
}

func TestWorldStateRunIdentityComesFromLogicalInvocation(t *testing.T) {
	ctx := func(callID string, rowID int64) context.Context {
		return context.WithValue(context.Background(), worldStateInvocationKey{}, worldStateInvocation{
			CallID: callID, ToolCallID: rowID, TaskID: 2, SubtaskID: 3,
		})
	}
	first, err := worldStateRequestKey(ctx("logical-1", 100), 1)
	if err != nil {
		t.Fatal(err)
	}
	retry, _ := worldStateRequestKey(ctx("logical-1", 101), 1)
	repeatedExperiment, _ := worldStateRequestKey(ctx("logical-2", 102), 1)
	otherFlow, _ := worldStateRequestKey(ctx("logical-1", 100), 4)
	if first != retry || first == repeatedExperiment || first == otherFlow {
		t.Fatalf("invalid identity separation: %q %q %q %q", first, retry, repeatedExperiment, otherFlow)
	}
	firstChain, _ := worldStateRequestKey(WithWorldStateChain(ctx("call_0", 200), 10), 1)
	secondChain, _ := worldStateRequestKey(WithWorldStateChain(ctx("call_0", 201), 11), 1)
	chainRetry, _ := worldStateRequestKey(WithWorldStateChain(ctx("call_0", 202), 10), 1)
	if firstChain == secondChain || firstChain != chainRetry {
		t.Fatal("specialist chains must be independent while retries in the same chain remain idempotent")
	}
	tool := NewWorldStateTool(1, &modelToolVersionDB{}, "pentester")
	_, err = tool.Handle(context.Background(), WorldStateUpdateToolName, json.RawMessage(`{"operation":"start_run","check_id":1,"parameters":{}}`))
	if err == nil || !strings.Contains(err.Error(), "stable tool invocation") {
		t.Fatalf("missing executor identity must fail: %v", err)
	}
}

func TestWorldStateQueryPaginationKeepsCompleteVerdictAndCounts(t *testing.T) {
	positive, negative := "vulnerable", "not_reproduced"
	snapshot := worldstate.ModelSnapshot{
		SchemaVersion: 2, FlowID: 1, Revision: 17, ObjectCount: 2, CheckCount: 2, RunCount: 3,
		Objects: []worldstate.Object{
			{ID: 1, FlowID: 1, Type: "parameter", ComputedStatus: "vulnerable", CheckCount: 1, RunCount: 3,
				Checks: []worldstate.Check{{ID: 10, ObjectID: 1, Kind: "xss", ComputedStatus: "vulnerable", RunCount: 3,
					Runs: []worldstate.CheckRun{
						{ID: 100, CheckID: 10, ExecutionStatus: "completed", Verdict: &positive},
						{ID: 101, CheckID: 10, ExecutionStatus: "completed", Verdict: &negative},
						{ID: 102, CheckID: 10, ExecutionStatus: "completed", Verdict: &negative},
					}}}},
			{ID: 2, FlowID: 1, Type: "host", ComputedStatus: "unknown", Checks: []worldstate.Check{}},
		},
	}
	encoded, err := pageModelSnapshot(snapshot, WorldStateQueryAction{
		CheckID: int64Ptr(10), RunOffset: int64Ptr(1),
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Objects []worldstate.Object `json:"objects"`
		Counts  map[string]int64    `json:"matched_counts"`
	}
	if err := json.Unmarshal([]byte(encoded), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Objects) != 1 || response.Counts["runs"] != 3 {
		t.Fatalf("bad page counts: %s", encoded)
	}
	check := response.Objects[0].Checks[0]
	if check.ComputedStatus != "vulnerable" || check.RunCount != 3 || len(check.Runs) != 1 || check.Runs[0].ID != 101 {
		t.Fatalf("pagination hid a positive verdict or selected wrong run: %s", encoded)
	}
	if len(snapshot.Objects[0].Checks[0].Runs) != 3 {
		t.Fatal("pagination mutated input snapshot")
	}
	if _, err := pageModelSnapshot(snapshot, WorldStateQueryAction{ObjectID: int64Ptr(999)}, 20, 20); err == nil {
		t.Fatal("foreign or missing object must not be returned")
	}
	if _, err := pageModelSnapshot(snapshot, WorldStateQueryAction{CheckID: int64Ptr(999)}, 20, 20); err == nil {
		t.Fatal("foreign or missing check must not be returned")
	}
	encoded, err = pageModelSnapshot(snapshot, WorldStateQueryAction{ObjectOffset: int64Ptr(1000)}, 20, 20)
	if err != nil || !strings.Contains(encoded, `"objects":[]`) {
		t.Fatalf("out-of-range offset must return an empty page: %s %v", encoded, err)
	}
}
