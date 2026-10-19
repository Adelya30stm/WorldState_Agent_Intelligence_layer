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
	if skipToolIngest(toolName) {
		return
	}
	version, err := ModelVersion(ctx, db, flowID)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).WithField("flow_id", flowID).
			Warn("worldstate: cannot choose ingestion model")
		return
	}
	if version == 2 {
		ingestModelObjects(ctx, db, flowID, toolName, result)
		return
	}

	candidates := ExtractCandidates(toolName, result)
	if len(candidates) == 0 {
		return
	}

	scope, err := LoadScope(ctx, db, flowID)
	if err != nil {
		logrus.WithContext(ctx).WithError(err).WithField("flow_id", flowID).
			Warn("worldstate: load scope failed; ingesting without scope filter")
		scope = Scope{}
	}

	store := NewStore(db)
	agent := AgentExecutor
	active := looksLikeActiveScan(toolName, result)
	hostIDs := map[string]int64{}

	for _, c := range candidates {
		switch ClassifyCandidate(scope, c) {
		case DecisionSkip:
			continue
		case DecisionGrey:
			if err := InsertCandidate(ctx, db, flowID, c, CandidateReasonSimilar); err != nil {
				logrus.WithContext(ctx).WithError(err).WithFields(ingestionFailureFields(flowID, c.Key, toolName)).
					Warn("worldstate: candidate insert failed")
			}
			continue
		}

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

// Discovery only records object locations. A tool transcript containing words
// such as XSS or CVE is not a check, a run, or proof of a vulnerability.
func ingestModelObjects(ctx context.Context, db database.Querier, flowID int64, toolName, result string) {
	store := NewModelStore(db)
	for _, candidate := range ExtractObjectCandidates(toolName, result) {
		_, err := store.ObserveObject(ctx, flowID, AgentExecutor, ObserveObjectInput{
			Type: candidate.Type, Locator: candidate.Locator,
		})
		if err != nil {
			// Avoid including a raw URL or parameters from untrusted tool output
			// in logs; the model store redacts the persisted locator.
			logrus.WithContext(ctx).WithError(err).WithFields(logrus.Fields{
				"flow_id": flowID, "tool": toolName, "object_type": candidate.Type,
			}).Warn("worldstate: object discovery failed")
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

func skipToolIngest(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "world_state_query", "world_state_update",
		"file", "maintenance", "maintenance_result",
		"done", "ask", "subtask_list", "subtask_patch",
		"memorist", "memorist_result",
		"search_guide", "store_guide", "search_answer", "store_answer",
		"search_code", "store_code", "search_in_memory",
		"graphiti_search",
		"google", "duckduckgo", "tavily", "traversaal", "perplexity", "searxng", "sploitus",
		"search", "search_result":
		return true
	default:
		return false
	}
}
