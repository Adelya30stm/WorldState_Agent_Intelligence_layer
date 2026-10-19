package worldstate

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pentagi/pkg/database"
)

func TestPlannerEvidenceIsDeterministicAndDisclosureBounded(t *testing.T) {
	secret := "planner-secret-sentinel"
	credentialURL := "https://admin:p%40ss@example.invalid/path"
	malformedCredentialURL := "https://admin:p%ss@example.invalid/path"
	safeURL := "https://example.invalid/path"
	oversized := strings.Repeat("oversized-sentinel", 100)
	entities := []database.WorldStateEntity{
		{
			ID: 2, FlowID: 7, EntityKey: "credential:token:" + secret, Type: EntityTypeCredential,
			State: database.WorldStateLifecycleDiscovered, Version: 3,
			Properties: plannerJSON(t, map[string]any{"username": "operator", "token": secret, "command": secret}),
		},
		{
			ID: 1, FlowID: 7, EntityKey: "host:target.example", Type: EntityTypeHost,
			State: database.WorldStateLifecycleScanning, Version: 4,
			Properties: plannerJSON(t, map[string]any{
				"host": "target.example", "port": 443, "status": "open", "url": credentialURL,
				"password": secret, "authorization": secret, "cookie": secret,
				"private_key": secret, "private-key": secret, "transcript": secret, "command": secret,
				"output": secret, "request_body": secret, "nested": map[string]any{"token": secret},
				"title": oversized,
			}),
		},
		{
			ID: 3, FlowID: 7, EntityKey: "host:malformed.example", Type: EntityTypeHost,
			State: database.WorldStateLifecycleDiscovered, Version: 1,
			Properties: plannerJSON(t, map[string]any{"url": malformedCredentialURL}),
		},
	}
	links := []database.WorldStateLink{{
		ID: 9, FlowID: 7, SourceID: 1, TargetID: 2, Type: "observed",
		Properties: plannerJSON(t, map[string]any{"protocol": "https", "url": safeURL, "headers": map[string]any{"Authorization": secret}, "command": secret}),
	}}
	events := []database.WorldStateEvent{
		plannerEvent(3, 7, database.WorldStateEventKindEntityTransitioned, map[string]any{
			"entity_id": 1, "entity_key": "host:target.example", "entity_type": "host",
			"from_state": "discovered", "to_state": "scanning", "transcript": secret,
		}),
		plannerEvent(1, 7, database.WorldStateEventKindEntityUpserted, map[string]any{
			"entity_id": 1, "entity_key": "host:target.example", "entity_type": "host", "state": "discovered", "created": true,
			"properties": map[string]any{"host": "target.example", "port": 443, "url": malformedCredentialURL, "password": secret, "command": secret, "nested": map[string]any{"cookie": secret}},
			"headers":    map[string]any{"Authorization": secret}, "body": secret,
		}),
		plannerEvent(2, 7, database.WorldStateEventKindLinkUpserted, map[string]any{
			"link_id": 9, "source_key": "host:target.example", "target_key": "credential:token:" + secret,
			"link_type": "observed", "created": true, "properties": map[string]any{"protocol": "https", "private_key": secret},
		}),
		plannerEvent(4, 7, database.WorldStateEventKind("future_unsafe"), map[string]any{"evidence": secret, "safe": "arbitrary-json"}),
	}

	first := buildPlannerEvidence(t, &deliveryReaderStub{events: events, entities: entities, links: links}, DefaultPlannerEvidenceLimits(), 7)
	second := buildPlannerEvidence(t, &deliveryReaderStub{
		events:   []database.WorldStateEvent{events[1], events[3], events[2], events[0]},
		entities: []database.WorldStateEntity{entities[1], entities[0], entities[2]}, links: links,
	}, DefaultPlannerEvidenceLimits(), 7)
	firstBytes := plannerEvidenceBytes(t, first)
	secondBytes := plannerEvidenceBytes(t, second)
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("planner evidence is not byte stable:\n%s\n%s", firstBytes, secondBytes)
	}
	if first.CapturedHead != 4 || first.Journal.ThroughRevision != 4 || len(first.Journal.Events) != 3 || first.Journal.OmittedEvents != 1 {
		t.Fatalf("unexpected revision coverage: %+v", first.Journal)
	}
	if first.Projection.Entities[0].ID != 2 && first.Projection.Entities[0].ID != 1 {
		t.Fatalf("stable entity identifiers missing: %+v", first.Projection.Entities)
	}
	if len(first.Projection.Links) != 1 || first.Projection.Links[0].ID != 9 {
		t.Fatalf("stable link identifier missing: %+v", first.Projection.Links)
	}
	port, portOK := first.Journal.Events[0].Entity.Properties["port"].(json.Number)
	if !portOK || port != json.Number("443") {
		t.Fatalf("allowlisted numeric journal property was not preserved: %#v", first.Journal.Events[0].Entity.Properties["port"])
	}
	text := string(firstBytes)
	for _, forbidden := range []string{
		secret, credentialURL, malformedCredentialURL, oversized, "arbitrary-json", "password", "authorization", "cookie", "private_key", "private-key",
		"command", "transcript", "output", "request_body", "headers", "body", "nested",
	} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("planner evidence contains forbidden sentinel %q: %s", forbidden, text)
		}
	}
	for _, expected := range []string{"target.example", safeURL, `"port":443`, `"version":4`, `"revision":1`, `"revision":2`, `"revision":3`, "credential:token"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("planner evidence missing operational field %q: %s", expected, text)
		}
	}
}

