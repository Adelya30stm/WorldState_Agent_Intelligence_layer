package worldstate

import (
	"strings"
	"testing"

	"pentagi/pkg/database"
)

func TestFormatSnapshotEmpty(t *testing.T) {
	snap := FormatSnapshot(nil)
	if !strings.Contains(snap, "(empty") {
		t.Fatalf("expected empty marker, got: %s", snap)
	}
}

func TestFormatSnapshotGroups(t *testing.T) {
	entities := []database.WorldStateEntity{
		{EntityKey: "host:a.example", Type: EntityTypeHost, State: database.WorldStateLifecycleDiscovered},
		{EntityKey: "credential:admin", Type: EntityTypeCredential, State: database.WorldStateLifecycleAssessed},
		{EntityKey: "finding:cve-2021-44228", Type: EntityTypeFinding, State: database.WorldStateLifecycleVulnerable},
	}
	snap := FormatSnapshot(entities)
	for _, want := range []string{
		"discovered=1",
		"assessed=1",
		"vulnerable=1",
		"host:a.example",
		"credential:admin",
		"finding:cve-2021-44228",
	} {
		if !strings.Contains(snap, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, snap)
		}
	}
}

func TestExtractCredentialsAndFindings(t *testing.T) {
	result := `
username=admin password=Secret123!
Found CVE-2021-44228 and possible SQL injection on /login
Authorization: Basic YWRtaW46c2VjcmV0
`
	cands := ExtractCandidates("terminal", result)
	var sawCred, sawCVE, sawFinding bool
	for _, c := range cands {
		switch c.Type {
		case EntityTypeCredential:
			sawCred = true
		case EntityTypeFinding:
			sawFinding = true
			if strings.Contains(c.Key, "cve-2021-44228") {
				sawCVE = true
			}
		}
	}
	if !sawCred {
		t.Fatalf("expected credential candidate: %+v", cands)
	}
	if !sawCVE || !sawFinding {
		t.Fatalf("expected finding candidates: %+v", cands)
	}
}

func TestFormatSnapshotSanitizesCredentialIdentifiers(t *testing.T) {
	snapshot := FormatSnapshot([]database.WorldStateEntity{{
		EntityKey: "credential:token:synthetic-token-sentinel",
		Type:      EntityTypeCredential,
		State:     database.WorldStateLifecycleDiscovered,
	}})
	if strings.Contains(snapshot, "synthetic-token-sentinel") || !strings.Contains(snapshot, "credential:token") {
		t.Fatalf("snapshot leaked or lost stable credential identity")
	}
}
