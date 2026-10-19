package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"pentagi/pkg/cast"
	"pentagi/pkg/database"
	"pentagi/pkg/providers/pconfig"
	providerpkg "pentagi/pkg/providers/provider"
	providermock "pentagi/pkg/providers/tester/mock"
	"pentagi/pkg/schema"
	"pentagi/pkg/templates"
	"pentagi/pkg/tools"
	"pentagi/pkg/worldstate"

	"github.com/sirupsen/logrus"
	"github.com/vxcontrol/langchaingo/llms"
	"github.com/vxcontrol/langchaingo/llms/streaming"
)

type primaryDeliveryBuilderFunc func(context.Context, int64, *int64) (worldstate.PrimaryDelivery, error)

func (f primaryDeliveryBuilderFunc) Build(ctx context.Context, flowID int64, cursor *int64) (worldstate.PrimaryDelivery, error) {
	return f(ctx, flowID, cursor)
}

type primaryDeliveryStoreStub struct {
	msgChain          database.Msgchain
	cursor            *int64
	persistErr        error
	persistedChain    []byte
	persistedRevision int64
	persistCalls      int
}

func (s *primaryDeliveryStoreStub) GetMsgChain(context.Context, int64) (database.Msgchain, error) {
	return s.msgChain, nil
}

func (s *primaryDeliveryStoreStub) GetCursor(context.Context, int64) (*int64, error) {
	if s.cursor == nil {
		return nil, nil
	}
	cursor := *s.cursor
	return &cursor, nil
}

func (s *primaryDeliveryStoreStub) Persist(
	_ context.Context,
	_ int64,
	chain []llms.MessageContent,
	envelope string,
	revision int64,
) ([]llms.MessageContent, bool, error) {
	s.persistCalls++
	if s.persistErr != nil {
		return nil, false, s.persistErr
	}
	nextChain, err := appendWorldStateEnvelope(chain, envelope)
	if err != nil {
		return nil, false, err
	}
	s.persistedChain, err = json.Marshal(nextChain)
	if err != nil {
		return nil, false, err
	}
	s.persistedRevision = revision
	s.cursor = &s.persistedRevision
	return nextChain, true, nil
}

type turnInjectorStub struct {
	calls int
	err   error
	empty bool
	next  []llms.MessageContent
}

func (s *turnInjectorStub) Inject(context.Context, int64, []llms.MessageContent) ([]llms.MessageContent, bool, error) {
	s.calls++
	if s.err != nil {
		return nil, false, s.err
	}
	if s.empty {
		return nil, false, nil
	}
	return s.next, true, nil
}

func TestWorldStateTurnPrimaryOnlyAndFailSoft(t *testing.T) {
	chain := []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeHuman, "request")}
	stub := &turnInjectorStub{next: append(chain, llms.TextParts(llms.ChatMessageTypeAI, "changed"))}
	fp := &flowProvider{worldState: stub}
	logger := worldStateTestLogger()

	nonPrimary := []pconfig.ProviderOptionsType{
		pconfig.OptionsTypeAssistant, pconfig.OptionsTypeGenerator, pconfig.OptionsTypeRefiner,
		pconfig.OptionsTypeAdviser, pconfig.OptionsTypeSearcher, pconfig.OptionsTypeCoder,
		pconfig.OptionsTypeInstaller, pconfig.OptionsTypePentester, pconfig.OptionsTypeReflector,
	}
	for _, typ := range nonPrimary {
		if got := fp.injectWorldStateForTurn(t.Context(), typ, 1, chain, logger); !reflect.DeepEqual(got, chain) {
			t.Fatalf("non-primary %s chain changed", typ)
		}
	}
	if stub.calls != 0 {
		t.Fatalf("non-primary turns called injector %d times", stub.calls)
	}
	if got := fp.injectWorldStateForTurn(t.Context(), pconfig.OptionsTypePrimaryAgent, 1, chain, logger); len(got) != 2 {
		t.Fatalf("primary delivery was not used: %#v", got)
	}
	stub.err = errors.New("persistence failed")
	if got := fp.injectWorldStateForTurn(t.Context(), pconfig.OptionsTypePrimaryAgent, 1, chain, logger); !reflect.DeepEqual(got, chain) {
		t.Fatal("failed delivery changed in-memory chain")
	}
}