func TestPlannerEvidenceUsesCapturedSnapshot(t *testing.T) {
	reader := &deliveryReaderStub{
		events: []database.WorldStateEvent{plannerEvent(1, 4, database.WorldStateEventKindEntityUpserted, map[string]any{
			"entity_id": 1, "entity_key": "host:before.example", "entity_type": "host", "state": "discovered",
		})},
		entities: []database.WorldStateEntity{{
			ID: 1, FlowID: 4, EntityKey: "host:before.example", Type: EntityTypeHost,
			State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{"host":"before.example"}`),
		}},
	}
	reader.afterHead = func() {
		reader.events = append(reader.events, plannerEvent(2, 4, database.WorldStateEventKindEntityUpserted, map[string]any{
			"entity_id": 2, "entity_key": "host:late.example", "entity_type": "host", "state": "discovered",
		}))
		reader.entities[0].EntityKey = "host:late.example"
	}
	evidence := buildPlannerEvidence(t, reader, DefaultPlannerEvidenceLimits(), 4)
	payload := plannerEvidenceBytes(t, evidence)
	if evidence.CapturedHead != 1 || bytes.Contains(payload, []byte("late.example")) || !bytes.Contains(payload, []byte("before.example")) {
		t.Fatalf("planner evidence crossed captured head: %s", payload)
	}
}

func TestPlannerEvidenceUsesGlobalRevisionBoundaryAcrossGaps(t *testing.T) {
	limits := DefaultPlannerEvidenceLimits()
	limits.MaxEvents = 2
	reader := &plannerHeadReader{
		deliveryReaderStub: &deliveryReaderStub{events: []database.WorldStateEvent{
			plannerEvent(37, 4, database.WorldStateEventKindEntityUpserted, map[string]any{
				"entity_id": 1, "entity_key": "host:gapped.example", "entity_type": "host", "state": "discovered",
			}),
		}},
		head: 37,
	}
	evidence := buildPlannerEvidence(t, reader, limits, 4)
	if evidence.Journal.AfterRevision != 35 || evidence.Journal.ThroughRevision != 37 || len(evidence.Journal.Events) != 1 {
		t.Fatalf("unexpected global revision coverage: %+v", evidence.Journal)
	}
}

func TestPlannerEvidenceRequiresSnapshotAndNonNegativeHead(t *testing.T) {
	nonSnapshot := &nonSnapshotPlannerEvidenceReader{base: unscopedPlannerEvidenceReader{head: 1}}
	builder, err := NewPlannerEvidenceBuilder(nonSnapshot, DefaultPlannerEvidenceLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Build(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "consistent snapshot") {
		t.Fatalf("expected consistent snapshot rejection, got %v", err)
	}
	if nonSnapshot.headCalled {
		t.Fatal("non-snapshot reader was queried before snapshot validation")
	}

	negative := &plannerHeadReader{deliveryReaderStub: &deliveryReaderStub{}, head: -1}
	builder, err = NewPlannerEvidenceBuilder(negative, DefaultPlannerEvidenceLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builder.Build(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "invalid planner evidence head") {
		t.Fatalf("expected negative head rejection, got %v", err)
	}
}

func TestPlannerEvidenceValidatesJournalIDsExactly(t *testing.T) {
	reader := &deliveryReaderStub{events: []database.WorldStateEvent{
		{Revision: 1, FlowID: 1, Kind: database.WorldStateEventKindEntityUpserted, Facts: json.RawMessage(`{"entity_id":9007199254740993,"entity_key":"host:rounded","entity_type":"host","state":"discovered"}`)},
		{Revision: 2, FlowID: 1, Kind: database.WorldStateEventKindEntityUpserted, Facts: json.RawMessage(`{"entity_id":9223372036854775807,"entity_key":"host:max","entity_type":"host","state":"discovered"}`)},
		{Revision: 3, FlowID: 1, Kind: database.WorldStateEventKindEntityUpserted, Facts: json.RawMessage(`{"entity_id":9223372036854775808,"entity_key":"host:overflow","entity_type":"host","state":"discovered"}`)},
		{Revision: 4, FlowID: 1, Kind: database.WorldStateEventKindEntityUpserted, Facts: json.RawMessage(`{"entity_id":-1,"entity_key":"host:negative","entity_type":"host","state":"discovered"}`)},
	}}
	builder, err := NewPlannerEvidenceBuilder(reader, DefaultPlannerEvidenceLimits())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := builder.Build(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Journal.Events) != 4 {
		t.Fatalf("unexpected event count: %+v", evidence.Journal)
	}
	if got := evidence.Journal.Events[0].Entity.ID; got != 9007199254740993 {
		t.Fatalf("expected exact 2^53+1 ID to survive, got %d", got)
	}
	if got := evidence.Journal.Events[1].Entity.ID; got != 9223372036854775807 {
		t.Fatalf("expected MaxInt64 ID to survive, got %d", got)
	}
	if got := evidence.Journal.Events[2].Entity.ID; got != 0 {
		t.Fatalf("expected 2^63 ID to be omitted, got %d", got)
	}
	if got := evidence.Journal.Events[3].Entity.ID; got != 0 {
		t.Fatalf("expected negative ID to be omitted, got %d", got)
	}
}

func TestPlannerEvidenceProjectionPreservesExactJSONNumbers(t *testing.T) {
	reader := &deliveryReaderStub{entities: []database.WorldStateEntity{{
		ID: 1, FlowID: 1, EntityKey: "host:exact", Type: EntityTypeHost,
		State:      database.WorldStateLifecycleDiscovered,
		Properties: json.RawMessage(`{"port":9007199254740993}`),
	}}}
	builder, err := NewPlannerEvidenceBuilder(reader, DefaultPlannerEvidenceLimits())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := builder.Build(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	port, ok := evidence.Projection.Entities[0].Properties["port"].(json.Number)
	if !ok || port != json.Number("9007199254740993") {
		t.Fatalf("projection integer lost precision: %#v", evidence.Projection.Entities[0].Properties["port"])
	}
}

func TestPlannerEvidenceLimitPolicy(t *testing.T) {
	base := DefaultPlannerEvidenceLimits()
	tests := []struct {
		name   string
		limits PlannerEvidenceLimits
		valid  bool
	}{
		{name: "zero events", limits: PlannerEvidenceLimits{MaxEvents: 0, MaxEntities: base.MaxEntities, MaxLinks: base.MaxLinks, MaxBytes: base.MaxBytes}},
		{name: "negative events", limits: PlannerEvidenceLimits{MaxEvents: -1, MaxEntities: base.MaxEntities, MaxLinks: base.MaxLinks, MaxBytes: base.MaxBytes}},
		{name: "zero entities", limits: PlannerEvidenceLimits{MaxEvents: base.MaxEvents, MaxEntities: 0, MaxLinks: base.MaxLinks, MaxBytes: base.MaxBytes}, valid: true},
		{name: "negative entities", limits: PlannerEvidenceLimits{MaxEvents: base.MaxEvents, MaxEntities: -1, MaxLinks: base.MaxLinks, MaxBytes: base.MaxBytes}},
		{name: "zero links", limits: PlannerEvidenceLimits{MaxEvents: base.MaxEvents, MaxEntities: base.MaxEntities, MaxLinks: 0, MaxBytes: base.MaxBytes}, valid: true},
		{name: "negative links", limits: PlannerEvidenceLimits{MaxEvents: base.MaxEvents, MaxEntities: base.MaxEntities, MaxLinks: -1, MaxBytes: base.MaxBytes}},
		{name: "zero bytes", limits: PlannerEvidenceLimits{MaxEvents: base.MaxEvents, MaxEntities: base.MaxEntities, MaxLinks: base.MaxLinks, MaxBytes: 0}},
		{name: "negative bytes", limits: PlannerEvidenceLimits{MaxEvents: base.MaxEvents, MaxEntities: base.MaxEntities, MaxLinks: base.MaxLinks, MaxBytes: -1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewPlannerEvidenceBuilder(&deliveryReaderStub{}, test.limits)
			if test.valid && err != nil {
				t.Fatalf("expected valid limits, got %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("expected invalid limits")
			}
		})
	}
}

func TestPlannerEvidenceCountAndByteLimits(t *testing.T) {
	reader := &deliveryReaderStub{}
	for i := int64(1); i <= 6; i++ {
		key := "host:" + string(rune('a'+i-1))
		reader.entities = append(reader.entities, database.WorldStateEntity{
			ID: i, FlowID: 1, EntityKey: key, Type: EntityTypeHost,
			State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{"port":443}`),
		})
		reader.events = append(reader.events, plannerEvent(i, 1, database.WorldStateEventKindEntityUpserted, map[string]any{
			"entity_id": i, "entity_key": key, "entity_type": "host", "state": "discovered",
		}))
		if i > 1 {
			reader.links = append(reader.links, database.WorldStateLink{ID: i, FlowID: 1, SourceID: i - 1, TargetID: i, Type: "next", Properties: json.RawMessage(`{}`)})
		}
	}
	limits := DefaultPlannerEvidenceLimits()
	limits.MaxEvents, limits.MaxEntities, limits.MaxLinks = 2, 3, 1
	bounded := buildPlannerEvidence(t, reader, limits, 1)
	if len(bounded.Journal.Events) != 2 || bounded.Journal.AfterRevision != 4 || len(bounded.Projection.Entities) != 3 || len(bounded.Projection.Links) > 1 {
		t.Fatalf("planner count limits not applied: %+v", bounded)
	}
	if bounded.Projection.Omitted == nil || bounded.Projection.Omitted.Entities != 3 {
		t.Fatalf("planner omissions not reported: %+v", bounded.Projection.Omitted)
	}

	payload := plannerEvidenceBytes(t, bounded)
	limits.MaxBytes = len(payload) - 1
	byteBounded := buildPlannerEvidence(t, reader, limits, 1)
	if got := len(plannerEvidenceBytes(t, byteBounded)); got > limits.MaxBytes || byteBounded.Journal.OmittedEvents == 0 {
		t.Fatalf("planner byte limit not applied: size=%d limit=%d evidence=%+v", got, limits.MaxBytes, byteBounded)
	}
}

