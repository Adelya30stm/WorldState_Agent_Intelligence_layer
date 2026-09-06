package worldstate

import (
	"net/url"
	"regexp"
	"strings"
)

const (
	EntityTypeHost       = "host"
	EntityTypeEndpoint   = "endpoint"
	EntityTypeService    = "service"
	EntityTypeFinding    = "finding"
	EntityTypeCredential = "credential"
)

// Candidate is a deterministic extraction from tool output.
type Candidate struct {
	Type       string
	Key        string // canonical entity_key, e.g. host:example.com
	Properties map[string]any
}

var (
	urlRE           = regexp.MustCompile(`https?://[^\s'"` + "`" + `,<>)\]]+`)
	ipv4RE          = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
	hostRE          = regexp.MustCompile(`\b([a-zA-Z0-9](?:[a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z]{2,})+)\b`)
	cveRE           = regexp.MustCompile(`(?i)\bCVE-\d{4}-\d{4,7}\b`)
	passwordRE      = regexp.MustCompile(`(?i)(?:password|passwd|pwd)\s*[=:]\s*([^\s"'\\,]{3,64})`)
	authorizationRE = regexp.MustCompile(`(?i)\bAuthorization:\s*(Basic|Bearer|Digest|Token)\s+([A-Za-z0-9+/=._~-]+)`)
	cookieHeaderRE  = regexp.MustCompile(`(?i)\b(?:Cookie|Set-Cookie):\s*([^\r\n]+)`)
	secretFieldRE   = regexp.MustCompile(`(?i)\b(access[_-]?token|refresh[_-]?token|token|api[_-]?key|apikey|client[_-]?secret|private[_-]?key|authorization|auth|secret|cookie|session(?:[_-]?id)?)\s*[=:]\s*([^\s"'\\,;]{3,128})`)
	userPassRE      = regexp.MustCompile(`(?i)\b(?:user(?:name)?|login)\s*[=:]\s*([^\s"'\\,]{2,64}).{0,80}?(?:password|passwd|pwd)\s*[=:]\s*([^\s"'\\,]{3,64})`)
	nmapReportRE    = regexp.MustCompile(`(?i)^Nmap scan report for (.+)$`)
	nmapPortRE      = regexp.MustCompile(`(?i)^\s*(\d{1,5})/(tcp|udp)\s+(open(?:\|filtered)?)\s+(\S+)`)
	nxcServiceRE    = regexp.MustCompile(`(?im)^\s*(SMB|HTTP|HTTPS|SSH|FTP|RDP|LDAP|WINRM)\s+(\d{1,3}(?:\.\d{1,3}){3})\s+(\d{1,5})\b`)
)

var noiseHosts = map[string]bool{
	"localhost": true, "example.com": true, "github.com": true,
	"npmjs.com": true, "docker.io": true, "golang.org": true,
	"pkg.go.dev": true, "ubuntu.com": true, "debian.org": true,
	"symfony.com": true, "google.com": true, "googleapis.com": true,
	"w3.org": true, "schema.org": true, "microsoft.com": true,
	"cloudflare.com": true, "cdnjs.cloudflare.com": true,
	"googletagmanager.com": true, "gstatic.com": true,
	"jquery.com": true, "jqueryui.com": true, "api.jqueryui.com": true,
	"builtwith.com": true, "crt.sh": true, "github.io": true,
}

var noiseHostSuffixes = []string{
	".cloudfront.net", ".github.io", ".cloudflare.com",
}

var fileExtTLDs = map[string]bool{
	"js": true, "css": true, "html": true, "htm": true, "ico": true,
	"json": true, "map": true, "xml": true, "txt": true, "png": true,
	"jpg": true, "gif": true, "svg": true, "woff": true, "woff2": true,
	"ttf": true, "gz": true, "zip": true, "dat": true, "php": true,
}

