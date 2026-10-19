package worldstate

import "testing"

func TestParseScopeFromInputMarker(t *testing.T) {
	input := "Pentest mokka.\n\nredscope-scope: mokka.ru, https://www.Foo.com/path, mokka.ru\n"
	got := ParseScopeFromInput(input)
	if len(got.Domains) != 2 || got.Domains[0] != "mokka.ru" || got.Domains[1] != "foo.com" {
		t.Fatalf("domains = %#v", got.Domains)
	}
}

func TestParseScopeFromInputHTMLComment(t *testing.T) {
	got := ParseScopeFromInput("task\n<!-- redscope-scope: mokka.ru -->\n")
	if len(got.Domains) != 1 || got.Domains[0] != "mokka.ru" {
		t.Fatalf("domains = %#v", got.Domains)
	}
}

func TestParseScopeFromInScopeSection(t *testing.T) {
	input := `Perform a pentest.

In scope:
- https://mokka.ru
- www.mokka.ru/login

Out of scope:
- mokka.bg
- 0mmokka.ru
`
	got := ParseScopeFromInput(input)
	if len(got.Domains) != 1 || got.Domains[0] != "mokka.ru" {
		t.Fatalf("expected only mokka.ru, got %#v", got.Domains)
	}
}

func TestParseScopeFromInputEmpty(t *testing.T) {
	if !ParseScopeFromInput("just a task with github.com mentioned").Empty() {
		t.Fatal("expected empty scope without marker or in-scope section")
	}
}

func TestClassifyHostInGreySkip(t *testing.T) {
	scope := Scope{Domains: []string{"mokka.ru"}}
	cases := []struct {
		host string
		want Decision
	}{
		{"mokka.ru", DecisionIn},
		{"www.mokka.ru", DecisionIn},
		{"cdn.mokka.ru", DecisionIn},
		{"0mmokka.ru", DecisionGrey},
		{"mokka.bg", DecisionGrey},
		{"evilmokka.com", DecisionGrey},
		{"testapp.site", DecisionSkip},
		{"github.com", DecisionSkip},
		{"185.1.2.3", DecisionSkip},
	}
	for _, tc := range cases {
		if got := ClassifyHost(scope, tc.host); got != tc.want {
			t.Errorf("ClassifyHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestClassifyHostEmptyScopeIsLegacy(t *testing.T) {
	if got := ClassifyHost(Scope{}, "anything.example"); got != DecisionIn {
		t.Fatalf("empty scope should ingest, got %v", got)
	}
}

func TestClassifyHostAcceptedIP(t *testing.T) {
	scope := Scope{Domains: []string{"10.0.0.5"}}
	if got := ClassifyHost(scope, "10.0.0.5"); got != DecisionIn {
		t.Fatalf("accepted IP should stay in scope, got %v", got)
	}
}

func TestClassifyHostCIDR(t *testing.T) {
	scope := Scope{CIDRs: []string{"10.0.0.0/8"}}
	if got := ClassifyHost(scope, "10.1.2.3"); got != DecisionIn {
		t.Fatalf("in-CIDR IP: %v", got)
	}
	if got := ClassifyHost(scope, "185.1.2.3"); got != DecisionSkip {
		t.Fatalf("out-of-CIDR IP: %v", got)
	}
}

func TestClassifyCandidateSkipsSecretsWhenScoped(t *testing.T) {
	scope := Scope{Domains: []string{"mokka.ru"}}
	cred := Candidate{Type: EntityTypeCredential, Key: "credential:x", Properties: map[string]any{"host": "mokka.ru"}}
	if got := ClassifyCandidate(scope, cred); got != DecisionSkip {
		t.Fatalf("credentials must not auto-ingest when scope is set, got %v", got)
	}
	ep := Candidate{Type: EntityTypeEndpoint, Key: "endpoint:https://mokka.ru/", Properties: map[string]any{"host": "mokka.ru"}}
	if got := ClassifyCandidate(scope, ep); got != DecisionIn {
		t.Fatalf("in-scope endpoint: %v", got)
	}
	grey := Candidate{Type: EntityTypeHost, Key: "host:mokka.bg"}
	if got := ClassifyCandidate(scope, grey); got != DecisionGrey {
		t.Fatalf("grey host: %v", got)
	}
}
