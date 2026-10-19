package worldstate

import (
	"fmt"
	"sort"
	"strings"

	"pentagi/pkg/database"
)

const snapshotEntityCap = 24

// FormatSnapshot builds a compact planner-facing view of current World State.
// Empty input yields an empty-state marker so agents still see the section.
func FormatSnapshot(entities []database.WorldStateEntity) string {
	counts := map[string]int{}
	byType := map[string][]database.WorldStateEntity{}
	for _, e := range entities {
		counts[string(e.State)]++
		byType[e.Type] = append(byType[e.Type], e)
	}

	var b strings.Builder
	b.WriteString("<world_state_snapshot>\n")
	b.WriteString("Authoritative engagement state. Prefer this over chat history for \"what do we already know?\".\n")
	b.WriteString("Counts by lifecycle: ")
	if len(counts) == 0 {
		b.WriteString("(empty — nothing recorded yet)")
	} else {
		order := []string{
			string(StateUnknown), string(StateDiscovered), string(StateScanning),
			string(StateAssessed), string(StateVulnerable), string(StateExploited), string(StateRemediated),
		}
		parts := make([]string, 0, len(order))
		for _, st := range order {
			if n := counts[st]; n > 0 {
				parts = append(parts, fmt.Sprintf("%s=%d", st, n))
			}
		}
		b.WriteString(strings.Join(parts, ", "))
	}
	b.WriteByte('\n')

	writeGroup := func(label, typ string) {
		list := byType[typ]
		if len(list) == 0 {
			return
		}
		sort.Slice(list, func(i, j int) bool { return list[i].EntityKey < list[j].EntityKey })
		b.WriteString(label)
		b.WriteByte('\n')
		limit := snapshotEntityCap
		if len(list) < limit {
			limit = len(list)
		}
		for _, e := range list[:limit] {
			fmt.Fprintf(&b, "  - %s [%s]\n", safeEntityKey(e.EntityKey), e.State)
		}
		if len(list) > limit {
			fmt.Fprintf(&b, "  - ... %d more\n", len(list)-limit)
		}
	}

	writeGroup("Hosts:", EntityTypeHost)
	writeGroup("Services:", EntityTypeService)
	writeGroup("Endpoints:", EntityTypeEndpoint)
	writeGroup("Credentials:", EntityTypeCredential)
	writeGroup("Findings:", EntityTypeFinding)
	b.WriteString("</world_state_snapshot>")
	return b.String()
}
