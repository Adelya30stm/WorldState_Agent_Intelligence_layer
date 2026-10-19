package worldstate

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

type Decision int

const (
	DecisionSkip Decision = iota
	DecisionIn
	DecisionGrey
)

const (
	CandidateReasonSimilar = "similar_to_scope"
)

type Scope struct {
	Domains []string
	CIDRs   []string
}

func (s Scope) Empty() bool {
	return len(s.Domains) == 0 && len(s.CIDRs) == 0
}

var (
	scopeMarkerRE = regexp.MustCompile(`(?im)^(?:<!--\s*)?redscope-scope:\s*(.+?)(?:\s*-->)?\s*$`)
	inScopeHeadRE = regexp.MustCompile(`(?is)in[\s-]+scope(.*?)(?:out[\s-]+of[\s-]+scope|\z)`)
)

// ParseScopeFromInput reads an explicit redscope-scope line, otherwise hosts
// mentioned in the "In scope" section of the task text.
func ParseScopeFromInput(input string) Scope {
	if m := scopeMarkerRE.FindStringSubmatch(input); len(m) == 2 {
		return Scope{Domains: parseDomainList(m[1])}
	}
	section := input
	if m := inScopeHeadRE.FindStringSubmatch(input); len(m) == 2 {
		section = m[1]
		if len(section) > 2500 {
			section = section[:2500]
		}
	} else {
		return Scope{}
	}
	return Scope{Domains: parseDomainList(section)}
}

func parseDomainList(raw string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(h string) {
		h = canonicalScopeHost(h)
		if h == "" {
			return
		}
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	}) {
		part = strings.Trim(part, "[]()<>\"'")
		if strings.Contains(part, "://") {
			continue
		}
		add(part)
	}
	for _, rawURL := range urlRE.FindAllString(raw, -1) {
		u, err := url.Parse(strings.TrimRight(rawURL, ".,;:)]}\"'>"))
		if err == nil {
			add(u.Hostname())
		}
	}
	return out
}

func canonicalScopeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "www.")
	h = strings.Trim(h, ".")
	if h == "" || strings.ContainsAny(h, "/:\\ ") {
		return ""
	}
	if net.ParseIP(h) != nil {
		return h
	}
	if !strings.Contains(h, ".") || len(h) < 3 {
		return ""
	}
	return h
}

func NormalizeScope(s Scope) Scope {
	var cidrs []string
	for _, c := range s.CIDRs {
		c = strings.TrimSpace(c)
		if c != "" {
			cidrs = append(cidrs, c)
		}
	}
	return Scope{Domains: parseDomainList(strings.Join(s.Domains, ",")), CIDRs: cidrs}
}

func ClassifyHost(scope Scope, host string) Decision {
	if scope.Empty() {
		return DecisionIn
	}
	host = canonicalScopeHost(host)
	if host == "" {
		return DecisionSkip
	}
	if ip := net.ParseIP(host); ip != nil {
		for _, d := range scope.Domains {
			if host == canonicalScopeHost(d) {
				return DecisionIn
			}
		}
		for _, cidr := range scope.CIDRs {
			_, n, err := net.ParseCIDR(strings.TrimSpace(cidr))
			if err == nil && n.Contains(ip) {
				return DecisionIn
			}
		}
		return DecisionSkip
	}
	for _, d := range scope.Domains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return DecisionIn
		}
	}
	if similarToScope(scope, host) {
		return DecisionGrey
	}
	return DecisionSkip
}

func ClassifyCandidate(scope Scope, c Candidate) Decision {
	if scope.Empty() {
		return DecisionIn
	}
	switch c.Type {
	case EntityTypeHost:
		return ClassifyHost(scope, strings.TrimPrefix(c.Key, "host:"))
	case EntityTypeEndpoint, EntityTypeService:
		host, _ := c.Properties["host"].(string)
		if host == "" {
			return DecisionSkip
		}
		return ClassifyHost(scope, host)
	default:
		return DecisionSkip
	}
}

func similarToScope(scope Scope, host string) bool {
	labels := strings.Split(host, ".")
	needles := scopeNeedles(scope)
	for _, label := range labels {
		for _, n := range needles {
			if label == n || strings.Contains(label, n) {
				return true
			}
		}
	}
	return false
}

func scopeNeedles(scope Scope) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, d := range scope.Domains {
		parts := strings.Split(d, ".")
		if len(parts) < 2 {
			continue
		}
		sld := parts[len(parts)-2]
		if len(sld) < 3 {
			continue
		}
		if _, ok := seen[sld]; ok {
			continue
		}
		seen[sld] = struct{}{}
		out = append(out, sld)
	}
	return out
}
