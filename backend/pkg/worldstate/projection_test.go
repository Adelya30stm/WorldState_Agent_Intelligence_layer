package worldstate

import (
	"testing"

	"pentagi/pkg/database"
)

func TestProjectionTextEmpty(t *testing.T) {
	p := &Projection{Empty: true, CountsByState: map[State]int{}, CountsByType: map[string]int{}}
	txt := p.Text()
	if !containsAll(txt, "world_state", "empty") {
		t.Fatalf("unexpected empty projection: %s", txt)
	}
}

func TestProjectionTextWithFrontier(t *testing.T) {
	p := &Projection{
		FlowID:        16,
		TotalEntities: 2,
		Empty:         false,
		CountsByState: map[State]int{StateDiscovered: 1, StateScanning: 1},
		CountsByType:  map[string]int{"host": 2},
		Frontier: []FrontierItem{
			{Key: "host:example.test", Type: "host", State: StateDiscovered, Why: "scan"},
		},
		RecentTransitions: []TransitionItem{
			{EntityKey: "host:example.test", From: StateUnknown, To: StateDiscovered, Agent: "executor"},
		},
	}
	txt := p.Text()
	for _, want := range []string{"frontier", "host:example.test", "discovered", "recent_transitions"} {
		if !containsAll(txt, want) {
			t.Fatalf("missing %q in:\n%s", want, txt)
		}
	}
}

func TestBuildFrontierSkipsOutOfScope(t *testing.T) {
	entities := []database.WorldStateEntity{
		{EntityKey: "host:in-scope.mokka.ru", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: []byte(`{}`)},
		{EntityKey: "host:requests.readthedocs.io", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: []byte(`{"out_of_scope":true}`)},
		{EntityKey: "credential:session", Type: EntityTypeCredential, State: database.WorldStateLifecycleDiscovered, Properties: []byte(`{"out_of_scope":"true"}`)},
		{EntityKey: "host:done.mokka.ru", Type: EntityTypeHost, State: database.WorldStateLifecycleRemediated, Properties: []byte(`{}`)},
	}
	got := buildFrontier(entities)
	if len(got) != 1 || got[0].Key != "host:in-scope.mokka.ru" {
		t.Fatalf("frontier=%+v, want only in-scope host", got)
	}
}

func TestIncompleteAssetsAndDoneGuard(t *testing.T) {
	entities := []database.WorldStateEntity{
		{EntityKey: "host:live.mokka.ru", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: []byte(`{}`)},
		{EntityKey: "endpoint:https://live.mokka.ru/", Type: EntityTypeEndpoint, State: database.WorldStateLifecycleScanning, Properties: []byte(`{}`)},
		{EntityKey: "host:docs.example", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: []byte(`{"out_of_scope":true}`)},
		{EntityKey: "finding:cve-1", Type: EntityTypeFinding, State: database.WorldStateLifecycleDiscovered, Properties: []byte(`{}`)},
		{EntityKey: "host:ok.mokka.ru", Type: EntityTypeHost, State: database.WorldStateLifecycleAssessed, Properties: []byte(`{}`)},
	}
	got := IncompleteAssets(entities)
	if len(got) != 2 {
		t.Fatalf("incomplete=%+v", got)
	}
	if err := ErrIncompleteWorldState(entities); err == nil || !containsAll(err.Error(), "cannot complete", "host:live.mokka.ru") {
		t.Fatalf("expected done guard, got %v", err)
	}
	if err := ErrIncompleteWorldState(entities[2:]); err != nil {
		t.Fatalf("out_of_scope and findings must not block done: %v", err)
	}
}

func TestProjectionTextSanitizesCredentialIdentifiers(t *testing.T) {
	p := &Projection{
		Frontier:          []FrontierItem{{Key: "credential:cookie:synthetic-cookie-sentinel", Type: EntityTypeCredential, State: StateDiscovered}},
		RecentTransitions: []TransitionItem{{EntityKey: "credential:password:synthetic-password-sentinel", From: StateUnknown, To: StateDiscovered}},
	}
	text := p.Text()
	if containsAll(text, "synthetic-cookie-sentinel") || containsAll(text, "synthetic-password-sentinel") {
		t.Fatalf("projection leaked credential identifier material")
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !stringContains(s, p) {
			return false
		}
	}
	return true
}

func stringContains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
