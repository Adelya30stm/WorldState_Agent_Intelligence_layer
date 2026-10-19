package attackplan

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"pentagi/pkg/worldstate"
)

func TestSearchPrerequisiteSemantics(t *testing.T) {
	tests := []struct {
		name       string
		statuses   []NodeStatus
		kinds      []EdgeKind
		wantReason ExclusionReason
	}{
		{name: "and satisfied", statuses: []NodeStatus{NodeStatusSucceeded, NodeStatusSucceeded}, kinds: []EdgeKind{EdgeKindAnd, EdgeKindAnd}},
		{name: "and blocked", statuses: []NodeStatus{NodeStatusSucceeded, NodeStatusPending}, kinds: []EdgeKind{EdgeKindAnd, EdgeKindAnd}, wantReason: ReasonBlockedPrerequisite},
		{name: "and invalidated", statuses: []NodeStatus{NodeStatusSucceeded, NodeStatusFailed}, kinds: []EdgeKind{EdgeKindAnd, EdgeKindAnd}, wantReason: ReasonInvalidatedPrerequisite},
		{name: "or satisfied", statuses: []NodeStatus{NodeStatusSucceeded, NodeStatusPending}, kinds: []EdgeKind{EdgeKindOr, EdgeKindOr}},
		{name: "or blocked", statuses: []NodeStatus{NodeStatusPending, NodeStatusFailed}, kinds: []EdgeKind{EdgeKindOr, EdgeKindOr}, wantReason: ReasonBlockedPrerequisite},
		{name: "or invalidated", statuses: []NodeStatus{NodeStatusFailed, NodeStatusSkipped}, kinds: []EdgeKind{EdgeKindOr, EdgeKindOr}, wantReason: ReasonInvalidatedPrerequisite},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baseSearchInput()
			input.Plan.Nodes = []Node{
				testNode("first", test.statuses[0]), testNode("second", test.statuses[1]), testNode("target", NodeStatusPending),
			}
			input.Plan.Edges = []Edge{
				{FromKey: "first", ToKey: "target", Kind: test.kinds[0]},
				{FromKey: "second", ToKey: "target", Kind: test.kinds[1]},
			}
			input.Actions = []Action{testAction("first"), testAction("second"), testAction("target")}
			result, err := Search(input)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantReason == "" {
				assertFrontierContains(t, result, "target")
				return
			}
			if got := reasonFor(result, "target"); got != test.wantReason {
				t.Fatalf("reason = %q, want %q", got, test.wantReason)
			}
		})
	}
}

