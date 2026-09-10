package worldstate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"pentagi/pkg/database"
)

const (
	maxFrontierItems     = 12
	maxRecentTransitions = 8
)

// Projection is a compact, planner-facing snapshot of World State.
type Projection struct {
	Model             *ModelContext
	FlowID            int64
	TotalEntities     int
	CountsByState     map[State]int
	CountsByType      map[string]int
	Frontier          []FrontierItem // actionable now
	RecentTransitions []TransitionItem
	Empty             bool
}

type FrontierItem struct {
	Key   string
	Type  string
	State State
	Why   string
}

type TransitionItem struct {
	EntityKey string
	From      State
	To        State
	Agent     string
}

// BuildProjection loads persisted World State and builds a planner snapshot.
func BuildProjection(ctx context.Context, db database.Querier, flowID int64) (*Projection, error) {
	if db == nil || flowID <= 0 {
		return &Projection{FlowID: flowID, Empty: true, CountsByState: map[State]int{}, CountsByType: map[string]int{}}, nil
	}
	version, err := ModelVersion(ctx, db, flowID)
	if err != nil {
		return nil, err
	}
	if version == 2 {
		snapshot, err := NewModelStore(db).ReadSnapshot(ctx, flowID)
		if err != nil {
			return nil, err
		}
		view, err := NewModelContext(snapshot, 32<<10)
		if err != nil {
			return nil, err
		}
		return &Projection{FlowID: flowID, Model: &view, TotalEntities: len(snapshot.Objects), Empty: len(snapshot.Objects) == 0}, nil
	}

	entities, err := db.GetWorldStateEntitiesByFlow(ctx, flowID)
	if err != nil {
		return nil, fmt.Errorf("worldstate: load entities: %w", err)
	}

	p := &Projection{
		FlowID:        flowID,
		TotalEntities: len(entities),
		CountsByState: map[State]int{},
		CountsByType:  map[string]int{},
		Empty:         len(entities) == 0,
	}

	for _, e := range entities {
		st := State(e.State)
		p.CountsByState[st]++
		p.CountsByType[e.Type]++
	}

	p.Frontier = buildFrontier(entities)

	trs, err := db.GetWorldStateTransitionsByFlow(ctx, database.GetWorldStateTransitionsByFlowParams{
		FlowID: flowID,
		Limit:  int64(maxRecentTransitions),
	})
	if err != nil {
		// Soft-fail transitions — snapshot without them is still useful.
		trs = nil
	}
	for _, t := range trs {
		p.RecentTransitions = append(p.RecentTransitions, TransitionItem{
			EntityKey: safeEntityKey(t.EntityKey),
			From:      State(t.FromState),
			To:        State(t.ToState),
			Agent:     t.Agent,
		})
	}

	return p, nil
}

func buildFrontier(entities []database.WorldStateEntity) []FrontierItem {
	priority := map[State]int{
		StateDiscovered: 1,
		StateScanning:   2,
		StateAssessed:   3,
		StateVulnerable: 4,
		StateUnknown:    5,
		StateExploited:  6,
	}
	why := map[State]string{
		StateUnknown:    "not yet confirmed reachable — verify discovery",
		StateDiscovered: "reachable but not enumerated — scan / fingerprint",
		StateScanning:   "enumeration in progress — finish assessment",
		StateAssessed:   "ready for vulnerability analysis",
		StateVulnerable: "confirmed vuln — exploit or document",
		StateExploited:  "foothold present — document / remediate path",
	}

	items := make([]FrontierItem, 0, len(entities))
	for _, e := range entities {
		st := State(e.State)
		if st == StateRemediated {
			continue
		}
		pr, ok := priority[st]
		if !ok {
			continue
		}
		_ = pr
		items = append(items, FrontierItem{
			Key:   safeEntityKey(e.EntityKey),
			Type:  e.Type,
			State: st,
			Why:   why[st],
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		pi, pj := priority[items[i].State], priority[items[j].State]
		if pi != pj {
			return pi < pj
		}
		return items[i].Key < items[j].Key
	})

	if len(items) > maxFrontierItems {
		items = items[:maxFrontierItems]
	}
	return items
}

// Text renders an XML block suitable for planner prompt injection.
func (p *Projection) Text() string {
	if p != nil && p.Model != nil {
		data, _ := json.Marshal(p.Model)
		return string(data)
	}
	if p == nil || p.Empty {
		return `<world_state status="empty">
  <message>No persisted World State yet. Plan initial reconnaissance to discover hosts/endpoints; state will fill automatically from tool output.</message>
</world_state>`
	}

	var b strings.Builder
	b.WriteString("<world_state>\n")
	fmt.Fprintf(&b, "  <summary total=\"%d\">", p.TotalEntities)

	states := []State{StateUnknown, StateDiscovered, StateScanning, StateAssessed, StateVulnerable, StateExploited, StateRemediated}
	parts := make([]string, 0, len(states))
	for _, st := range states {
		if n := p.CountsByState[st]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", st, n))
		}
	}
	b.WriteString(strings.Join(parts, " "))
	b.WriteString("</summary>\n")

	if len(p.CountsByType) > 0 {
		b.WriteString("  <by_type>")
		types := make([]string, 0, len(p.CountsByType))
		for t := range p.CountsByType {
			types = append(types, t)
		}
		sort.Strings(types)
		tp := make([]string, 0, len(types))
		for _, t := range types {
			tp = append(tp, fmt.Sprintf("%s=%d", t, p.CountsByType[t]))
		}
		b.WriteString(strings.Join(tp, " "))
		b.WriteString("</by_type>\n")
	}

	b.WriteString("  <frontier>\n")
	b.WriteString("    <instruction>Prefer work on frontier items. Do NOT re-enumerate entities already in scanning/assessed/vulnerable unless new evidence requires it.</instruction>\n")
	for _, f := range p.Frontier {
		fmt.Fprintf(&b, "    <entity key=%q type=%q state=%q why=%q/>\n", safeEntityKey(f.Key), f.Type, f.State, f.Why)
	}
	b.WriteString("  </frontier>\n")

	if len(p.RecentTransitions) > 0 {
		b.WriteString("  <recent_transitions>\n")
		for _, t := range p.RecentTransitions {
			fmt.Fprintf(&b, "    <transition entity=%q from=%q to=%q agent=%q/>\n", safeEntityKey(t.EntityKey), t.From, t.To, t.Agent)
		}
		b.WriteString("  </recent_transitions>\n")
	}

	b.WriteString("</world_state>")
	return b.String()
}
