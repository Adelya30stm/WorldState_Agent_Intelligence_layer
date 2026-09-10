package worldstate

import (
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Brackets are allowed for IPv6 authorities. Legacy extraction deliberately
// retains its own pattern and behavior for old flows.
var objectURLRE = regexp.MustCompile(`https?://[^\s'"` + "`" + `,<>)]+`)

// ObjectCandidate is a location discovered deterministically in an environment
// tool's result. It makes no assertion about security properties or execution.
type ObjectCandidate struct {
	Type    string
	Key     string
	Locator map[string]any
}

// ExtractObjectCandidates preserves ports, www hostnames, method uncertainty and
// query parameter names. A bare URL does not prove that a GET was executed.
func ExtractObjectCandidates(toolName, result string) []ObjectCandidate {
	if !looksLikeReconTool(toolName) {
		return nil
	}
	seen := map[string]bool{}
	urlHosts := map[string]bool{}
	out := make([]ObjectCandidate, 0)
	add := func(typ string, locator map[string]any) {
		key, err := CanonicalObjectKey(typ, locator)
		if err != nil || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, ObjectCandidate{Type: typ, Key: key, Locator: locator})
	}
	// Do not apply field regexes to a URL query: a token field regex could consume
	// the following &q parameter too. URL credentials are redacted structurally.
	safeResult := authorizationRE.ReplaceAllString(result, "$1 [REDACTED]")
	safeResult = cookieHeaderRE.ReplaceAllString(safeResult, "Cookie: [REDACTED]")
	for _, match := range objectURLRE.FindAllStringIndex(safeResult, -1) {
		raw := strings.TrimRight(safeResult[match[0]:match[1]], ".,;)}\"'>")
		for strings.HasSuffix(raw, "]") && strings.Count(raw, "]") > strings.Count(raw, "[") {
			raw = strings.TrimSuffix(raw, "]")
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		host := strings.ToLower(u.Hostname())
		// Loopback and private addresses are valid targets for local audits.
		if noiseHosts[host] && host != "localhost" && host != "example.com" {
			continue
		}
		urlHosts[host] = true
		hostLocator := map[string]any{"host": host}
		if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				continue
			}
			hostLocator["port"] = port
		}
		add("host", hostLocator)
		method := observedHTTPMethod(safeResult[:match[0]])
		u.User, u.Fragment, u.RawFragment = nil, "", ""
		u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
		if u.Path == "" {
			u.Path = "/"
		}
		safeURL := redactModelURLs(u.String()).(string)
		// Canonical object identity is decided in one place, shared with explicit
		// updates. Parameters retain their names; values are experiment conditions.
		endpointLocator := map[string]any{"url": safeURL, "method": method}
		add("endpoint", endpointLocator)
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			continue // Do not invent parameter names for malformed query strings.
		}
		names := make([]string, 0, len(query))
		for name := range query {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			add("parameter", map[string]any{
				"url": safeURL, "method": method, "location": "query", "name": name,
			})
		}
	}
	for _, ip := range ipv4RE.FindAllString(safeResult, -1) {
		if net.ParseIP(ip) == nil || urlHosts[ip] {
			continue
		}
		add("host", map[string]any{"host": ip})
	}
	return out
}

func observedHTTPMethod(prefix string) string {
	// Only an immediately preceding HTTP method token provides evidence of the
	// method. Do not infer it from tool names, curl defaults or unrelated lines.
	if start := strings.LastIndexAny(prefix, "\r\n"); start >= 0 {
		prefix = prefix[start+1:]
	}
	fields := strings.Fields(prefix)
	if len(fields) == 0 {
		return "UNSPECIFIED"
	}
	switch method := fields[len(fields)-1]; method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		return method
	default:
		return "UNSPECIFIED"
	}
}