func TestSearchMixedPrerequisiteSatisfaction(t *testing.T) {
	input := baseSearchInput()
	input.Plan.Nodes = []Node{
		testNode("and", NodeStatusSucceeded), testNode("dependency", NodeStatusSucceeded),
		testNode("or-failed", NodeStatusFailed), testNode("or-succeeded", NodeStatusSucceeded),
		testNode("target", NodeStatusPending),
	}
	input.Plan.Edges = []Edge{
		{FromKey: "and", ToKey: "target", Kind: EdgeKindAnd},
		{FromKey: "dependency", ToKey: "target", Kind: EdgeKindDependency},
		{FromKey: "or-failed", ToKey: "target", Kind: EdgeKindOr},
		{FromKey: "or-succeeded", ToKey: "target", Kind: EdgeKindOr},
	}
	input.Actions = []Action{
		testAction("and"), testAction("dependency"), testAction("or-failed"),
		testAction("or-succeeded"), testAction("target"),
	}
	result, err := Search(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range result.Frontier {
		if candidate.NodeKey == "target" {
			if candidate.Score.PrerequisiteSatisfaction != 1 {
				t.Fatalf("prerequisite satisfaction = %v, want 1", candidate.Score.PrerequisiteSatisfaction)
			}
			return
		}
	}
	t.Fatalf("target missing from frontier: %+v", result)
}

func TestSearchStablePrerequisiteDetail(t *testing.T) {
	input := baseSearchInput()
	input.Plan.Nodes = []Node{
		testNode("z-blocked", NodeStatusPending), testNode("a-blocked", NodeStatusPending),
		testNode("or-pending", NodeStatusPending), testNode("target", NodeStatusPending),
	}
	edges := []Edge{
		{FromKey: "z-blocked", ToKey: "target", Kind: EdgeKindAnd},
		{FromKey: "a-blocked", ToKey: "target", Kind: EdgeKindDependency},
		{FromKey: "or-pending", ToKey: "target", Kind: EdgeKindOr},
	}
	input.Actions = []Action{testAction("z-blocked"), testAction("a-blocked"), testAction("or-pending"), testAction("target")}
	var want SearchResult
	for attempt := 0; attempt < 2; attempt++ {
		input.Plan.Edges = edges
		result, err := Search(input)
		if err != nil {
			t.Fatal(err)
		}
		if detail := detailFor(result, "target"); detail != "a-blocked" {
			t.Fatalf("detail = %q, want a-blocked", detail)
		}
		if attempt == 0 {
			want = result
		} else if !reflect.DeepEqual(result, want) {
			t.Fatalf("edge order changed result: got=%+v want=%+v", result, want)
		}
		edges = []Edge{edges[2], edges[1], edges[0]}
	}
}

func TestSearchSharedDAGPrerequisite(t *testing.T) {
	input := baseSearchInput()
	input.Plan.Nodes = []Node{testNode("shared", NodeStatusSucceeded), testNode("left", NodeStatusPending), testNode("right", NodeStatusPending)}
	input.Plan.Edges = []Edge{
		{FromKey: "shared", ToKey: "left", Kind: EdgeKindDependency},
		{FromKey: "shared", ToKey: "right", Kind: EdgeKindDependency},
	}
	input.Actions = []Action{testAction("shared"), testAction("left"), testAction("right")}
	result, err := Search(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := frontierKeys(result); !reflect.DeepEqual(got, []string{"left", "right"}) {
		t.Fatalf("frontier = %v", got)
	}
}

func TestSearchRejectsCycle(t *testing.T) {
	input := baseSearchInput()
	input.Plan.Nodes = []Node{testNode("a", NodeStatusPending), testNode("b", NodeStatusPending)}
	input.Plan.Edges = []Edge{{FromKey: "a", ToKey: "b", Kind: EdgeKindAnd}, {FromKey: "b", ToKey: "a", Kind: EdgeKindAnd}}
	input.Actions = []Action{testAction("a"), testAction("b")}
	if _, err := Search(input); !errors.Is(err, ErrCycle) {
		t.Fatalf("error = %v, want ErrCycle", err)
	}
}

func TestSearchStableTieBreakAndReplay(t *testing.T) {
	input := baseSearchInput()
	input.Plan.Nodes = []Node{testNode("zeta", NodeStatusPending), testNode("alpha", NodeStatusPending)}
	input.Actions = []Action{testAction("zeta"), testAction("alpha")}
	want, err := Search(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := frontierKeys(want); !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("frontier = %v", got)
	}
	for i := 0; i < 100; i++ {
		got, searchErr := Search(input)
		if searchErr != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("replay %d differs: result=%+v error=%v", i, got, searchErr)
		}
	}
}

func TestSearchTypedEvidence(t *testing.T) {
	tests := []struct {
		name       string
		property   any
		duplicate  any
		condition  Precondition
		wantReason ExclusionReason
	}{
		{
			name: "matching typed number", property: float64(443),
			condition: Precondition{Fact: entityProperty("host:target", "port"), Predicate: PredicateAtLeast, Value: numberValue(80)},
		},
		{
			name: "matching planner integer", property: json.Number("443"),
			condition: Precondition{Fact: entityProperty("host:target", "port"), Predicate: PredicateAtLeast, Value: integerValue(80)},
		},
		{
			name: "matching exact planner integer", property: json.Number("9007199254740993"),
			condition: Precondition{Fact: entityProperty("host:target", "port"), Predicate: PredicateEqual, Value: integerValue(1<<53 + 1)},
		},
		{
			name: "matching planner number", property: json.Number("443.5"),
			condition: Precondition{Fact: entityProperty("host:target", "port"), Predicate: PredicateEqual, Value: numberValue(443.5)},
		},
		{
			name: "out of range planner integer is dropped", property: json.Number("9223372036854775808"),
			condition: Precondition{Fact: entityProperty("host:target", "port"), Predicate: PredicateExists}, wantReason: ReasonBlockedPrecondition,
		},
		{
			name: "type mismatch blocks", property: "443",
			condition: Precondition{Fact: entityProperty("host:target", "port"), Predicate: PredicateAtLeast, Value: numberValue(80)}, wantReason: ReasonBlockedPrecondition,
		},
		{
			name: "contradiction invalidates", property: float64(443), duplicate: float64(80),
			condition: Precondition{Fact: entityProperty("host:target", "port"), Predicate: PredicateEqual, Value: numberValue(443)}, wantReason: ReasonContradictoryEvidence,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baseSearchInput()
			input.Plan.Nodes = []Node{testNode("action", NodeStatusPending)}
			action := testAction("action")
			action.Preconditions = []Precondition{test.condition}
			action.Effects = []Effect{{Fact: entityProperty("host:target", "checked"), Kind: EffectSet, Value: Value{Kind: ValueBoolean, Boolean: true}}}
			input.Actions = []Action{action}
			input.Evidence.Projection.Entities = []worldstate.PlannerEvidenceEntity{{
				Key: "host:target", Type: "host", State: "discovered", Version: 1,
				Properties: map[string]any{"port": test.property},
			}}
			if test.duplicate != nil {
				input.Evidence.Projection.Entities = append(input.Evidence.Projection.Entities, worldstate.PlannerEvidenceEntity{
					Key: "host:target", Type: "host", State: "scanning", Version: 2,
					Properties: map[string]any{"port": test.duplicate},
				})
			}
			result, err := Search(input)
			if err != nil {
				t.Fatal(err)
			}
			if got := reasonFor(result, "action"); got != test.wantReason {
				t.Fatalf("reason = %q, want %q", got, test.wantReason)
			}
		})
	}
}