var codeHostSuffixes = map[string]bool{
	"get": true, "set": true, "put": true, "delete": true, "post": true,
	"pathname": true, "replace": true, "write": true, "length": true,
	"push": true, "gtag": true, "dialog": true, "search": true,
	"indexof": true, "min": true, "now": true, "parse": true,
	"format": true, "encode": true, "decode": true, "split": true,
	"join": true, "resolve": true, "resolver": true, "lookup": true,
	"query": true, "fetch": true, "open": true, "close": true,
	"read": true, "url": true, "path": true, "text": true, "json": true,
	"data": true, "status": true, "headers": true, "content": true,
	"body": true, "params": true, "error": true, "result": true,
	"response": true, "request": true, "handler": true, "method": true,
}

// ExtractCandidates pulls hosts and endpoints from raw tool output.
// Deterministic only — no LLM.
func ExtractCandidates(toolName, result string) []Candidate {
	seen := map[string]bool{}
	out := make([]Candidate, 0, 8)
	safeResult := maskCredentialMaterial(result)

	add := func(c Candidate) {
		if c.Key == "" || seen[c.Key] {
			return
		}
		seen[c.Key] = true
		out = append(out, c)
	}

	for _, raw := range urlRE.FindAllString(safeResult, -1) {
		raw = strings.TrimRight(raw, ".,;:)]}\"'>")
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		host := canonicalHost(u.Hostname())
		if host == "" || noiseHosts[host] {
			continue
		}
		add(Candidate{
			Type: EntityTypeHost,
			Key:  "host:" + host,
			Properties: map[string]any{
				"host":   host,
				"source": "url",
			},
		})
		path := strings.TrimRight(u.Path, "/")
		if path == "" {
			path = "/"
		}
		epKey := "endpoint:" + strings.ToLower(u.Scheme) + "://" + host + path
		add(Candidate{
			Type: EntityTypeEndpoint,
			Key:  epKey,
			Properties: map[string]any{
				"url":    strings.ToLower(u.Scheme) + "://" + host + path,
				"host":   host,
				"path":   path,
				"scheme": u.Scheme,
			},
		})
	}

	for _, ip := range ipv4RE.FindAllString(safeResult, -1) {
		if isNoiseIP(ip) {
			continue
		}
		add(Candidate{
			Type: EntityTypeHost,
			Key:  "host:" + ip,
			Properties: map[string]any{
				"host":   ip,
				"source": "ipv4",
			},
		})
	}

	extractNmap(safeResult, add)
	extractNXC(safeResult, add)

	// Bare hostnames (skip if already covered via URL).
	if looksLikeReconTool(toolName) {
		for _, h := range hostRE.FindAllString(safeResult, -1) {
			host := canonicalHost(h)
			if host == "" || noiseHosts[host] {
				continue
			}
			add(Candidate{
				Type: EntityTypeHost,
				Key:  "host:" + host,
				Properties: map[string]any{
					"host":   host,
					"source": "hostname",
				},
			})
		}
	}

	for _, cve := range cveRE.FindAllString(safeResult, -1) {
		cve = strings.ToUpper(cve)
		add(Candidate{
			Type: EntityTypeFinding,
			Key:  "finding:" + strings.ToLower(cve),
			Properties: map[string]any{
				"cve":    cve,
				"source": "cve",
			},
		})
	}

	for _, m := range userPassRE.FindAllStringSubmatch(result, -1) {
		if len(m) < 3 {
			continue
		}
		user := strings.TrimSpace(m[1])
		pass := strings.TrimSpace(m[2])
		if user == "" || pass == "" || looksLikePlaceholder(pass) {
			continue
		}
		key := "credential:" + strings.ToLower(user)
		add(Candidate{
			Type: EntityTypeCredential,
			Key:  key,
			Properties: map[string]any{
				"username": user,
				"password": pass,
				"source":   "user_pass",
			},
		})
	}

	for _, m := range passwordRE.FindAllStringSubmatch(result, -1) {
		if len(m) < 2 {
			continue
		}
		pass := strings.TrimSpace(m[1])
		if pass == "" || looksLikePlaceholder(pass) {
			continue
		}
		add(Candidate{
			Type: EntityTypeCredential,
			Key:  credentialEntityKey("password"),
			Properties: map[string]any{
				"password": pass,
				"source":   "password_field",
			},
		})
	}

	for _, m := range authorizationRE.FindAllStringSubmatch(result, -1) {
		if len(m) < 3 {
			continue
		}
		scheme := strings.ToLower(strings.TrimSpace(m[1]))
		token := strings.TrimSpace(m[2])
		if token == "" {
			continue
		}
		add(Candidate{
			Type: EntityTypeCredential,
			Key:  credentialEntityKey(scheme),
			Properties: map[string]any{
				"auth":   scheme,
				"token":  token,
				"source": "authorization_header",
			},
		})
	}

	for _, m := range secretFieldRE.FindAllStringSubmatch(result, -1) {
		if len(m) < 3 {
			continue
		}
		kind := credentialFieldIdentity(m[1])
		value := strings.TrimSpace(m[2])
		if kind == "" || value == "" || looksLikePlaceholder(value) {
			continue
		}
		add(Candidate{
			Type: EntityTypeCredential,
			Key:  credentialEntityKey(kind),
			Properties: map[string]any{
				kind:     value,
				"source": "credential_field",
			},
		})
	}

	for _, m := range cookieHeaderRE.FindAllStringSubmatch(result, -1) {
		if len(m) < 2 {
			continue
		}
		cookie := strings.TrimSpace(m[1])
		if cookie == "" || looksLikePlaceholder(cookie) {
			continue
		}
		add(Candidate{
			Type: EntityTypeCredential,
			Key:  credentialEntityKey("cookie"),
			Properties: map[string]any{
				"cookie": cookie,
				"source": "cookie_header",
			},
		})
	}

	return out
}