func TestWorldStateTurnDedupesWhenDeliveryEmpty(t *testing.T) {
	dup := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{
			llms.ToolCall{ID: "dup", Type: "function", FunctionCall: &llms.FunctionCall{Name: "query", Arguments: `{}`}},
			llms.ToolCall{ID: "dup", Type: "function", FunctionCall: &llms.FunctionCall{Name: "query", Arguments: `{}`}},
		}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{
			llms.ToolCallResponse{ToolCallID: "dup", Name: "query", Content: "ok"},
		}},
	}
	fp := &flowProvider{worldState: &turnInjectorStub{empty: true}}
	got := fp.injectWorldStateForTurn(t.Context(), pconfig.OptionsTypePrimaryAgent, 1, dup, worldStateTestLogger())
	assertUniqueToolUseIDs(t, got)
}

func TestWorldStateDeliveryKindsAndToolOrdering(t *testing.T) {
	for _, kind := range []worldstate.DeliveryKind{
		worldstate.DeliveryBaseline, worldstate.DeliveryDelta, worldstate.DeliveryCheckpoint,
	} {
		t.Run(string(kind), func(t *testing.T) {
			var cursor *int64
			if kind != worldstate.DeliveryBaseline {
				value := int64(2)
				cursor = &value
			}
			store := newPrimaryStore(cursor)
			builder := primaryDeliveryBuilderFunc(func(_ context.Context, flowID int64, got *int64) (worldstate.PrimaryDelivery, error) {
				if flowID != 7 {
					t.Fatalf("unexpected flow: %d", flowID)
				}
				if got == nil {
					return delivery(worldstate.DeliveryBaseline, 5), nil
				}
				if *got != 2 {
					t.Fatalf("unexpected cursor: %v", *got)
				}
				return delivery(kind, 5), nil
			})
			injector := &databasePrimaryWorldStateTurnInjector{flowID: 7, builder: builder, store: store}
			original := completedToolChain()
			got, delivered, err := injector.Inject(t.Context(), 11, original)
			if err != nil || !delivered {
				t.Fatalf("delivery failed: delivered=%v err=%v", delivered, err)
			}
			if store.persistCalls != 1 || store.persistedRevision != 5 || len(store.persistedChain) == 0 {
				t.Fatalf("unexpected persistence: %+v", store)
			}
			if len(got) != len(original)+1 || got[len(got)-2].Role != llms.ChatMessageTypeTool || got[len(got)-1].Role != llms.ChatMessageTypeHuman {
				t.Fatalf("tool response adjacency broken: %#v", got)
			}
			if !reflect.DeepEqual(got[:len(original)], original) {
				t.Fatalf("delivery rewrote historical messages: got=%#v want=%#v", got[:len(original)], original)
			}
			if _, err := cast.NewChainAST(got, false); err != nil {
				t.Fatalf("delivered chain is invalid: %v", err)
			}
			if !chainContains(got, "<world_state>") || !chainContains(got, `"through_revision":5`) {
				t.Fatalf("missing tagged delivery: %#v", got)
			}
		})
	}
}

func TestWorldStateDeliveryAppendsAtPendingHumanBoundary(t *testing.T) {
	store := newPrimaryStore(nil)
	injector := &databasePrimaryWorldStateTurnInjector{
		flowID: 7,
		store:  store,
		builder: primaryDeliveryBuilderFunc(func(context.Context, int64, *int64) (worldstate.PrimaryDelivery, error) {
			return delivery(worldstate.DeliveryBaseline, 1), nil
		}),
	}
	original := performerInitialChain()
	got, delivered, err := injector.Inject(t.Context(), 11, original)
	if err != nil || !delivered {
		t.Fatalf("delivery failed: delivered=%v err=%v", delivered, err)
	}
	if len(got) != len(original) || !reflect.DeepEqual(got[0], original[0]) {
		t.Fatalf("delivery rewrote messages before the pending boundary: %#v", got)
	}
	if len(got[1].Parts) != len(original[1].Parts)+1 || !reflect.DeepEqual(got[1].Parts[:len(original[1].Parts)], original[1].Parts) {
		t.Fatalf("delivery rewrote the pending human message: %#v", got[1])
	}
	if _, err := cast.NewChainAST(got, false); err != nil {
		t.Fatalf("pending delivery boundary is invalid: %v", err)
	}
}

