package worldstate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"pentagi/pkg/database"
)

type DeliveryKind string

const (
	DeliveryNone       DeliveryKind = "none"
	DeliveryBaseline   DeliveryKind = "baseline"
	DeliveryDelta      DeliveryKind = "delta"
	DeliveryCheckpoint DeliveryKind = "checkpoint"
)

type CheckpointReason string

const (
	CheckpointEventLimit CheckpointReason = "event_count_limit"
	CheckpointByteLimit  CheckpointReason = "byte_limit"
)

var ErrDeliveryTooLarge = errors.New("worldstate: delivery metadata exceeds byte limit")

type DeliveryLimits struct {
	MaxEvents   int
	MaxEntities int
	MaxLinks    int
	MaxBytes    int
}

func DefaultDeliveryLimits() DeliveryLimits {
	return DeliveryLimits{MaxEvents: 64, MaxEntities: 128, MaxLinks: 128, MaxBytes: 64 << 10}
}

type RevisionCoverage struct {
	AfterRevision   *int64 `json:"after_revision"`
	ThroughRevision int64  `json:"through_revision"`
}

type PrimaryDelivery struct {
	Kind             DeliveryKind
	Coverage         RevisionCoverage
	Payload          []byte
	EventCount       int
	CheckpointReason CheckpointReason
}

func (d PrimaryDelivery) Empty() bool { return d.Kind == DeliveryNone }

type PrimaryDeliveryReader interface {
	GetWorldStateEventHead(context.Context, int64) (int64, error)
	GetWorldStateEventsByRevision(context.Context, database.GetWorldStateEventsByRevisionParams) ([]database.WorldStateEvent, error)
	GetWorldStateEntitiesByFlow(context.Context, int64) ([]database.WorldStateEntity, error)
	GetWorldStateLinksByFlow(context.Context, int64) ([]database.WorldStateLink, error)
}

var _ PrimaryDeliveryReader = (*database.Queries)(nil)

type primaryDeliverySnapshotter interface {
	beginPrimaryDeliverySnapshot(context.Context) (PrimaryDeliveryReader, func() error, error)
}

type PrimaryDeliveryBuilder struct {
	reader PrimaryDeliveryReader
	limits DeliveryLimits
}

func NewPrimaryDeliveryBuilder(reader PrimaryDeliveryReader, limits DeliveryLimits) (*PrimaryDeliveryBuilder, error) {
	if reader == nil {
		return nil, fmt.Errorf("worldstate: nil primary delivery reader")
	}
	if limits.MaxEvents <= 0 || limits.MaxEntities < 0 || limits.MaxLinks < 0 || limits.MaxBytes <= 0 {
		return nil, fmt.Errorf("worldstate: invalid delivery limits")
	}
	return &PrimaryDeliveryBuilder{reader: reader, limits: limits}, nil
}

func (b *PrimaryDeliveryBuilder) Build(ctx context.Context, flowID int64, cursor *int64) (delivery PrimaryDelivery, err error) {
	if flowID <= 0 {
		return PrimaryDelivery{}, fmt.Errorf("worldstate: invalid flow id %d", flowID)
	}
	reader, closeSnapshot, err := b.beginSnapshot(ctx)
	if err != nil {
		return PrimaryDelivery{}, err
	}
	defer func() {
		if closeErr := closeSnapshot(); err == nil && closeErr != nil {
			delivery = PrimaryDelivery{}
			err = fmt.Errorf("worldstate: close primary delivery snapshot: %w", closeErr)
		}
	}()
	return b.build(ctx, reader, flowID, cursor)
}

func (b *PrimaryDeliveryBuilder) beginSnapshot(ctx context.Context) (PrimaryDeliveryReader, func() error, error) {
	if snapshotter, ok := b.reader.(primaryDeliverySnapshotter); ok {
		return snapshotter.beginPrimaryDeliverySnapshot(ctx)
	}
	queries, ok := b.reader.(*database.Queries)
	if !ok {
		return nil, nil, fmt.Errorf("worldstate: reader does not provide a consistent snapshot")
	}
	tx, err := queries.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("worldstate: begin primary delivery snapshot: %w", err)
	}
	return queries.WithTx(tx), tx.Rollback, nil
}