func looksLikePlaceholder(s string) bool {
	l := strings.ToLower(s)
	for _, p := range []string{"***", "redacted", "null", "none", "password", "changeme", "{password}", "<password>"} {
		if l == p {
			return true
		}
	}
	return false
}

func credentialFieldIdentity(field string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(field), "-", "_"))
	switch normalized {
	case "access_token", "refresh_token", "token":
		return "token"
	case "api_key", "apikey":
		return "api_key"
	case "client_secret", "secret":
		return "secret"
	case "private_key":
		return "private"
	case "authorization", "auth":
		return "authorization"
	case "cookie":
		return "cookie"
	case "session_id", "session":
		return "session"
	default:
		return ""
	}
}

func maskCredentialMaterial(result string) string {
	masked := passwordRE.ReplaceAllString(result, "password=[REDACTED]")
	masked = authorizationRE.ReplaceAllString(masked, "$1 [REDACTED]")
	masked = secretFieldRE.ReplaceAllString(masked, "$1=[REDACTED]")
	masked = cookieHeaderRE.ReplaceAllString(masked, "Cookie: [REDACTED]")
	return masked
}

func extractNmap(result string, add func(Candidate)) {
	currentHost := ""
	for _, raw := range strings.Split(result, "\n") {
		line := strings.TrimSpace(raw)
		if m := nmapReportRE.FindStringSubmatch(line); len(m) == 2 {
			currentHost = parseNmapTarget(m[1])
			if currentHost == "" {
				continue
			}
			props := map[string]any{"host": currentHost, "source": "nmap"}
			if name := nmapHostname(m[1], currentHost); name != "" {
				props["hostname"] = name
			}
			add(Candidate{Type: EntityTypeHost, Key: "host:" + currentHost, Properties: props})
			continue
		}
		if currentHost == "" {
			continue
		}
		m := nmapPortRE.FindStringSubmatch(line)
		if len(m) != 5 {
			continue
		}
		port, proto, svc := m[1], strings.ToLower(m[2]), strings.ToLower(m[4])
		add(Candidate{
			Type: EntityTypeService,
			Key:  "service:" + currentHost + ":" + port + "/" + proto,
			Properties: map[string]any{
				"host":    currentHost,
				"port":    port,
				"proto":   proto,
				"service": svc,
				"source":  "nmap",
			},
		})
	}
}

