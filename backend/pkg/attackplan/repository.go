package attackplan

import "context"

// Repository is the orchestration-facing boundary for durable attack plans.
type Repository interface {
	Create(context.Context, CreatePlanRequest) (MutationResult, error)
	Get(context.Context, int64, int64) (Plan, error)
	GetActive(context.Context, int64, string) (Plan, error)
	GetRun(context.Context, int64, int64, string) (Run, error)
	ReplaceGraph(context.Context, ReplaceGraphRequest) (MutationResult, error)
	PatchGraph(context.Context, PatchGraphRequest) (MutationResult, error)
	TransitionNode(context.Context, TransitionNodeRequest) (MutationResult, error)
	TransitionPlan(context.Context, TransitionPlanRequest) (MutationResult, error)
	BindAction(context.Context, BindActionRequest) (MutationResult, error)
}
