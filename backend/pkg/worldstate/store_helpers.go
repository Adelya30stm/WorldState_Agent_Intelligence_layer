package worldstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"pentagi/pkg/database"
)

const (
	maxJournalPropertyCount       = 16
	maxJournalPropertyStringBytes = 256
	maxJournalPropertiesBytes     = 2 << 10
)

var journalPropertyKeys = map[string]struct{}{
	"cve": {}, "host": {}, "path": {}, "port": {}, "protocol": {},
	"scheme": {}, "service": {}, "severity": {}, "source": {}, "status": {},
	"title": {}, "tool": {}, "url": {}, "username": {}, "version": {},
}

func lockOrInsertEntity(ctx context.Context, q *database.Queries, flowID int64, entityType, key string, props json.RawMessage) (database.WorldStateEntity, bool, error) {
	params := database.LockWorldStateEntityByKeyParams{FlowID: flowID, EntityKey: key}
	entity, err := q.LockWorldStateEntityByKey(ctx, params)
	if err == nil {
		return entity, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return database.WorldStateEntity{}, false, fmt.Errorf("worldstate: lock entity: %w", err)
	}
	entity, err = q.InsertWorldStateEntity(ctx, database.InsertWorldStateEntityParams{
		FlowID: flowID, EntityKey: key, Type: entityType,
		State: database.WorldStateLifecycleUnknown, Properties: props, Annotations: emptyArray,
	})
	if err == nil {
		return entity, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return database.WorldStateEntity{}, false, fmt.Errorf("worldstate: insert entity: %w", err)
	}
	entity, err = q.LockWorldStateEntityByKey(ctx, params)
	if err != nil {
		return database.WorldStateEntity{}, false, fmt.Errorf("worldstate: lock concurrent entity: %w", err)
	}
	return entity, false, nil
}

func lockLinkEndpoints(ctx context.Context, q *database.Queries, flowID, sourceID, targetID int64) (database.WorldStateEntity, database.WorldStateEntity, error) {
	firstID, secondID := sourceID, targetID
	if firstID > secondID {
		firstID, secondID = secondID, firstID
	}
	first, err := q.LockWorldStateEntityByID(ctx, firstID)
	if err != nil {
		return database.WorldStateEntity{}, database.WorldStateEntity{}, fmt.Errorf("worldstate: lock link endpoint: %w", err)
	}
	second := first
	if secondID != firstID {
		second, err = q.LockWorldStateEntityByID(ctx, secondID)
		if err != nil {
			return database.WorldStateEntity{}, database.WorldStateEntity{}, fmt.Errorf("worldstate: lock link endpoint: %w", err)
		}
	}
	if first.FlowID != flowID || second.FlowID != flowID {
		return database.WorldStateEntity{}, database.WorldStateEntity{}, fmt.Errorf("worldstate: link endpoints do not belong to flow %d", flowID)
	}
	if sourceID == first.ID {
		return first, second, nil
	}
	return second, first, nil
}

func lockOrInsertLink(ctx context.Context, q *database.Queries, flowID, sourceID, targetID int64, linkType string, props json.RawMessage) (database.WorldStateLink, bool, error) {
	lock := database.LockWorldStateLinkParams{FlowID: flowID, SourceID: sourceID, TargetID: targetID, Type: linkType}
	link, err := q.LockWorldStateLink(ctx, lock)
	if err == nil {
		return link, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return database.WorldStateLink{}, false, fmt.Errorf("worldstate: lock link: %w", err)
	}
	link, err = q.InsertWorldStateLink(ctx, database.InsertWorldStateLinkParams{
		FlowID: flowID, SourceID: sourceID, TargetID: targetID, Type: linkType, Properties: props,
	})
	if err == nil {
		return link, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return database.WorldStateLink{}, false, fmt.Errorf("worldstate: insert link: %w", err)
	}
	link, err = q.LockWorldStateLink(ctx, lock)
	if err != nil {
		return database.WorldStateLink{}, false, fmt.Errorf("worldstate: lock concurrent link: %w", err)
	}
	return link, false, nil
}

