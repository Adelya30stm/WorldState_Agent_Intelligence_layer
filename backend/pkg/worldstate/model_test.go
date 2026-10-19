package worldstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func modelVerdict(s string) *string { return &s }

func TestModelComputedStatus(t *testing.T) {
	negative := CheckRun{ExecutionStatus: "completed", Verdict: modelVerdict("not_reproduced")}
	positive := CheckRun{ExecutionStatus: "completed", Verdict: modelVerdict("vulnerable")}
	cases := []struct {
		name string
		runs []CheckRun
		want string
	}{
		{"none", nil, "unknown"},
		{"cancelled only", []CheckRun{{ExecutionStatus: "cancelled"}}, "unknown"},
		{"negative", []CheckRun{negative}, "not_confirmed"},
		{"failed", []CheckRun{negative, {ExecutionStatus: "failed"}}, "inconclusive"},
		{"unfinished", []CheckRun{negative, {ExecutionStatus: "planned"}}, "in_progress"},
		{"positive survives later negative", []CheckRun{positive, negative}, "vulnerable"},
		{"positive survives running and failure", []CheckRun{positive, {ExecutionStatus: "running"}, {ExecutionStatus: "failed"}}, "vulnerable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputedCheckStatus(tc.runs); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestModelCanonicalIdentityAndRedaction(t *testing.T) {
	loc := map[string]any{"url": "https://user:secret@EXAMPLE.test:8443/search?action=find&q=first", "method": "get", "location": "query", "name": "q"}
	key, err := CanonicalObjectKey("parameter", loc)
	if err != nil {
		t.Fatal(err)
	}
	if key != "parameter:GET:https://example.test:8443/search?action=find&q=:query:q" {
		t.Fatal(key)
	}
	loc["url"] = "https://example.test:8443/search?q=second&action=find"
	repeat, err := CanonicalObjectKey("parameter", loc)
	if err != nil || repeat != key {
		t.Fatalf("same object different payload: %s %v", repeat, err)
	}
	loc["url"] = "https://example.test:9443/search?q=second&action=find"
	port, _ := CanonicalObjectKey("parameter", loc)
	loc["url"] = "https://example.test:8443/search?q=second&action=delete"
	route, _ := CanonicalObjectKey("parameter", loc)
	if key == port || key == route {
		t.Fatal("distinct port/routing query was merged")
	}
	safe, err := modelSafeMap(map[string]any{"headers": map[string]any{"Cookie": "secret-cookie", "Authorization": "Bearer hidden"}, "url": "https://name:secret-pass@example.test/a?access_token=secret-token&x=1", "raw": "Authorization: secret-header", "payload": "<probe>"})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-cookie", "hidden", "secret-pass", "secret-token", "secret-header"} {
		if strings.Contains(string(safe), secret) {
			t.Fatalf("secret leaked: %s", safe)
		}
	}
	if !strings.Contains(string(safe), "example.test") {
		t.Fatalf("redaction removed URL identity: %s", safe)
	}
	malformed, err := modelSafeMap(map[string]any{"raw": "request https://user:secret-malformed@%broken/path", "path": "https://example.test/password=secret-path"})
	if err != nil || strings.Contains(string(malformed), "secret-") {
		t.Fatalf("malformed URL or path leaked credentials: %s %v", malformed, err)
	}
	if _, _, _, err := normalizeModelObject(ObserveObjectInput{Type: "parameter", ObjectKey: "wrong", Locator: loc}); err == nil {
		t.Fatal("accepted conflicting identity")
	}
}

func newModelFixture(t *testing.T) storeFixture {
	t.Helper()
	f := newStoreFixture(t)
	if _, err := f.db.Exec(`UPDATE flows SET world_state_schema_version=2 WHERE id=$1`, f.flowID); err != nil {
		t.Fatal(err)
	}
	return f
}

func modelFixtureCheck(t *testing.T, f storeFixture) (*ModelStore, Object, Check) {
	t.Helper()
	s := NewModelStore(f.q)
	o, err := s.ObserveObject(context.Background(), f.flowID, AgentSystem, ObserveObjectInput{Type: "parameter", Locator: map[string]any{"url": "https://model.example:8443/search", "method": "GET", "location": "query", "name": "q"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.EnsureCheck(context.Background(), f.flowID, o.ID, "XSS", AgentSystem)
	if err != nil {
		t.Fatal(err)
	}
	return s, o, c
}

func TestModelStoreConcurrentRepeatAndIdempotency(t *testing.T) {
	f := newModelFixture(t)
	s, o, check := modelFixtureCheck(t, f)
	ctx := context.Background()
	const workers = 8
	ids := make(chan int64, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.StartRun(ctx, f.flowID, AgentSystem, StartRunInput{CheckID: check.ID, RequestKey: "same-call", Parameters: map[string]any{"payload": "a", "password": "secret-run"}})
			ids <- r.ID
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first int64
	for id := range ids {
		if first == 0 {
			first = id
		}
		if id != first {
			t.Fatal("storage retry duplicated a run")
		}
	}
	before, err := s.ReadSnapshot(ctx, f.flowID)
	if err != nil {
		t.Fatal(err)
	}
	if before.RunCount != 1 || before.Objects[0].ComputedStatus != "in_progress" {
		t.Fatalf("unexpected snapshot %+v", before)
	}
	if strings.Contains(string(before.Objects[0].Checks[0].Runs[0].Parameters), "secret-run") {
		t.Fatal("run secret persisted")
	}
	positive := UpdateRunInput{ExecutionStatus: "completed", Verdict: modelVerdict("vulnerable"), Evidence: map[string]any{"observed": true, "Cookie": "secret-evidence"}}
	if _, err := s.UpdateRun(ctx, f.flowID, first, AgentSystem, positive); err != nil {
		t.Fatal(err)
	}
	completed, err := s.ReadSnapshot(ctx, f.flowID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRun(ctx, f.flowID, first, AgentSystem, positive); err != nil {
		t.Fatal(err)
	}
	again, _ := s.ReadSnapshot(ctx, f.flowID)
	if completed.Revision != again.Revision {
		t.Fatal("duplicate completion emitted event")
	}
	if _, err := s.UpdateRun(ctx, f.flowID, first, AgentSystem, UpdateRunInput{ExecutionStatus: "completed", Verdict: modelVerdict("not_reproduced"), Evidence: map[string]any{}}); !errors.Is(err, ErrModelConflict) {
		t.Fatalf("changed terminal result: %v", err)
	}
	if _, err := s.StartRun(ctx, f.flowID, AgentSystem, StartRunInput{CheckID: check.ID, RequestKey: "same-call", Parameters: map[string]any{"payload": "changed"}}); !errors.Is(err, ErrModelConflict) {
		t.Fatalf("changed retry input: %v", err)
	}
	otherCheck, err := s.EnsureCheck(ctx, f.flowID, check.ObjectID, "sqli", AgentSystem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(ctx, f.flowID, AgentSystem, StartRunInput{CheckID: otherCheck.ID, RequestKey: "same-call", Parameters: map[string]any{"payload": "a", "password": "secret-run"}}); !errors.Is(err, ErrModelConflict) {
		t.Fatalf("same invocation created a run for a different check: %v", err)
	}
	repeat, err := s.StartRun(ctx, f.flowID, AgentSystem, StartRunInput{CheckID: check.ID, RequestKey: "new-real-execution", Parameters: map[string]any{"payload": "a", "password": "secret-run"}})
	if err != nil || repeat.ID == first {
		t.Fatalf("real repeat was deduplicated: %v", err)
	}
	if _, err := s.UpdateRun(ctx, f.flowID, repeat.ID, AgentSystem, UpdateRunInput{ExecutionStatus: "completed", Verdict: modelVerdict("not_reproduced")}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.ReadSnapshot(ctx, f.flowID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ObjectCount != 1 || snapshot.CheckCount != 2 || snapshot.RunCount != 2 || snapshot.Objects[0].ComputedStatus != "vulnerable" {
		t.Fatalf("incorrect aggregate %+v", snapshot)
	}
	observed, err := s.ObserveObject(ctx, f.flowID, AgentSystem, ObserveObjectInput{Type: o.Type, ObjectKey: o.ObjectKey, Locator: map[string]any{"url": "https://model.example:8443/search", "method": "GET", "location": "query", "name": "q"}})
	if err != nil || observed.ComputedStatus != "vulnerable" {
		t.Fatalf("idempotent observe forgot runs: %+v %v", observed, err)
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "secret-evidence") {
		t.Fatal("evidence leaked")
	}
}

func TestModelStoreOwnershipRollbackAndVersions(t *testing.T) {
	f := newModelFixture(t)
	other := newModelFixture(t)
	s, _, check := modelFixtureCheck(t, f)
	ctx := context.Background()
	if _, err := s.EnsureCheck(ctx, other.flowID, check.ObjectID, "sqli", AgentSystem); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("crossflow object accepted: %v", err)
	}
	if _, err := s.StartRun(ctx, other.flowID, AgentSystem, StartRunInput{CheckID: check.ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("crossflow check accepted: %v", err)
	}
	run, err := s.StartRun(ctx, f.flowID, AgentSystem, StartRunInput{CheckID: check.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRun(ctx, other.flowID, run.ID, AgentSystem, UpdateRunInput{ExecutionStatus: "failed"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("crossflow run accepted: %v", err)
	}
	before, _ := s.ReadSnapshot(ctx, f.flowID)
	if _, err := s.StartRun(ctx, f.flowID, " ", StartRunInput{CheckID: check.ID, RequestKey: "rollback-start"}); err == nil {
		t.Fatal("expected journal failure")
	}
	after, _ := s.ReadSnapshot(ctx, f.flowID)
	if before.RunCount != after.RunCount || before.Revision != after.Revision {
		t.Fatal("failed journal committed domain change")
	}
	if _, err := s.UpdateRun(ctx, f.flowID, run.ID, " ", UpdateRunInput{ExecutionStatus: "completed", Verdict: modelVerdict("vulnerable")}); err == nil {
		t.Fatal("expected completion journal failure")
	}
	after, _ = s.ReadSnapshot(ctx, f.flowID)
	if after.Objects[0].Checks[0].Runs[0].ExecutionStatus != "running" || after.Revision != before.Revision {
		t.Fatal("failed journal changed run")
	}
	var foreignTool int64
	if err := other.db.QueryRow(`INSERT INTO toolcalls(call_id,status,name,args,flow_id) VALUES($1,'finished','terminal','{}',$2) RETURNING id`, fmt.Sprintf("foreign-%d", run.ID), other.flowID).Scan(&foreignTool); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(ctx, f.flowID, AgentSystem, StartRunInput{CheckID: check.ID, ToolCallID: &foreignTool}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("crossflow tool accepted: %v", err)
	}
	legacy := newStoreFixture(t)
	if version, err := ModelVersion(ctx, legacy.q, legacy.flowID); err != nil || version != 1 {
		t.Fatalf("legacy version=%d err=%v", version, err)
	}
	if _, err := NewModelStore(legacy.q).ReadSnapshot(ctx, legacy.flowID); !errors.Is(err, ErrModelVersion) {
		t.Fatalf("legacy flow silently read as empty v2: %v", err)
	}
}
