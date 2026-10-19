package attackplan

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"pentagi/pkg/worldstate"
)

type ValueKind string

const (
	ValueBoolean ValueKind = "boolean"
	ValueInteger ValueKind = "integer"
	ValueNumber  ValueKind = "number"
	ValueString  ValueKind = "string"
)

type Value struct {
	Kind    ValueKind
	Boolean bool
	Integer int64
	Number  float64
	String  string
}

type FactKind string

const (
	FactCapturedHead      FactKind = "captured_head"
	FactEntityState       FactKind = "entity_state"
	FactEntityType        FactKind = "entity_type"
	FactEntityVersion     FactKind = "entity_version"
	FactEntityProperty    FactKind = "entity_property"
	FactSummaryStateCount FactKind = "summary_state_count"
	FactSummaryTypeCount  FactKind = "summary_type_count"
)

type FactRef struct {
	Kind      FactKind
	EntityKey string
	Name      string
}

type Predicate string

const (
	PredicateExists    Predicate = "exists"
	PredicateNotExists Predicate = "not_exists"
	PredicateEqual     Predicate = "equal"
	PredicateNotEqual  Predicate = "not_equal"
	PredicateAtLeast   Predicate = "at_least"
	PredicateAtMost    Predicate = "at_most"
)

type Precondition struct {
	Fact      FactRef
	Predicate Predicate
	Value     Value
}

type EffectKind string

const (
	EffectSet    EffectKind = "set"
	EffectDelete EffectKind = "delete"
)

type Effect struct {
	Fact  FactRef
	Kind  EffectKind
	Value Value
}

type Action struct {
	NodeKey          string
	Supported        bool
	Preconditions    []Precondition
	Effects          []Effect
	Confidence       float64
	ExpectedProgress float64
	InformationGain  float64
	Cost             float64
	OperationalRisk  float64
	// EvaluationTime is a deterministic logical charge, never wall-clock time.
	EvaluationTime time.Duration
}

type SearchLimits struct {
	MaxNodes           int
	MaxDepth           int
	BeamWidth          int
	CostBudget         float64
	MaxOperationalRisk float64
	// TimeBudget bounds the sum of action EvaluationTime charges.
	TimeBudget time.Duration
}

type SearchInput struct {
	Plan     Plan
	Actions  []Action
	Evidence worldstate.PlannerEvidence
	Limits   SearchLimits
}

type Score struct {
	Confidence               float64
	ExpectedProgress         float64
	InformationGain          float64
	Cost                     float64
	OperationalRisk          float64
	PrerequisiteSatisfaction float64
	Total                    float64
}

type Candidate struct {
	NodeKey string
	Depth   int
	Score   Score
}

type ExclusionReason string

const (
	ReasonTerminalPlan            ExclusionReason = "terminal_plan"
	ReasonTerminalNode            ExclusionReason = "terminal_node"
	ReasonBlockedNode             ExclusionReason = "blocked_node"
	ReasonRunningNode             ExclusionReason = "running_node"
	ReasonUnsupportedAction       ExclusionReason = "unsupported_action"
	ReasonDepthBudget             ExclusionReason = "depth_budget"
	ReasonNodeBudget              ExclusionReason = "node_budget"
	ReasonTimeBudget              ExclusionReason = "time_budget"
	ReasonCostBudget              ExclusionReason = "cost_budget"
	ReasonRiskBudget              ExclusionReason = "risk_budget"
	ReasonBlockedPrerequisite     ExclusionReason = "blocked_prerequisite"
	ReasonInvalidatedPrerequisite ExclusionReason = "invalidated_prerequisite"
	ReasonBlockedPrecondition     ExclusionReason = "blocked_precondition"
	ReasonContradictoryEvidence   ExclusionReason = "contradictory_evidence"
	ReasonInvalidEffect           ExclusionReason = "invalid_effect"
	ReasonBeamLimit               ExclusionReason = "beam_limit"
)

type Exclusion struct {
	NodeKey string
	Reason  ExclusionReason
	Detail  string
}

type SearchResult struct {
	Frontier       []Candidate
	Excluded       []Exclusion
	EvaluatedNodes int
	EvaluationTime time.Duration
}