func (b *PrimaryDeliveryBuilder) build(ctx context.Context, reader PrimaryDeliveryReader, flowID int64, cursor *int64) (PrimaryDelivery, error) {
	head, err := reader.GetWorldStateEventHead(ctx, flowID)
	if err != nil {
		return PrimaryDelivery{}, fmt.Errorf("worldstate: capture event head: %w", err)
	}
	if cursor == nil {
		return b.buildProjection(ctx, reader, flowID, nil, head, DeliveryBaseline, "")
	}
	if *cursor > head {
		return PrimaryDelivery{}, fmt.Errorf("worldstate: cursor %d is ahead of captured head %d", *cursor, head)
	}
	if *cursor == head {
		return PrimaryDelivery{Kind: DeliveryNone, Coverage: RevisionCoverage{AfterRevision: copyRevision(cursor), ThroughRevision: head}}, nil
	}

	events, err := reader.GetWorldStateEventsByRevision(ctx, database.GetWorldStateEventsByRevisionParams{
		FlowID: flowID, AfterRevision: *cursor, ThroughRevision: head, LimitRows: int64(b.limits.MaxEvents) + 1,
	})
	if err != nil {
		return PrimaryDelivery{}, fmt.Errorf("worldstate: load event range: %w", err)
	}
	if len(events) > b.limits.MaxEvents {
		return b.buildProjection(ctx, reader, flowID, cursor, head, DeliveryCheckpoint, CheckpointEventLimit)
	}
	if err := validateAndSortEvents(events, flowID, *cursor, head); err != nil {
		return PrimaryDelivery{}, err
	}
	if len(events) == 0 {
		return PrimaryDelivery{}, fmt.Errorf("worldstate: captured nonempty revision range has no events")
	}

	rendered, err := renderEvents(events)
	if err != nil {
		return PrimaryDelivery{}, err
	}
	envelope := deliveryEnvelope{
		Type: DeliveryDelta, FlowID: flowID,
		Coverage: RevisionCoverage{AfterRevision: copyRevision(cursor), ThroughRevision: head},
		Events:   rendered,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return PrimaryDelivery{}, fmt.Errorf("worldstate: marshal delta: %w", err)
	}
	if len(payload) > b.limits.MaxBytes {
		return b.buildProjection(ctx, reader, flowID, cursor, head, DeliveryCheckpoint, CheckpointByteLimit)
	}
	return PrimaryDelivery{Kind: DeliveryDelta, Coverage: envelope.Coverage, Payload: payload, EventCount: len(events)}, nil
}

type deliveryEnvelope struct {
	Type             DeliveryKind        `json:"type"`
	FlowID           int64               `json:"flow_id"`
	Coverage         RevisionCoverage    `json:"coverage"`
	CheckpointReason CheckpointReason    `json:"checkpoint_reason,omitempty"`
	Projection       *deliveryProjection `json:"projection,omitempty"`
	Events           []renderedEvent     `json:"events,omitempty"`
}

type renderedEvent struct {
	Revision  int64          `json:"revision"`
	Kind      string         `json:"kind"`
	Rendering string         `json:"rendering,omitempty"`
	Actor     string         `json:"actor,omitempty"`
	Facts     map[string]any `json:"facts"`
}

type deliveryProjection struct {
	Summary  projectionSummary  `json:"summary"`
	Entities []projectedEntity  `json:"entities,omitempty"`
	Links    []projectedLink    `json:"links,omitempty"`
	Omitted  *projectionOmitted `json:"omitted,omitempty"`
}

type projectionSummary struct {
	Entities int            `json:"entities"`
	Links    int            `json:"links"`
	ByState  map[string]int `json:"by_state"`
	ByType   map[string]int `json:"by_type"`
}

type projectionOmitted struct {
	Entities int `json:"entities"`
	Links    int `json:"links"`
}

type projectedEntity struct {
	ID         int64          `json:"-"`
	Key        string         `json:"key"`
	Type       string         `json:"type"`
	State      string         `json:"state"`
	Version    int32          `json:"-"`
	Properties map[string]any `json:"properties,omitempty"`
}

