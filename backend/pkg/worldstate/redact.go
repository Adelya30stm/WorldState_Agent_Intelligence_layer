package worldstate

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

const redactedValue = "[REDACTED]"

func marshalSafeJSON(value any) (json.RawMessage, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("worldstate: normalize safe json: %w", err)
	}
	var normalized any
	if err := json.Unmarshal(plain, &normalized); err != nil {
		return nil, fmt.Errorf("worldstate: normalize safe json: %w", err)
	}
	b, err := json.Marshal(redactValue(normalized))
	if err != nil {
		return nil, fmt.Errorf("worldstate: marshal safe json: %w", err)
	}
	return b, nil
}

func redactRawJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return emptyObject, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("worldstate: decode evidence: %w", err)
	}
	return marshalSafeJSON(value)
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if credentialKey(key) {
				out[key] = redactedValue
			} else if entityIdentifierKey(key) {
				if identifier, ok := child.(string); ok {
					out[key] = safeEntityKey(identifier)
				} else {
					out[key] = redactValue(child)
				}
			} else {
				out[key] = redactValue(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactValue(child)
		}
		return out
	case string:
		if authorizationMaterial(typed) {
			return redactedValue
		}
	}
	return value
}

func entityIdentifierKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	switch normalized {
	case "entity_key", "source_key", "target_key":
		return true
	default:
		return false
	}
}

func credentialEntityKey(identity string) string {
	return "credential:" + identity
}

func safeEntityKey(key string) string {
	parts := strings.SplitN(strings.TrimSpace(key), ":", 3)
	if len(parts) < 2 || !strings.EqualFold(parts[0], EntityTypeCredential) {
		return key
	}
	identity := strings.ToLower(strings.TrimSpace(parts[1]))
	if identity == "" {
		return credentialEntityKey("unknown")
	}
	if canonical := credentialKeyIdentity(identity); canonical != "" {
		return credentialEntityKey(canonical)
	}
	if len(parts) == 3 && isCredentialSecretIdentity(identity) {
		return credentialEntityKey(credentialKeyIdentity(identity))
	}
	return key
}

func credentialKeyIdentity(identity string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(identity), "-", "_"))
	for _, marker := range []struct {
		prefix string
		kind   string
	}{
		{prefix: "password", kind: "password"},
		{prefix: "passwd", kind: "password"},
		{prefix: "pwd", kind: "password"},
		{prefix: "authorization", kind: "authorization"},
		{prefix: "auth", kind: "authorization"},
		{prefix: "basic", kind: "basic"},
		{prefix: "bearer", kind: "bearer"},
		{prefix: "digest", kind: "digest"},
		{prefix: "token", kind: "token"},
		{prefix: "access_token", kind: "token"},
		{prefix: "refresh_token", kind: "token"},
		{prefix: "api_key", kind: "api_key"},
		{prefix: "apikey", kind: "api_key"},
		{prefix: "client_secret", kind: "secret"},
		{prefix: "secret", kind: "secret"},
		{prefix: "cookie", kind: "cookie"},
		{prefix: "session", kind: "session"},
		{prefix: "private", kind: "private"},
		{prefix: "private_key", kind: "private"},
		{prefix: "access_key", kind: "access"},
		{prefix: "access", kind: "access"},
	} {
		if strings.HasPrefix(normalized, marker.prefix) {
			return marker.kind
		}
	}
	return ""
}

func isCredentialSecretIdentity(identity string) bool {
	return credentialKeyIdentity(identity) != ""
}

func credentialKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
	for _, marker := range []string{
		"password", "passwd", "secret", "token", "apikey", "authorization",
		"credential", "cookie", "privatekey", "clientsecret", "accesskey",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return normalized == "auth" || normalized == "pwd"
}

func authorizationMaterial(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{"bearer ", "basic ", "digest ", "token "} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	for _, marker := range []string{
		"password=", "password:", "passwd=", "passwd:", "secret=", "secret:",
		"token=", "token:", "api_key=", "api_key:", "apikey=", "apikey:",
		"authorization=", "authorization:", "cookie=", "cookie:",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return strings.Contains(lower, "-----begin private key-----") ||
		strings.Contains(lower, "-----begin rsa private key-----") ||
		strings.Contains(lower, "-----begin ec private key-----")
}