func TestWorldStateDeliveryRepairsPendingToolCallBeforeAppend(t *testing.T) {
	store := newPrimaryStore(nil)
	injector := &databasePrimaryWorldStateTurnInjector{
		flowID: 7,
		store:  store,
		builder: primaryDeliveryBuilderFunc(func(context.Context, int64, *int64) (worldstate.PrimaryDelivery, error) {
			return delivery(worldstate.DeliveryBaseline, 1), nil
		}),
	}
	original := pendingToolCallChain()
	got, delivered, err := injector.Inject(t.Context(), 11, original)
	if err != nil || !delivered {
		t.Fatalf("delivery failed: delivered=%v err=%v", delivered, err)
	}
	if len(got) != len(original)+2 {
		t.Fatalf("unexpected chain length after repair: got=%d want=%d", len(got), len(original)+2)
	}
	if got[len(got)-2].Role != llms.ChatMessageTypeTool || got[len(got)-1].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("repaired chain has invalid ending: %#v", got)
	}
	responseFound := false
	for _, part := range got[len(got)-2].Parts {
		response, ok := part.(llms.ToolCallResponse)
		if ok && response.ToolCallID == "call-pending" {
			responseFound = true
			break
		}
	}
	if !responseFound {
		t.Fatalf("missing fallback tool response for pending tool call: %#v", got[len(got)-2])
	}
	if _, err := cast.NewChainAST(got, false); err != nil {
		t.Fatalf("repaired chain is invalid: %v", err)
	}
}

func TestWorldStateDeliveryKeepsToolUseIDsUnique(t *testing.T) {
	store := newPrimaryStore(nil)
	injector := &databasePrimaryWorldStateTurnInjector{
		flowID: 7,
		store:  store,
		builder: primaryDeliveryBuilderFunc(func(context.Context, int64, *int64) (worldstate.PrimaryDelivery, error) {
			return delivery(worldstate.DeliveryBaseline, 1), nil
		}),
	}
	original := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		llms.TextParts(llms.ChatMessageTypeHuman, "request"),
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{
			llms.TextContent{Text: "one"},
			llms.TextContent{Text: "two"},
			llms.ToolCall{ID: "dup", Type: "function", FunctionCall: &llms.FunctionCall{Name: "query", Arguments: `{}`}},
			llms.ToolCall{ID: "call-2", Type: "function", FunctionCall: &llms.FunctionCall{Name: "search", Arguments: `{}`}},
			llms.TextContent{Text: "three"},
			llms.ToolCall{ID: "dup", Type: "function", FunctionCall: &llms.FunctionCall{Name: "query", Arguments: `{}`}},
		}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{
			llms.ToolCallResponse{ToolCallID: "dup", Name: "query", Content: "first"},
			llms.ToolCallResponse{ToolCallID: "call-2", Name: "search", Content: "ok"},
			llms.ToolCallResponse{ToolCallID: "dup", Name: "query", Content: "again"},
		}},
	}
	got, delivered, err := injector.Inject(t.Context(), 11, original)
	if err != nil || !delivered {
		t.Fatalf("delivery failed: delivered=%v err=%v", delivered, err)
	}
	assertUniqueToolUseIDs(t, got)
	if _, err := cast.NewChainAST(got, false); err != nil {
		t.Fatalf("delivered chain is invalid: %v", err)
	}
	if !chainContains(got, "<world_state>") {
		t.Fatalf("missing tagged delivery: %#v", got)
	}
}

