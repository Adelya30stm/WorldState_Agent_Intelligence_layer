package providers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"pentagi/pkg/cast"
	"pentagi/pkg/database"
	"pentagi/pkg/providers/pconfig"
	"pentagi/pkg/worldstate"

	"github.com/sirupsen/logrus"
	"github.com/vxcontrol/langchaingo/llms"
)

const worldStateEnvelopeTag = "world_state"

type primaryWorldStateTurnInjector interface {
	Inject(context.Context, int64, []llms.MessageContent) ([]llms.MessageContent, bool, error)
}

type primaryWorldStateDeliveryBuilder interface {
	Build(context.Context, int64, *int64) (worldstate.PrimaryDelivery, error)
}

type primaryWorldStateDeliveryStore interface {
	GetMsgChain(context.Context, int64) (database.Msgchain, error)
	GetCursor(context.Context, int64) (*int64, error)
	Persist(context.Context, int64, []llms.MessageContent, string, int64) ([]llms.MessageContent, bool, error)
}

type primaryWorldStateDatabase interface {
	database.Querier
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	WithTx(*sql.Tx) *database.Queries
}

type databasePrimaryWorldStateTurnInjector struct {
	flowID  int64
	version func(context.Context) (int32, error)
	builder primaryWorldStateDeliveryBuilder
	store   primaryWorldStateDeliveryStore
}

func (fp *flowProvider) injectWorldStateForTurn(
	ctx context.Context,
	optAgentType pconfig.ProviderOptionsType,
	chainID int64,
	chain []llms.MessageContent,
	logger *logrus.Entry,
) []llms.MessageContent {
	if optAgentType != pconfig.OptionsTypePrimaryAgent || fp.worldState == nil {
		return chain
	}
	nextChain, delivered, err := fp.worldState.Inject(ctx, chainID, chain)
	if err != nil {
		logger.WithError(err).Warn("failed to inject World State delivery")
		return chain
	}
	if !delivered {
		return chain
	}
	return nextChain
}

func newPrimaryWorldStateTurnInjector(db database.Querier, flowID int64) primaryWorldStateTurnInjector {
	queries, ok := db.(primaryWorldStateDatabase)
	if !ok {
		return nil
	}
	builder, err := worldstate.NewPrimaryDeliveryBuilder(queries, worldstate.DefaultDeliveryLimits())
	if err != nil {
		return nil
	}
	return &databasePrimaryWorldStateTurnInjector{
		flowID:  flowID,
		version: func(ctx context.Context) (int32, error) { return worldstate.ModelVersion(ctx, db, flowID) },
		builder: builder,
		store:   &databasePrimaryWorldStateDeliveryStore{queries: queries},
	}
}

func (i *databasePrimaryWorldStateTurnInjector) Inject(
	ctx context.Context,
	chainID int64,
	chain []llms.MessageContent,
) ([]llms.MessageContent, bool, error) {
	if i.version != nil {
		version, err := i.version(ctx)
		if err != nil {
			return nil, false, err
		}
		if version == 2 {
			return chain, false, nil
		}
	}
	msgChain, err := i.store.GetMsgChain(ctx, chainID)
	if err != nil {
		return nil, false, fmt.Errorf("load World State recipient chain: %w", err)
	}
	if msgChain.FlowID != i.flowID || msgChain.Type != database.MsgchainTypePrimaryAgent {
		return chain, false, nil
	}

	cursor, err := i.store.GetCursor(ctx, chainID)
	if err != nil {
		return nil, false, fmt.Errorf("load World State cursor: %w", err)
	}
	delivery, err := i.builder.Build(ctx, i.flowID, cursor)
	if err != nil {
		return nil, false, fmt.Errorf("build World State delivery: %w", err)
	}
	if delivery.Empty() {
		return chain, false, nil
	}
	if !json.Valid(delivery.Payload) {
		return nil, false, fmt.Errorf("format World State delivery: invalid JSON payload")
	}

	envelope := fmt.Sprintf("<%s>\n%s\n</%s>", worldStateEnvelopeTag, delivery.Payload, worldStateEnvelopeTag)
	nextChain, delivered, err := i.store.Persist(ctx, chainID, chain, envelope, delivery.Coverage.ThroughRevision)
	if err != nil {
		return nil, false, fmt.Errorf("persist World State delivery: %w", err)
	}
	return nextChain, delivered, nil
}

func appendWorldStateEnvelope(chain []llms.MessageContent, envelope string) ([]llms.MessageContent, error) {
	chainCopy := make([]llms.MessageContent, len(chain))
	for idx := range chain {
		chainCopy[idx] = chain[idx]
		chainCopy[idx].Parts = append([]llms.ContentPart(nil), chain[idx].Parts...)
	}
	if len(chainCopy) > 0 && chainCopy[len(chainCopy)-1].Role == llms.ChatMessageTypeHuman {
		last := &chainCopy[len(chainCopy)-1]
		last.Parts = append(last.Parts, llms.TextContent{Text: envelope})
		return uniqueToolUseIDs(chainCopy), nil
	}
	// Force normalization here so delivery can be appended even when the stored
	// chain has an unfinished tool-call pair. Without this, injection fails and
	// the next model turn receives a malformed history.
	ast, err := cast.NewChainAST(chainCopy, true)
	if err != nil {
		return nil, fmt.Errorf("prepare chain for World State delivery: %w", err)
	}
	ast.AppendHumanMessage(envelope)
	return uniqueToolUseIDs(ast.Messages()), nil
}

