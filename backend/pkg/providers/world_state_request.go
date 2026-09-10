package providers

import (
	"context"
	"encoding/json"
	"fmt"

	"pentagi/pkg/database"
	"pentagi/pkg/providers/pconfig"
	"pentagi/pkg/worldstate"

	"github.com/vxcontrol/langchaingo/llms"
)

type worldStateRequestContext interface {
	Read(context.Context) (*worldstate.ModelContext, error)
	Acknowledge(context.Context, int64, int64) error
}

type databaseWorldStateRequestContext struct {
	db     database.Querier
	flowID int64
}

func newWorldStateRequestContext(db database.Querier, flowID int64) worldStateRequestContext {
	if _, ok := db.(primaryWorldStateDatabase); !ok {
		return nil
	}
	return &databaseWorldStateRequestContext{db: db, flowID: flowID}
}

func (w *databaseWorldStateRequestContext) Read(ctx context.Context) (*worldstate.ModelContext, error) {
	version, err := worldstate.ModelVersion(ctx, w.db, w.flowID)
	if err != nil {
		return nil, err
	}
	if version != 2 {
		return nil, nil
	}
	snapshot, err := worldstate.NewModelStore(w.db).ReadSnapshot(ctx, w.flowID)
	if err != nil {
		return nil, err
	}
	view, err := worldstate.NewModelContext(snapshot, 64<<10)
	if err != nil {
		return nil, err
	}
	return &view, nil
}

func (w *databaseWorldStateRequestContext) Acknowledge(ctx context.Context, chainID, revision int64) error {
	chain, err := w.db.GetMsgChain(ctx, chainID)
	if err != nil {
		return err
	}
	if chain.FlowID != w.flowID || chain.Type != database.MsgchainTypePrimaryAgent {
		return fmt.Errorf("World State recipient chain does not belong to primary flow")
	}
	_, err = w.db.AdvanceWorldStateChainCursor(ctx, database.AdvanceWorldStateChainCursorParams{MsgchainID: chainID, Revision: revision})
	return err
}

// prepareWorldStateRequest only changes a copy used for the provider call. The
// typed snapshot is never appended to the persisted/summarized message chain.
func (fp *flowProvider) prepareWorldStateRequest(ctx context.Context, chain []llms.MessageContent) ([]llms.MessageContent, *worldstate.ModelContext, error) {
	if fp.requestWorldState == nil {
		return chain, nil, nil
	}
	view, err := fp.requestWorldState.Read(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load current World State for model request: %w", err)
	}
	if view == nil {
		return chain, nil, nil
	}
	data, err := json.Marshal(view)
	if err != nil {
		return nil, nil, err
	}
	// Keep the semantic contract in trusted system instructions. Observations
	// remain data in the request copy and are never promoted to system authority.
	requestChain := withWorldStateModelProtocol(chain)
	next, err := appendWorldStateEnvelope(requestChain, "Current World State data:\n"+string(data))
	return next, view, err
}

func (fp *flowProvider) acknowledgeWorldStateRequest(ctx context.Context, opt pconfig.ProviderOptionsType, chainID int64, view *worldstate.ModelContext) error {
	if view == nil || opt != pconfig.OptionsTypePrimaryAgent {
		return nil
	}
	return fp.requestWorldState.Acknowledge(ctx, chainID, view.Revision)
}