func Search(input SearchInput) (SearchResult, error) {
	if err := validateSearchInput(input); err != nil {
		return SearchResult{}, err
	}
	depths, predecessors := graphMetadata(input.Plan.Nodes, input.Plan.Edges)
	facts := evidenceFacts(input.Evidence)
	actions := make(map[string]Action, len(input.Actions))
	for _, action := range input.Actions {
		actions[action.NodeKey] = action
	}

	result := SearchResult{}
	queue := make([]Candidate, 0, len(input.Actions))
	for _, node := range input.Plan.Nodes {
		if node.Kind != NodeKindAction {
			continue
		}
		action, ok := actions[node.Key]
		if reason, detail := staticExclusion(input.Plan.Status, node, action, ok, depths[node.Key], input.Limits); reason != "" {
			result.Excluded = append(result.Excluded, Exclusion{NodeKey: node.Key, Reason: reason, Detail: detail})
			continue
		}
		satisfaction, _, _ := prerequisiteState(predecessors[node.Key])
		queue = append(queue, Candidate{NodeKey: node.Key, Depth: depths[node.Key], Score: actionScore(action, satisfaction)})
	}
	sortCandidates(queue)

	eligible := make([]Candidate, 0, len(queue))
	for index, candidate := range queue {
		action := actions[candidate.NodeKey]
		if index >= input.Limits.MaxNodes {
			result.Excluded = append(result.Excluded, Exclusion{NodeKey: candidate.NodeKey, Reason: ReasonNodeBudget})
			continue
		}
		if action.EvaluationTime > input.Limits.TimeBudget-result.EvaluationTime {
			result.Excluded = append(result.Excluded, Exclusion{NodeKey: candidate.NodeKey, Reason: ReasonTimeBudget})
			continue
		}
		result.EvaluatedNodes++
		result.EvaluationTime += action.EvaluationTime
		_, prerequisiteReason, detail := prerequisiteState(predecessors[candidate.NodeKey])
		if prerequisiteReason != "" {
			result.Excluded = append(result.Excluded, Exclusion{NodeKey: candidate.NodeKey, Reason: prerequisiteReason, Detail: detail})
			continue
		}
		if reason, detail := evaluateAction(action, facts, input.Limits); reason != "" {
			result.Excluded = append(result.Excluded, Exclusion{NodeKey: candidate.NodeKey, Reason: reason, Detail: detail})
			continue
		}
		eligible = append(eligible, candidate)
	}

	usedCost := 0.0
	for _, candidate := range eligible {
		action := actions[candidate.NodeKey]
		switch {
		case len(result.Frontier) >= input.Limits.BeamWidth:
			result.Excluded = append(result.Excluded, Exclusion{NodeKey: candidate.NodeKey, Reason: ReasonBeamLimit})
		case action.Cost > input.Limits.CostBudget || usedCost > input.Limits.CostBudget-action.Cost:
			result.Excluded = append(result.Excluded, Exclusion{NodeKey: candidate.NodeKey, Reason: ReasonCostBudget})
		default:
			result.Frontier = append(result.Frontier, candidate)
			usedCost += action.Cost
		}
	}
	sort.Slice(result.Excluded, func(i, j int) bool { return result.Excluded[i].NodeKey < result.Excluded[j].NodeKey })
	return result, nil
}

func validateSearchInput(input SearchInput) error {
	if input.Plan.FlowID <= 0 || !input.Plan.Status.isKnown() || input.Limits.MaxNodes <= 0 || input.Limits.MaxDepth < 0 || input.Limits.BeamWidth <= 0 ||
		input.Limits.CostBudget < 0 || !finite(input.Limits.CostBudget) || !unit(input.Limits.MaxOperationalRisk) || input.Limits.TimeBudget < 0 {
		return ErrInvalidInput
	}
	if err := validateGraph(input.Plan.Nodes, input.Plan.Edges); err != nil {
		return err
	}
	nodes := make(map[string]Node, len(input.Plan.Nodes))
	for _, node := range input.Plan.Nodes {
		nodes[node.Key] = node
	}
	seenActions := make(map[string]struct{}, len(input.Actions))
	for _, action := range input.Actions {
		node, ok := nodes[action.NodeKey]
		if !ok || node.Kind != NodeKindAction || action.EvaluationTime < 0 {
			return fmt.Errorf("%w: invalid action %q", ErrInvalidInput, action.NodeKey)
		}
		if _, exists := seenActions[action.NodeKey]; exists {
			return fmt.Errorf("%w: duplicate action %q", ErrInvalidInput, action.NodeKey)
		}
		seenActions[action.NodeKey] = struct{}{}
		if action.Supported && !validScoreInputs(action) {
			return fmt.Errorf("%w: invalid score inputs for %q", ErrInvalidInput, action.NodeKey)
		}
	}
	if input.Evidence.FlowID != input.Plan.FlowID || input.Evidence.CapturedHead < 0 || input.Evidence.Journal.AfterRevision < 0 ||
		input.Evidence.Journal.AfterRevision > input.Evidence.Journal.ThroughRevision || input.Evidence.Journal.ThroughRevision != input.Evidence.CapturedHead {
		return fmt.Errorf("%w: invalid planner evidence revision", ErrInvalidInput)
	}
	return nil
}

