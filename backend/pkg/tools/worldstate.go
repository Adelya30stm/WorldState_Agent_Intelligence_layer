package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pentagi/pkg/database"
	"pentagi/pkg/worldstate"

	"github.com/sirupsen/logrus"
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

	logger := logrus.WithContext(ctx).WithFields(logrus.Fields{
		"flow_id": w.flowID,
		"tool":    name,
		"agent":   w.agent,
	})

	switch name {
	case WorldStateQueryToolName:
		var action WorldStateQueryAction
		if err := json.Unmarshal(args, &action); err != nil {
			return "", fmt.Errorf("failed to unmarshal world_state_query args: %w", err)
		}
		return w.query(ctx, action)
	case WorldStateUpdateToolName:
		var action WorldStateUpdateAction
		if err := json.Unmarshal(args, &action); err != nil {
			return "", fmt.Errorf("failed to unmarshal world_state_update args: %w", err)
		}
		return w.update(ctx, logger, action)
	default:
		return "", fmt.Errorf("unknown world state tool: %s", name)
	}
}

func (w *worldStateTool) query(ctx context.Context, action WorldStateQueryAction) (string, error) {
	entities, err := w.db.GetWorldStateEntitiesByFlow(ctx, w.flowID)
	if err != nil {
		return "", fmt.Errorf("failed to load world state: %w", err)
	}

	types := worldStateFilterSet(action.Type, action.Types)
	states := worldStateFilterSet(action.State, action.States)

	filtered := make([]database.WorldStateEntity, 0, len(entities))
	for _, e := range entities {
		if !worldStateQueryMatches(e, types, states) {
			continue
		}
		filtered = append(filtered, e)
	}

	type row struct {
		Key        string          `json:"entity_key"`
		Type       string          `json:"type"`
		State      string          `json:"state"`
		Properties json.RawMessage `json:"properties"`
	}
	out := make([]row, 0, len(filtered))
	for _, e := range filtered {
		props := e.Properties
		if len(props) == 0 {
			props = json.RawMessage(`{}`)
		}
		out = append(out, row{
			Key:        e.EntityKey,
			Type:       e.Type,
			State:      string(e.State),
			Properties: props,
		})
	}

	payload, err := json.MarshalIndent(map[string]any{
		"count":    len(out),
		"entities": out,
		"hint":     "Use these entities before choosing the next recon/exploit action to avoid duplicates. This is a function tool, not a shell command.",
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (w *worldStateTool) update(ctx context.Context, logger *logrus.Entry, action WorldStateUpdateAction) (string, error) {
	mutations, err := worldStateMutations(action)
	if err != nil {
		return "", err
	}

	batch := len(action.Entities) > 0
	results := make([]map[string]any, 0, len(mutations))
	applied, failed := 0, 0
	var firstErr error
	for _, m := range mutations {
		row, err := w.applyMutation(ctx, logger, m, action.Message)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			results = append(results, map[string]any{
				"ok":         false,
				"entity_key": m.EntityKey,
				"error":      err.Error(),
			})
			continue
		}
		applied++
		results = append(results, row)
	}

	if !batch {
		if firstErr != nil {
			return "", firstErr
		}
		payload, err := json.MarshalIndent(results[0], "", "  ")
		if err != nil {
			return "", err
		}
		return string(payload), nil
	}

	payload, err := json.MarshalIndent(map[string]any{
		"ok":      failed == 0,
		"applied": applied,
		"failed":  failed,
		"results": results,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (w *worldStateTool) applyMutation(ctx context.Context, logger *logrus.Entry, m worldStateMutation, message string) (map[string]any, error) {
	evidence := map[string]any{
		"source":  "world_state_update",
		"message": message,
	}

	store := worldstate.NewStore(w.db)
	entity, err := store.Observe(ctx, w.flowID, m.Type, m.EntityKey, w.agent, m.Properties, evidence)
	if err != nil {
		logger.WithError(err).Error("world_state_update observe failed")
		return nil, fmt.Errorf("failed to observe entity: %w", err)
	}

	current := worldstate.State(entity.State)
	if current != m.ToState {
		ev, _ := json.Marshal(evidence)
		entity, err = store.Transition(ctx, entity.ID, m.ToState, w.agent, ev)
		if err != nil {
			logger.WithError(err).WithFields(logrus.Fields{
				"from": current,
				"to":   m.ToState,
				"key":  m.EntityKey,
			}).Warn("world_state_update transition failed")
			return nil, fmt.Errorf("entity %s is in state %s; cannot transition to %s: %w", m.EntityKey, current, m.ToState, err)
		}
	}

	return map[string]any{
		"ok":         true,
		"entity_key": entity.EntityKey,
		"type":       entity.Type,
		"state":      entity.State,
		"id":         entity.ID,
	}, nil
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
