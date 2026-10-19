package worldstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"

	"pentagi/pkg/database"
)

var ErrModelVersion = errors.New("worldstate: flow does not use object/check/run storage")
var ErrModelConflict = errors.New("worldstate: operation conflicts with an existing record")

type ModelStore struct {
	db    database.Querier
	hints []PostCommitHint
}

func NewModelStore(db database.Querier, hints ...PostCommitHint) *ModelStore {
	return &ModelStore{db: db, hints: hints}
}

func ModelVersion(ctx context.Context, db database.Querier, flowID int64) (int32, error) {
	if flowID <= 0 {
		return 0, fmt.Errorf("worldstate: invalid flow id")
	}
	return db.GetWorldStateModelVersion(ctx, flowID)
}

func (s *ModelStore) mutate(ctx context.Context, flowID int64, fn func(*database.Queries) (bool, error)) error {
	if flowID <= 0 {
		return fmt.Errorf("worldstate: invalid flow id")
	}
	db, ok := s.db.(transactionalQuerier)
	if !ok {
		return fmt.Errorf("worldstate: database does not support transactions")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.WithTx(tx)
	version, err := q.LockWorldStateModelFlow(ctx, flowID)
	if err != nil {
		return err
	}
	if version != 2 {
		return ErrModelVersion
	}
	changed, err := fn(q)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if changed {
		for _, hint := range s.hints {
			if hint != nil {
				_ = hint(context.WithoutCancel(ctx), flowID)
			}
		}
	}
	return nil
}

func modelEvent(ctx context.Context, q *database.Queries, flowID int64, kind database.WorldStateEventKind, agent string, facts map[string]any) error {
	facts["schema_version"] = 2
	return createEvent(ctx, q, flowID, kind, normalizedAgent(agent), facts)
}

func (s *ModelStore) ObserveObject(ctx context.Context, flowID int64, agent string, input ObserveObjectInput) (Object, error) {
	typ, key, locator, err := normalizeModelObject(input)
	if err != nil {
		return Object{}, err
	}
	var result Object
	err = s.mutate(ctx, flowID, func(q *database.Queries) (bool, error) {
		row, err := q.GetWorldStateModelObjectByKey(ctx, database.GetWorldStateModelObjectByKeyParams{FlowID: flowID, ObjectKey: key})
		created := errors.Is(err, sql.ErrNoRows)
		if err != nil && !created {
			return false, err
		}
		if created {
			row, err = q.InsertWorldStateModelObject(ctx, database.InsertWorldStateModelObjectParams{FlowID: flowID, ObjectKey: key, Type: typ, Locator: locator})
		} else {
			if row.Type != typ {
				return false, fmt.Errorf("%w: object type cannot change", ErrModelConflict)
			}
			if equalModelJSON(row.Locator, locator) {
				result, err = loadModelObject(ctx, q, flowID, row)
				return false, err
			}
			row, err = q.UpdateWorldStateModelObject(ctx, database.UpdateWorldStateModelObjectParams{FlowID: flowID, ID: row.ID, Locator: locator})
		}
		if err != nil {
			return false, err
		}
		result, err = loadModelObject(ctx, q, flowID, row)
		if err != nil {
			return false, err
		}
		err = modelEvent(ctx, q, flowID, database.WorldStateEventKindObjectUpserted, agent, map[string]any{"object_id": row.ID, "object_key": row.ObjectKey, "object_type": row.Type, "created": created})
		return true, err
	})
	return result, err
}

func (s *ModelStore) EnsureCheck(ctx context.Context, flowID, objectID int64, kind, agent string) (Check, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if objectID <= 0 || !modelToken.MatchString(kind) {
		return Check{}, fmt.Errorf("worldstate: invalid object id or check kind")
	}
	var result Check
	err := s.mutate(ctx, flowID, func(q *database.Queries) (bool, error) {
		if _, err := q.GetWorldStateModelObject(ctx, database.GetWorldStateModelObjectParams{FlowID: flowID, ID: objectID}); err != nil {
			return false, err
		}
		row, err := q.GetWorldStateModelCheckByKind(ctx, database.GetWorldStateModelCheckByKindParams{FlowID: flowID, ObjectID: objectID, Kind: kind})
		if err == nil {
			result, err = loadModelCheck(ctx, q, flowID, row)
			return false, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		row, err = q.InsertWorldStateModelCheck(ctx, database.InsertWorldStateModelCheckParams{ObjectID: objectID, Kind: kind})
		if err != nil {
			return false, err
		}
		result = modelCheck(row)
		err = modelEvent(ctx, q, flowID, database.WorldStateEventKindCheckCreated, agent, map[string]any{"object_id": objectID, "check_id": row.ID, "check_kind": kind})
		return true, err
	})
	return result, err
}

func (s *ModelStore) StartRun(ctx context.Context, flowID int64, agent string, input StartRunInput) (CheckRun, error) {
	status := input.ExecutionStatus
	if status == "" {
		status = "running"
	}
	if input.CheckID <= 0 || (status != "planned" && status != "running") {
		return CheckRun{}, fmt.Errorf("worldstate: start requires check_id and planned/running status")
	}
	if len(input.RequestKey) > 512 || (input.RequestKey != "" && strings.TrimSpace(input.RequestKey) == "") {
		return CheckRun{}, fmt.Errorf("worldstate: invalid request key")
	}
	parameters, err := modelSafeMap(input.Parameters)
	if err != nil {
		return CheckRun{}, err
	}
	var result CheckRun
	err = s.mutate(ctx, flowID, func(q *database.Queries) (bool, error) {
		if _, err := q.GetWorldStateModelCheck(ctx, database.GetWorldStateModelCheckParams{FlowID: flowID, ID: input.CheckID}); err != nil {
			return false, err
		}
		toolID, err := validateModelTool(ctx, q, flowID, input.ToolCallID)
		if err != nil {
			return false, err
		}
		request := sql.NullString{}
		if input.RequestKey != "" {
			request = database.StringToNullString(fmt.Sprintf("flow:%d:%s", flowID, input.RequestKey))
		}
		if request.Valid {
			row, err := q.GetWorldStateModelRunByRequest(ctx, database.GetWorldStateModelRunByRequestParams{FlowID: flowID, RequestKey: request})
			if err == nil {
				if row.CheckID != input.CheckID || !equalModelJSON(row.Parameters, parameters) || (toolID.Valid && row.ToolCallID != toolID) {
					return false, fmt.Errorf("%w: request key already identifies different run inputs", ErrModelConflict)
				}
				result = modelRun(row)
				return false, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return false, err
			}
		}
		var started sql.NullTime
		if status == "running" {
			started = sql.NullTime{Time: time.Now().UTC(), Valid: true}
		}
		row, err := q.InsertWorldStateModelRun(ctx, database.InsertWorldStateModelRunParams{CheckID: input.CheckID, ToolCallID: toolID, RequestKey: request, Parameters: parameters, ExecutionStatus: status, StartedAt: started})
		if err != nil {
			return false, err
		}
		if err := q.TouchWorldStateModelCheck(ctx, input.CheckID); err != nil {
			return false, err
		}
		result = modelRun(row)
		err = modelEvent(ctx, q, flowID, database.WorldStateEventKindCheckRunStarted, agent, map[string]any{"check_id": row.CheckID, "run_id": row.ID, "execution_status": row.ExecutionStatus})
		return true, err
	})
	return result, err
}

func (s *ModelStore) UpdateRun(ctx context.Context, flowID, runID int64, agent string, input UpdateRunInput) (CheckRun, error) {
	if runID <= 0 {
		return CheckRun{}, fmt.Errorf("worldstate: invalid run id")
	}
	if err := validateModelOutcome(input.ExecutionStatus, input.Verdict); err != nil {
		return CheckRun{}, err
	}
	evidence, err := modelSafeMap(input.Evidence)
	if err != nil {
		return CheckRun{}, err
	}
	var result CheckRun
	err = s.mutate(ctx, flowID, func(q *database.Queries) (bool, error) {
		row, err := q.GetWorldStateModelRun(ctx, database.GetWorldStateModelRunParams{FlowID: flowID, ID: runID})
		if err != nil {
			return false, err
		}
		toolID := row.ToolCallID
		if input.ToolCallID != nil {
			toolID, err = validateModelTool(ctx, q, flowID, input.ToolCallID)
			if err != nil {
				return false, err
			}
			if row.ToolCallID.Valid && row.ToolCallID != toolID {
				return false, fmt.Errorf("%w: tool call cannot change", ErrModelConflict)
			}
		}
		verdict := database.PtrStringToNullString(input.Verdict)
		if row.ExecutionStatus == input.ExecutionStatus && row.Verdict == verdict && equalModelJSON(row.Evidence, evidence) && row.ToolCallID == toolID {
			result = modelRun(row)
			return false, nil
		}
		if row.ExecutionStatus == "completed" || row.ExecutionStatus == "failed" || row.ExecutionStatus == "cancelled" {
			return false, fmt.Errorf("%w: terminal run is immutable; create a new run for a new observation", ErrModelConflict)
		}
		if row.ExecutionStatus == "running" && input.ExecutionStatus == "planned" {
			return false, fmt.Errorf("worldstate: running run cannot become planned")
		}
		started, finished := row.StartedAt, row.FinishedAt
		now := time.Now().UTC()
		if !started.Valid && (input.ExecutionStatus == "running" || input.ExecutionStatus == "completed") {
			started = sql.NullTime{Time: now, Valid: true}
		}
		if input.ExecutionStatus == "completed" || input.ExecutionStatus == "failed" || input.ExecutionStatus == "cancelled" {
			finished = sql.NullTime{Time: now, Valid: true}
		}
		row, err = q.UpdateWorldStateModelRun(ctx, database.UpdateWorldStateModelRunParams{ID: runID, ToolCallID: toolID, ExecutionStatus: input.ExecutionStatus, Verdict: verdict, Evidence: evidence, StartedAt: started, FinishedAt: finished})
		if err != nil {
			return false, err
		}
		if err := q.TouchWorldStateModelCheck(ctx, row.CheckID); err != nil {
			return false, err
		}
		result = modelRun(row)
		err = modelEvent(ctx, q, flowID, database.WorldStateEventKindCheckRunUpdated, agent, map[string]any{"check_id": row.CheckID, "run_id": row.ID, "execution_status": row.ExecutionStatus, "verdict": result.Verdict})
		return true, err
	})
	return result, err
}

func validateModelOutcome(status string, verdict *string) error {
	switch status {
	case "planned", "running", "failed", "cancelled":
		if verdict != nil {
			return fmt.Errorf("worldstate: only completed runs have a verdict")
		}
	case "completed":
		if verdict == nil || (*verdict != "vulnerable" && *verdict != "not_reproduced" && *verdict != "inconclusive") {
			return fmt.Errorf("worldstate: completed run requires a supported verdict")
		}
	default:
		return fmt.Errorf("worldstate: invalid execution status")
	}
	return nil
}

func validateModelTool(ctx context.Context, q *database.Queries, flowID int64, toolID *int64) (sql.NullInt64, error) {
	if toolID == nil {
		return sql.NullInt64{}, nil
	}
	if *toolID <= 0 {
		return sql.NullInt64{}, fmt.Errorf("worldstate: invalid tool call id")
	}
	if _, err := q.ValidateWorldStateModelToolCall(ctx, database.ValidateWorldStateModelToolCallParams{FlowID: flowID, ID: *toolID}); err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: *toolID, Valid: true}, nil
}

func equalModelJSON(a, b json.RawMessage) bool {
	var left, right any
	if decodeModelJSON(a, &left) != nil || decodeModelJSON(b, &right) != nil {
		return false
	}
	left, leftOK := exactModelNumbers(left)
	right, rightOK := exactModelNumbers(right)
	return leftOK && rightOK && reflect.DeepEqual(left, right)
}

// PostgreSQL JSONB may serialize 1e3 as 1000. Compare exact numeric values so a
// persistence retry remains idempotent without rounding large numeric IDs.
func exactModelNumbers(value any) (any, bool) {
	switch typed := value.(type) {
	case json.Number:
		if !boundedModelNumber(typed) {
			return nil, false
		}
		if number, ok := new(big.Rat).SetString(string(typed)); ok {
			return number, true
		}
		return nil, false
	case map[string]any:
		for key, child := range typed {
			normalized, ok := exactModelNumbers(child)
			if !ok {
				return nil, false
			}
			typed[key] = normalized
		}
	case []any:
		for i, child := range typed {
			normalized, ok := exactModelNumbers(child)
			if !ok {
				return nil, false
			}
			typed[i] = normalized
		}
	}
	return value, true
}

func boundedModelNumber(number json.Number) bool {
	// JSONB uses PostgreSQL numeric, bounded to 131072 integral and 16383
	// fractional digits. Reject extreme spellings before big.Rat allocates.
	const maxDigits = 131072 + 16383
	raw := string(number)
	if len(raw) > maxDigits+32 {
		return false
	}
	mantissa := raw
	if split := strings.IndexAny(raw, "eE"); split >= 0 {
		mantissa = raw[:split]
		exponent, err := strconv.ParseInt(raw[split+1:], 10, 64)
		if err != nil || exponent < -16383 || exponent > 131071 {
			return false
		}
	}
	var digits int
	for _, character := range mantissa {
		if character >= '0' && character <= '9' {
			digits++
		}
	}
	return digits <= maxDigits
}

func modelObject(row database.WorldStateObject) Object {
	return Object{ID: row.ID, FlowID: row.FlowID, ObjectKey: row.ObjectKey, Type: row.Type, Locator: row.Locator, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ComputedStatus: "unknown", Checks: []Check{}}
}

func modelCheck(row database.WorldStateCheck) Check {
	return Check{ID: row.ID, ObjectID: row.ObjectID, Kind: row.Kind, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, ComputedStatus: "unknown", Runs: []CheckRun{}}
}

func modelRun(row database.WorldStateCheckRun) CheckRun {
	run := CheckRun{ID: row.ID, CheckID: row.CheckID, ToolCallID: database.NullInt64ToInt64(row.ToolCallID), Parameters: row.Parameters, ExecutionStatus: row.ExecutionStatus, Verdict: database.NullStringToPtrString(row.Verdict), Evidence: row.Evidence, CreatedAt: row.CreatedAt}
	if row.StartedAt.Valid {
		run.StartedAt = &row.StartedAt.Time
	}
	if row.FinishedAt.Valid {
		run.FinishedAt = &row.FinishedAt.Time
	}
	return run
}

func loadModelCheck(ctx context.Context, q *database.Queries, flowID int64, row database.WorldStateCheck) (Check, error) {
	check := modelCheck(row)
	runs, err := q.ListWorldStateModelCheckRuns(ctx, database.ListWorldStateModelCheckRunsParams{FlowID: flowID, ID: row.ID})
	if err != nil {
		return Check{}, err
	}
	for _, run := range runs {
		check.Runs = append(check.Runs, modelRun(run))
	}
	check.RunCount = int64(len(check.Runs))
	check.ComputedStatus = ComputedCheckStatus(check.Runs)
	return check, nil
}

func loadModelObject(ctx context.Context, q *database.Queries, flowID int64, row database.WorldStateObject) (Object, error) {
	object := modelObject(row)
	checks, err := q.ListWorldStateModelObjectChecks(ctx, database.ListWorldStateModelObjectChecksParams{FlowID: flowID, ID: row.ID})
	if err != nil {
		return Object{}, err
	}
	for _, row := range checks {
		check, err := loadModelCheck(ctx, q, flowID, row)
		if err != nil {
			return Object{}, err
		}
		object.Checks = append(object.Checks, check)
		object.RunCount += check.RunCount
	}
	object.CheckCount = int64(len(object.Checks))
	object.ComputedStatus = computedObjectStatus(object.Checks)
	return object, nil
}