func graphMetadata(nodes []Node, edges []Edge) (map[string]int, map[string][]predecessor) {
	depths := make(map[string]int, len(nodes))
	indegree := make(map[string]int, len(nodes))
	adjacency := make(map[string][]Edge, len(nodes))
	predecessors := make(map[string][]predecessor, len(nodes))
	byKey := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		byKey[node.Key] = node
		indegree[node.Key] = 0
	}
	for _, edge := range edges {
		indegree[edge.ToKey]++
		adjacency[edge.FromKey] = append(adjacency[edge.FromKey], edge)
		predecessors[edge.ToKey] = append(predecessors[edge.ToKey], predecessor{node: byKey[edge.FromKey], kind: edge.Kind})
	}
	queue := make([]string, 0, len(nodes))
	for key, degree := range indegree {
		if degree == 0 {
			queue = append(queue, key)
		}
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		for _, edge := range adjacency[key] {
			if depths[edge.ToKey] < depths[key]+1 {
				depths[edge.ToKey] = depths[key] + 1
			}
			indegree[edge.ToKey]--
			if indegree[edge.ToKey] == 0 {
				queue = append(queue, edge.ToKey)
				sort.Strings(queue)
			}
		}
	}
	return depths, predecessors
}

type predecessor struct {
	node Node
	kind EdgeKind
}

func prerequisiteState(items []predecessor) (float64, ExclusionReason, string) {
	if len(items) == 0 {
		return 1, "", ""
	}
	satisfiedClauses, clauseCount := 0, 0
	orCount, orSucceeded, orPossible := 0, false, false
	blockedKey, invalidatedKey := "", ""
	for _, item := range items {
		if item.kind == EdgeKindOr {
			orCount++
			if item.node.Status == NodeStatusSucceeded {
				orSucceeded = true
			} else if !item.node.Status.IsTerminal() {
				orPossible = true
			}
			continue
		}
		clauseCount++
		if item.node.Status == NodeStatusSucceeded {
			satisfiedClauses++
		}
		if item.node.Status.IsTerminal() && item.node.Status != NodeStatusSucceeded {
			invalidatedKey = firstKey(invalidatedKey, item.node.Key)
		}
		if item.node.Status != NodeStatusSucceeded {
			blockedKey = firstKey(blockedKey, item.node.Key)
		}
	}
	if orCount > 0 {
		clauseCount++
		if orSucceeded {
			satisfiedClauses++
		} else if !orPossible {
			invalidatedKey = firstKey(invalidatedKey, "or")
		}
	}
	satisfaction := float64(satisfiedClauses) / float64(clauseCount)
	if invalidatedKey != "" {
		return satisfaction, ReasonInvalidatedPrerequisite, invalidatedKey
	}
	if orCount > 0 && !orSucceeded {
		if orPossible {
			blockedKey = firstKey(blockedKey, "or")
		}
	}
	if blockedKey != "" {
		return satisfaction, ReasonBlockedPrerequisite, blockedKey
	}
	return satisfaction, "", ""
}

func firstKey(current, candidate string) string {
	if current == "" || candidate < current {
		return candidate
	}
	return current
}

