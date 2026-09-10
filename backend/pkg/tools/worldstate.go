package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pentagi/pkg/database"
	"pentagi/pkg/worldstate"

	"github.com/sirupsen/logrus"
)

type worldStateTool struct {
	flowID int64
	db     database.Querier
	agent  string
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
	version, err := worldstate.ModelVersion(ctx, w.db, w.flowID)
	if err != nil {
		return "", fmt.Errorf("failed to load world state model version: %w", err)
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
		if version == 2 {
			return w.queryModel(ctx, action)
		}
		return w.query(ctx, action)
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
		if version == 2 {
			return w.updateModel(ctx, action)
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

	typeFilter := strings.TrimSpace(strings.ToLower(action.Type))
	stateFilter := strings.TrimSpace(strings.ToLower(action.State))

	filtered := make([]database.WorldStateEntity, 0, len(entities))
	for _, e := range entities {
		if typeFilter != "" && !strings.EqualFold(e.Type, typeFilter) {
			continue
		}
		if stateFilter != "" && !strings.EqualFold(string(e.State), stateFilter) {
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
		"schema_version": 1,
		"count":          len(out),
		"entities":       out,
		"hint":           "Use these entities before choosing the next recon/exploit action to avoid duplicates.",
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (w *worldStateTool) update(ctx context.Context, logger *logrus.Entry, action WorldStateUpdateAction) (string, error) {
	entityType := strings.TrimSpace(action.Type)
	entityKey := strings.TrimSpace(action.EntityKey)
	toState := worldstate.State(strings.TrimSpace(strings.ToLower(action.ToState)))
	if entityType == "" || entityKey == "" {
		return "", fmt.Errorf("type and entity_key are required")
	}
	if !toState.IsKnown() {
		return "", fmt.Errorf("unknown to_state %q", action.ToState)
	}
	if !strings.Contains(entityKey, ":") {
		entityKey = entityType + ":" + entityKey
	}

	props := map[string]any{}
	if strings.TrimSpace(action.Properties) != "" {
		if err := json.Unmarshal([]byte(action.Properties), &props); err != nil {
			return "", fmt.Errorf("properties must be a JSON object string: %w", err)
		}
	}

	evidence := map[string]any{
		"source":  "world_state_update",
		"message": action.Message,
	}

	store := worldstate.NewStore(w.db)
	entity, err := store.Observe(ctx, w.flowID, entityType, entityKey, w.agent, props, evidence)
	if err != nil {
		logger.WithError(err).Error("world_state_update observe failed")
		return "", fmt.Errorf("failed to observe entity: %w", err)
	}

	current := worldstate.State(entity.State)
	if current != toState {
		ev, _ := json.Marshal(evidence)
		entity, err = store.Transition(ctx, entity.ID, toState, w.agent, ev)
		if err != nil {
			logger.WithError(err).WithFields(logrus.Fields{
				"from": current,
				"to":   toState,
				"key":  entityKey,
			}).Warn("world_state_update transition failed")
			return "", fmt.Errorf("entity %s is in state %s; cannot transition to %s: %w", entityKey, current, toState, err)
		}
	}

	payload, err := json.MarshalIndent(map[string]any{
		"ok":         true,
		"entity_key": entity.EntityKey,
		"type":       entity.Type,
		"state":      entity.State,
		"id":         entity.ID,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload), nil
}
