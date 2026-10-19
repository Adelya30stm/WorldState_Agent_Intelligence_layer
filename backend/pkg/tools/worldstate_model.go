package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"pentagi/pkg/worldstate"
)

// The executor supplies identity independently of the model's arguments. A retry
// may create another toolcalls row, but keeps the logical call ID and run identity.
type worldStateInvocationKey struct{}
type worldStateChainKey struct{}

// WithWorldStateChain scopes provider-generated call IDs to their message chain.
// Different specialists can legitimately use the same call ID in one subtask.
func WithWorldStateChain(ctx context.Context, chainID int64) context.Context {
	return context.WithValue(ctx, worldStateChainKey{}, chainID)
}

type worldStateInvocation struct {
	CallID     string
	ToolCallID int64
	TaskID     int64
	SubtaskID  int64
}

func worldStateRequestKey(ctx context.Context, flowID int64) (string, error) {
	invocation, ok := ctx.Value(worldStateInvocationKey{}).(worldStateInvocation)
	if !ok || strings.TrimSpace(invocation.CallID) == "" {
		return "", fmt.Errorf("start_run requires a stable tool invocation identity supplied by the executor")
	}
	chainID, _ := ctx.Value(worldStateChainKey{}).(int64)
	return fmt.Sprintf("flow:%d/task:%d/subtask:%d/chain:%d/call:%s", flowID, invocation.TaskID, invocation.SubtaskID, chainID, invocation.CallID), nil
}

func (w *worldStateTool) updateModel(ctx context.Context, action WorldStateUpdateAction) (string, error) {
	if err := validateModelUpdate(action); err != nil {
		return "", err
	}
	store := worldstate.NewModelStore(w.db)
	var result any
	var err error
	switch action.Operation {
	case "observe_object":
		result, err = store.ObserveObject(ctx, w.flowID, w.agent, worldstate.ObserveObjectInput{
			ObjectKey: action.ObjectKey, Type: action.Type, Locator: action.Locator,
		})
	case "ensure_check":
		result, err = store.EnsureCheck(ctx, w.flowID, action.ObjectID.Int64(), action.Kind, w.agent)
	case "start_run":
		requestKey, keyErr := worldStateRequestKey(ctx, w.flowID)
		if keyErr != nil {
			return "", keyErr
		}
		result, err = store.StartRun(ctx, w.flowID, w.agent, worldstate.StartRunInput{
			CheckID: action.CheckID.Int64(), ToolCallID: action.ToolCallID.PtrInt64(), RequestKey: requestKey,
			Parameters: action.Parameters, ExecutionStatus: action.ExecutionStatus,
		})
	case "finish_run":
		result, err = store.UpdateRun(ctx, w.flowID, action.RunID.Int64(), w.agent, worldstate.UpdateRunInput{
			ExecutionStatus: action.ExecutionStatus, Verdict: action.Verdict,
			Evidence: action.Evidence, ToolCallID: action.ToolCallID.PtrInt64(),
		})
	}
	if err != nil {
		return "", fmt.Errorf("world_state_update %s: %w", action.Operation, err)
	}
	response := map[string]any{
		"ok": true, "schema_version": 2, "operation": action.Operation, "result": result,
	}
	switch record := result.(type) {
	case worldstate.Object:
		response["object_id"] = record.ID
	case worldstate.Check:
		response["object_id"], response["check_id"] = record.ObjectID, record.ID
	case worldstate.CheckRun:
		response["check_id"], response["run_id"] = record.CheckID, record.ID
	}
	payload, err := json.Marshal(response)
	return string(payload), err
}

func validateModelUpdate(action WorldStateUpdateAction) error {
	if action.EntityKey != "" || action.ToState != "" || action.Properties != "" {
		return fmt.Errorf("entity_key, to_state and properties apply only to legacy flows; use an explicit operation with objects, checks and runs")
	}
	if action.ToolCallID != nil && action.ToolCallID.Int64() <= 0 {
		return fmt.Errorf("tool_call_id must be a positive database toolcalls.id")
	}
	switch action.Operation {
	case "observe_object":
		if strings.TrimSpace(action.Type) == "" || action.Locator == nil {
			return fmt.Errorf("observe_object requires type and locator as a JSON object")
		}
	case "ensure_check":
		if action.ObjectID.Int64() <= 0 || strings.TrimSpace(action.Kind) == "" {
			return fmt.Errorf("ensure_check requires a positive object_id and kind")
		}
	case "start_run":
		if action.CheckID.Int64() <= 0 || action.Parameters == nil {
			return fmt.Errorf("start_run requires a positive check_id and parameters as a JSON object")
		}
		if action.ExecutionStatus != "" && action.ExecutionStatus != "running" {
			return fmt.Errorf("start_run execution_status must be running; call it immediately before executing the experiment")
		}
		if action.Verdict != nil || action.Evidence != nil {
			return fmt.Errorf("start_run records conditions before execution; save verdict and evidence with finish_run")
		}
	case "finish_run":
		if action.RunID.Int64() <= 0 || action.Evidence == nil {
			return fmt.Errorf("finish_run requires a positive run_id and evidence as a JSON object")
		}
		if action.Parameters != nil {
			return fmt.Errorf("run parameters are immutable; create a new run for a different experiment")
		}
		if action.ExecutionStatus != "completed" && action.ExecutionStatus != "failed" && action.ExecutionStatus != "cancelled" {
			return fmt.Errorf("finish_run execution_status must be completed, failed or cancelled")
		}
		if action.ExecutionStatus == "completed" && action.Verdict == nil {
			return fmt.Errorf("a completed run requires verdict")
		}
		if action.Verdict != nil {
			if *action.Verdict != "vulnerable" && *action.Verdict != "not_reproduced" && *action.Verdict != "inconclusive" {
				return fmt.Errorf("unknown run verdict %q", *action.Verdict)
			}
			if action.ExecutionStatus != "completed" {
				return fmt.Errorf("only completed runs have a verdict; record failure or cancellation details in evidence")
			}
		}
	default:
		return fmt.Errorf("operation must be observe_object, ensure_check, start_run or finish_run")
	}
	return nil
}