func staticExclusion(status PlanStatus, node Node, action Action, exists bool, depth int, limits SearchLimits) (ExclusionReason, string) {
	switch {
	case status.IsTerminal():
		return ReasonTerminalPlan, string(status)
	case node.Status.IsTerminal():
		return ReasonTerminalNode, string(node.Status)
	case node.Status == NodeStatusBlocked:
		return ReasonBlockedNode, ""
	case node.Status == NodeStatusRunning:
		return ReasonRunningNode, ""
	case !exists || !action.Supported:
		return ReasonUnsupportedAction, ""
	case depth > limits.MaxDepth:
		return ReasonDepthBudget, ""
	default:
		return "", ""
	}
}

type evidenceFact struct {
	value         Value
	contradictory bool
}

func evidenceFacts(evidence worldstate.PlannerEvidence) map[FactRef]evidenceFact {
	facts := make(map[FactRef]evidenceFact)
	addEvidenceFact(facts, FactRef{Kind: FactCapturedHead}, integerValue(evidence.CapturedHead), false)
	for state, count := range evidence.Projection.Summary.ByState {
		addEvidenceFact(facts, FactRef{Kind: FactSummaryStateCount, Name: state}, integerValue(int64(count)), false)
	}
	for typ, count := range evidence.Projection.Summary.ByType {
		addEvidenceFact(facts, FactRef{Kind: FactSummaryTypeCount, Name: typ}, integerValue(int64(count)), false)
	}
	entityCounts := make(map[string]int, len(evidence.Projection.Entities))
	for _, entity := range evidence.Projection.Entities {
		entityCounts[entity.Key]++
	}
	for _, entity := range evidence.Projection.Entities {
		contradictory := entityCounts[entity.Key] > 1
		addEvidenceFact(facts, FactRef{Kind: FactEntityState, EntityKey: entity.Key}, stringValue(entity.State), contradictory)
		addEvidenceFact(facts, FactRef{Kind: FactEntityType, EntityKey: entity.Key}, stringValue(entity.Type), contradictory)
		if evidence.SchemaVersion != 2 {
			addEvidenceFact(facts, FactRef{Kind: FactEntityVersion, EntityKey: entity.Key}, integerValue(int64(entity.Version)), contradictory)
		}
		for name, raw := range entity.Properties {
			if value, ok := evidenceValue(raw); ok {
				addEvidenceFact(facts, FactRef{Kind: FactEntityProperty, EntityKey: entity.Key, Name: name}, value, contradictory)
			}
		}
	}
	return facts
}

func addEvidenceFact(facts map[FactRef]evidenceFact, ref FactRef, value Value, contradictory bool) {
	if current, exists := facts[ref]; exists {
		contradictory = contradictory || current.contradictory || !valuesEqual(current.value, value)
	}
	facts[ref] = evidenceFact{value: value, contradictory: contradictory}
}

func evidenceValue(value any) (Value, bool) {
	switch typed := value.(type) {
	case bool:
		return Value{Kind: ValueBoolean, Boolean: typed}, true
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integerValue(integer), true
		}
		if !strings.ContainsAny(string(typed), ".eE") {
			return Value{}, false
		}
		number, err := typed.Float64()
		return numberValue(number), err == nil && finite(number)
	case float64:
		return numberValue(typed), finite(typed)
	case string:
		return stringValue(typed), true
	default:
		return Value{}, false
	}
}

func evaluateAction(action Action, facts map[FactRef]evidenceFact, limits SearchLimits) (ExclusionReason, string) {
	if action.OperationalRisk > limits.MaxOperationalRisk {
		return ReasonRiskBudget, ""
	}
	for _, condition := range action.Preconditions {
		fact, exists := facts[condition.Fact]
		if exists && fact.contradictory {
			return ReasonContradictoryEvidence, condition.Fact.String()
		}
		matched, valid := matches(condition, fact, exists)
		if !valid {
			return ReasonUnsupportedAction, condition.Fact.String()
		}
		if !matched {
			return ReasonBlockedPrecondition, condition.Fact.String()
		}
	}
	seenEffects := make(map[FactRef]struct{}, len(action.Effects))
	for _, effect := range action.Effects {
		if _, exists := seenEffects[effect.Fact]; exists {
			return ReasonInvalidEffect, effect.Fact.String()
		}
		seenEffects[effect.Fact] = struct{}{}
		if !effect.Fact.Valid() || (effect.Kind != EffectSet && effect.Kind != EffectDelete) || (effect.Kind == EffectSet && !validValue(effect.Value)) {
			return ReasonInvalidEffect, effect.Fact.String()
		}
		if fact, exists := facts[effect.Fact]; exists && fact.contradictory {
			return ReasonContradictoryEvidence, effect.Fact.String()
		}
	}
	return "", ""
}

