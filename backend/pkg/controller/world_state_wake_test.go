package controller

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"pentagi/pkg/database"
	"pentagi/pkg/providers"
)

func TestSubtaskLateWorldStateInputUsesProviderPath(t *testing.T) {
	provider := &lateInputProviderStub{}
	msglog := &lateInputMsgLogStub{}
	db := &lateInputDBStub{}
	worker := &subtaskWorker{
		mx:        &sync.RWMutex{},
		waiting:   true,
		completed: false,
		updater:   &lateInputTaskUpdater{},
		subtaskCtx: &SubtaskContext{
			MsgChainID: 42,
			SubtaskID:  7,
			TaskContext: TaskContext{TaskID: 8, FlowContext: FlowContext{
				DB:       db,
				Provider: provider,
				MsgLog:   msglog,
			}},
		},
	}

	if err := worker.PutInput(context.Background(), "late-human"); err != nil {
		t.Fatalf("late input rejected by controller: %v", err)
	}
	if provider.input != "late-human" || provider.msgChainID != 42 {
		t.Fatalf("provider input=(%q,%d)", provider.input, provider.msgChainID)
	}
	if len(msglog.inputs) != 1 || msglog.inputs[0] != "late-human" {
		t.Fatalf("logged inputs=%v", msglog.inputs)
	}
	if worker.IsWaiting() {
		t.Fatal("controller kept the subtask waiting after accepted input")
	}
	if err := worker.Run(context.Background()); err != nil {
		t.Fatalf("resumed chain run failed: %v", err)
	}
	if provider.performTaskID != 8 || provider.performSubtaskID != 7 || provider.performMsgChainID != 42 {
		t.Fatalf("resumed provider call=(%d,%d,%d)", provider.performTaskID, provider.performSubtaskID, provider.performMsgChainID)
	}
	if !worker.IsWaiting() {
		t.Fatal("waiting result from resumed chain was not restored")
	}
}

func TestSubtaskStaleWorldStateResumeDoesNotResumeReask(t *testing.T) {
	worker := &subtaskWorker{
		mx:      &sync.RWMutex{},
		waiting: true,
		updater: &lateInputTaskUpdater{},
		subtaskCtx: &SubtaskContext{MsgChainID: 42, SubtaskID: 7, TaskContext: TaskContext{
			FlowContext: FlowContext{DB: &staleResumeDBStub{}},
		}},
	}
	stale := database.AgentChainWait{
		MsgchainID: 42, PendingToolCallID: sql.NullString{String: "ask-1", Valid: true},
		ResolutionRef: sql.NullInt64{Int64: 11, Valid: true}, ResumeIntent: []byte(`{"generation":"old"}`),
		LeaseOwner: sql.NullString{String: "old-dispatcher", Valid: true},
	}
	resumed, err := worker.ResumePrimaryWait(context.Background(), stale)
	if err != nil {
		t.Fatalf("stale callback validation failed: %v", err)
	}
	if resumed || !worker.IsWaiting() || worker.resumeWait != nil {
		t.Fatalf("stale callback resumed re-asked worker: resumed=%v waiting=%v", resumed, worker.IsWaiting())
	}
}

type lateInputProviderStub struct {
	providers.FlowProvider
	input             string
	msgChainID        int64
	performTaskID     int64
	performSubtaskID  int64
	performMsgChainID int64
}

func (p *lateInputProviderStub) PutInputToAgentChain(_ context.Context, msgChainID int64, input string) error {
	p.msgChainID = msgChainID
	p.input = input
	return nil
}

func (p *lateInputProviderStub) PerformAgentChain(_ context.Context, taskID, subtaskID, msgChainID int64) (providers.PerformResult, error) {
	p.performTaskID = taskID
	p.performSubtaskID = subtaskID
	p.performMsgChainID = msgChainID
	return providers.PerformResultWaiting, nil
}

type lateInputDBStub struct {
	database.Querier
}

func (d *lateInputDBStub) UpdateSubtaskStatus(context.Context, database.UpdateSubtaskStatusParams) (database.Subtask, error) {
	return database.Subtask{}, nil
}

func (d *lateInputDBStub) ClaimPrimaryWorldStateResumeForController(context.Context, database.ClaimPrimaryWorldStateResumeForControllerParams) (database.AgentChainWait, error) {
	return database.AgentChainWait{}, sql.ErrNoRows
}

type staleResumeDBStub struct {
	database.Querier
}

func (*staleResumeDBStub) GetClaimedPrimaryWorldStateResume(context.Context, database.GetClaimedPrimaryWorldStateResumeParams) (database.AgentChainWait, error) {
	return database.AgentChainWait{}, sql.ErrNoRows
}

type lateInputTaskUpdater struct{}

func (*lateInputTaskUpdater) SetStatus(context.Context, database.TaskStatus) error { return nil }

type lateInputMsgLogStub struct {
	FlowMsgLogWorker
	inputs []string
}

func (m *lateInputMsgLogStub) PutSubtaskMsg(_ context.Context, _ database.MsglogType, _, _ int64, _, msg string) (int64, error) {
	m.inputs = append(m.inputs, msg)
	return 1, nil
}
