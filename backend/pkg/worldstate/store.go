package worldstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"pentagi/pkg/database"
)

var emptyObject = json.RawMessage(`{}`)
var emptyArray = json.RawMessage(`[]`)

// PostCommitHint optionally nudges a scanner after durable state is visible.
type PostCommitHint func(context.Context, int64) error

type transactionalQuerier interface {
	database.Querier
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	WithTx(*sql.Tx) *database.Queries
}

// Store persists World State mutations and their ordered journal facts.
type Store struct {
	db    database.Querier
	hints []PostCommitHint
}

func NewStore(db database.Querier, hints ...PostCommitHint) *Store {
	return &Store{db: db, hints: hints}
}

// Observe ensures an entity exists and atomically journals meaningful changes.
func (s *Store) Observe(
	ctx context.Context,
	flowID int64,
	entityType string,
	entityKey string,
	agent string,
	properties map[string]any,
	evidence map[string]any,
) (database.WorldStateEntity, error) {
	if flowID <= 0 || entityKey == "" || entityType == "" {
		return database.WorldStateEntity{}, fmt.Errorf("worldstate: invalid flow, entity type, or key")
	}
	entityKey = safeEntityKey(entityKey)
	safeProperties, props, err := safePropertyMap(properties)
	if err != nil {
		return database.WorldStateEntity{}, err
	}
	if evidence == nil {
		evidence = map[string]any{}
	}
	safeEvidence, err := marshalSafeJSON(evidence)
	if err != nil {
		return database.WorldStateEntity{}, err
	}
	agent = normalizedAgent(agent)

	var result database.WorldStateEntity
	meaningful := false
	err = s.withTx(ctx, func(q *database.Queries) error {
		entity, created, err := lockOrInsertEntity(ctx, q, flowID, entityType, entityKey, props)
		if err != nil {
			return err
		}
		if created {
			meaningful = true
			if err := createEntityEvent(ctx, q, entity, agent, true, safeProperties); err != nil {
				return err
			}
		} else {
			changes, err := changedProperties(entity.Properties, safeProperties)
			if err != nil {
				return err
			}
			if len(changes) != 0 {
				entity, err = q.MergeWorldStateEntityProperties(ctx, database.MergeWorldStateEntityPropertiesParams{
					Properties: props,
					ID:         entity.ID,
				})
				if err != nil {
					return fmt.Errorf("worldstate: merge entity: %w", err)
				}
				meaningful = true
				if err := createEntityEvent(ctx, q, entity, agent, false, changes); err != nil {
					return err
				}
			}
		}
		if State(entity.State) == StateUnknown {
			entity, err = transitionLoaded(ctx, q, entity, StateDiscovered, agent, safeEvidence)
			if err != nil {
				return err
			}
			meaningful = true
		}
		result = entity
		return nil
	})
	if err != nil {
		return database.WorldStateEntity{}, err
	}
	s.postCommit(ctx, flowID, meaningful)
	return result, nil
}

// Transition locks and revalidates the lifecycle edge before writing audit and journal rows.
func (s *Store) Transition(
	ctx context.Context,
	entityID int64,
	to State,
	agent string,
	evidence json.RawMessage,
) (database.WorldStateEntity, error) {
	safeEvidence, err := redactRawJSON(evidence)
	if err != nil {
		return database.WorldStateEntity{}, err
	}
	agent = normalizedAgent(agent)
	var result database.WorldStateEntity
	err = s.withTx(ctx, func(q *database.Queries) error {
		entity, err := q.LockWorldStateEntityByID(ctx, entityID)
		if err != nil {
			return fmt.Errorf("worldstate: lock entity %d: %w", entityID, err)
		}
		result, err = transitionLoaded(ctx, q, entity, to, agent, safeEvidence)
		return err
	})
	if err != nil {
		return database.WorldStateEntity{}, err
	}
	s.postCommit(ctx, result.FlowID, true)
	return result, nil
}

