package worldstate

import (
	"context"
	"strings"

	"pentagi/pkg/database"

	"github.com/sirupsen/logrus"
)

// IngestToolResult extracts entities from a finished tool call and writes them
// into World State. Errors are logged and never returned to the caller — tool
// execution must not fail because of state ingestion.
func IngestToolResult(ctx context.Context, db database.Querier, flowID int64, toolName, result string) {
	if db == nil || flowID <= 0 || strings.TrimSpace(result) == "" {
		return
	}

	candidates := ExtractCandidates(toolName, result)
	if len(candidates) == 0 {
		return
	}

	store := NewStore(db)
	agent := AgentExecutor
	active := looksLikeActiveScan(toolName, result)
	hostIDs := map[string]int64{}

	for _, c := range candidates {
		entityKey := safeEntityKey(c.Key)
		evidence := map[string]any{
			"tool":   toolName,
			"source": "tool_result",
		}
		entity, err := store.Observe(ctx, flowID, c.Type, entityKey, agent, c.Properties, evidence)
		if err != nil {
			logrus.WithContext(ctx).WithError(err).WithFields(ingestionFailureFields(flowID, entityKey, toolName)).Warn("worldstate: observe failed")
			continue
		}

		if c.Type == EntityTypeHost {
			hostIDs[entityKey] = entity.ID
			if active {
				if err := store.PromoteScanning(ctx, entity.ID, agent, evidence); err != nil {
					logrus.WithContext(ctx).WithError(err).WithField("entity_id", entity.ID).
						Warn("worldstate: promote scanning failed")
				}
			}
		}
	}

	for _, c := range candidates {
		if c.Type != EntityTypeEndpoint && c.Type != EntityTypeService {
			continue
		}
		kind := "found_on"
		if c.Type == EntityTypeService {
			kind = "has_service"
		}
		host, _ := c.Properties["host"].(string)
		if host == "" {
			continue
		}
		hostKey := "host:" + host
		hostID, ok := hostIDs[hostKey]
		if !ok {
			existing, err := db.GetWorldStateEntityByKey(ctx, database.GetWorldStateEntityByKeyParams{
				FlowID:    flowID,
				EntityKey: hostKey,
			})
			if err != nil {
				continue
			}
			hostID = existing.ID
		}
		child, err := db.GetWorldStateEntityByKey(ctx, database.GetWorldStateEntityByKeyParams{
			FlowID:    flowID,
			EntityKey: c.Key,
		})
		if err != nil {
			continue
		}
		src, dst := child.ID, hostID
		if c.Type == EntityTypeService {
			src, dst = hostID, child.ID
		}
		if _, err := store.Link(ctx, flowID, src, dst, kind, map[string]any{
			"tool": toolName,
		}); err != nil {
			logrus.WithContext(ctx).WithError(err).Warn("worldstate: link failed")
		}
	}
}

func ingestionFailureFields(flowID int64, entityKey, toolName string) logrus.Fields {
	return logrus.Fields{
		"flow_id":    flowID,
		"entity_key": safeEntityKey(entityKey),
		"tool":       toolName,
	}
}