func modelPageSize(value *Int64, fallback int) (int, error) {
	if value == nil {
		return fallback, nil
	}
	if value.Int64() < 1 || value.Int64() > 100 {
		return 0, fmt.Errorf("page limits must be between 1 and 100")
	}
	return value.Int(), nil
}

func (w *worldStateTool) queryModel(ctx context.Context, action WorldStateQueryAction) (string, error) {
	objectLimit, err := modelPageSize(action.ObjectLimit, 20)
	if err != nil {
		return "", err
	}
	runLimit, err := modelPageSize(action.RunLimit, 20)
	if err != nil {
		return "", err
	}
	if action.ObjectOffset.Int64() < 0 || action.RunOffset.Int64() < 0 {
		return "", fmt.Errorf("pagination offsets must be nonnegative")
	}
	if (action.ObjectID != nil && action.ObjectID.Int64() <= 0) || (action.CheckID != nil && action.CheckID.Int64() <= 0) {
		return "", fmt.Errorf("object_id and check_id must be positive")
	}
	snapshot, err := worldstate.NewModelStore(w.db).ReadSnapshot(ctx, w.flowID)
	if err != nil {
		return "", fmt.Errorf("failed to read world state objects: %w", err)
	}
	return pageModelSnapshot(snapshot, action, objectLimit, runLimit)
}

func pageModelSnapshot(snapshot worldstate.ModelSnapshot, action WorldStateQueryAction, objectLimit, runLimit int) (string, error) {
	typeFilter := strings.TrimSpace(action.Type)
	stateFilter := strings.TrimSpace(action.State)
	kindFilter := strings.TrimSpace(action.Kind)
	objects := make([]worldstate.Object, 0)
	objectFound, checkFound := action.ObjectID == nil, action.CheckID == nil
	var checkCount, runCount int64
	for _, object := range snapshot.Objects {
		if object.ID == action.ObjectID.Int64() {
			objectFound = true
		}
		for _, check := range object.Checks {
			if check.ID == action.CheckID.Int64() {
				checkFound = true
			}
		}
		if action.ObjectID != nil && object.ID != action.ObjectID.Int64() {
			continue
		}
		if typeFilter != "" && !strings.EqualFold(object.Type, typeFilter) {
			continue
		}
		if stateFilter != "" && !strings.EqualFold(object.ComputedStatus, stateFilter) {
			continue
		}
		checks := make([]worldstate.Check, 0, len(object.Checks))
		for _, check := range object.Checks {
			if action.CheckID != nil && check.ID != action.CheckID.Int64() {
				continue
			}
			if kindFilter != "" && !strings.EqualFold(check.Kind, kindFilter) {
				continue
			}
			checkCount++
			runCount += check.RunCount
			// Counts and computed status remain based on all runs. Pagination only
			// bounds the records delivered to the caller, never the verdict.
			start := min(action.RunOffset.Int64(), int64(len(check.Runs)))
			end := min(start+int64(runLimit), int64(len(check.Runs)))
			check.Runs = check.Runs[start:end]
			checks = append(checks, check)
		}
		if len(checks) == 0 && (action.CheckID != nil || kindFilter != "") {
			continue
		}
		object.Checks = checks
		objects = append(objects, object)
	}
	if !objectFound || !checkFound {
		return "", fmt.Errorf("requested object or check was not found in this flow")
	}
	matchedObjects := len(objects)
	start := min(action.ObjectOffset.Int64(), int64(matchedObjects))
	end := min(start+int64(objectLimit), int64(matchedObjects))
	objects = objects[start:end]
	payload, err := json.Marshal(map[string]any{
		"schema_version": snapshot.SchemaVersion, "flow_id": snapshot.FlowID, "revision": snapshot.Revision,
		"objects":        objects,
		"matched_counts": map[string]any{"objects": matchedObjects, "checks": checkCount, "runs": runCount},
		"pagination": map[string]any{
			"object_offset": action.ObjectOffset.Int64(), "object_limit": objectLimit,
			"objects_has_more": end < int64(matchedObjects),
			"run_offset":       action.RunOffset.Int64(), "run_limit": runLimit,
		},
		"hint": "Computed statuses and counts include all stored runs, not only this page. For each check, compare run_count with run_offset plus len(runs); use check_id and run_offset for further pages. A positive result establishes vulnerability only under its recorded conditions; target revisions may differ.",
	})
	return string(payload), err
}
