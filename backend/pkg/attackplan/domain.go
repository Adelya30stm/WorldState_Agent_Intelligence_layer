// Package attackplan persists flow-scoped, revision-bound attack DAGs.
package attackplan

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidInput        = errors.New("attackplan: invalid input")
	ErrInvalidTransition   = errors.New("attackplan: invalid terminal transition")
	ErrStaleVersion        = errors.New("attackplan: stale version")
	ErrIdempotencyConflict = errors.New("attackplan: idempotency key conflict")
	ErrCycle               = errors.New("attackplan: graph contains a cycle")
	ErrNotFound            = errors.New("attackplan: not found")
	ErrAlreadyBound        = errors.New("attackplan: action already bound")
)

type PlanStatus string

const (
	PlanStatusDraft      PlanStatus = "draft"
	PlanStatusActive     PlanStatus = "active"
	PlanStatusCompleted  PlanStatus = "completed"
	PlanStatusFailed     PlanStatus = "failed"
	PlanStatusCancelled  PlanStatus = "cancelled"
	PlanStatusSuperseded PlanStatus = "superseded"
)

func (s PlanStatus) IsTerminal() bool {
	return s == PlanStatusCompleted || s == PlanStatusFailed || s == PlanStatusCancelled || s == PlanStatusSuperseded
}

type NodeKind string

const (
	NodeKindGoal   NodeKind = "goal"
	NodeKindAction NodeKind = "action"
)

type NodeStatus string

const (
	NodeStatusPending   NodeStatus = "pending"
	NodeStatusReady     NodeStatus = "ready"
	NodeStatusRunning   NodeStatus = "running"
	NodeStatusSucceeded NodeStatus = "succeeded"
	NodeStatusFailed    NodeStatus = "failed"
	NodeStatusBlocked   NodeStatus = "blocked"
	NodeStatusSkipped   NodeStatus = "skipped"
	NodeStatusCancelled NodeStatus = "cancelled"
)

func (s NodeStatus) IsTerminal() bool {
	return s == NodeStatusSucceeded || s == NodeStatusFailed || s == NodeStatusSkipped || s == NodeStatusCancelled
}

type EdgeKind string

const (
	EdgeKindAnd        EdgeKind = "and"
	EdgeKindOr         EdgeKind = "or"
	EdgeKindDependency EdgeKind = "dependency"
)

type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCancelled RunStatus = "cancelled"
)

type Node struct {
	ID          int64
	Key         string
	Kind        NodeKind
	Status      NodeStatus
	Title       string
	Description string
	Payload     json.RawMessage
	Version     int64
}

type Edge struct {
	ID      int64
	FromKey string
	ToKey   string
	Kind    EdgeKind
}

type Plan struct {
	ID           int64
	FlowID       int64
	ObjectiveKey string
	Objective    string
	Status       PlanStatus
	Version      int64
	Planner      string
	Nodes        []Node
	Edges        []Edge
	Bindings     []Binding
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Binding struct {
	ID        int64
	NodeKey   string
	TaskID    *int64
	SubtaskID *int64
	CreatedAt time.Time
}

type Run struct {
	ID                 int64
	PlanID             int64
	FlowID             int64
	Status             RunStatus
	RequestedVersion   int64
	ResultingVersion   *int64
	WorldStateRevision int64
	IdempotencyKey     string
	Planner            string
	StartedAt          time.Time
	FinishedAt         *time.Time
}

type Revision struct {
	ExpectedVersion        int64
	WorldStateRevisionFrom int64
	WorldStateRevision     int64
	IdempotencyKey         string
	Planner                string
}

type CreatePlanRequest struct {
	FlowID       int64
	ObjectiveKey string
	Objective    string
	Status       PlanStatus
	Revision     Revision
	Nodes        []Node
	Edges        []Edge
}

type ReplaceGraphRequest struct {
	FlowID int64
	PlanID int64
	Revision
	Nodes []Node
	Edges []Edge
}

type GraphPatch struct {
	UpsertNodes    []Node
	RemoveNodeKeys []string
	AddEdges       []Edge
	RemoveEdges    []Edge
}

type PatchGraphRequest struct {
	FlowID int64
	PlanID int64
	Revision
	Patch GraphPatch
}

type TransitionNodeRequest struct {
	FlowID  int64
	PlanID  int64
	NodeKey string
	To      NodeStatus
	Revision
}

type TransitionPlanRequest struct {
	FlowID int64
	PlanID int64
	To     PlanStatus
	Revision
}

type BindActionRequest struct {
	FlowID    int64
	PlanID    int64
	NodeKey   string
	TaskID    int64
	SubtaskID int64
	Revision
}

// MutationResult returns the full current plan for a new mutation or immediate
// replay. A delayed replay returns only plan identity and the recorded version.
type MutationResult struct {
	Plan     Plan
	Run      Run
	Replayed bool
}
