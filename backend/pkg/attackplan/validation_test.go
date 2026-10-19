package attackplan

import (
	"errors"
	"testing"
)

func TestValidateGraph(t *testing.T) {
	nodes := []Node{
		{Key: "root", Kind: NodeKindGoal, Status: NodeStatusReady, Title: "Root"},
		{Key: "action", Kind: NodeKindAction, Status: NodeStatusPending, Title: "Action"},
	}
	if err := validateGraph(nodes, []Edge{{FromKey: "root", ToKey: "action", Kind: EdgeKindAnd}}); err != nil {
		t.Fatal(err)
	}
	if err := validateGraph(nodes, []Edge{
		{FromKey: "root", ToKey: "action", Kind: EdgeKindAnd},
		{FromKey: "action", ToKey: "root", Kind: EdgeKindDependency},
	}); !errors.Is(err, ErrCycle) {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestValidateGraphRejectsInvalidReferencesAndDuplicates(t *testing.T) {
	nodes := []Node{{Key: "root", Kind: NodeKindGoal, Status: NodeStatusPending, Title: "Root"}}
	if err := validateGraph(nodes, []Edge{{FromKey: "root", ToKey: "missing", Kind: EdgeKindAnd}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing endpoint error = %v", err)
	}
	if err := validateGraph(append(nodes, nodes[0]), nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate node error = %v", err)
	}
}

func TestTerminalTransitionsCannotReopen(t *testing.T) {
	for _, terminal := range []NodeStatus{NodeStatusSucceeded, NodeStatusFailed, NodeStatusSkipped, NodeStatusCancelled} {
		if err := validateNodeTransition(terminal, NodeStatusReady); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s -> ready error = %v", terminal, err)
		}
		if err := validateNodeTransition(terminal, terminal); err != nil {
			t.Errorf("%s no-op error = %v", terminal, err)
		}
	}
	if err := validateNodeTransition(NodeStatusBlocked, NodeStatusReady); err != nil {
		t.Fatalf("blocked -> ready error = %v", err)
	}
}

func TestApplyPatch(t *testing.T) {
	nodes := []Node{
		{Key: "root", Kind: NodeKindGoal, Status: NodeStatusReady, Title: "Root"},
		{Key: "old", Kind: NodeKindAction, Status: NodeStatusPending, Title: "Old"},
	}
	edges := []Edge{{FromKey: "root", ToKey: "old", Kind: EdgeKindAnd}}
	patchedNodes, patchedEdges, err := applyPatch(nodes, edges, GraphPatch{
		RemoveNodeKeys: []string{"old"},
		UpsertNodes:    []Node{{Key: "new", Kind: NodeKindAction, Status: NodeStatusReady, Title: "New"}},
		AddEdges:       []Edge{{FromKey: "root", ToKey: "new", Kind: EdgeKindDependency}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(patchedNodes) != 2 || patchedNodes[1].Key != "new" {
		t.Fatalf("nodes = %#v", patchedNodes)
	}
	if len(patchedEdges) != 1 || patchedEdges[0].ToKey != "new" {
		t.Fatalf("edges = %#v", patchedEdges)
	}
}
