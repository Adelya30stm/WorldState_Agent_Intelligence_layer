package attackplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func validateGraph(nodes []Node, edges []Edge) error {
	byKey := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		if err := validateNode(node); err != nil {
			return err
		}
		if _, exists := byKey[node.Key]; exists {
			return fmt.Errorf("%w: duplicate node key %q", ErrInvalidInput, node.Key)
		}
		byKey[node.Key] = node
	}

	adjacency := make(map[string][]string, len(nodes))
	indegree := make(map[string]int, len(nodes))
	for key := range byKey {
		indegree[key] = 0
	}
	seen := make(map[string]struct{}, len(edges))
	for _, edge := range edges {
		if !edge.Kind.isKnown() || edge.FromKey == edge.ToKey {
			return fmt.Errorf("%w: invalid edge %q -> %q", ErrInvalidInput, edge.FromKey, edge.ToKey)
		}
		if _, ok := byKey[edge.FromKey]; !ok {
			return fmt.Errorf("%w: missing source node %q", ErrInvalidInput, edge.FromKey)
		}
		if _, ok := byKey[edge.ToKey]; !ok {
			return fmt.Errorf("%w: missing target node %q", ErrInvalidInput, edge.ToKey)
		}
		logicalKey := edge.FromKey + "\x00" + edge.ToKey + "\x00" + string(edge.Kind)
		if _, exists := seen[logicalKey]; exists {
			return fmt.Errorf("%w: duplicate edge %q -> %q", ErrInvalidInput, edge.FromKey, edge.ToKey)
		}
		seen[logicalKey] = struct{}{}
		adjacency[edge.FromKey] = append(adjacency[edge.FromKey], edge.ToKey)
		indegree[edge.ToKey]++
	}

	queue := make([]string, 0, len(nodes))
	for key, degree := range indegree {
		if degree == 0 {
			queue = append(queue, key)
		}
	}
	visited := 0
	for len(queue) > 0 {
		key := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		visited++
		for _, target := range adjacency[key] {
			indegree[target]--
			if indegree[target] == 0 {
				queue = append(queue, target)
			}
		}
	}
	if visited != len(nodes) {
		return ErrCycle
	}
	return nil
}

func validateNode(node Node) error {
	if strings.TrimSpace(node.Key) == "" || strings.TrimSpace(node.Title) == "" || !node.Kind.isKnown() || !node.Status.isKnown() {
		return fmt.Errorf("%w: invalid node %q", ErrInvalidInput, node.Key)
	}
	payload := bytes.TrimSpace(node.Payload)
	if len(payload) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return fmt.Errorf("%w: node %q payload must be a JSON object", ErrInvalidInput, node.Key)
	}
	return nil
}

func validateNodeTransition(from, to NodeStatus) error {
	if !from.isKnown() || !to.isKnown() {
		return fmt.Errorf("%w: unknown node status", ErrInvalidInput)
	}
	if from.IsTerminal() && from != to {
		return ErrInvalidTransition
	}
	return nil
}

func validatePlanTransition(from, to PlanStatus) error {
	if !from.isKnown() || !to.isKnown() {
		return fmt.Errorf("%w: unknown plan status", ErrInvalidInput)
	}
	if from.IsTerminal() && from != to {
		return ErrInvalidTransition
	}
	return nil
}

func (s PlanStatus) isKnown() bool {
	return s == PlanStatusDraft || s == PlanStatusActive || s.IsTerminal()
}

func (k NodeKind) isKnown() bool { return k == NodeKindGoal || k == NodeKindAction }

func (s NodeStatus) isKnown() bool {
	return s == NodeStatusPending || s == NodeStatusReady || s == NodeStatusRunning || s == NodeStatusBlocked || s.IsTerminal()
}

func (k EdgeKind) isKnown() bool {
	return k == EdgeKindAnd || k == EdgeKindOr || k == EdgeKindDependency
}
