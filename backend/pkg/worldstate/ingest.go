package worldstate

import (
	"context"
	"strings"

	"pentagi/pkg/database"

	"github.com/sirupsen/logrus"
)

// IngestToolResult records object locations from a finished tool call.
// Errors are logged and never returned to the caller — tool execution must
// not fail because of state ingestion.
func IngestToolResult(ctx context.Context, db database.Querier, flowID int64, toolName, result string) {
	if db == nil || flowID <= 0 || strings.TrimSpace(result) == "" {
		return
	}
	if skipToolIngest(toolName) {
		return
	}
	ingestModelObjects(ctx, db, flowID, toolName, result)
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