func TestSearchPreservesExactRevisionFacts(t *testing.T) {
	const capturedHead int64 = 1<<53 + 1
	input := baseSearchInput()
	input.Plan.Nodes = []Node{testNode("exact", NodeStatusPending), testNode("nearby", NodeStatusPending)}
	exact, nearby := testAction("exact"), testAction("nearby")
	exact.Preconditions = []Precondition{{Fact: FactRef{Kind: FactCapturedHead}, Predicate: PredicateEqual, Value: integerValue(capturedHead)}}
	nearby.Preconditions = []Precondition{{Fact: FactRef{Kind: FactCapturedHead}, Predicate: PredicateEqual, Value: integerValue(capturedHead - 1)}}
	input.Actions = []Action{exact, nearby}
	input.Evidence.CapturedHead = capturedHead
	input.Evidence.Journal.ThroughRevision = capturedHead
	result, err := Search(input)
	if err != nil {
		t.Fatal(err)
	}
	assertFrontierContains(t, result, "exact")
	if got := reasonFor(result, "nearby"); got != ReasonBlockedPrecondition {
		t.Fatalf("nearby reason = %q, want %q", got, ReasonBlockedPrecondition)
	}
}

func TestSearchInvalidEffectIsAuditable(t *testing.T) {
	tests := []Effect{
		{Fact: entityProperty("host:target", "state"), Kind: EffectKind("unsupported")},
		{Fact: entityProperty("host:target", "score"), Kind: EffectSet, Value: numberValue(math.NaN())},
		{Fact: entityProperty("host:target", "score"), Kind: EffectSet, Value: numberValue(math.Inf(1))},
	}
	for _, effect := range tests {
		input := baseSearchInput()
		input.Plan.Nodes = []Node{testNode("action", NodeStatusPending)}
		action := testAction("action")
		action.Effects = []Effect{effect}
		input.Actions = []Action{action}
		result, err := Search(input)
		if err != nil {
			t.Fatal(err)
		}
		if got := reasonFor(result, "action"); got != ReasonInvalidEffect {
			t.Fatalf("effect %+v reason = %q, want %q", effect, got, ReasonInvalidEffect)
		}
	}
}