func matches(condition Precondition, fact evidenceFact, exists bool) (bool, bool) {
	if !condition.Fact.Valid() {
		return false, false
	}
	switch condition.Predicate {
	case PredicateExists:
		return exists, true
	case PredicateNotExists:
		return !exists, true
	case PredicateEqual, PredicateNotEqual:
		if !validValue(condition.Value) {
			return false, false
		}
		equal := exists && valuesEqual(fact.value, condition.Value)
		if condition.Predicate == PredicateNotEqual {
			return exists && !equal, true
		}
		return equal, true
	case PredicateAtLeast, PredicateAtMost:
		if (condition.Value.Kind != ValueInteger && condition.Value.Kind != ValueNumber) || !validValue(condition.Value) {
			return false, false
		}
		if !exists || fact.value.Kind != condition.Value.Kind {
			return false, true
		}
		if condition.Value.Kind == ValueInteger {
			if condition.Predicate == PredicateAtLeast {
				return fact.value.Integer >= condition.Value.Integer, true
			}
			return fact.value.Integer <= condition.Value.Integer, true
		}
		if condition.Predicate == PredicateAtLeast {
			return fact.value.Number >= condition.Value.Number, true
		}
		return fact.value.Number <= condition.Value.Number, true
	default:
		return false, false
	}
}

func actionScore(action Action, prerequisites float64) Score {
	total := action.Confidence + action.ExpectedProgress + action.InformationGain + prerequisites - action.Cost - action.OperationalRisk
	return Score{Confidence: action.Confidence, ExpectedProgress: action.ExpectedProgress, InformationGain: action.InformationGain,
		Cost: action.Cost, OperationalRisk: action.OperationalRisk, PrerequisiteSatisfaction: prerequisites, Total: total}
}

func sortCandidates(candidates []Candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score.Total != candidates[j].Score.Total {
			return candidates[i].Score.Total > candidates[j].Score.Total
		}
		return candidates[i].NodeKey < candidates[j].NodeKey
	})
}

func validScoreInputs(action Action) bool {
	return unit(action.Confidence) && unit(action.ExpectedProgress) && unit(action.InformationGain) && action.Cost >= 0 && finite(action.Cost) && unit(action.OperationalRisk)
}

func validValue(value Value) bool {
	return value.Kind == ValueBoolean || value.Kind == ValueInteger || value.Kind == ValueString || (value.Kind == ValueNumber && finite(value.Number))
}

func (ref FactRef) Valid() bool {
	switch ref.Kind {
	case FactCapturedHead:
		return ref.EntityKey == "" && ref.Name == ""
	case FactEntityState, FactEntityType, FactEntityVersion:
		return ref.EntityKey != "" && ref.Name == ""
	case FactEntityProperty:
		return ref.EntityKey != "" && ref.Name != ""
	case FactSummaryStateCount, FactSummaryTypeCount:
		return ref.EntityKey == "" && ref.Name != ""
	default:
		return false
	}
}

func (ref FactRef) String() string {
	if ref.EntityKey != "" && ref.Name != "" {
		return string(ref.Kind) + ":" + ref.EntityKey + ":" + ref.Name
	}
	if ref.EntityKey != "" {
		return string(ref.Kind) + ":" + ref.EntityKey
	}
	if ref.Name != "" {
		return string(ref.Kind) + ":" + ref.Name
	}
	return string(ref.Kind)
}

func valuesEqual(left, right Value) bool {
	if left.Kind != right.Kind {
		return false
	}
	switch left.Kind {
	case ValueBoolean:
		return left.Boolean == right.Boolean
	case ValueInteger:
		return left.Integer == right.Integer
	case ValueNumber:
		return left.Number == right.Number
	case ValueString:
		return left.String == right.String
	default:
		return false
	}
}

func unit(value float64) bool         { return value >= 0 && value <= 1 && finite(value) }
func finite(value float64) bool       { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func integerValue(value int64) Value  { return Value{Kind: ValueInteger, Integer: value} }
func numberValue(value float64) Value { return Value{Kind: ValueNumber, Number: value} }
func stringValue(value string) Value  { return Value{Kind: ValueString, String: value} }
