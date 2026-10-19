package worldstate

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"pentagi/pkg/database"
)

const maxPlannerEvidenceStringBytes = 256

type PlannerEvidenceLimits = DeliveryLimits

func DefaultPlannerEvidenceLimits() PlannerEvidenceLimits {
	return PlannerEvidenceLimits{MaxEvents: 32, MaxEntities: 64, MaxLinks: 64, MaxBytes: 32 << 10}
}

type PlannerEvidence struct {
	SchemaVersion int32                     `json:"schema_version,omitempty"`
	Model         *ModelContext             `json:"model,omitempty"`
	FlowID        int64                     `json:"flow_id"`
	CapturedHead  int64                     `json:"captured_head"`
	Projection    PlannerEvidenceProjection `json:"projection"`
	Journal       PlannerEvidenceJournal    `json:"journal"`
}

type PlannerEvidenceProjection struct {
	Summary  PlannerEvidenceSummary  `json:"summary"`
	Entities []PlannerEvidenceEntity `json:"entities,omitempty"`
	Links    []PlannerEvidenceLink   `json:"links,omitempty"`
	Omitted  *PlannerEvidenceOmitted `json:"omitted,omitempty"`
}

type PlannerEvidenceSummary struct {
	Entities int            `json:"entities"`
	Links    int            `json:"links"`
	ByState  map[string]int `json:"by_state"`
	ByType   map[string]int `json:"by_type"`
}

type PlannerEvidenceOmitted struct {
	Entities int `json:"entities"`
	Links    int `json:"links"`
}

type PlannerEvidenceEntity struct {
	ID         int64          `json:"id"`
	Key        string         `json:"key"`
	Type       string         `json:"type"`
	State      string         `json:"state"`
	Version    int32          `json:"version"`
	Properties map[string]any `json:"properties,omitempty"`
}