// uniqueToolUseIDs drops or renames duplicate tool_use IDs so Anthropic will
// accept the chain after World State delivery. Extra tool results for the same
// ID are dropped; IDs reused across assistant turns are renamed and remapped.
func uniqueToolUseIDs(chain []llms.MessageContent) []llms.MessageContent {
	out := make([]llms.MessageContent, 0, len(chain))
	used := make(map[string]struct{})
	remap := map[string]string{}
	answered := make(map[string]struct{})
	for msgIdx, msg := range chain {
		msg.Parts = append([]llms.ContentPart(nil), msg.Parts...)
		switch msg.Role {
		case llms.ChatMessageTypeAI:
			remap = map[string]string{}
			answered = make(map[string]struct{})
			parts := make([]llms.ContentPart, 0, len(msg.Parts))
			seenHere := make(map[string]struct{})
			for partIdx, part := range msg.Parts {
				toolCall, ok := part.(llms.ToolCall)
				if !ok || toolCall.FunctionCall == nil {
					parts = append(parts, part)
					continue
				}
				id := toolCall.ID
				if id == "" {
					id = fmt.Sprintf("toolu_dedup_%d_%d", msgIdx, partIdx)
					toolCall.ID = id
					part = toolCall
				}
				if _, dup := seenHere[id]; dup {
					continue
				}
				if _, exists := used[id]; exists {
					newID := fmt.Sprintf("toolu_dedup_%d_%d", msgIdx, partIdx)
					remap[id] = newID
					id = newID
					toolCall.ID = newID
					part = toolCall
				}
				seenHere[id] = struct{}{}
				used[id] = struct{}{}
				parts = append(parts, part)
			}
			msg.Parts = parts
		case llms.ChatMessageTypeTool:
			parts := make([]llms.ContentPart, 0, len(msg.Parts))
			for _, part := range msg.Parts {
				resp, ok := part.(llms.ToolCallResponse)
				if !ok {
					parts = append(parts, part)
					continue
				}
				if newID, ok := remap[resp.ToolCallID]; ok {
					resp.ToolCallID = newID
					part = resp
				}
				if _, dup := answered[resp.ToolCallID]; dup {
					continue
				}
				answered[resp.ToolCallID] = struct{}{}
				parts = append(parts, part)
			}
			if len(parts) == 0 {
				continue
			}
			msg.Parts = parts
		}
		out = append(out, msg)
	}
	return out
}

type databasePrimaryWorldStateDeliveryStore struct {
	queries primaryWorldStateDatabase
}

func (s *databasePrimaryWorldStateDeliveryStore) GetMsgChain(ctx context.Context, chainID int64) (database.Msgchain, error) {
	return s.queries.GetMsgChain(ctx, chainID)
}

func (s *databasePrimaryWorldStateDeliveryStore) GetCursor(ctx context.Context, chainID int64) (*int64, error) {
	cursor, err := s.queries.GetWorldStateChainCursor(ctx, chainID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &cursor.Revision, nil
}

func (s *databasePrimaryWorldStateDeliveryStore) Persist(
	ctx context.Context,
	chainID int64,
	chain []llms.MessageContent,
	envelope string,
	revision int64,
) (nextChain []llms.MessageContent, delivered bool, err error) {
	tx, err := s.queries.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); err == nil && rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			err = rollbackErr
		}
	}()

	queries := s.queries.WithTx(tx)
	msgChain, err := queries.LockMsgChain(ctx, chainID)
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(msgChain.Chain, &chain); err != nil {
		return nil, false, err
	}
	cursor, cursorErr := queries.GetWorldStateChainCursor(ctx, chainID)
	if cursorErr != nil && !errors.Is(cursorErr, sql.ErrNoRows) {
		return nil, false, cursorErr
	}
	if cursorErr == nil && cursor.Revision >= revision {
		return chain, true, nil
	}
	nextChain, err = appendWorldStateEnvelope(chain, envelope)
	if err != nil {
		return nil, false, err
	}
	chainJSON, err := json.Marshal(nextChain)
	if err != nil {
		return nil, false, err
	}
	if _, err = queries.UpdateMsgChain(ctx, database.UpdateMsgChainParams{
		Chain: chainJSON, ID: chainID,
	}); err != nil {
		return nil, false, err
	}
	if _, err = queries.AdvanceWorldStateChainCursor(ctx, database.AdvanceWorldStateChainCursorParams{
		Revision: revision, MsgchainID: chainID,
	}); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return nextChain, true, nil
}