func TestWorldStateDeliveryDedupesIDsOnPendingHumanBoundary(t *testing.T) {
	original := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{llms.TextContent{Text: "request"}}},
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{
			llms.ToolCall{ID: "dup", Type: "function", FunctionCall: &llms.FunctionCall{Name: "query", Arguments: `{}`}},
			llms.ToolCall{ID: "dup", Type: "function", FunctionCall: &llms.FunctionCall{Name: "query", Arguments: `{}`}},
		}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{
			llms.ToolCallResponse{ToolCallID: "dup", Name: "query", Content: "ok"},
		}},
		llms.TextParts(llms.ChatMessageTypeHuman, "continue"),
	}
	got, err := appendWorldStateEnvelope(original, "<world_state>\n{}\n</world_state>")
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}
	assertUniqueToolUseIDs(t, got)
	if _, err := cast.NewChainAST(got, false); err != nil {
		t.Fatalf("human-boundary chain is invalid: %v", err)
	}
}

func TestWorldStateDeliveryReplacesPreviousEnvelope(t *testing.T) {
	original := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		llms.TextParts(llms.ChatMessageTypeHuman, "request\n<world_state>\n{\"stale\":true}\n</world_state>"),
	}
	got, err := appendWorldStateEnvelope(original, "<world_state>\n{\"fresh\":true}\n</world_state>")
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}
	if !chainContains(got, `"fresh":true`) {
		t.Fatalf("missing fresh envelope: %#v", got)
	}
	if chainContains(got, `"stale":true`) {
		t.Fatalf("stale envelope was kept: %#v", got)
	}
	envelopes := 0
	for _, message := range got {
		for _, part := range message.Parts {
			if text, ok := part.(llms.TextContent); ok {
				for i := 0; i+len("<world_state>") <= len(text.Text); i++ {
					if text.Text[i:i+len("<world_state>")] == "<world_state>" {
						envelopes++
					}
				}
			}
		}
	}
	if envelopes != 1 {
		t.Fatalf("want one envelope, got %d: %#v", envelopes, got)
	}
}

func TestWorldStateDeliveryNoChangeAndChainTypeGuard(t *testing.T) {
	for _, msgChain := range []database.Msgchain{
		{ID: 11, FlowID: 7, Type: database.MsgchainTypeAssistant},
		{ID: 11, FlowID: 8, Type: database.MsgchainTypePrimaryAgent},
	} {
		calls := 0
		store := &primaryDeliveryStoreStub{msgChain: msgChain}
		injector := &databasePrimaryWorldStateTurnInjector{
			flowID: 7,
			store:  store,
			builder: primaryDeliveryBuilderFunc(func(context.Context, int64, *int64) (worldstate.PrimaryDelivery, error) {
				calls++
				return delivery(worldstate.DeliveryBaseline, 1), nil
			}),
		}
		chain := completedToolChain()
		got, delivered, err := injector.Inject(t.Context(), 11, chain)
		if err != nil || delivered || calls != 0 || !reflect.DeepEqual(got, chain) || store.persistCalls != 0 {
			t.Fatalf("ineligible chain delivered: delivered=%v calls=%d err=%v", delivered, calls, err)
		}
	}

	cursor := int64(4)
	store := newPrimaryStore(&cursor)
	injector := &databasePrimaryWorldStateTurnInjector{
		flowID: 7, store: store,
		builder: primaryDeliveryBuilderFunc(func(context.Context, int64, *int64) (worldstate.PrimaryDelivery, error) {
			return worldstate.PrimaryDelivery{Kind: worldstate.DeliveryNone}, nil
		}),
	}
	chain := completedToolChain()
	got, delivered, err := injector.Inject(t.Context(), 11, chain)
	if err != nil || delivered || store.persistCalls != 0 || !reflect.DeepEqual(got, chain) {
		t.Fatalf("no-change delivery wrote state: delivered=%v err=%v", delivered, err)
	}
}