func transitionLoaded(
	ctx context.Context,
	q *database.Queries,
	entity database.WorldStateEntity,
	to State,
	agent string,
	evidence json.RawMessage,
) (database.WorldStateEntity, error) {
	from := State(entity.State)
	if err := ValidateTransition(from, to); err != nil {
		return database.WorldStateEntity{}, err
	}
	if _, err := q.CreateWorldStateTransition(ctx, database.CreateWorldStateTransitionParams{
		EntityID: entity.ID, FromState: entity.State, ToState: database.WorldStateLifecycle(to),
		Agent: agent, Evidence: evidence,
	}); err != nil {
		return database.WorldStateEntity{}, fmt.Errorf("worldstate: write transition: %w", err)
	}
	updated, err := q.UpdateWorldStateEntityState(ctx, database.UpdateWorldStateEntityStateParams{
		State: database.WorldStateLifecycle(to), ID: entity.ID,
	})
	if err != nil {
		return database.WorldStateEntity{}, fmt.Errorf("worldstate: update state: %w", err)
	}
	facts := map[string]any{
		"entity_id": entity.ID, "entity_key": entity.EntityKey, "entity_type": entity.Type,
		"from_state": from, "to_state": to,
	}
	if err := createEvent(ctx, q, entity.FlowID, database.WorldStateEventKindEntityTransitioned, agent, facts); err != nil {
		return database.WorldStateEntity{}, err
	}
	return updated, nil
}

// PromoteScanning is an idempotent discovered-to-scanning promotion.
func (s *Store) PromoteScanning(ctx context.Context, entityID int64, agent string, evidence map[string]any) error {
	if evidence == nil {
		evidence = map[string]any{}
	}
	ev, err := marshalSafeJSON(evidence)
	if err != nil {
		return err
	}
	_, err = s.Transition(ctx, entityID, StateScanning, agent, ev)
	if errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrUnknownState) {
		return nil
	}
	return err
}

// Link locks endpoints and the logical edge before atomically merging and journalling it.
func (s *Store) Link(
	ctx context.Context,
	flowID, sourceID, targetID int64,
	linkType string,
	properties map[string]any,
) (database.WorldStateLink, error) {
	if flowID <= 0 || sourceID <= 0 || targetID <= 0 || linkType == "" {
		return database.WorldStateLink{}, fmt.Errorf("worldstate: invalid link")
	}
	safeProperties, props, err := safePropertyMap(properties)
	if err != nil {
		return database.WorldStateLink{}, err
	}
	var result database.WorldStateLink
	meaningful := false
	err = s.withTx(ctx, func(q *database.Queries) error {
		source, target, err := lockLinkEndpoints(ctx, q, flowID, sourceID, targetID)
		if err != nil {
			return err
		}
		link, created, err := lockOrInsertLink(ctx, q, flowID, sourceID, targetID, linkType, props)
		if err != nil {
			return err
		}
		changes := safeProperties
		if !created {
			changes, err = changedProperties(link.Properties, safeProperties)
			if err != nil {
				return err
			}
			if len(changes) != 0 {
				link, err = q.MergeWorldStateLinkProperties(ctx, database.MergeWorldStateLinkPropertiesParams{
					Properties: props, ID: link.ID,
				})
				if err != nil {
					return fmt.Errorf("worldstate: merge link: %w", err)
				}
			}
		}
		meaningful = created || len(changes) != 0
		if meaningful {
			facts := map[string]any{
				"link_id": link.ID, "link_type": link.Type, "created": created,
				"source_id": source.ID, "source_key": source.EntityKey,
				"target_id": target.ID, "target_key": target.EntityKey,
				"properties": changes,
			}
			if err := createEvent(ctx, q, flowID, database.WorldStateEventKindLinkUpserted, AgentSystem, facts); err != nil {
				return err
			}
		}
		result = link
		return nil
	})
	if err != nil {
		return database.WorldStateLink{}, err
	}
	s.postCommit(ctx, flowID, meaningful)
	return result, nil
}

func (s *Store) withTx(ctx context.Context, fn func(*database.Queries) error) error {
	db, ok := s.db.(transactionalQuerier)
	if !ok {
		return fmt.Errorf("worldstate: database does not support transactions")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("worldstate: begin transaction: %w", err)
	}
	if err := fn(db.WithTx(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("worldstate: commit transaction: %w", err)
	}
	return nil
}

func (s *Store) postCommit(ctx context.Context, flowID int64, meaningful bool) {
	if !meaningful {
		return
	}
	for _, hint := range s.hints {
		if hint != nil {
			_ = hint(context.WithoutCancel(ctx), flowID)
		}
	}
}

func normalizedAgent(agent string) string {
	if agent == "" {
		return AgentSystem
	}
	return agent
}
