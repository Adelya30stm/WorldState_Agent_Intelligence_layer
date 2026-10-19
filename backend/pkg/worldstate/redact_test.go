package worldstate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalSafeJSONRecursivelyRedactsCredentials(t *testing.T) {
	payload := map[string]any{
		"safe": "visible",
		"nested": map[string]string{
			"api_key": "sensitive-api-value",
			"name":    "kept",
		},
		"items": []any{
			map[string]any{"Authorization": "sensitive-auth-value"},
			"Bearer sensitive-bearer-value",
			"-----BEGIN PRIVATE KEY-----\nsensitive-private-value",
			"request password=sensitive-inline-value",
		},
	}

	got, err := marshalSafeJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, secret := range []string{
		"sensitive-api-value", "sensitive-auth-value", "sensitive-bearer-value", "sensitive-private-value", "sensitive-inline-value",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("safe JSON contains credential material: %s", secret)
		}
	}
	if strings.Count(text, redactedValue) != 5 {
		t.Fatalf("safe JSON = %s, want five redactions", text)
	}

	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["safe"] != "visible" {
		t.Fatalf("safe value was changed: %v", decoded["safe"])
	}
}

func TestRedactRawJSONRejectsMalformedEvidence(t *testing.T) {
	if _, err := redactRawJSON(json.RawMessage(`{"broken"`)); err == nil {
		t.Fatal("expected malformed evidence error")
	}
}

func TestMarshalSafeJSONSanitizesEntityIdentifiers(t *testing.T) {
	got, err := marshalSafeJSON(map[string]any{
		"entity_key": "credential:password:synthetic-password-sentinel",
		"source_key": "credential:basic:synthetic-basic-sentinel",
		"safe":       "host:target.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, "synthetic-") {
		t.Fatalf("safe JSON contains credential identifier material")
	}
	for _, key := range []string{`"entity_key":"credential:password"`, `"source_key":"credential:basic"`} {
		if !strings.Contains(text, key) {
			t.Fatalf("safe JSON missing sanitized identifier %s: %s", key, text)
		}
	}
}