func TestWorldStateDeliveryFailuresDoNotAcknowledge(t *testing.T) {
	tests := []struct {
		name       string
		build      primaryDeliveryBuilderFunc
		persistErr error
	}{
		{name: "builder", build: func(context.Context, int64, *int64) (worldstate.PrimaryDelivery, error) {
			return worldstate.PrimaryDelivery{}, errors.New("formatter failed")
		}},
		{name: "invalid payload", build: func(context.Context, int64, *int64) (worldstate.PrimaryDelivery, error) {
			return worldstate.PrimaryDelivery{Kind: worldstate.DeliveryDelta, Coverage: worldstate.RevisionCoverage{ThroughRevision: 3}, Payload: []byte("not-json")}, nil
		}},
		{name: "persistence", build: func(context.Context, int64, *int64) (worldstate.PrimaryDelivery, error) {
			return delivery(worldstate.DeliveryDelta, 3), nil
		}, persistErr: errors.New("commit failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cursor := int64(1)
			store := newPrimaryStore(&cursor)
			store.persistErr = test.persistErr
			injector := &databasePrimaryWorldStateTurnInjector{flowID: 7, builder: test.build, store: store}
			chain := completedToolChain()
			got, delivered, err := injector.Inject(t.Context(), 11, chain)
			if err == nil || delivered || got != nil || store.persistedChain != nil || *store.cursor != 1 {
				t.Fatalf("failure acknowledged delivery: delivered=%v cursor=%d err=%v", delivered, *store.cursor, err)
			}
		})
	}
}

func TestWorldStateDeliveryRestartSummarizationAndCapturedHead(t *testing.T) {
	store := newPrimaryStore(nil)
	head := int64(3)
	builder := primaryDeliveryBuilderFunc(func(_ context.Context, _ int64, cursor *int64) (worldstate.PrimaryDelivery, error) {
		if cursor != nil && *cursor == head {
			return worldstate.PrimaryDelivery{Kind: worldstate.DeliveryNone}, nil
		}
		kind := worldstate.DeliveryDelta
		if cursor == nil {
			kind = worldstate.DeliveryBaseline
		}
		return delivery(kind, head), nil
	})
	injector := &databasePrimaryWorldStateTurnInjector{flowID: 7, builder: builder, store: store}
	first, delivered, err := injector.Inject(t.Context(), 11, completedToolChain())
	if err != nil || !delivered || store.cursor == nil || *store.cursor != 3 {
		t.Fatalf("baseline failed: delivered=%v cursor=%v err=%v", delivered, store.cursor, err)
	}
	_ = first

	summarized := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		llms.TextParts(llms.ChatMessageTypeHuman, "summary"),
	}
	restarted := &databasePrimaryWorldStateTurnInjector{flowID: 7, builder: builder, store: store}
	if got, changed, err := restarted.Inject(t.Context(), 11, summarized); err != nil || changed || !reflect.DeepEqual(got, summarized) {
		t.Fatalf("restart redelivered acknowledged head: changed=%v err=%v", changed, err)
	}
	head = 4
	got, changed, err := restarted.Inject(t.Context(), 11, summarized)
	if err != nil || !changed || *store.cursor != 4 || !chainContains(got, `"through_revision":4`) {
		t.Fatalf("event after captured head was not delivered next turn: changed=%v cursor=%v err=%v", changed, store.cursor, err)
	}
	reflected := append(got, llms.TextParts(llms.ChatMessageTypeAI, "reflection"))
	if _, err := cast.NewChainAST(reflected, false); err != nil {
		t.Fatalf("reflection chain lost provider validity: %v", err)
	}
}