func extractNXC(result string, add func(Candidate)) {
	for _, m := range nxcServiceRE.FindAllStringSubmatch(result, -1) {
		if len(m) < 4 {
			continue
		}
		svc, ip, port := strings.ToLower(m[1]), m[2], m[3]
		if isNoiseIP(ip) {
			continue
		}
		add(Candidate{
			Type: EntityTypeHost,
			Key:  "host:" + ip,
			Properties: map[string]any{
				"host":   ip,
				"source": "nxc",
			},
		})
		add(Candidate{
			Type: EntityTypeService,
			Key:  "service:" + ip + ":" + port + "/tcp",
			Properties: map[string]any{
				"host":    ip,
				"port":    port,
				"proto":   "tcp",
				"service": svc,
				"source":  "nxc",
			},
		})
	}
}

func parseNmapTarget(s string) string {
	s = strings.TrimSpace(s)
	if ip := ipv4RE.FindString(s); ip != "" && !isNoiseIP(ip) {
		return ip
	}
	if i := strings.Index(s, " ("); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	return canonicalScanHost(s)
}

func nmapHostname(raw, hostKey string) string {
	name := strings.TrimSpace(raw)
	if i := strings.Index(name, " ("); i > 0 {
		name = strings.TrimSpace(name[:i])
	}
	name = strings.ToLower(name)
	if name == "" || name == hostKey {
		return ""
	}
	return name
}

func canonicalHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(h, "www.")
	if strings.Contains(h, ":") {
		h = strings.Split(h, ":")[0]
	}
	if len(h) < 3 || !strings.Contains(h, ".") || !isPlausibleHost(h) {
		return ""
	}
	return h
}

func isPlausibleHost(h string) bool {
	if h == "" || noiseHosts[h] {
		return false
	}
	for _, suffix := range noiseHostSuffixes {
		if strings.HasSuffix(h, suffix) {
			return false
		}
	}
	if strings.Contains(h, "_") {
		return false
	}
	parts := strings.Split(h, ".")
	if len(parts) < 2 {
		return false
	}
	tld := parts[len(parts)-1]
	if len(tld) > 8 || fileExtTLDs[tld] || codeHostSuffixes[tld] {
		return false
	}
	return true
}

func canonicalScanHost(h string) string {
	if ip := ipv4RE.FindString(h); ip != "" && !isNoiseIP(ip) {
		return ip
	}
	if fqdn := canonicalHost(h); fqdn != "" && !noiseHosts[fqdn] {
		return fqdn
	}
	h = strings.ToLower(strings.TrimSpace(h))
	if len(h) < 2 || len(h) > 63 || strings.ContainsAny(h, " ./:@") {
		return ""
	}
	if noiseHosts[h] || nmapNoiseWords[h] {
		return ""
	}
	return h
}

var nmapNoiseWords = map[string]bool{
	"nmap": true, "scan": true, "report": true, "host": true, "port": true,
	"open": true, "tcp": true, "udp": true, "filtered": true, "closed": true,
	"unknown": true, "starting": true, "done": true, "latency": true,
}

func isNoiseIP(ip string) bool {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return true
	}
	octets := make([]int, 4)
	for i, p := range parts {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				return true
			}
			n = n*10 + int(c-'0')
			if n > 255 {
				return true
			}
		}
		if len(p) > 1 && p[0] == '0' {
			return true
		}
		octets[i] = n
	}
	switch octets[0] {
	case 0, 127, 255:
		return true
	}
	if octets[0] >= 224 {
		return true
	}
	return false
}

func looksLikeReconTool(name string) bool {
	n := strings.ToLower(name)
	for _, k := range []string{"terminal", "browser", "nmap", "gobuster", "ffuf", "nuclei", "curl", "whatweb", "http"} {
		if strings.Contains(n, k) {
			return true
		}
	}
	return false
}

func looksLikeActiveScan(toolName, result string) bool {
	blob := strings.ToLower(toolName + " " + result)
	for _, k := range []string{"nmap", "gobuster", "feroxbuster", "ffuf", "masscan", "nuclei", "dirb", "wfuzz"} {
		if strings.Contains(blob, k) {
			return true
		}
	}
	return false
}
