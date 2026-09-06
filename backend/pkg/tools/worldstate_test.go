package tools

import (
	"fmt"
	"strings"
	"testing"

	"pentagi/pkg/database"
	"pentagi/pkg/worldstate"
)

func TestWorldStateQueryMatchesTypesAndStates(t *testing.T) {
	host := database.WorldStateEntity{Type: "host", State: database.WorldStateLifecycleDiscovered, EntityKey: "host:a.mokka.ru"}
	ep := database.WorldStateEntity{Type: "endpoint", State: database.WorldStateLifecycleScanning, EntityKey: "endpoint:https://a.mokka.ru/"}
	cred := database.WorldStateEntity{Type: "credential", State: database.WorldStateLifecycleDiscovered, EntityKey: "credential:session"}

	hostAndEndpoint := worldStateFilterSet("", []string{"host", "endpoint"})
	frontier := worldStateFilterSet("", []string{"discovered", "scanning"})
	discoveredOnly := worldStateFilterSet("discovered", nil)

	if !worldStateQueryMatches(host, hostAndEndpoint, frontier) {
		t.Fatal("expected in-scope discovered host to match")
	}
	if !worldStateQueryMatches(ep, hostAndEndpoint, frontier) {
		t.Fatal("expected scanning endpoint to match")
	}
	if worldStateQueryMatches(cred, hostAndEndpoint, frontier) {
		t.Fatal("credential must not match host/endpoint type filter")
	}
	if worldStateQueryMatches(ep, nil, discoveredOnly) {
		t.Fatal("scanning endpoint must not match discovered-only filter")
	}
	if !worldStateQueryMatches(cred, nil, nil) {
		t.Fatal("empty filters must match everything")
	}
}

func TestWorldStateMutationsBatchAndSingle(t *testing.T) {
	single, err := worldStateMutations(WorldStateUpdateAction{
		Type: "host", EntityKey: "a.mokka.ru", ToState: "assessed",
		Properties: `{"out_of_scope":false}`,
		Message:    "promote",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(single) != 1 || single[0].EntityKey != "host:a.mokka.ru" || single[0].ToState != worldstate.StateAssessed {
		t.Fatalf("unexpected single mutation: %+v", single)
	}

	batch, err := worldStateMutations(WorldStateUpdateAction{
		Entities: []WorldStateUpdateItem{
			{Type: "host", EntityKey: "host:a.mokka.ru", ToState: "assessed"},
			{Type: "credential", EntityKey: "credential:session", ToState: "assessed", Properties: `{"out_of_scope":true}`},
		},
		Message: "batch",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 {
		t.Fatalf("want 2 mutations, got %d", len(batch))
	}
	if batch[1].Properties["out_of_scope"] != true {
		t.Fatalf("expected out_of_scope on credential: %+v", batch[1].Properties)
	}

	tooBig := make([]WorldStateUpdateItem, maxWorldStateUpdateBatch+1)
	for i := range tooBig {
		tooBig[i] = WorldStateUpdateItem{Type: "host", EntityKey: fmt.Sprintf("host:%d.example", i), ToState: "assessed"}
	}
	if _, err := worldStateMutations(WorldStateUpdateAction{Entities: tooBig, Message: "nope"}); err == nil {
		t.Fatal("expected batch size error")
	}

	_, err = worldStateMutations(WorldStateUpdateAction{
		Entities: []WorldStateUpdateItem{{Type: "host", EntityKey: "host:x", ToState: "ignored"}},
		Message:  "bad",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown to_state") {
		t.Fatalf("expected unknown to_state for ignored, got %v", err)
	}
}