func TestWorldStatePerformerBoundary(t *testing.T) {
	var (
		captured = make(chan struct{})
		release  = make(chan struct{})
		injector = &performerRecordingInjector{
			nextRevision:  1,
			firstCaptured: captured,
			releaseFirst:  release,
		}
		provider = newPerformerRecordingProvider()
		db       = &performerDBStub{}
		order    = &performerEventLog{}
	)
	provider.events = order
	injector.events = order

	fp := newPerformerBoundaryProvider(db, provider, injector)
	errCh := make(chan error, 1)
	go func() {
		errCh <- fp.performAgentChain(
			t.Context(),
			pconfig.OptionsTypePrimaryAgent,
			42,
			nil,
			nil,
			performerInitialChain(),
			&performerExecutor{},
			nil,
		)
	}()
	<-captured
	arrivalDone := make(chan struct{})
	go func() {
		injector.SetRevision(2)
		close(arrivalDone)
	}()
	<-arrivalDone
	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("primary performer failed: %v", err)
	}

	if got := injector.Calls(); got != 2 {
		t.Fatalf("expected one injection per outer turn, got %d", got)
	}
	if got := injector.Revisions(); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("unexpected captured revisions: %v", got)
	}

	modelChains := provider.Chains()
	if len(modelChains) != 5 {
		t.Fatalf("expected retry, reflector, and next-turn model calls, got %d", len(modelChains))
	}
	for idx, chain := range modelChains[:4] {
		if !chainContains(chain, "world state revision 1") {
			t.Fatalf("model call %d did not reuse acknowledged revision 1: %#v", idx, chain)
		}
		if chainContains(chain, "world state revision 2") {
			t.Fatalf("model call %d captured the later event during the same turn", idx)
		}
	}
	if !chainContains(modelChains[len(modelChains)-1], "world state revision 2") {
		t.Fatalf("next outer turn did not capture later event: %#v", modelChains[len(modelChains)-1])
	}

	events := order.Events()
	if len(events) < 3 || events[0] != "inject" || events[1] != "model" {
		t.Fatalf("injection was not at the model boundary: %v", events)
	}
	secondInjection := slicesIndex(events, "inject", 1)
	if secondInjection < 0 || secondInjection != len(events)-2 || events[len(events)-1] != "model" {
		t.Fatalf("unexpected injection/model ordering: %v", events)
	}
	for _, event := range events[1:secondInjection] {
		if event == "inject" {
			t.Fatalf("internal retry/reflection recaptured World State: %v", events)
		}
	}

	updated := db.LastChain()
	if _, err := cast.NewChainAST(updated, false); err != nil {
		t.Fatalf("final persisted chain is invalid: %v", err)
	}
	assertToolResponseGroups(t, updated)
}

func TestWorldStatePerformerBoundaryNonPrimaryGating(t *testing.T) {
	injector := &performerRecordingInjector{nextRevision: 1}
	provider := newPerformerRecordingProvider()
	provider.failCalls = 0
	fp := newPerformerBoundaryProvider(&performerDBStub{}, provider, injector)
	if err := fp.performAgentChain(
		t.Context(), pconfig.OptionsTypeAssistant, 43, nil, nil,
		performerInitialChain(), &performerExecutor{}, nil,
	); err != nil {
		t.Fatalf("non-primary performer failed: %v", err)
	}
	if got := injector.Calls(); got != 0 {
		t.Fatalf("non-primary performer invoked World State injector %d times", got)
	}
}

type performerEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *performerEventLog) Add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *performerEventLog) Events() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type performerRecordingInjector struct {
	mu            sync.Mutex
	events        *performerEventLog
	firstCaptured chan struct{}
	releaseFirst  chan struct{}
	nextRevision  int64
	revisions     []int64
	calls         int
}

func (i *performerRecordingInjector) Inject(_ context.Context, _ int64, chain []llms.MessageContent) ([]llms.MessageContent, bool, error) {
	i.mu.Lock()
	i.calls++
	call := i.calls
	revision := i.nextRevision
	i.revisions = append(i.revisions, revision)
	i.mu.Unlock()
	if call == 1 && i.firstCaptured != nil {
		close(i.firstCaptured)
		<-i.releaseFirst
	}
	if i.events != nil {
		i.events.Add("inject")
	}
	ast, err := cast.NewChainAST(cloneChain(chain), false)
	if err != nil {
		return nil, false, err
	}
	ast.AppendHumanMessage("world state revision " + fmt.Sprint(revision))
	return ast.Messages(), true, nil
}

func (i *performerRecordingInjector) SetRevision(revision int64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.nextRevision = revision
}

func (i *performerRecordingInjector) Calls() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls
}

func (i *performerRecordingInjector) Revisions() []int64 {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]int64(nil), i.revisions...)
}

type performerRecordingProvider struct {
	*providermock.Provider
	events    *performerEventLog
	finalTool string
	failCalls int
	mu        sync.Mutex
	callCount int
	chains    [][]llms.MessageContent
}

func newPerformerRecordingProvider() *performerRecordingProvider {
	return &performerRecordingProvider{
		Provider:  providermock.NewProvider(providerpkg.ProviderCustom, "test-model"),
		finalTool: tools.FinalyToolName,
		failCalls: 3,
	}
}

