package worldstate

import (
	"strings"
	"testing"
)

func TestModelObjectRejectsCredentialBearingPath(t *testing.T) {
	for _, objectType := range []string{"endpoint", "parameter"} {
		for _, path := range []string{"/password=secret-path", "/token:secret-path", "/%70assword%3Dsecret-path"} {
			t.Run(objectType+path, func(t *testing.T) {
				input := ObserveObjectInput{Type: objectType, Locator: map[string]any{
					"url": "https://example.test" + path, "method": "GET", "location": "query", "name": "q",
				}}
				_, key, locator, err := normalizeModelObject(input)
				if err == nil {
					t.Fatalf("credential-bearing locator accepted: key=%q locator=%s", key, locator)
				}
				if key != "" || len(locator) != 0 || strings.Contains(err.Error(), "secret-path") {
					t.Fatal("rejected object exposed credential material in its output or error")
				}
			})
		}
	}
	// A route name mentioning passwords is not itself credential material.
	key, err := CanonicalObjectKey("endpoint", map[string]any{"url": "https://example.test/password-reset", "method": "POST"})
	if err != nil || key != "endpoint:POST:https://example.test/password-reset" {
		t.Fatalf("ordinary password route rejected: key=%q err=%v", key, err)
	}
}
