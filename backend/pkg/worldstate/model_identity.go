package worldstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var modelToken = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// CanonicalObjectKey preserves the transport, explicit port, HTTP method and
// parameter location. Fixed query parameters remain part of the address; only
// the tested query parameter's value is removed from its object's identity.
func CanonicalObjectKey(objectType string, locator map[string]any) (string, error) {
	_, key, _, err := normalizeModelObject(ObserveObjectInput{Type: objectType, Locator: locator})
	return key, err
}

func normalizeModelObject(input ObserveObjectInput) (string, string, json.RawMessage, error) {
	typ := strings.ToLower(strings.TrimSpace(input.Type))
	if !modelToken.MatchString(typ) || len(input.Locator) == 0 {
		return "", "", nil, fmt.Errorf("worldstate: type and nonempty locator are required")
	}
	// Clone before canonicalization; callers may reuse their input maps.
	plain, err := json.Marshal(input.Locator)
	if err != nil {
		return "", "", nil, err
	}
	var loc map[string]any
	if err := decodeModelJSON(plain, &loc); err != nil {
		return "", "", nil, err
	}
	key := strings.TrimSpace(input.ObjectKey)
	switch typ {
	case "endpoint", "parameter":
		rawURL, _ := loc["url"].(string)
		u, err := url.Parse(rawURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return "", "", nil, fmt.Errorf("worldstate: HTTP locator requires an absolute http(s) url")
		}
		// A credential-bearing path cannot identify a safe, usable object: later
		// redaction would erase locator.url while leaving the secret in its key.
		// Validate the decoded path before constructing either persisted field.
		if authorizationMaterial(u.Path) {
			return "", "", nil, fmt.Errorf("worldstate: HTTP locator path contains credential material")
		}
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		u.User, u.Fragment, u.RawFragment = nil, "", ""
		u.ForceQuery = false
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return "", "", nil, fmt.Errorf("worldstate: HTTP locator query cannot be canonicalized: %w", err)
		}
		for name := range query {
			if credentialKey(name) {
				query.Set(name, redactedValue)
			}
		}
		if u.Path == "" {
			u.Path = "/"
		}
		method, _ := loc["method"].(string)
		method = strings.ToUpper(strings.TrimSpace(method))
		if !regexp.MustCompile(`^[A-Z]{1,20}$`).MatchString(method) {
			return "", "", nil, fmt.Errorf("worldstate: HTTP locator requires a method")
		}
		suffix := ""
		if typ == "parameter" {
			location, _ := loc["location"].(string)
			location = strings.ToLower(strings.TrimSpace(location))
			name, _ := loc["name"].(string)
			if !modelToken.MatchString(location) || strings.TrimSpace(name) == "" || len(name) > 512 {
				return "", "", nil, fmt.Errorf("worldstate: parameter locator requires location and name")
			}
			if location == "header" {
				name = strings.ToLower(name)
			}
			loc["location"], loc["name"] = location, name
			if location == "query" {
				query.Set(name, "")
			}
			suffix = ":" + location + ":" + url.QueryEscape(name)
		}
		u.RawQuery = query.Encode()
		loc["url"], loc["method"] = u.String(), method
		canonical := typ + ":" + method + ":" + u.String() + suffix
		if key != "" && key != canonical {
			return "", "", nil, fmt.Errorf("worldstate: object_key must match canonical locator key %q", canonical)
		}
		key = canonical
	case "host":
		host, _ := loc["host"].(string)
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || strings.ContainsAny(host, "/@?# \t\n\r") {
			return "", "", nil, fmt.Errorf("worldstate: host locator requires a hostname or IP address")
		}
		loc["host"] = host
		canonical := "host:" + host
		if port, exists := loc["port"]; exists {
			number, ok := port.(json.Number)
			value, err := number.Int64()
			if !ok || err != nil || value < 1 || value > 65535 {
				return "", "", nil, fmt.Errorf("worldstate: invalid host port")
			}
			canonical = "host:" + net.JoinHostPort(host, strconv.FormatInt(value, 10))
		}
		if key != "" && key != canonical {
			return "", "", nil, fmt.Errorf("worldstate: object_key must match canonical locator key %q", canonical)
		}
		key = canonical
	default:
		if key == "" {
			return "", "", nil, fmt.Errorf("worldstate: object_key is required for type %q", typ)
		}
		key = safeEntityKey(key)
	}
	if len(key) > 2048 || malformedCredentialBearingURL(key) || ((typ != "endpoint" && typ != "parameter") && authorizationMaterial(key)) || strings.ContainsAny(key, "\r\n\x00") {
		return "", "", nil, fmt.Errorf("worldstate: invalid or sensitive object_key")
	}
	safe, err := modelSafeMap(loc)
	return typ, key, safe, err
}

func modelSafeMap(value map[string]any) (json.RawMessage, error) {
	if value == nil {
		value = map[string]any{}
	}
	// Existing redaction handles credential keys and raw HTTP header strings.
	// Additionally remove userinfo from embedded URLs before normal redaction.
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("worldstate: encode JSON: %w", err)
	}
	var normalized any
	if err := decodeModelJSON(encoded, &normalized); err != nil {
		return nil, err
	}
	return json.Marshal(redactModelURLs(normalized))
}

// JSON numbers in experiment conditions may be exact identifiers larger than
// float64 can represent. Keep their decimal representation through redaction.
func decodeModelJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(destination)
}

func redactModelURLs(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if credentialKey(key) {
				v[key] = redactedValue
			} else {
				v[key] = redactModelURLs(child)
			}
		}
	case []any:
		for i, child := range v {
			v[i] = redactModelURLs(child)
		}
	case string:
		u, err := url.Parse(v)
		if err == nil && u.Host != "" && u.Scheme != "" {
			if authorizationMaterial(u.Path) || authorizationMaterial(u.Fragment) {
				return redactedValue
			}
			return redactExactModelURL(v, u.User != nil)
		}
		if malformedCredentialBearingURL(v) {
			return redactedValue
		}
		return redactValue(v)
	}
	return value
}

// Experiment URLs preserve their byte spelling, ordering, repeated parameters
// and even deliberately malformed values. Canonicalization belongs to locators;
// redaction changes only credentials within the recorded actual request.
func redactExactModelURL(raw string, hasUserinfo bool) string {
	if hasUserinfo {
		if start := strings.Index(raw, "://"); start >= 0 {
			start += 3
			end := len(raw)
			if offset := strings.IndexAny(raw[start:], "/?#"); offset >= 0 {
				end = start + offset
			}
			if at := strings.LastIndexByte(raw[start:end], '@'); at >= 0 {
				raw = raw[:start] + raw[start+at+1:]
			}
		}
	}
	start := strings.IndexByte(raw, '?')
	end := len(raw)
	if fragment := strings.IndexByte(raw, '#'); fragment >= 0 {
		end = fragment
	}
	if start < 0 || start >= end {
		return raw
	}
	query := raw[start+1 : end]
	var safe strings.Builder
	for len(query) > 0 {
		separator := strings.IndexAny(query, "&;")
		part := query
		if separator >= 0 {
			part = query[:separator]
		}
		name, _, hasValue := strings.Cut(part, "=")
		decodedName, err := url.QueryUnescape(name)
		if hasValue && err == nil && credentialKey(decodedName) {
			safe.WriteString(name + "=" + url.QueryEscape(redactedValue))
		} else {
			safe.WriteString(part)
		}
		if separator < 0 {
			break
		}
		safe.WriteByte(query[separator])
		query = query[separator+1:]
	}
	return raw[:start+1] + safe.String() + raw[end:]
}