func (p *performerRecordingProvider) GetUsage(map[string]any) pconfig.CallUsage {
	return pconfig.CallUsage{}
}

func (p *performerRecordingProvider) CallEx(ctx context.Context, opt pconfig.ProviderOptionsType, chain []llms.MessageContent, streamCb streaming.Callback) (*llms.ContentResponse, error) {
	if p.events != nil {
		p.events.Add("model-simple")
	}
	return p.Provider.CallEx(ctx, opt, chain, streamCb)
}

func (p *performerRecordingProvider) CallWithTools(_ context.Context, _ pconfig.ProviderOptionsType, chain []llms.MessageContent, _ []llms.Tool, _ streaming.Callback) (*llms.ContentResponse, error) {
	p.mu.Lock()
	p.callCount++
	call := p.callCount
	p.chains = append(p.chains, cloneChain(chain))
	p.mu.Unlock()
	if p.events != nil {
		p.events.Add("model")
	}
	if call <= p.failCalls {
		return nil, errors.New("temporary model failure")
	}
	toolName := "test_tool"
	if call > 4 {
		toolName = p.finalTool
	}
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{ToolCalls: []llms.ToolCall{{
		ID:           fmt.Sprintf("performer-call-%d", call),
		Type:         "function",
		FunctionCall: &llms.FunctionCall{Name: toolName, Arguments: `{}`},
	}}}}}, nil
}

func (p *performerRecordingProvider) Chains() [][]llms.MessageContent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]llms.MessageContent(nil), p.chains...)
}

type performerDBStub struct {
	database.Querier
	mu      sync.Mutex
	chains  [][]llms.MessageContent
	created int64
}

func (db *performerDBStub) GetFlowTasks(context.Context, int64) ([]database.Task, error) {
	return nil, nil
}

func (db *performerDBStub) GetFlowSubtasks(context.Context, int64) ([]database.Subtask, error) {
	return nil, nil
}

func (db *performerDBStub) UpdateMsgChain(_ context.Context, arg database.UpdateMsgChainParams) (database.Msgchain, error) {
	var chain []llms.MessageContent
	if err := json.Unmarshal(arg.Chain, &chain); err != nil {
		return database.Msgchain{}, err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.chains = append(db.chains, cloneChain(chain))
	return database.Msgchain{ID: arg.ID}, nil
}

func (db *performerDBStub) CreateMsgChain(context.Context, database.CreateMsgChainParams) (database.Msgchain, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.created++
	return database.Msgchain{ID: 100 + db.created}, nil
}

func (db *performerDBStub) LastChain() []llms.MessageContent {
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.chains) == 0 {
		return nil
	}
	return cloneChain(db.chains[len(db.chains)-1])
}

type performerExecutor struct{}

func (*performerExecutor) Tools() []llms.Tool { return nil }

func (*performerExecutor) Execute(context.Context, int64, string, string, string, string, json.RawMessage) (string, error) {
	return "tool response", nil
}

func (*performerExecutor) IsBarrierFunction(name string) bool { return name == tools.FinalyToolName }

func (*performerExecutor) IsFunctionExists(string) bool { return true }

func (*performerExecutor) GetBarrierToolNames() []string { return []string{tools.FinalyToolName} }

func (*performerExecutor) GetBarrierTools() []tools.FunctionInfo { return nil }

func (*performerExecutor) GetToolSchema(string) (*schema.Schema, error) { return nil, nil }

func newPerformerBoundaryProvider(db database.Querier, provider providerpkg.Provider, injector primaryWorldStateTurnInjector) *flowProvider {
	return &flowProvider{
		db:              db,
		mx:              &sync.RWMutex{},
		callCounter:     new(atomic.Int64),
		flowID:          7,
		prompter:        templates.NewDefaultPrompter(),
		maxGACallsLimit: 6,
		maxLACallsLimit: 6,
		buildMonitor: func() *executionMonitor {
			return &executionMonitor{enabled: false}
		},
		worldState: injector,
		Provider:   provider,
	}
}

func performerInitialChain() []llms.MessageContent {
	return []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		llms.TextParts(llms.ChatMessageTypeHuman, "request"),
	}
}