func TestPlannerEvidenceRejectsForeignFlowRows(t *testing.T) {
	local := database.WorldStateEntity{ID: 1, FlowID: 1, EntityKey: "host:local", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{}`)}
	tests := []struct {
		name   string
		reader *unscopedPlannerEvidenceReader
	}{
		{name: "entity", reader: &unscopedPlannerEvidenceReader{entities: []database.WorldStateEntity{{ID: 2, FlowID: 2, EntityKey: "host:foreign", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{}`)}}}},
		{name: "link", reader: &unscopedPlannerEvidenceReader{entities: []database.WorldStateEntity{local}, links: []database.WorldStateLink{{ID: 2, FlowID: 2, SourceID: 1, TargetID: 1, Type: "foreign", Properties: json.RawMessage(`{}`)}}}},
		{name: "event", reader: &unscopedPlannerEvidenceReader{head: 1, events: []database.WorldStateEvent{plannerEvent(1, 2, database.WorldStateEventKindEntityUpserted, map[string]any{"entity_key": "host:foreign"})}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			builder, err := NewPlannerEvidenceBuilder(test.reader, DefaultPlannerEvidenceLimits())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := builder.Build(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "belongs to flow 2") {
				t.Fatalf("expected foreign-flow rejection, got %v", err)
			}
		})
	}
}

