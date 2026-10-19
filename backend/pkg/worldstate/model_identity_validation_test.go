package worldstate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModelJSONPreservesExactNumericIdentifiers(t *testing.T) {
	left := json.RawMessage(`{"id":9007199254740993,"duration":1e3}`)
	if equalModelJSON(left, json.RawMessage(`{"id":9007199254740992,"duration":1000}`)) {
		t.Fatal("neighboring integer IDs above float64 precision must differ")
	}
	if !equalModelJSON(left, json.RawMessage(`{"id":9007199254740993,"duration":1000.0}`)) {
		t.Fatal("PostgreSQL normalization of equivalent numbers must not break retries")
	}
	safe, err := modelSafeMap(map[string]any{"id": json.Number("9007199254740993"), "nested": []any{json.Number("9007199254740995")}})
	if err != nil || !strings.Contains(string(safe), "9007199254740993") || !strings.Contains(string(safe), "9007199254740995") {
		t.Fatalf("redaction rounded numeric IDs: %s err=%v", safe, err)
	}
	_, _, locator, err := normalizeModelObject(ObserveObjectInput{Type: "host", Locator: map[string]any{
		"host": "localhost", "port": 18080, "external_id": json.Number("9007199254740993"),
	}})
	if err != nil || !strings.Contains(string(locator), "9007199254740993") {
		t.Fatalf("locator canonicalization rounded numeric ID: %s err=%v", locator, err)
	}
}

func TestModelIdentityRejectsMalformedQueryInsteadOfDroppingRoute(t *testing.T) {
	for _, raw := range []string{
		"https://example.invalid/search?action=%zz&q=test",
		"https://example.invalid/search?action=find;delete&q=test",
		"https://example.invalid/search?q=%XX",
	} {
		for _, kind := range []string{"endpoint", "parameter"} {
			t.Run(kind+":"+raw, func(t *testing.T) {
				_, err := CanonicalObjectKey(kind, map[string]any{
					"url": raw, "method": "GET", "location": "query", "name": "q",
				})
				if err == nil {
					t.Fatal("invalid query was silently canonicalized as a different target")
				}
			})
		}
	}
}

func TestModelExperimentURLsPreserveExactQueryBytes(t *testing.T) {
	for _, raw := range []string{
		"https://EXAMPLE.invalid/a%2fb?b=2&a=%20&a=+&&q=%zz#fragment",
		"https://example.invalid/search?q=one;two&b=2&",
		"https://example.invalid/search?",
	} {
		if got := redactModelURLs(raw); got != raw {
			t.Fatalf("experiment URL changed without credentials: got %q want %q", got, raw)
		}
	}
	raw := "https://admin:synthetic-password@EXAMPLE.invalid:8443/a%2fb?b=2&%74oken=synthetic-token&a=%20&q=%zz&"
	want := "https://EXAMPLE.invalid:8443/a%2fb?b=2&%74oken=%5BREDACTED%5D&a=%20&q=%zz&"
	if got := redactModelURLs(raw); got != want {
		t.Fatalf("URL redaction changed experiment conditions: got %q want %q", got, want)
	}
}

func TestModelExactNumberComparisonBoundsAllocation(t *testing.T) {
	for _, raw := range []string{"1e999999999", "1e-999999999", "1e9223372036854775808"} {
		if boundedModelNumber(json.Number(raw)) {
			t.Fatalf("unsafe exponent accepted: %s", raw)
		}
		value := json.RawMessage(`{"id":` + raw + `}`)
		if equalModelJSON(value, value) {
			t.Fatal("out-of-range numbers must not reach arbitrary-precision allocation")
		}
	}
	if boundedModelNumber(json.Number(strings.Repeat("1", 131072+16383+1))) {
		t.Fatal("unbounded mantissa accepted")
	}
}