func slicesIndex(values []string, value string, start int) int {
	for idx := start; idx < len(values); idx++ {
		if values[idx] == value {
			return idx
		}
	}
	return -1
}

func assertToolResponseGroups(t *testing.T, chain []llms.MessageContent) {
	t.Helper()
	for idx, message := range chain {
		if message.Role != llms.ChatMessageTypeAI {
			continue
		}
		var calls []llms.ToolCall
		for _, part := range message.Parts {
			if call, ok := part.(llms.ToolCall); ok {
				calls = append(calls, call)
			}
		}
		if len(calls) == 0 {
			continue
		}
		if idx+1 >= len(chain) || chain[idx+1].Role != llms.ChatMessageTypeTool {
			t.Fatalf("tool call at message %d is not followed by a tool response", idx)
		}
		for _, call := range calls {
			matched := false
			for _, part := range chain[idx+1].Parts {
				response, ok := part.(llms.ToolCallResponse)
				if ok && response.ToolCallID == call.ID {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("tool call %q has no adjacent response", call.ID)
			}
		}
	}
}

func newPrimaryStore(cursor *int64) *primaryDeliveryStoreStub {
	return &primaryDeliveryStoreStub{
		msgChain: database.Msgchain{ID: 11, FlowID: 7, Type: database.MsgchainTypePrimaryAgent},
		cursor:   cursor,
	}
}

func delivery(kind worldstate.DeliveryKind, through int64) worldstate.PrimaryDelivery {
	payload, _ := json.Marshal(map[string]any{
		"type": kind, "coverage": map[string]any{"through_revision": through},
	})
	return worldstate.PrimaryDelivery{Kind: kind, Coverage: worldstate.RevisionCoverage{ThroughRevision: through}, Payload: payload}
}

func completedToolChain() []llms.MessageContent {
	return []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		llms.TextParts(llms.ChatMessageTypeHuman, "request"),
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{llms.ToolCall{ID: "call-1", Type: "function", FunctionCall: &llms.FunctionCall{Name: "query", Arguments: `{}`}}}},
		{Role: llms.ChatMessageTypeTool, Parts: []llms.ContentPart{llms.ToolCallResponse{ToolCallID: "call-1", Name: "query", Content: "result"}}},
	}
}

func pendingToolCallChain() []llms.MessageContent {
	return []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		llms.TextParts(llms.ChatMessageTypeHuman, "request"),
		{Role: llms.ChatMessageTypeAI, Parts: []llms.ContentPart{
			llms.ToolCall{ID: "call-pending", Type: "function", FunctionCall: &llms.FunctionCall{Name: "query", Arguments: `{}`}},
		}},
	}
}

func chainContains(chain []llms.MessageContent, want string) bool {
	for _, message := range chain {
		for _, part := range message.Parts {
			if text, ok := part.(llms.TextContent); ok && len(text.Text) >= len(want) {
				for i := 0; i+len(want) <= len(text.Text); i++ {
					if text.Text[i:i+len(want)] == want {
						return true
					}
				}
			}
		}
	}
	return false
}

func assertUniqueToolUseIDs(t *testing.T, chain []llms.MessageContent) {
	t.Helper()
	seen := map[string]struct{}{}
	for msgIdx, message := range chain {
		if message.Role != llms.ChatMessageTypeAI {
			continue
		}
		here := map[string]struct{}{}
		for partIdx, part := range message.Parts {
			toolCall, ok := part.(llms.ToolCall)
			if !ok || toolCall.FunctionCall == nil {
				continue
			}
			if toolCall.ID == "" {
				t.Fatalf("empty tool_use id at message %d part %d", msgIdx, partIdx)
			}
			if _, dup := here[toolCall.ID]; dup {
				t.Fatalf("duplicate tool_use id %q in message %d", toolCall.ID, msgIdx)
			}
			here[toolCall.ID] = struct{}{}
			if _, dup := seen[toolCall.ID]; dup {
				t.Fatalf("duplicate tool_use id %q across chain at message %d", toolCall.ID, msgIdx)
			}
			seen[toolCall.ID] = struct{}{}
		}
	}
}

func worldStateTestLogger() *logrus.Entry {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return logrus.NewEntry(logger)
}
