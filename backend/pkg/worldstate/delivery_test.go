package worldstate

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"pentagi/pkg/database"
)

type deliveryReaderStub struct {
	events    []database.WorldStateEvent
	entities  []database.WorldStateEntity
	links     []database.WorldStateLink
	afterHead func()
}

func (s *deliveryReaderStub) beginPrimaryDeliverySnapshot(_ context.Context) (PrimaryDeliveryReader, func() error, error) {
	snapshot := &deliveryReaderStub{
		events:    cloneDeliveryEvents(s.events),
		entities:  cloneDeliveryEntities(s.entities),
		links:     cloneDeliveryLinks(s.links),
		afterHead: s.afterHead,
	}
	s.afterHead = nil
	return snapshot, func() error { return nil }, nil
}

func (s *deliveryReaderStub) GetWorldStateEventHead(_ context.Context, flowID int64) (int64, error) {
	var head int64
	for _, event := range s.events {
		if event.FlowID == flowID && event.Revision > head {
			head = event.Revision
		}
	}
	if s.afterHead != nil {
		hook := s.afterHead
		s.afterHead = nil
		hook()
	}
	return head, nil
}

func (s *deliveryReaderStub) GetWorldStateEventsByRevision(_ context.Context, arg database.GetWorldStateEventsByRevisionParams) ([]database.WorldStateEvent, error) {
	result := make([]database.WorldStateEvent, 0)
	for _, event := range s.events {
		if event.FlowID == arg.FlowID && event.Revision > arg.AfterRevision && event.Revision <= arg.ThroughRevision {
			result = append(result, event)
			if int64(len(result)) == arg.LimitRows {
				break
			}
		}
	}
	return result, nil
}

func (s *deliveryReaderStub) GetWorldStateEntitiesByFlow(_ context.Context, flowID int64) ([]database.WorldStateEntity, error) {
	result := make([]database.WorldStateEntity, 0)
	for _, entity := range s.entities {
		if entity.FlowID == flowID {
			result = append(result, entity)
		}
	}
	return result, nil
}

func (s *deliveryReaderStub) GetWorldStateLinksByFlow(_ context.Context, flowID int64) ([]database.WorldStateLink, error) {
	result := make([]database.WorldStateLink, 0)
	for _, link := range s.links {
		if link.FlowID == flowID {
			result = append(result, link)
		}
	}
	return result, nil
}

