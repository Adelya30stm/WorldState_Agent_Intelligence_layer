package worldstate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModelContextBoundsDoNotChangeComputedStatusOrSource(t *testing.T) {
	positive, negative := "vulnerable", "not_reproduced"
	runs := []CheckRun{}
	for i := int64(1); i <= 20; i++ {
		verdict := &negative
		if i == 1 {
			verdict = &positive
		}
		runs = append(runs, CheckRun{ID: i, CheckID: 2, ExecutionStatus: "completed", Verdict: verdict, Parameters: json.RawMessage(`{"payload":"case"}`), Evidence: json.RawMessage(`{"excerpt":"` + strings.Repeat("data", 500) + `"}`)})
	}
	snapshot := ModelSnapshot{SchemaVersion: 2, FlowID: 1, Revision: 40, ObjectCount: 1, CheckCount: 1, RunCount: 20,
		Objects: []Object{{ID: 1, ObjectKey: "parameter:q", Type: "parameter", Locator: json.RawMessage(`{}`), ComputedStatus: "vulnerable", CheckCount: 1, RunCount: 20,
			Checks: []Check{{ID: 2, Kind: "xss", ComputedStatus: ComputedCheckStatus(runs), RunCount: 20, Runs: runs}},
		}},
	}
	before, _ := json.Marshal(snapshot)
	view, err := NewModelContext(snapshot, 5000)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(view)
	if len(payload) > 5000 || view.Omitted.Runs == 0 || len(view.Objects) != 1 || len(view.Objects[0].Checks) != 1 {
		t.Fatalf("invalid bounded view: %s", payload)
	}
	if view.Objects[0].ComputedStatus != "vulnerable" || view.Objects[0].Checks[0].ComputedStatus != "vulnerable" {
		t.Fatal("truncation changed full-data verdict")
	}
	after, _ := json.Marshal(snapshot)
	if string(before) != string(after) {
		t.Fatal("presentation mutated source snapshot")
	}
	if view.Omitted.Runs+int64(len(view.Objects[0].Checks[0].Runs)) != 20 {
		t.Fatal("omitted counts do not match")
	}
	evidence, err := modelPlannerEvidence(snapshot, DefaultPlannerEvidenceLimits())
	if err != nil {
		t.Fatal(err)
	}
	if evidence.SchemaVersion != 2 || evidence.Model == nil || evidence.Projection.Summary.ByState["vulnerable"] != 1 || evidence.Projection.Entities[0].Properties["check_xss_status"] != "vulnerable" {
		t.Fatalf("planner lost check facts: %+v", evidence)
	}
}