type projectedLink struct {
	ID         int64          `json:"-"`
	Source     string         `json:"source"`
	Target     string         `json:"target"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

func (b *PrimaryDeliveryBuilder) buildProjection(
	ctx context.Context,
	reader PrimaryDeliveryReader,
	flowID int64,
	cursor *int64,
	head int64,
	kind DeliveryKind,
	reason CheckpointReason,
) (PrimaryDelivery, error) {
	entities, err := reader.GetWorldStateEntitiesByFlow(ctx, flowID)
	if err != nil {
		return PrimaryDelivery{}, fmt.Errorf("worldstate: load projection entities: %w", err)
	}
	links, err := reader.GetWorldStateLinksByFlow(ctx, flowID)
	if err != nil {
		return PrimaryDelivery{}, fmt.Errorf("worldstate: load projection links: %w", err)
	}
	projection, err := canonicalProjection(flowID, entities, links)
	if err != nil {
		return PrimaryDelivery{}, err
	}
	projection.applyCountLimits(b.limits)
	envelope := deliveryEnvelope{
		Type: kind, FlowID: flowID,
		Coverage:         RevisionCoverage{AfterRevision: copyRevision(cursor), ThroughRevision: head},
		CheckpointReason: reason, Projection: &projection,
	}
	payload, err := fitProjectionEnvelope(envelope, b.limits.MaxBytes)
	if err != nil {
		return PrimaryDelivery{}, err
	}
	return PrimaryDelivery{
		Kind: kind, Coverage: envelope.Coverage, Payload: payload, CheckpointReason: reason,
	}, nil
}

func validateAndSortEvents(events []database.WorldStateEvent, flowID, after, through int64) error {
	sort.Slice(events, func(i, j int) bool { return events[i].Revision < events[j].Revision })
	var previous int64
	for i, event := range events {
		if event.FlowID != flowID {
			return fmt.Errorf("worldstate: event %d belongs to flow %d, expected %d", event.Revision, event.FlowID, flowID)
		}
		if event.Revision <= after || event.Revision > through {
			return fmt.Errorf("worldstate: event revision %d outside captured range (%d,%d]", event.Revision, after, through)
		}
		if i > 0 && event.Revision == previous {
			return fmt.Errorf("worldstate: duplicate event revision %d", event.Revision)
		}
		previous = event.Revision
	}
	return nil
}

func renderEvents(events []database.WorldStateEvent) ([]renderedEvent, error) {
	out := make([]renderedEvent, 0, len(events))
	for _, event := range events {
		facts, err := decodeRedactedObject(event.Facts)
		if err != nil {
			return nil, fmt.Errorf("worldstate: malformed facts at revision %d: %w", event.Revision, err)
		}
		rendering := ""
		switch event.Kind {
		case database.WorldStateEventKindEntityUpserted,
			database.WorldStateEventKindEntityTransitioned,
			database.WorldStateEventKindLinkUpserted:
		default:
			rendering = "generic"
		}
		out = append(out, renderedEvent{
			Revision: event.Revision, Kind: safeUTF8(string(event.Kind)), Rendering: rendering,
			Actor: safeUTF8(event.Actor), Facts: facts,
		})
	}
	return out, nil
}

func canonicalProjection(flowID int64, entities []database.WorldStateEntity, links []database.WorldStateLink) (deliveryProjection, error) {
	p := deliveryProjection{Summary: projectionSummary{Entities: len(entities), Links: len(links), ByState: map[string]int{}, ByType: map[string]int{}}}
	keys := make(map[int64]string, len(entities))
	for _, entity := range entities {
		if entity.FlowID != flowID {
			return deliveryProjection{}, fmt.Errorf("worldstate: projection entity %d belongs to flow %d", entity.ID, entity.FlowID)
		}
		properties, err := decodeProjectedProperties(entity.Properties)
		if err != nil {
			return deliveryProjection{}, fmt.Errorf("worldstate: malformed properties for entity %q: %w", safeEntityKey(entity.EntityKey), err)
		}
		key, typ, state := safeUTF8(safeEntityKey(entity.EntityKey)), safeUTF8(entity.Type), safeUTF8(string(entity.State))
		keys[entity.ID] = key
		p.Summary.ByState[state]++
		p.Summary.ByType[typ]++
		p.Entities = append(p.Entities, projectedEntity{ID: entity.ID, Key: key, Type: typ, State: state, Version: entity.Version, Properties: properties})
	}
	for _, link := range links {
		if link.FlowID != flowID {
			return deliveryProjection{}, fmt.Errorf("worldstate: projection link %d belongs to flow %d", link.ID, link.FlowID)
		}
		source, sourceOK := keys[link.SourceID]
		target, targetOK := keys[link.TargetID]
		if !sourceOK || !targetOK {
			return deliveryProjection{}, fmt.Errorf("worldstate: projection link %d has missing endpoint", link.ID)
		}
		properties, err := decodeProjectedProperties(link.Properties)
		if err != nil {
			return deliveryProjection{}, fmt.Errorf("worldstate: malformed properties for link %d: %w", link.ID, err)
		}
		p.Links = append(p.Links, projectedLink{ID: link.ID, Source: source, Target: target, Type: safeUTF8(link.Type), Properties: properties})
	}
	sort.Slice(p.Entities, func(i, j int) bool {
		if p.Entities[i].Key != p.Entities[j].Key {
			return p.Entities[i].Key < p.Entities[j].Key
		}
		if p.Entities[i].Type != p.Entities[j].Type {
			return p.Entities[i].Type < p.Entities[j].Type
		}
		if p.Entities[i].State != p.Entities[j].State {
			return p.Entities[i].State < p.Entities[j].State
		}
		return p.Entities[i].ID < p.Entities[j].ID
	})
	sort.Slice(p.Links, func(i, j int) bool {
		if p.Links[i].Source != p.Links[j].Source {
			return p.Links[i].Source < p.Links[j].Source
		}
		if p.Links[i].Target != p.Links[j].Target {
			return p.Links[i].Target < p.Links[j].Target
		}
		if p.Links[i].Type != p.Links[j].Type {
			return p.Links[i].Type < p.Links[j].Type
		}
		return p.Links[i].ID < p.Links[j].ID
	})
	return p, nil
}

func (p *deliveryProjection) applyCountLimits(limits DeliveryLimits) {
	if len(p.Entities) > limits.MaxEntities {
		p.Entities = p.Entities[:limits.MaxEntities]
	}
	if len(p.Links) > limits.MaxLinks {
		p.Links = p.Links[:limits.MaxLinks]
	}
	p.updateOmitted()
}

func (p *deliveryProjection) updateOmitted() {
	omitted := projectionOmitted{Entities: p.Summary.Entities - len(p.Entities), Links: p.Summary.Links - len(p.Links)}
	if omitted.Entities > 0 || omitted.Links > 0 {
		p.Omitted = &omitted
	} else {
		p.Omitted = nil
	}
}

func fitProjectionEnvelope(envelope deliveryEnvelope, maxBytes int) ([]byte, error) {
	for {
		payload, err := json.Marshal(envelope)
		if err != nil {
			return nil, fmt.Errorf("worldstate: marshal projection: %w", err)
		}
		if len(payload) <= maxBytes {
			return payload, nil
		}
		projection := envelope.Projection
		switch {
		case len(projection.Links) > 0:
			projection.Links = projection.Links[:len(projection.Links)-1]
		case len(projection.Entities) > 0:
			projection.Entities = projection.Entities[:len(projection.Entities)-1]
		default:
			return nil, fmt.Errorf("%w: need %d bytes, limit is %d", ErrDeliveryTooLarge, len(payload), maxBytes)
		}
		projection.updateOmitted()
	}
}

func decodeRedactedObject(raw json.RawMessage) (map[string]any, error) {
	value, err := decodeJSONValue(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected JSON object")
	}
	redacted, ok := redactValue(normalizeDeliveryValue(object)).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected redacted JSON object")
	}
	return redacted, nil
}

func decodeProjectedProperties(raw json.RawMessage) (map[string]any, error) {
	value, err := decodeJSONValue(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected JSON object")
	}
	normalized, ok := normalizeDeliveryValue(object).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected normalized JSON object")
	}
	return projectJournalProperties(normalized), nil
}

func decodeJSONValue(raw json.RawMessage) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("expected one JSON value")
		}
		return nil, err
	}
	return value, nil
}

func normalizeDeliveryValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			out[safeUTF8(key)] = normalizeDeliveryValue(child)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i := range value {
			out[i] = normalizeDeliveryValue(value[i])
		}
		return out
	case string:
		return safeUTF8(value)
	default:
		return value
	}
}

func safeUTF8(value string) string {
	if utf8.ValidString(value) {
		return value
	}
	return strings.ToValidUTF8(value, string(utf8.RuneError))
}

func copyRevision(revision *int64) *int64 {
	if revision == nil {
		return nil
	}
	value := *revision
	return &value
}