func TestSearchRejectsNonFinitePreconditionValues(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		input := baseSearchInput()
		input.Plan.Nodes = []Node{testNode("action", NodeStatusPending)}
		action := testAction("action")
		action.Preconditions = []Precondition{{
			Fact: FactRef{Kind: FactCapturedHead}, Predicate: PredicateEqual, Value: numberValue(value),
		}}
		input.Actions = []Action{action}
		result, err := Search(input)
		if err != nil {
			t.Fatal(err)
		}
		if got := reasonFor(result, "action"); got != ReasonUnsupportedAction {
			t.Fatalf("value %v reason = %q, want %q", value, got, ReasonUnsupportedAction)
		}
	}
}

func TestSearchBudgetExhaustion(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*SearchInput)
		node       string
		wantReason ExclusionReason
	}{
		{name: "node", node: "low", wantReason: ReasonNodeBudget, mutate: func(input *SearchInput) { input.Limits.MaxNodes = 1 }},
		{name: "depth", node: "low", wantReason: ReasonDepthBudget, mutate: func(input *SearchInput) { input.Limits.MaxDepth = 0 }},
		{name: "beam", node: "low", wantReason: ReasonBeamLimit, mutate: func(input *SearchInput) { input.Limits.BeamWidth = 1 }},
		{name: "cost", node: "high", wantReason: ReasonCostBudget, mutate: func(input *SearchInput) { input.Limits.CostBudget = 0.1 }},
		{name: "risk", node: "high", wantReason: ReasonRiskBudget, mutate: func(input *SearchInput) { input.Limits.MaxOperationalRisk = 0.1 }},
		{name: "time", node: "high", wantReason: ReasonTimeBudget, mutate: func(input *SearchInput) { input.Limits.TimeBudget = time.Millisecond }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baseSearchInput()
			input.Plan.Nodes = []Node{testNode("root", NodeStatusSucceeded), testNode("high", NodeStatusPending), testNode("low", NodeStatusPending)}
			input.Plan.Edges = []Edge{{FromKey: "root", ToKey: "low", Kind: EdgeKindDependency}}
			high, low := testAction("high"), testAction("low")
			high.Confidence = 1
			high.Cost = 0.2
			high.OperationalRisk = 0.2
			high.EvaluationTime = 2 * time.Millisecond
			low.Confidence = 0
			input.Actions = []Action{testAction("root"), high, low}
			test.mutate(&input)
			result, err := Search(input)
			if err != nil {
				t.Fatal(err)
			}
			if got := reasonFor(result, test.node); got != test.wantReason {
				t.Fatalf("reason = %q, want %q; result=%+v", got, test.wantReason, result)
			}
		})
	}
}