type unscopedPlannerEvidenceReader struct {
	head     int64
	events   []database.WorldStateEvent
	entities []database.WorldStateEntity
	links    []database.WorldStateLink
}

func (r *unscopedPlannerEvidenceReader) beginPrimaryDeliverySnapshot(context.Context) (PrimaryDeliveryReader, func() error, error) {
	return &unscopedPlannerEvidenceReader{
		head: r.head, events: append([]database.WorldStateEvent(nil), r.events...),
		entities: append([]database.WorldStateEntity(nil), r.entities...), links: append([]database.WorldStateLink(nil), r.links...),
	}, func() error { return nil }, nil
}

func (r *unscopedPlannerEvidenceReader) GetWorldStateEventHead(context.Context, int64) (int64, error) {
	return r.head, nil
}

func (r *unscopedPlannerEvidenceReader) GetWorldStateEventsByRevision(context.Context, database.GetWorldStateEventsByRevisionParams) ([]database.WorldStateEvent, error) {
	return r.events, nil
}

func (r *unscopedPlannerEvidenceReader) GetWorldStateEntitiesByFlow(context.Context, int64) ([]database.WorldStateEntity, error) {
	return r.entities, nil
}

func (r *unscopedPlannerEvidenceReader) GetWorldStateLinksByFlow(context.Context, int64) ([]database.WorldStateLink, error) {
	return r.links, nil
}

