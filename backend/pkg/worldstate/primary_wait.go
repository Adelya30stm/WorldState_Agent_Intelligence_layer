package worldstate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"pentagi/pkg/database"

	"github.com/vxcontrol/langchaingo/llms"
)

const primaryAskToolName = "ask"

var ErrPrimaryWaitResolved = errors.New("worldstate: primary wait already resolved")

type TransactionDB interface {
	database.Querier
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	WithTx(*sql.Tx) *database.Queries
}

func RegisterPrimaryAskWait(
	ctx context.Context,
	db TransactionDB,
	flowID, msgchainID int64,
	toolCallID string,
	chain []llms.MessageContent,
	durationDelta float64,
) error {
	if toolCallID == "" {
		return fmt.Errorf("worldstate: empty ask tool call ID")
	}
	if !hasExactToolResult(chain, toolCallID, primaryAskToolName) {
		return fmt.Errorf("worldstate: exact pending ask response not found")
	}
	chainBlob, err := json.Marshal(chain)
	if err != nil {
		return fmt.Errorf("worldstate: marshal waiting chain: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := db.WithTx(tx)
	if _, err := q.UpdateMsgChain(ctx, database.UpdateMsgChainParams{
		Chain: chainBlob, DurationSeconds: durationDelta, ID: msgchainID,
	}); err != nil {
		return fmt.Errorf("worldstate: persist waiting chain: %w", err)
	}
	if _, err := q.DeleteAcceptedPrimaryWorldStateWait(ctx, msgchainID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("worldstate: retire accepted primary wait: %w", err)
	}
	if _, err := q.UpsertAgentChainWait(ctx, database.UpsertAgentChainWaitParams{
		WaitKind:          database.AgentChainWaitKindTool,
		PendingToolCallID: sql.NullString{String: toolCallID, Valid: true},
		MsgchainID:        msgchainID, FlowID: flowID,
	}); err != nil {
		return fmt.Errorf("worldstate: register primary wait: %w", err)
	}
	return tx.Commit()
}

func ResolvePrimaryWaitWithUser(
	ctx context.Context,
	db TransactionDB,
	msgchainID int64,
	answer string,
) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	q := db.WithTx(tx)
	wait, err := q.LockAgentChainWait(ctx, msgchainID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if wait.State != database.AgentChainWaitStatePending || wait.ResolutionWinner.Valid {
		if wait.State == database.AgentChainWaitStateResolved && wait.ResolutionWinner.String == "world_state" {
			if err := appendAndPersistHumanInput(ctx, q, wait.MsgchainID, answer); err != nil {
				return true, err
			}
			return true, tx.Commit()
		}
		return true, ErrPrimaryWaitResolved
	}
	if _, err := q.ResolveAgentChainWait(ctx, database.ResolveAgentChainWaitParams{
		ResolutionWinner: sql.NullString{String: "user", Valid: true},
		ResolvedAt:       sql.NullTime{Time: time.Now(), Valid: true},
		ResumeIntent:     json.RawMessage(`{}`), MsgchainID: msgchainID,
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return true, ErrPrimaryWaitResolved
		}
		return true, err
	}
	if err := replaceAndPersistWaitResult(ctx, q, wait, answer); err != nil {
		return true, err
	}
	if err := q.DeleteAgentChainWait(ctx, msgchainID); err != nil {
		return true, err
	}
	return true, tx.Commit()
}

func ResolveLeasedPrimaryWorldStateWait(
	ctx context.Context,
	db TransactionDB,
	wait database.AgentChainWait,
	leaseOwner string,
	head int64,
) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	q := db.WithTx(tx)
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return false, fmt.Errorf("worldstate: create resume generation: %w", err)
	}
	intent, err := json.Marshal(map[string]any{
		"reason": "world_state", "through_revision": head,
		"generation": fmt.Sprintf("%x", generation[:]),
	})
	if err != nil {
		return false, fmt.Errorf("worldstate: marshal resume intent: %w", err)
	}
	resolved, err := q.ResolveLeasedPrimaryWorldStateWait(ctx, database.ResolveLeasedPrimaryWorldStateWaitParams{
		ResolutionRef: sql.NullInt64{Int64: head, Valid: true},
		ResolvedAt:    sql.NullTime{Time: time.Now(), Valid: true},
		ResumeIntent:  intent,
		MsgchainID:    wait.MsgchainID,
		LeaseOwner:    sql.NullString{String: leaseOwner, Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, _ := json.Marshal(map[string]any{
		"status": "world_state_changed", "user_answer_received": false,
		"through_revision": head,
		"message":          "No user answer was received. World State changed; reconsider using the injected update and ask again if needed.",
	})
	if err := replaceAndPersistWaitResult(ctx, q, resolved, string(result)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func replaceAndPersistWaitResult(ctx context.Context, q *database.Queries, wait database.AgentChainWait, result string) error {
	msgchain, err := q.LockMsgChain(ctx, wait.MsgchainID)
	if err != nil {
		return err
	}
	var chain []llms.MessageContent
	if err := json.Unmarshal(msgchain.Chain, &chain); err != nil {
		return err
	}
	if !replaceExactToolResult(chain, wait.PendingToolCallID.String, primaryAskToolName, result) {
		return fmt.Errorf("worldstate: exact pending ask response not found")
	}
	blob, err := json.Marshal(chain)
	if err != nil {
		return err
	}
	_, err = q.UpdateMsgChain(ctx, database.UpdateMsgChainParams{Chain: blob, ID: wait.MsgchainID})
	return err
}

func appendAndPersistHumanInput(ctx context.Context, q *database.Queries, msgchainID int64, input string) error {
	msgchain, err := q.LockMsgChain(ctx, msgchainID)
	if err != nil {
		return err
	}
	var chain []llms.MessageContent
	if err := json.Unmarshal(msgchain.Chain, &chain); err != nil {
		return err
	}
	chain = append(chain, llms.TextParts(llms.ChatMessageTypeHuman, input))
	blob, err := json.Marshal(chain)
	if err != nil {
		return err
	}
	_, err = q.UpdateMsgChain(ctx, database.UpdateMsgChainParams{Chain: blob, ID: msgchainID})
	return err
}

func replaceExactToolResult(chain []llms.MessageContent, callID, name, result string) bool {
	for midx := range chain {
		for pidx, part := range chain[midx].Parts {
			response, ok := part.(llms.ToolCallResponse)
			if ok && response.ToolCallID == callID && response.Name == name {
				response.Content = result
				chain[midx].Parts[pidx] = response
				return true
			}
		}
	}
	return false
}

func hasExactToolResult(chain []llms.MessageContent, callID, name string) bool {
	for _, message := range chain {
		for _, part := range message.Parts {
			response, ok := part.(llms.ToolCallResponse)
			if ok && response.ToolCallID == callID && response.Name == name {
				return true
			}
		}
	}
	return false
}