func createEntityEvent(ctx context.Context, q *database.Queries, entity database.WorldStateEntity, agent string, created bool, properties map[string]any) error {
	facts := map[string]any{
		"entity_id": entity.ID, "entity_key": entity.EntityKey, "entity_type": entity.Type,
		"state": entity.State, "created": created, "properties": properties,
	}
	return createEvent(ctx, q, entity.FlowID, database.WorldStateEventKindEntityUpserted, agent, facts)
}

func createEvent(ctx context.Context, q *database.Queries, flowID int64, kind database.WorldStateEventKind, actor string, facts map[string]any) error {
	if properties, ok := facts["properties"].(map[string]any); ok {
		facts["properties"] = projectJournalProperties(properties)
	}
	safeFacts, err := marshalSafeJSON(facts)
	if err != nil {
		return err
	}
	_, err = q.CreateWorldStateEvent(ctx, database.CreateWorldStateEventParams{
		FlowID: flowID, Kind: kind, Facts: safeFacts, Actor: normalizedAgent(actor),
		Provenance: emptyObject,
	})
	if err != nil {
		return fmt.Errorf("worldstate: write journal event: %w", err)
	}
	return nil
}

func projectJournalProperties(properties map[string]any) map[string]any {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		if _, ok := journalPropertyKeys[key]; ok && !credentialKey(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	projected := make(map[string]any, min(len(keys), maxJournalPropertyCount))
	for _, key := range keys {
		if len(projected) == maxJournalPropertyCount {
			break
		}
		value, ok := journalPropertyValue(properties[key])
		if !ok {
			continue
		}
		projected[key] = value
		encoded, err := json.Marshal(projected)
		if err != nil || len(encoded) > maxJournalPropertiesBytes {
			delete(projected, key)
		}
	}
	return projected
}

func journalPropertyValue(value any) (any, bool) {
	value = redactValue(value)
	switch value := value.(type) {
	case nil, bool, float64, json.Number:
		return value, true
	case string:
		if credentialBearingURL(value) {
			return nil, false
		}
		return value, len(value) <= maxJournalPropertyStringBytes
	default:
		return nil, false
	}
}

func changedProperties(raw json.RawMessage, incoming map[string]any) (map[string]any, error) {
	if len(incoming) == 0 {
		return nil, nil
	}
	existing := map[string]any{}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return nil, fmt.Errorf("worldstate: decode properties: %w", err)
		}
	}
	normalizedRaw, err := json.Marshal(incoming)
	if err != nil {
		return nil, fmt.Errorf("worldstate: normalize properties: %w", err)
	}
	normalized := map[string]any{}
	if err := json.Unmarshal(normalizedRaw, &normalized); err != nil {
		return nil, fmt.Errorf("worldstate: normalize properties: %w", err)
	}
	changed := make(map[string]any)
	for key, value := range normalized {
		if current, ok := existing[key]; !ok || !reflect.DeepEqual(current, value) {
			changed[key] = value
		}
	}
	return changed, nil
}

func safePropertyMap(properties map[string]any) (map[string]any, json.RawMessage, error) {
	if properties == nil {
		properties = map[string]any{}
	}
	raw, err := marshalSafeJSON(redactCredentialBearingURLs(properties))
	if err != nil {
		return nil, nil, err
	}
	var safe map[string]any
	if err := json.Unmarshal(raw, &safe); err != nil {
		return nil, nil, fmt.Errorf("worldstate: decode safe properties: %w", err)
	}
	return safe, raw, nil
}

func credentialBearingURL(value string) bool {
	trimmed := strings.TrimSpace(value)
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return malformedCredentialBearingURL(trimmed)
	}
	if parsed.Scheme == "" || parsed.Host == "" || parsed.User == nil {
		return false
	}
	_, hasPassword := parsed.User.Password()
	return hasPassword
}

func malformedCredentialBearingURL(value string) bool {
	schemeEnd := strings.Index(value, "://")
	if schemeEnd <= 0 {
		return false
	}
	authority := value[schemeEnd+3:]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	userinfoEnd := strings.LastIndexByte(authority, '@')
	if userinfoEnd <= 0 {
		return false
	}
	return strings.Contains(authority[:userinfoEnd], ":")
}

func redactCredentialBearingURLs(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = redactCredentialBearingURLs(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactCredentialBearingURLs(child)
		}
		return out
	case string:
		if credentialBearingURL(typed) {
			return redactedValue
		}
	}
	return value
}