func TestPrimaryDeliveryInitialAndEmptyRange(t *testing.T) {
	builder := mustDeliveryBuilder(t, &deliveryReaderStub{}, DefaultDeliveryLimits())
	baseline, err := builder.Build(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Kind != DeliveryBaseline || baseline.Coverage.AfterRevision != nil || baseline.Coverage.ThroughRevision != 0 {
		t.Fatalf("unexpected initial baseline: %+v", baseline)
	}
	assertPayloadCoverage(t, baseline.Payload, "baseline", nil, 0)

	cursor := int64(0)
	empty, err := builder.Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Empty() || len(empty.Payload) != 0 {
		t.Fatalf("equal cursor should consume no bytes: %+v", empty)
	}
}

func TestPrimaryDeliveryOrderedDeltaAndStableHead(t *testing.T) {
	reader := &deliveryReaderStub{events: []database.WorldStateEvent{
		event(5, 1, database.WorldStateEventKindEntityTransitioned, `{"state":"scanning"}`),
		event(2, 2, database.WorldStateEventKindEntityUpserted, `{"foreign":true}`),
		event(3, 1, database.WorldStateEventKindEntityUpserted, `{"key":"host:a"}`),
	}}
	reader.afterHead = func() {
		reader.events = append(reader.events, event(6, 1, database.WorldStateEventKindEntityUpserted, `{"key":"host:late"}`))
	}
	builder := mustDeliveryBuilder(t, reader, DefaultDeliveryLimits())
	cursor := int64(1)
	delivery, err := builder.Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Kind != DeliveryDelta || delivery.Coverage.ThroughRevision != 5 || delivery.EventCount != 2 {
		t.Fatalf("unexpected delta: %+v", delivery)
	}
	text := string(delivery.Payload)
	if strings.Index(text, `"revision":3`) > strings.Index(text, `"revision":5`) {
		t.Fatalf("events are not revision ordered: %s", text)
	}
	if strings.Contains(text, `host:late`) || strings.Contains(text, `foreign`) {
		t.Fatalf("post-head or foreign-flow event leaked: %s", text)
	}
	assertPayloadCoverage(t, delivery.Payload, "delta", &cursor, 5)
}

func TestPrimaryDeliveryProjectionUsesCapturedSnapshot(t *testing.T) {
	for _, test := range []struct {
		name       string
		cursor     *int64
		limits     DeliveryLimits
		wantKind   DeliveryKind
		wantReason CheckpointReason
	}{
		{name: "baseline", limits: DefaultDeliveryLimits(), wantKind: DeliveryBaseline},
		{name: "checkpoint", cursor: revisionPointer(0), limits: deliveryLimitsWithMaxEvents(1), wantKind: DeliveryCheckpoint, wantReason: CheckpointEventLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &deliveryReaderStub{
				events: []database.WorldStateEvent{
					event(1, 1, database.WorldStateEventKindEntityUpserted, `{"key":"host:a"}`),
					event(2, 1, database.WorldStateEventKindLinkUpserted, `{"type":"reaches"}`),
				},
				entities: []database.WorldStateEntity{
					{ID: 1, FlowID: 1, EntityKey: "host:a", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{"version":"entity-before"}`)},
					{ID: 2, FlowID: 1, EntityKey: "host:b", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{}`)},
				},
				links: []database.WorldStateLink{
					{ID: 1, FlowID: 1, SourceID: 1, TargetID: 2, Type: "reaches", Properties: json.RawMessage(`{"version":"link-before"}`)},
				},
			}
			reader.afterHead = func() {
				reader.events = append(reader.events, event(3, 1, database.WorldStateEventKindEntityUpserted, `{"key":"host:late"}`))
				reader.entities[0].Properties = json.RawMessage(`{"version":"entity-after"}`)
				reader.links[0].Properties = json.RawMessage(`{"version":"link-after"}`)
			}

			delivery, err := mustDeliveryBuilder(t, reader, test.limits).Build(context.Background(), 1, test.cursor)
			if err != nil {
				t.Fatal(err)
			}
			if delivery.Kind != test.wantKind || delivery.CheckpointReason != test.wantReason || delivery.Coverage.ThroughRevision != 2 {
				t.Fatalf("unexpected snapshot delivery: %+v", delivery)
			}
			if !bytes.Contains(delivery.Payload, []byte("entity-before")) || !bytes.Contains(delivery.Payload, []byte("link-before")) {
				t.Fatalf("captured projection missing pre-head state: %s", delivery.Payload)
			}
			for _, late := range []string{"entity-after", "link-after", "host:late"} {
				if bytes.Contains(delivery.Payload, []byte(late)) {
					t.Fatalf("post-head state %q leaked into revision-2 projection: %s", late, delivery.Payload)
				}
			}
		})
	}
}

func TestPrimaryDeliveryUnknownAndMalformedEvents(t *testing.T) {
	reader := &deliveryReaderStub{events: []database.WorldStateEvent{
		event(1, 1, database.WorldStateEventKind("future_safe_kind"), `{"safe":"value"}`),
	}}
	builder := mustDeliveryBuilder(t, reader, DefaultDeliveryLimits())
	cursor := int64(0)
	delivery, err := builder.Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(delivery.Payload, []byte(`"kind":"future_safe_kind","rendering":"generic"`)) {
		t.Fatalf("unknown event was not rendered generically: %s", delivery.Payload)
	}

	reader.events[0].Facts = json.RawMessage(`{"broken"`)
	if _, err := builder.Build(context.Background(), 1, &cursor); err == nil || !strings.Contains(err.Error(), "malformed facts") {
		t.Fatalf("expected typed malformed-facts failure, got %v", err)
	}
}