func TestSearchBudgetArithmetic(t *testing.T) {
	t.Run("zero time is strict", func(t *testing.T) {
		input := baseSearchInput()
		input.Plan.Nodes = []Node{testNode("zero", NodeStatusPending), testNode("positive", NodeStatusPending)}
		zero, positive := testAction("zero"), testAction("positive")
		positive.EvaluationTime = 1
		input.Actions = []Action{zero, positive}
		input.Limits.TimeBudget = 0
		result, err := Search(input)
		if err != nil {
			t.Fatal(err)
		}
		assertFrontierContains(t, result, "zero")
		if got := reasonFor(result, "positive"); got != ReasonTimeBudget {
			t.Fatalf("positive reason = %q, want %q", got, ReasonTimeBudget)
		}
	})

	t.Run("duration overflow", func(t *testing.T) {
		input := baseSearchInput()
		input.Plan.Nodes = []Node{testNode("first", NodeStatusPending), testNode("overflow", NodeStatusPending)}
		first, overflow := testAction("first"), testAction("overflow")
		first.Confidence = 1
		first.EvaluationTime = 1
		overflow.Confidence = 0
		overflow.EvaluationTime = time.Duration(1<<63 - 1)
		input.Actions = []Action{first, overflow}
		input.Limits.TimeBudget = time.Duration(1<<63 - 1)
		result, err := Search(input)
		if err != nil {
			t.Fatal(err)
		}
		if result.EvaluationTime != 1 || reasonFor(result, "overflow") != ReasonTimeBudget {
			t.Fatalf("unexpected overflow result: %+v", result)
		}
	})

	t.Run("rounded cost addition", func(t *testing.T) {
		input := baseSearchInput()
		input.Plan.Nodes = []Node{testNode("first", NodeStatusPending), testNode("huge", NodeStatusPending)}
		first, huge := testAction("first"), testAction("huge")
		first.Confidence, first.Cost = 1, 1
		huge.Confidence, huge.Cost = 0, 1e16
		input.Actions = []Action{first, huge}
		input.Limits.CostBudget = 1e16
		result, err := Search(input)
		if err != nil {
			t.Fatal(err)
		}
		assertFrontierContains(t, result, "first")
		if got := reasonFor(result, "huge"); got != ReasonCostBudget {
			t.Fatalf("huge reason = %q, want %q", got, ReasonCostBudget)
		}
	})

	t.Run("exact boundaries", func(t *testing.T) {
		input := baseSearchInput()
		input.Plan.Nodes = []Node{testNode("first", NodeStatusPending), testNode("second", NodeStatusPending)}
		first, second := testAction("first"), testAction("second")
		first.Confidence, first.Cost, first.EvaluationTime = 1, 0.25, time.Millisecond
		second.Confidence, second.Cost, second.EvaluationTime = 0, 0.75, 2*time.Millisecond
		input.Actions = []Action{first, second}
		input.Limits.CostBudget = 1
		input.Limits.TimeBudget = 3 * time.Millisecond
		result, err := Search(input)
		if err != nil {
			t.Fatal(err)
		}
		if got := frontierKeys(result); !reflect.DeepEqual(got, []string{"first", "second"}) || result.EvaluationTime != 3*time.Millisecond {
			t.Fatalf("exact-boundary result = %+v", result)
		}
	})
}

