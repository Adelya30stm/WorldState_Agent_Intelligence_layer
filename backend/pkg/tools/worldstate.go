package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pentagi/pkg/database"
	"pentagi/pkg/worldstate"
)

const maxWorldStateUpdateBatch = 50

type worldStateTool struct {
	flowID int64
	db     database.Querier
	agent  string
}

type worldStateMutation struct {
	Type       string
	EntityKey  string
	ToState    worldstate.State
	Properties map[string]any
}

// NewWorldStateTool exposes world_state_query / world_state_update to agents.
func NewWorldStateTool(flowID int64, db database.Querier, agent string) Tool {
	if agent == "" {
		agent = worldstate.AgentExecutor
	}
	return &worldStateTool{flowID: flowID, db: db, agent: agent}
}

func (w *worldStateTool) IsAvailable() bool {
	return w.db != nil && w.flowID > 0
}

func (w *worldStateTool) Handle(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if !w.IsAvailable() {
		return "", fmt.Errorf("world state tool is not available")
	}
	trimmed := strings.TrimSpace(string(args))
	if !strings.HasPrefix(trimmed, "{") {
		return "", fmt.Errorf("world state arguments must be a JSON object")
	}

	switch name {
	case WorldStateQueryToolName:
		var action WorldStateQueryAction
		if err := json.Unmarshal(args, &action); err != nil {
			return "", fmt.Errorf("failed to unmarshal world_state_query args: %w", err)
		}
		return w.queryModel(ctx, action)
	case WorldStateUpdateToolName:
		var action WorldStateUpdateAction
		if !json.Valid(args) {
			return "", fmt.Errorf("world_state_update args must be valid JSON")
		}
		decoder := json.NewDecoder(bytes.NewReader(args))
		decoder.UseNumber()
		if err := decoder.Decode(&action); err != nil {
			return "", fmt.Errorf("failed to unmarshal world_state_update args: %w", err)
		}
		return w.updateModel(ctx, action)
	default:
		return "", fmt.Errorf("unknown world state tool: %s", name)
	}
}

func worldStateFilterSet(one string, many []string) map[string]struct{} {
	out := map[string]struct{}{}
	if s := strings.TrimSpace(strings.ToLower(one)); s != "" {
		out[s] = struct{}{}
	}
	for _, raw := range many {
		if s := strings.TrimSpace(strings.ToLower(raw)); s != "" {
			out[s] = struct{}{}
		}
	}
	return out
}

func worldStateQueryMatches(e database.WorldStateEntity, types, states map[string]struct{}) bool {
	if len(types) > 0 {
		if _, ok := types[strings.ToLower(e.Type)]; !ok {
			return false
		}
	}
	if len(states) > 0 {
		if _, ok := states[strings.ToLower(string(e.State))]; !ok {
			return false
		}
	}
	return true
}

func worldStateMutations(action WorldStateUpdateAction) ([]worldStateMutation, error) {
	if len(action.Entities) > 0 {
		if len(action.Entities) > maxWorldStateUpdateBatch {
			return nil, fmt.Errorf("entities batch exceeds %d (got %d)", maxWorldStateUpdateBatch, len(action.Entities))
		}
		out := make([]worldStateMutation, 0, len(action.Entities))
		for i, item := range action.Entities {
			m, err := normalizeWorldStateMutation(item.Type, item.EntityKey, item.ToState, item.Properties)
			if err != nil {
				return nil, fmt.Errorf("entities[%d]: %w", i, err)
			}
			out = append(out, m)
		}
		return out, nil
	}
	m, err := normalizeWorldStateMutation(action.Type, action.EntityKey, action.ToState, action.Properties)
	if err != nil {
		return nil, err
	}
	return []worldStateMutation{m}, nil
}

func normalizeWorldStateMutation(entityType, entityKey, toStateRaw, properties string) (worldStateMutation, error) {
	entityType = strings.TrimSpace(entityType)
	entityKey = strings.TrimSpace(entityKey)
	toState := worldstate.State(strings.TrimSpace(strings.ToLower(toStateRaw)))
	if entityType == "" || entityKey == "" {
		return worldStateMutation{}, fmt.Errorf("type and entity_key are required")
	}
	if !toState.IsKnown() {
		return worldStateMutation{}, fmt.Errorf("unknown to_state %q", toStateRaw)
	}
	if !strings.Contains(entityKey, ":") {
		entityKey = entityType + ":" + entityKey
	}
	props := map[string]any{}
	if strings.TrimSpace(properties) != "" {
		if err := json.Unmarshal([]byte(properties), &props); err != nil {
			return worldStateMutation{}, fmt.Errorf("properties must be a JSON object string: %w", err)
		}
	}
	return worldStateMutation{
		Type:       entityType,
		EntityKey:  entityKey,
		ToState:    toState,
		Properties: props,
	}, nil
}
