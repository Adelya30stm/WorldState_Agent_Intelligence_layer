package worldstate

import (
	"encoding/json"
	"time"
)

// ModelSnapshot is the complete, consistent version-2 domain view. Presentation
// limits must not be applied until statuses and counts have been computed.
type ModelSnapshot struct {
	SchemaVersion int32    `json:"schema_version"`
	FlowID        int64    `json:"flow_id"`
	Revision      int64    `json:"revision"`
	ObjectCount   int64    `json:"object_count"`
	CheckCount    int64    `json:"check_count"`
	RunCount      int64    `json:"run_count"`
	Objects       []Object `json:"objects"`
}

type Object struct {
	ID             int64           `json:"id"`
	FlowID         int64           `json:"flow_id"`
	ObjectKey      string          `json:"object_key"`
	Type           string          `json:"type"`
	Locator        json.RawMessage `json:"locator"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	ComputedStatus string          `json:"computed_status"`
	CheckCount     int64           `json:"check_count"`
	RunCount       int64           `json:"run_count"`
	Checks         []Check         `json:"checks"`
}

type Check struct {
	ID             int64      `json:"id"`
	ObjectID       int64      `json:"object_id"`
	Kind           string     `json:"kind"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ComputedStatus string     `json:"computed_status"`
	RunCount       int64      `json:"run_count"`
	Runs           []CheckRun `json:"runs"`
}

type CheckRun struct {
	ID              int64           `json:"id"`
	CheckID         int64           `json:"check_id"`
	ToolCallID      *int64          `json:"tool_call_id"`
	Parameters      json.RawMessage `json:"parameters"`
	ExecutionStatus string          `json:"execution_status"`
	Verdict         *string         `json:"verdict"`
	Evidence        json.RawMessage `json:"evidence"`
	CreatedAt       time.Time       `json:"created_at"`
	StartedAt       *time.Time      `json:"started_at"`
	FinishedAt      *time.Time      `json:"finished_at"`
}

type ObserveObjectInput struct {
	ObjectKey string         `json:"object_key"`
	Type      string         `json:"type"`
	Locator   map[string]any `json:"locator"`
}

type StartRunInput struct {
	CheckID         int64          `json:"check_id"`
	ToolCallID      *int64         `json:"tool_call_id"`
	RequestKey      string         `json:"-"`
	Parameters      map[string]any `json:"parameters"`
	ExecutionStatus string         `json:"execution_status"`
}

type UpdateRunInput struct {
	ExecutionStatus string         `json:"execution_status"`
	Verdict         *string        `json:"verdict"`
	Evidence        map[string]any `json:"evidence"`
	ToolCallID      *int64         `json:"tool_call_id"`
}

// ComputedCheckStatus never treats a negative experiment as cancellation of a
// historical positive. Repair/version invalidation requires an explicit future
// policy; the model must not guess it from a target_revision string.
func ComputedCheckStatus(runs []CheckRun) string {
	var active, uncertain, negative bool
	for _, run := range runs {
		if run.ExecutionStatus == "completed" && run.Verdict != nil && *run.Verdict == "vulnerable" {
			return "vulnerable"
		}
		switch run.ExecutionStatus {
		case "planned", "running":
			active = true
		case "failed":
			uncertain = true
		case "completed":
			if run.Verdict != nil && *run.Verdict == "not_reproduced" {
				negative = true
			} else {
				uncertain = true
			}
		}
	}
	if active {
		return "in_progress"
	}
	if uncertain {
		return "inconclusive"
	}
	if negative {
		return "not_confirmed"
	}
	return "unknown"
}

func computedObjectStatus(checks []Check) string {
	var active, uncertain, negative, unknown bool
	for _, check := range checks {
		switch check.ComputedStatus {
		case "vulnerable":
			return "vulnerable"
		case "in_progress":
			active = true
		case "inconclusive":
			uncertain = true
		case "not_confirmed":
			negative = true
		default:
			unknown = true
		}
	}
	if active {
		return "in_progress"
	}
	if uncertain {
		return "inconclusive"
	}
	if unknown || len(checks) == 0 {
		return "unknown"
	}
	if negative {
		return "not_confirmed"
	}
	return "unknown"
}
