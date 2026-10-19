package worldstate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestModelIngestionProducesOnlyObjects(t *testing.T) {
	f := newModelFixture(t)
	ctx := context.Background()
	output := `GET http://localhost:18080/search?action=find&q=first
GET http://localhost:18080/search?action=find&q=second
XSS vulnerable SQL injection CVE-2026-1234`
	IngestToolResult(ctx, f.q, f.flowID, "terminal", output)
	snapshot, err := NewModelStore(f.q).ReadSnapshot(ctx, f.flowID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ObjectCount == 0 || snapshot.CheckCount != 0 || snapshot.RunCount != 0 {
		t.Fatalf("deterministic discovery fabricated experiments: %+v", snapshot)
	}
	var qParameters int
	for _, object := range snapshot.Objects {
		var locator map[string]any
		if err := json.Unmarshal(object.Locator, &locator); err != nil {
			t.Fatal(err)
		}
		if object.Type == "parameter" && locator["name"] == "q" {
			qParameters++
		}
		if object.ComputedStatus != "unknown" {
			t.Fatalf("discovered object has unsupported security status: %+v", object)
		}
	}
	if qParameters != 1 {
		t.Fatalf("payload variants should observe one q object; got %d", qParameters)
	}
	IngestToolResult(ctx, f.q, f.flowID, "terminal", output)
	repeated, err := NewModelStore(f.q).ReadSnapshot(ctx, f.flowID)
	if err != nil || repeated.Revision != snapshot.Revision {
		t.Fatalf("same discovery added duplicate events: before=%d after=%d err=%v", snapshot.Revision, repeated.Revision, err)
	}
	legacy, err := f.q.GetWorldStateEntitiesByFlow(ctx, f.flowID)
	if err != nil || len(legacy) != 0 {
		t.Fatalf("v2 ingestion wrote legacy entity records: %d err=%v", len(legacy), err)
	}
}

func TestExtractObjectsRetainsPortMethodAndParameterIdentity(t *testing.T) {
	input := `GET http://www.example.invalid:8080/search?q=first
POST http://www.example.invalid:8080/search?q=first
http://www.example.invalid:8081/search?q=second
http://localhost:18080/search?q=local
http://[::1]:18080/search?q=ipv6`
	candidates := ExtractObjectCandidates("terminal", input)
	methods := map[string]bool{}
	var privateTarget, ipv6Target, wwwHost bool
	for _, c := range candidates {
		if c.Type == "parameter" {
			if c.Locator["name"] != "q" || c.Locator["location"] != "query" {
				t.Fatalf("incorrect parameter locator: %+v", c)
			}
			methods[c.Locator["method"].(string)] = true
		}
		privateTarget = privateTarget || strings.Contains(c.Key, "localhost:18080")
		ipv6Target = ipv6Target || strings.Contains(c.Key, "[::1]:18080")
		wwwHost = wwwHost || strings.Contains(c.Key, "www.example.invalid:8080")
	}
	if !methods["GET"] || !methods["POST"] || !methods["UNSPECIFIED"] || !privateTarget || !ipv6Target || !wwwHost {
		t.Fatalf("identity was lost: methods=%v private=%v ipv6=%v www=%v", methods, privateTarget, ipv6Target, wwwHost)
	}
}

func TestExtractObjectsDoesNotCreateSecurityConclusions(t *testing.T) {
	input := `XSS confirmed vulnerable! SQL injection CVE-2026-1234
http://127.0.0.1:18080/?q=test
username=admin password=synthetic-secret`
	candidates := ExtractObjectCandidates("terminal", input)
	if len(candidates) == 0 {
		t.Fatal("expected local object observations")
	}
	for _, c := range candidates {
		if c.Type != "host" && c.Type != "endpoint" && c.Type != "parameter" {
			t.Fatalf("invented security conclusion from keywords: %+v", c)
		}
	}
	if candidates := ExtractObjectCandidates("world_state_query", input); len(candidates) != 0 {
		t.Fatal("reading World State must not ingest its own output")
	}
	if candidates := ExtractObjectCandidates("pentester", input); len(candidates) != 0 {
		t.Fatal("a delegated agent summary must not become a discovered object")
	}
}

func TestExtractObjectsRedactsURLsWithoutLosingQueryNames(t *testing.T) {
	candidates := ExtractObjectCandidates("terminal", `http://admin:synthetic-password@localhost:18080/search?token=synthetic-token&q=test`)
	var q, token bool
	for _, c := range candidates {
		encoded, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "synthetic-password") || strings.Contains(string(encoded), "synthetic-token") {
			t.Fatalf("raw secret leaked into candidate: %s", encoded)
		}
		if c.Type == "parameter" {
			q = q || c.Locator["name"] == "q"
			token = token || c.Locator["name"] == "token"
		}
	}
	if !q || !token {
		t.Fatalf("query parameters disappeared during redaction: %+v", candidates)
	}
}

func TestObservedHTTPMethodDoesNotGuessFromAnotherLine(t *testing.T) {
	for _, prefix := range []string{"", "curl ", "GET\n", "POST request to "} {
		if got := observedHTTPMethod(prefix); got != "UNSPECIFIED" {
			t.Fatalf("inferred method %s from %q", got, prefix)
		}
	}
}