func TestPrimaryDeliveryCanonicalUTF8AndRecursiveRedaction(t *testing.T) {
	reader := &deliveryReaderStub{
		events: []database.WorldStateEvent{
			{
				Revision: 7, FlowID: 1, Kind: database.WorldStateEventKindEntityUpserted,
				Actor: string([]byte{'a', 0xff, 'b'}),
				Facts: json.RawMessage(`{"z":1,"nested":{"password":"dont-print-me","safe":"ok"},"headers":[{"Authorization":"dont-print-auth"}],"message":"Bearer dont-print-bearer"}`),
			},
		},
		entities: []database.WorldStateEntity{
			{ID: 2, FlowID: 1, EntityKey: "host:z", Type: EntityTypeHost, State: database.WorldStateLifecycleScanning, Properties: json.RawMessage(`{"version":"2","port":443}`)},
			{ID: 1, FlowID: 1, EntityKey: "host:a", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{"cookie":"dont-print-cookie","label":"東京"}`)},
		},
		links: []database.WorldStateLink{
			{ID: 1, FlowID: 1, SourceID: 2, TargetID: 1, Type: "reaches", Properties: json.RawMessage(`{"api_key":"dont-print-key","port":443}`)},
		},
	}
	builder := mustDeliveryBuilder(t, reader, DefaultDeliveryLimits())
	cursor := int64(0)
	delta, err := builder.Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	assertSafeAndRedacted(t, delta.Payload)

	first, err := builder.Build(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	reader.entities[0], reader.entities[1] = reader.entities[1], reader.entities[0]
	second, err := builder.Build(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Payload, second.Payload) {
		t.Fatalf("equivalent projection was not deterministic:\n%s\n%s", first.Payload, second.Payload)
	}
	assertSafeAndNoSecrets(t, first.Payload)
	if !bytes.Contains(first.Payload, []byte(`"properties":{"port":443,"version":"2"}`)) {
		t.Fatalf("allowlisted properties were not rendered canonically: %s", first.Payload)
	}
}

func TestPrimaryDeliveryDoesNotExposeAuthAcrossDeliveryKinds(t *testing.T) {
	reader := &deliveryReaderStub{
		events: []database.WorldStateEvent{
			event(1, 1, database.WorldStateEventKindEntityUpserted, `{"auth":"sentinel-auth-delta"}`),
			event(2, 1, database.WorldStateEventKindLinkUpserted, `{"safe":true}`),
		},
		entities: []database.WorldStateEntity{
			{ID: 1, FlowID: 1, EntityKey: "host:a", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{"auth":"sentinel-auth-entity"}`)},
			{ID: 2, FlowID: 1, EntityKey: "host:b", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{}`)},
		},
		links: []database.WorldStateLink{
			{ID: 1, FlowID: 1, SourceID: 1, TargetID: 2, Type: "reaches", Properties: json.RawMessage(`{"auth":"sentinel-auth-link"}`)},
		},
	}
	cursor := int64(0)
	delta, err := mustDeliveryBuilder(t, reader, DefaultDeliveryLimits()).Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := mustDeliveryBuilder(t, reader, DefaultDeliveryLimits()).Build(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := mustDeliveryBuilder(t, reader, deliveryLimitsWithMaxEvents(1)).Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	assertSafeAndRedacted(t, delta.Payload)
	assertSafeAndNoSecrets(t, baseline.Payload)
	assertSafeAndNoSecrets(t, checkpoint.Payload)
}

func TestPrimaryDeliveryProjectionUsesBoundedAllowlist(t *testing.T) {
	rawOutput := "harmless-raw-output-sentinel"
	secret := "synthetic-secret-sentinel"
	reader := &deliveryReaderStub{
		events: []database.WorldStateEvent{
			event(1, 1, database.WorldStateEventKindEntityUpserted, `{"entity_key":"host:a"}`),
			event(2, 1, database.WorldStateEventKindLinkUpserted, `{"link_type":"serves"}`),
		},
		entities: []database.WorldStateEntity{
			{ID: 2, FlowID: 1, EntityKey: "endpoint:https://allow.example/", Type: EntityTypeEndpoint, State: database.WorldStateLifecycleScanning, Properties: json.RawMessage(`{}`)},
			{ID: 1, FlowID: 1, EntityKey: "host:allow.example", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: mustJSON(t, map[string]any{
				"host": "allow.example", "port": 443, "status": "open",
				"output": rawOutput, "transcript": secret, "command": "scanner --target allow.example",
				"headers": map[string]any{"Authorization": secret}, "body": secret, "token": secret,
				"nested": map[string]any{"host": "nested.example"},
				"title":  strings.Repeat("x", maxJournalPropertyStringBytes+1),
			})},
		},
		links: []database.WorldStateLink{{
			ID: 1, FlowID: 1, SourceID: 1, TargetID: 2, Type: "serves",
			Properties: mustJSON(t, map[string]any{
				"protocol": "https", "tool": "scanner", "version": "1.2.3",
				"output": rawOutput, "request_body": secret, "response_headers": map[string]any{"X-Secret": secret},
			}),
		}},
	}
	cursor := int64(0)
	baseline, err := mustDeliveryBuilder(t, reader, DefaultDeliveryLimits()).Build(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := mustDeliveryBuilder(t, reader, deliveryLimitsWithMaxEvents(1)).Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range []PrimaryDelivery{baseline, checkpoint} {
		text := string(delivery.Payload)
		for _, excluded := range []string{rawOutput, secret, `"output"`, `"transcript"`, `"command"`, `"headers"`, `"body"`, `"token"`, `"nested"`, `"title"`, `"request_body"`, `"response_headers"`} {
			if strings.Contains(text, excluded) {
				t.Fatalf("projection contains excluded value or field %q: %s", excluded, text)
			}
		}
		for _, expected := range []string{
			`"key":"host:allow.example","type":"host","state":"discovered"`,
			`"properties":{"host":"allow.example","port":443,"status":"open"}`,
			`"source":"host:allow.example","target":"endpoint:https://allow.example/","type":"serves"`,
			`"properties":{"protocol":"https","tool":"scanner","version":"1.2.3"}`,
			`"summary":{"entities":2,"links":1`,
		} {
			if !strings.Contains(text, expected) {
				t.Fatalf("projection missing bounded operational fact %q: %s", expected, text)
			}
		}
	}
}

func TestPrimaryDeliverySanitizesCredentialIdentifiers(t *testing.T) {
	reader := &deliveryReaderStub{
		events: []database.WorldStateEvent{
			event(1, 1, database.WorldStateEventKindEntityUpserted, `{"entity_key":"credential:token:synthetic-token-sentinel"}`),
		},
		entities: []database.WorldStateEntity{{
			ID: 1, FlowID: 1, EntityKey: "credential:cookie:synthetic-cookie-sentinel",
			Type: EntityTypeCredential, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{}`),
		}},
	}
	cursor := int64(0)
	delta, err := mustDeliveryBuilder(t, reader, DefaultDeliveryLimits()).Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := mustDeliveryBuilder(t, reader, DefaultDeliveryLimits()).Build(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		payload []byte
		key     string
	}{
		{payload: delta.Payload, key: "credential:token"},
		{payload: baseline.Payload, key: "credential:cookie"},
	} {
		text := string(test.payload)
		if strings.Contains(text, "synthetic-") {
			t.Fatalf("delivery leaked credential identifier material")
		}
		if !strings.Contains(text, test.key) {
			t.Fatalf("delivery missing sanitized credential identity %q", test.key)
		}
	}
}