type PlannerEvidenceLink struct {
	ID         int64          `json:"id"`
	Source     string         `json:"source"`
	Target     string         `json:"target"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type PlannerEvidenceJournal struct {
	AfterRevision   int64                  `json:"after_revision"`
	ThroughRevision int64                  `json:"through_revision"`
	Events          []PlannerEvidenceEvent `json:"events,omitempty"`
	OmittedEvents   int                    `json:"omitted_events,omitempty"`
}

type PlannerEvidenceEvent struct {
	Revision int64                       `json:"revision"`
	Kind     string                      `json:"kind"`
	Entity   *PlannerEvidenceEntityEvent `json:"entity,omitempty"`
	Link     *PlannerEvidenceLinkEvent   `json:"link,omitempty"`
}

type PlannerEvidenceEntityEvent struct {
	ID         int64          `json:"id,omitempty"`
	Key        string         `json:"key,omitempty"`
	Type       string         `json:"type,omitempty"`
	State      string         `json:"state,omitempty"`
	FromState  string         `json:"from_state,omitempty"`
	ToState    string         `json:"to_state,omitempty"`
	Created    *bool          `json:"created,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

type PlannerEvidenceLinkEvent struct {
	ID         int64          `json:"id,omitempty"`
	Source     string         `json:"source,omitempty"`
	Target     string         `json:"target,omitempty"`
	Type       string         `json:"type,omitempty"`
	Created    *bool          `json:"created,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

func (e PlannerEvidence) Bytes() ([]byte, error) {
	return json.Marshal(e)
}

type PlannerEvidenceBuilder struct {
	reader PrimaryDeliveryReader
	limits PlannerEvidenceLimits
}

func NewPlannerEvidenceBuilder(reader PrimaryDeliveryReader, limits PlannerEvidenceLimits) (*PlannerEvidenceBuilder, error) {
	if reader == nil {
		return nil, fmt.Errorf("worldstate: nil planner evidence reader")
	}
	if limits.MaxEvents <= 0 || limits.MaxEntities < 0 || limits.MaxLinks < 0 || limits.MaxBytes <= 0 {
		return nil, fmt.Errorf("worldstate: invalid planner evidence limits")
	}
	return &PlannerEvidenceBuilder{reader: reader, limits: limits}, nil
}

func (b *PlannerEvidenceBuilder) Build(ctx context.Context, flowID int64) (evidence PlannerEvidence, err error) {
	if flowID <= 0 {
		return PlannerEvidence{}, fmt.Errorf("worldstate: invalid flow id %d", flowID)
	}
	reader, closeSnapshot, err := (&PrimaryDeliveryBuilder{reader: b.reader}).beginSnapshot(ctx)
	if err != nil {
		return PlannerEvidence{}, err
	}
	defer func() {
		if closeErr := closeSnapshot(); err == nil && closeErr != nil {
			evidence = PlannerEvidence{}
			err = fmt.Errorf("worldstate: close planner evidence snapshot: %w", closeErr)
		}
	}()
	return b.build(ctx, reader, flowID)
}

func (b *PlannerEvidenceBuilder) build(ctx context.Context, reader PrimaryDeliveryReader, flowID int64) (PlannerEvidence, error) {
	if q, ok := reader.(*database.Queries); ok {
		version, err := ModelVersion(ctx, q, flowID)
		if err != nil {
			return PlannerEvidence{}, err
		}
		if version == 2 {
			snapshot, err := readModelSnapshot(ctx, q, flowID)
			if err != nil {
				return PlannerEvidence{}, err
			}
			return modelPlannerEvidence(snapshot, b.limits)
		}
	}
	head, err := reader.GetWorldStateEventHead(ctx, flowID)
	if err != nil {
		return PlannerEvidence{}, fmt.Errorf("worldstate: capture planner evidence head: %w", err)
	}
	if head < 0 {
		return PlannerEvidence{}, fmt.Errorf("worldstate: invalid planner evidence head %d", head)
	}
	entities, err := reader.GetWorldStateEntitiesByFlow(ctx, flowID)
	if err != nil {
		return PlannerEvidence{}, fmt.Errorf("worldstate: load planner evidence entities: %w", err)
	}
	links, err := reader.GetWorldStateLinksByFlow(ctx, flowID)
	if err != nil {
		return PlannerEvidence{}, fmt.Errorf("worldstate: load planner evidence links: %w", err)
	}
	projection, err := canonicalProjection(flowID, entities, links)
	if err != nil {
		return PlannerEvidence{}, err
	}

	after := max(int64(0), head-int64(b.limits.MaxEvents))
	events, err := reader.GetWorldStateEventsByRevision(ctx, database.GetWorldStateEventsByRevisionParams{
		FlowID: flowID, AfterRevision: after, ThroughRevision: head, LimitRows: int64(b.limits.MaxEvents),
	})
	if err != nil {
		return PlannerEvidence{}, fmt.Errorf("worldstate: load planner evidence journal: %w", err)
	}
	if err := validateAndSortEvents(events, flowID, after, head); err != nil {
		return PlannerEvidence{}, err
	}
	rendered, err := renderEvents(events)
	if err != nil {
		return PlannerEvidence{}, err
	}

	evidence := PlannerEvidence{
		FlowID: flowID, CapturedHead: head,
		Projection: plannerEvidenceProjection(projection, b.limits),
		Journal:    PlannerEvidenceJournal{AfterRevision: after, ThroughRevision: head},
	}
	for _, event := range rendered {
		if safe, ok := plannerEvidenceEvent(event); ok {
			evidence.Journal.Events = append(evidence.Journal.Events, safe)
		} else {
			evidence.Journal.OmittedEvents++
		}
	}
	return fitPlannerEvidence(evidence, b.limits.MaxBytes)
}

func plannerEvidenceProjection(source deliveryProjection, limits PlannerEvidenceLimits) PlannerEvidenceProjection {
	out := PlannerEvidenceProjection{Summary: PlannerEvidenceSummary{
		Entities: source.Summary.Entities, Links: source.Summary.Links,
		ByState: map[string]int{}, ByType: map[string]int{},
	}}
	keys := make(map[string]struct{}, len(source.Entities))
	for _, entity := range source.Entities {
		key, keyOK := plannerEvidenceIdentifierString(entity.Key)
		typ, typeOK := plannerEvidenceEntityType(entity.Type)
		state, stateOK := plannerEvidenceString(entity.State)
		if !keyOK || !typeOK || !stateOK || !State(state).IsKnown() {
			continue
		}
		keys[key] = struct{}{}
		out.Summary.ByState[state]++
		out.Summary.ByType[typ]++
		out.Entities = append(out.Entities, PlannerEvidenceEntity{
			ID: entity.ID, Key: key, Type: typ, State: state, Version: entity.Version,
			Properties: projectJournalProperties(entity.Properties),
		})
	}
	for _, link := range source.Links {
		sourceKey, sourceOK := plannerEvidenceIdentifierString(link.Source)
		targetKey, targetOK := plannerEvidenceIdentifierString(link.Target)
		typ, typeOK := plannerEvidenceString(link.Type)
		_, hasSource := keys[sourceKey]
		_, hasTarget := keys[targetKey]
		if !sourceOK || !targetOK || !typeOK || !hasSource || !hasTarget {
			continue
		}
		out.Links = append(out.Links, PlannerEvidenceLink{
			ID: link.ID, Source: sourceKey, Target: targetKey, Type: typ,
			Properties: projectJournalProperties(link.Properties),
		})
	}
	if len(out.Entities) > limits.MaxEntities {
		out.Entities = out.Entities[:limits.MaxEntities]
	}
	retained := make(map[string]struct{}, len(out.Entities))
	for _, entity := range out.Entities {
		retained[entity.Key] = struct{}{}
	}
	links := out.Links[:0]
	for _, link := range out.Links {
		_, sourceOK := retained[link.Source]
		_, targetOK := retained[link.Target]
		if sourceOK && targetOK {
			links = append(links, link)
		}
	}
	out.Links = links
	if len(out.Links) > limits.MaxLinks {
		out.Links = out.Links[:limits.MaxLinks]
	}
	out.updateOmitted()
	return out
}

func plannerEvidenceEvent(event renderedEvent) (PlannerEvidenceEvent, bool) {
	out := PlannerEvidenceEvent{Revision: event.Revision, Kind: event.Kind}
	switch database.WorldStateEventKind(event.Kind) {
	case database.WorldStateEventKindEntityUpserted, database.WorldStateEventKindEntityTransitioned:
		entity := &PlannerEvidenceEntityEvent{}
		entity.ID, _ = plannerEvidenceInt64(event.Facts["entity_id"])
		var keyOK bool
		entity.Key, keyOK = plannerEvidenceIdentifier(event.Facts["entity_key"])
		entity.Type, _ = plannerEvidenceEntityTypeFact(event.Facts["entity_type"])
		entity.State, _ = plannerEvidenceStateFact(event.Facts["state"])
		entity.FromState, _ = plannerEvidenceStateFact(event.Facts["from_state"])
		entity.ToState, _ = plannerEvidenceStateFact(event.Facts["to_state"])
		entity.Created = plannerEvidenceBool(event.Facts["created"])
		if properties, ok := event.Facts["properties"].(map[string]any); ok {
			entity.Properties = projectJournalProperties(properties)
		}
		if !keyOK {
			return PlannerEvidenceEvent{}, false
		}
		out.Entity = entity
	case database.WorldStateEventKindLinkUpserted:
		link := &PlannerEvidenceLinkEvent{}
		link.ID, _ = plannerEvidenceInt64(event.Facts["link_id"])
		var sourceOK, targetOK, typeOK bool
		link.Source, sourceOK = plannerEvidenceIdentifier(event.Facts["source_key"])
		link.Target, targetOK = plannerEvidenceIdentifier(event.Facts["target_key"])
		link.Type, typeOK = plannerEvidenceFactString(event.Facts["link_type"])
		link.Created = plannerEvidenceBool(event.Facts["created"])
		if properties, ok := event.Facts["properties"].(map[string]any); ok {
			link.Properties = projectJournalProperties(properties)
		}
		if !sourceOK || !targetOK || !typeOK {
			return PlannerEvidenceEvent{}, false
		}
		out.Link = link
	default:
		return PlannerEvidenceEvent{}, false
	}
	return out, true
}

func plannerEvidenceString(value string) (string, bool) {
	value = safeUTF8(value)
	return value, value != "" && len(value) <= maxPlannerEvidenceStringBytes && !authorizationMaterial(value)
}

func plannerEvidenceFactString(value any) (string, bool) {
	text, ok := redactValue(value).(string)
	if !ok || text == redactedValue {
		return "", false
	}
	return plannerEvidenceString(text)
}

func plannerEvidenceEntityType(value string) (string, bool) {
	switch value {
	case EntityTypeHost, EntityTypeEndpoint, EntityTypeFinding, EntityTypeCredential:
		return value, true
	default:
		return "", false
	}
}

func plannerEvidenceEntityTypeFact(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return plannerEvidenceEntityType(text)
}

func plannerEvidenceStateFact(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || !State(text).IsKnown() {
		return "", false
	}
	return text, true
}

func plannerEvidenceIdentifier(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return plannerEvidenceIdentifierString(text)
}

func plannerEvidenceIdentifierString(value string) (string, bool) {
	value = safeUTF8(safeEntityKey(value))
	if strings.HasPrefix(strings.ToLower(value), EntityTypeCredential+":") && strings.Count(value, ":") == 1 {
		return value, len(value) <= maxPlannerEvidenceStringBytes
	}
	return plannerEvidenceString(value)
}

func plannerEvidenceInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil || parsed < 0 {
			return 0, false
		}
		return parsed, true
	case float64:
		const maxExactFloatInteger = float64(1 << 53)
		if number < 0 || number > maxExactFloatInteger || math.Trunc(number) != number {
			return 0, false
		}
		return int64(number), true
	default:
		return 0, false
	}
}

func plannerEvidenceBool(value any) *bool {
	boolean, ok := value.(bool)
	if !ok {
		return nil
	}
	return &boolean
}

func fitPlannerEvidence(evidence PlannerEvidence, maxBytes int) (PlannerEvidence, error) {
	for {
		payload, err := evidence.Bytes()
		if err != nil {
			return PlannerEvidence{}, fmt.Errorf("worldstate: marshal planner evidence: %w", err)
		}
		if len(payload) <= maxBytes {
			return evidence, nil
		}
		switch {
		case evidence.Model != nil && evidence.Model.removeDetail():
			evidence.Model.updateOmitted()
		case len(evidence.Journal.Events) > 0:
			evidence.Journal.Events = evidence.Journal.Events[1:]
			evidence.Journal.OmittedEvents++
		case len(evidence.Projection.Links) > 0:
			evidence.Projection.Links = evidence.Projection.Links[:len(evidence.Projection.Links)-1]
			evidence.Projection.updateOmitted()
		case len(evidence.Projection.Entities) > 0:
			evidence.Projection.Entities = evidence.Projection.Entities[:len(evidence.Projection.Entities)-1]
			evidence.Projection.updateOmitted()
		default:
			return PlannerEvidence{}, fmt.Errorf("%w: planner evidence needs %d bytes, limit is %d", ErrDeliveryTooLarge, len(payload), maxBytes)
		}
	}
}

func (p *PlannerEvidenceProjection) updateOmitted() {
	omitted := PlannerEvidenceOmitted{
		Entities: p.Summary.Entities - len(p.Entities), Links: p.Summary.Links - len(p.Links),
	}
	if omitted.Entities > 0 || omitted.Links > 0 {
		p.Omitted = &omitted
	} else {
		p.Omitted = nil
	}
}