type nonSnapshotPlannerEvidenceReader struct {
	base       unscopedPlannerEvidenceReader
	headCalled bool
}

func (r *nonSnapshotPlannerEvidenceReader) GetWorldStateEventHead(ctx context.Context, flowID int64) (int64, error) {
	r.headCalled = true
	return r.base.GetWorldStateEventHead(ctx, flowID)
}

func (r *nonSnapshotPlannerEvidenceReader) GetWorldStateEventsByRevision(ctx context.Context, params database.GetWorldStateEventsByRevisionParams) ([]database.WorldStateEvent, error) {
	return r.base.GetWorldStateEventsByRevision(ctx, params)
}

func (r *nonSnapshotPlannerEvidenceReader) GetWorldStateEntitiesByFlow(ctx context.Context, flowID int64) ([]database.WorldStateEntity, error) {
	return r.base.GetWorldStateEntitiesByFlow(ctx, flowID)
}

func (r *nonSnapshotPlannerEvidenceReader) GetWorldStateLinksByFlow(ctx context.Context, flowID int64) ([]database.WorldStateLink, error) {
	return r.base.GetWorldStateLinksByFlow(ctx, flowID)
}

type plannerHeadReader struct {
	*deliveryReaderStub
	head int64
}

func (r *plannerHeadReader) beginPrimaryDeliverySnapshot(ctx context.Context) (PrimaryDeliveryReader, func() error, error) {
	reader, closeSnapshot, err := r.deliveryReaderStub.beginPrimaryDeliverySnapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	return &plannerHeadReader{deliveryReaderStub: reader.(*deliveryReaderStub), head: r.head}, closeSnapshot, nil
}

func (r *plannerHeadReader) GetWorldStateEventHead(context.Context, int64) (int64, error) {
	return r.head, nil
}

func plannerEvent(revision, flowID int64, kind database.WorldStateEventKind, facts map[string]any) database.WorldStateEvent {
	return database.WorldStateEvent{Revision: revision, FlowID: flowID, Kind: kind, Facts: plannerJSON(nil, facts)}
}

func plannerJSON(t *testing.T, value any) json.RawMessage {
	if t != nil {
		t.Helper()
	}
	raw, err := json.Marshal(value)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return raw
}

func buildPlannerEvidence(t *testing.T, reader PrimaryDeliveryReader, limits PlannerEvidenceLimits, flowID int64) PlannerEvidence {
	t.Helper()
	builder, err := NewPlannerEvidenceBuilder(reader, limits)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := builder.Build(context.Background(), flowID)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func plannerEvidenceBytes(t *testing.T, evidence PlannerEvidence) []byte {
	t.Helper()
	payload, err := evidence.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