func TestPrimaryDeliveryMalformedPropertiesErrorSanitizesCredentialIdentifier(t *testing.T) {
	reader := &deliveryReaderStub{entities: []database.WorldStateEntity{{
		ID: 1, FlowID: 1, EntityKey: "credential:password:synthetic-password-sentinel",
		Type: EntityTypeCredential, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`[]`),
	}}}
	_, err := mustDeliveryBuilder(t, reader, DefaultDeliveryLimits()).Build(context.Background(), 1, nil)
	if err == nil || !strings.Contains(err.Error(), "malformed properties") {
		t.Fatalf("expected malformed properties error")
	}
	if strings.Contains(err.Error(), "synthetic-password-sentinel") {
		t.Fatalf("delivery error leaked credential identifier material")
	}
	if !strings.Contains(err.Error(), "credential:password") {
		t.Fatalf("delivery error lost sanitized credential identity")
	}
}

func TestPrimaryDeliveryCountAndByteCheckpointFallback(t *testing.T) {
	reader := &deliveryReaderStub{
		events: []database.WorldStateEvent{
			event(1, 1, database.WorldStateEventKindEntityUpserted, `{"key":"a"}`),
			event(2, 1, database.WorldStateEventKindEntityUpserted, `{"key":"b"}`),
			event(3, 1, database.WorldStateEventKindEntityUpserted, `{"key":"c"}`),
		},
		entities: []database.WorldStateEntity{
			{ID: 1, FlowID: 1, EntityKey: "host:a", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{}`)},
		},
	}
	cursor := int64(0)
	countLimits := DefaultDeliveryLimits()
	countLimits.MaxEvents = 2
	countDelivery, err := mustDeliveryBuilder(t, reader, countLimits).Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	assertCheckpoint(t, countDelivery, CheckpointEventLimit, cursor, 3, countLimits.MaxBytes)
	if bytes.Contains(countDelivery.Payload, []byte(`"events"`)) {
		t.Fatalf("checkpoint must not silently retain an event prefix: %s", countDelivery.Payload)
	}

	highLimits := DefaultDeliveryLimits()
	delta, err := mustDeliveryBuilder(t, reader, highLimits).Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	exactLimits := highLimits
	exactLimits.MaxBytes = len(delta.Payload)
	exact, err := mustDeliveryBuilder(t, reader, exactLimits).Build(context.Background(), 1, &cursor)
	if err != nil || exact.Kind != DeliveryDelta {
		t.Fatalf("exact byte cut should fit delta: kind=%s err=%v", exact.Kind, err)
	}
	exactLimits.MaxBytes--
	byteDelivery, err := mustDeliveryBuilder(t, reader, exactLimits).Build(context.Background(), 1, &cursor)
	if err != nil {
		t.Fatal(err)
	}
	assertCheckpoint(t, byteDelivery, CheckpointByteLimit, cursor, 3, exactLimits.MaxBytes)
}

func TestPrimaryDeliveryProjectionCountAndByteLimits(t *testing.T) {
	reader := &deliveryReaderStub{entities: []database.WorldStateEntity{
		{ID: 1, FlowID: 1, EntityKey: "host:a", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered, Properties: json.RawMessage(`{"detail":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)},
		{ID: 2, FlowID: 1, EntityKey: "host:b", Type: EntityTypeHost, State: database.WorldStateLifecycleScanning, Properties: json.RawMessage(`{"detail":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)},
	}}
	limits := DefaultDeliveryLimits()
	limits.MaxEntities = 1
	baseline, err := mustDeliveryBuilder(t, reader, limits).Build(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Payload) > limits.MaxBytes || !bytes.Contains(baseline.Payload, []byte(`"omitted":{"entities":1,"links":0}`)) {
		t.Fatalf("projection count limit was not explicit: %s", baseline.Payload)
	}

	limits.MaxEntities = 2
	full, err := mustDeliveryBuilder(t, reader, limits).Build(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	limits.MaxBytes = len(full.Payload) - 1
	bounded, err := mustDeliveryBuilder(t, reader, limits).Build(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded.Payload) > limits.MaxBytes || !bytes.Contains(bounded.Payload, []byte(`"omitted"`)) {
		t.Fatalf("projection byte limit was not explicit: %s", bounded.Payload)
	}
}

func event(revision, flowID int64, kind database.WorldStateEventKind, facts string) database.WorldStateEvent {
	return database.WorldStateEvent{Revision: revision, FlowID: flowID, Kind: kind, Facts: json.RawMessage(facts), Actor: "system"}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func revisionPointer(revision int64) *int64 { return &revision }

func deliveryLimitsWithMaxEvents(maxEvents int) DeliveryLimits {
	limits := DefaultDeliveryLimits()
	limits.MaxEvents = maxEvents
	return limits
}

func cloneDeliveryEvents(events []database.WorldStateEvent) []database.WorldStateEvent {
	cloned := append([]database.WorldStateEvent(nil), events...)
	for i := range cloned {
		cloned[i].Facts = bytes.Clone(cloned[i].Facts)
	}
	return cloned
}

func cloneDeliveryEntities(entities []database.WorldStateEntity) []database.WorldStateEntity {
	cloned := append([]database.WorldStateEntity(nil), entities...)
	for i := range cloned {
		cloned[i].Properties = bytes.Clone(cloned[i].Properties)
		cloned[i].Annotations = bytes.Clone(cloned[i].Annotations)
	}
	return cloned
}

func cloneDeliveryLinks(links []database.WorldStateLink) []database.WorldStateLink {
	cloned := append([]database.WorldStateLink(nil), links...)
	for i := range cloned {
		cloned[i].Properties = bytes.Clone(cloned[i].Properties)
	}
	return cloned
}

func mustDeliveryBuilder(t *testing.T, reader PrimaryDeliveryReader, limits DeliveryLimits) *PrimaryDeliveryBuilder {
	t.Helper()
	builder, err := NewPrimaryDeliveryBuilder(reader, limits)
	if err != nil {
		t.Fatal(err)
	}
	return builder
}

func assertPayloadCoverage(t *testing.T, payload []byte, kind string, after *int64, through int64) {
	t.Helper()
	var envelope struct {
		Type     string           `json:"type"`
		Coverage RevisionCoverage `json:"coverage"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("invalid delivery JSON: %v: %s", err, payload)
	}
	if envelope.Type != kind || envelope.Coverage.ThroughRevision != through {
		t.Fatalf("unexpected payload coverage: %+v", envelope)
	}
	if (after == nil) != (envelope.Coverage.AfterRevision == nil) || (after != nil && *after != *envelope.Coverage.AfterRevision) {
		t.Fatalf("unexpected after revision: want %v got %v", after, envelope.Coverage.AfterRevision)
	}
}

func assertSafeAndRedacted(t *testing.T, payload []byte) {
	t.Helper()
	assertSafeAndNoSecrets(t, payload)
	if !bytes.Contains(payload, []byte(redactedValue)) {
		t.Fatalf("redaction marker missing: %s", payload)
	}
}

func assertSafeAndNoSecrets(t *testing.T, payload []byte) {
	t.Helper()
	if !utf8.Valid(payload) || !json.Valid(payload) {
		t.Fatalf("delivery is not valid UTF-8 JSON: %q", payload)
	}
	for _, secret := range []string{
		"dont-print-me", "dont-print-auth", "dont-print-bearer", "dont-print-cookie", "dont-print-key",
		"sentinel-auth-delta", "sentinel-auth-entity", "sentinel-auth-link",
	} {
		if bytes.Contains(payload, []byte(secret)) {
			t.Fatalf("secret %q leaked in delivery: %s", secret, payload)
		}
	}
}

func assertCheckpoint(t *testing.T, delivery PrimaryDelivery, reason CheckpointReason, after, through int64, maxBytes int) {
	t.Helper()
	if delivery.Kind != DeliveryCheckpoint || delivery.CheckpointReason != reason || delivery.Coverage.ThroughRevision != through {
		t.Fatalf("unexpected checkpoint: %+v", delivery)
	}
	if delivery.Coverage.AfterRevision == nil || *delivery.Coverage.AfterRevision != after || len(delivery.Payload) > maxBytes {
		t.Fatalf("checkpoint coverage or size invalid: %+v size=%d", delivery, len(delivery.Payload))
	}
	assertPayloadCoverage(t, delivery.Payload, "checkpoint", &after, through)
	if !bytes.Contains(delivery.Payload, []byte(`"checkpoint_reason":"`+string(reason)+`"`)) {
		t.Fatalf("checkpoint reason not labelled: %s", delivery.Payload)
	}
}