func TestSearchRejectsMalformedLimitsAndScores(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SearchInput, *Action)
	}{
		{name: "zero max nodes", mutate: func(input *SearchInput, _ *Action) { input.Limits.MaxNodes = 0 }},
		{name: "zero beam width", mutate: func(input *SearchInput, _ *Action) { input.Limits.BeamWidth = 0 }},
		{name: "negative depth", mutate: func(input *SearchInput, _ *Action) { input.Limits.MaxDepth = -1 }},
		{name: "negative time", mutate: func(input *SearchInput, _ *Action) { input.Limits.TimeBudget = -1 }},
		{name: "negative evaluation time", mutate: func(_ *SearchInput, action *Action) { action.EvaluationTime = -1 }},
		{name: "nan cost budget", mutate: func(input *SearchInput, _ *Action) { input.Limits.CostBudget = math.NaN() }},
		{name: "infinite cost budget", mutate: func(input *SearchInput, _ *Action) { input.Limits.CostBudget = math.Inf(1) }},
		{name: "nan risk limit", mutate: func(input *SearchInput, _ *Action) { input.Limits.MaxOperationalRisk = math.NaN() }},
		{name: "infinite risk limit", mutate: func(input *SearchInput, _ *Action) { input.Limits.MaxOperationalRisk = math.Inf(1) }},
		{name: "infinite confidence", mutate: func(_ *SearchInput, action *Action) { action.Confidence = math.Inf(1) }},
		{name: "nan action cost", mutate: func(_ *SearchInput, action *Action) { action.Cost = math.NaN() }},
		{name: "infinite action risk", mutate: func(_ *SearchInput, action *Action) { action.OperationalRisk = math.Inf(1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := baseSearchInput()
			input.Plan.Nodes = []Node{testNode("action", NodeStatusPending)}
			action := testAction("action")
			test.mutate(&input, &action)
			input.Actions = []Action{action}
			if _, err := Search(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestSearchExcludesTerminalAndUnsupportedNodes(t *testing.T) {
	input := baseSearchInput()
	input.Plan.Nodes = []Node{
		testNode("done", NodeStatusSucceeded), testNode("failed", NodeStatusFailed), testNode("running", NodeStatusRunning),
		testNode("blocked", NodeStatusBlocked), testNode("unsupported", NodeStatusPending),
	}
	input.Actions = []Action{testAction("done"), testAction("failed"), testAction("running"), testAction("blocked"), {NodeKey: "unsupported"}}
	result, err := Search(input)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]ExclusionReason{
		"done": ReasonTerminalNode, "failed": ReasonTerminalNode, "running": ReasonRunningNode,
		"blocked": ReasonBlockedNode, "unsupported": ReasonUnsupportedAction,
	}
	for key, reason := range want {
		if got := reasonFor(result, key); got != reason {
			t.Errorf("%s reason = %q, want %q", key, got, reason)
		}
	}
}

func TestSearchTerminalPlan(t *testing.T) {
	input := baseSearchInput()
	input.Plan.Status = PlanStatusCompleted
	input.Plan.Nodes = []Node{testNode("action", NodeStatusPending)}
	input.Actions = []Action{testAction("action")}
	result, err := Search(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := reasonFor(result, "action"); got != ReasonTerminalPlan {
		t.Fatalf("reason = %q", got)
	}
}

func TestSearchRejectsForeignFlowEvidence(t *testing.T) {
	input := baseSearchInput()
	input.Plan.Nodes = []Node{testNode("action", NodeStatusPending)}
	input.Actions = []Action{testAction("action")}
	input.Evidence.FlowID = 2
	if _, err := Search(input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}

func baseSearchInput() SearchInput {
	return SearchInput{
		Plan:     Plan{FlowID: 1, Status: PlanStatusActive},
		Evidence: worldstate.PlannerEvidence{FlowID: 1},
		Limits: SearchLimits{
			MaxNodes: 20, MaxDepth: 20, BeamWidth: 20, CostBudget: 20,
			MaxOperationalRisk: 1, TimeBudget: time.Second,
		},
	}
}

func testNode(key string, status NodeStatus) Node {
	return Node{Key: key, Title: key, Kind: NodeKindAction, Status: status}
}

func testAction(key string) Action {
	return Action{NodeKey: key, Supported: true, Confidence: 0.5, ExpectedProgress: 0.5, InformationGain: 0.5, Cost: 0.1, OperationalRisk: 0.1}
}

func entityProperty(entityKey, name string) FactRef {
	return FactRef{Kind: FactEntityProperty, EntityKey: entityKey, Name: name}
}

func reasonFor(result SearchResult, key string) ExclusionReason {
	for _, exclusion := range result.Excluded {
		if exclusion.NodeKey == key {
			return exclusion.Reason
		}
	}
	return ""
}

func detailFor(result SearchResult, key string) string {
	for _, exclusion := range result.Excluded {
		if exclusion.NodeKey == key {
			return exclusion.Detail
		}
	}
	return ""
}

func frontierKeys(result SearchResult) []string {
	keys := make([]string, len(result.Frontier))
	for i, candidate := range result.Frontier {
		keys[i] = candidate.NodeKey
	}
	return keys
}

func assertFrontierContains(t *testing.T, result SearchResult, key string) {
	t.Helper()
	for _, candidate := range result.Frontier {
		if candidate.NodeKey == key {
			return
		}
	}
	t.Fatalf("frontier %v does not contain %q", frontierKeys(result), key)
}
