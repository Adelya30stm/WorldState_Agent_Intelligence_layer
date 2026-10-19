package worldstate

import (
	"strings"
	"testing"
)

func TestExtractCandidatesFromURL(t *testing.T) {
	result := `HTTP/2 200
location: https://dh91a5qq5somh28whqwy.testapp.site/web/login
x-powered-by: PHP/5.6.40
`
	cands := ExtractCandidates("terminal", result)
	if len(cands) == 0 {
		t.Fatal("expected candidates")
	}
	var sawHost, sawEP bool
	for _, c := range cands {
		if c.Key == "host:dh91a5qq5somh28whqwy.testapp.site" {
			sawHost = true
		}
		if c.Type == EntityTypeEndpoint && strings.Contains(c.Key, "dh91a5qq5somh28whqwy.testapp.site") {
			sawEP = true
		}
	}
	if !sawHost {
		t.Fatalf("missing host candidate: %+v", cands)
	}
	if !sawEP {
		t.Fatalf("missing endpoint candidate: %+v", cands)
	}
}

func TestExtractSkipsNoise(t *testing.T) {
	cands := ExtractCandidates("browser", "See docs at https://github.com/foo/bar and https://symfony.com/doc")
	for _, c := range cands {
		if c.Type == EntityTypeHost {
			t.Fatalf("unexpected host from noise domains: %s", c.Key)
		}
	}
}

func TestExtractLabNmapWritesHostAndService(t *testing.T) {
	result := `Nmap scan report for dc01 (10.0.0.5)
Host is up (0.001s latency).
PORT     STATE SERVICE
22/tcp   open  ssh
445/tcp  open  microsoft-ds
`
	cands := ExtractCandidates("terminal", result)
	want := map[string]bool{
		"host:10.0.0.5":            false,
		"service:10.0.0.5:22/tcp":  false,
		"service:10.0.0.5:445/tcp": false,
	}
	for _, c := range cands {
		if _, ok := want[c.Key]; ok {
			want[c.Key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Fatalf("missing %q in %+v", key, keys(cands))
		}
	}
}

func TestExtractPrivateIPIsAHost(t *testing.T) {
	cands := ExtractCandidates("terminal", "ping 192.168.56.10")
	saw := false
	for _, c := range cands {
		if c.Key == "host:192.168.56.10" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected RFC1918 host, got %+v", keys(cands))
	}
}

func TestExtractSkipsLoopback(t *testing.T) {
	cands := ExtractCandidates("terminal", "connected to 127.0.0.1")
	for _, c := range cands {
		if c.Type == EntityTypeHost {
			t.Fatalf("did not expect loopback host: %s", c.Key)
		}
	}
}

func TestExtractNXCSMB(t *testing.T) {
	cands := ExtractCandidates("terminal", "SMB         192.168.56.10    445    DC01    [*] signing:false")
	wantHost, wantSvc := false, false
	for _, c := range cands {
		if c.Key == "host:192.168.56.10" {
			wantHost = true
		}
		if c.Key == "service:192.168.56.10:445/tcp" && c.Properties["service"] == "smb" {
			wantSvc = true
		}
	}
	if !wantHost || !wantSvc {
		t.Fatalf("expected SMB host+service, got %+v", keys(cands))
	}
}

func keys(cands []Candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Key)
	}
	return out
}

func TestLooksLikeActiveScan(t *testing.T) {
	if !looksLikeActiveScan("terminal", "running nmap -sV target") {
		t.Fatal("expected active scan")
	}
	if looksLikeActiveScan("browser", "page title Login") {
		t.Fatal("did not expect active scan")
	}
}

func TestCredentialIdentifiersUseStableNonSecretKeys(t *testing.T) {
	result := `username=analyst password=synthetic-password-sentinel
token=synthetic-token-sentinel
api_key=synthetic-api-key-sentinel
Authorization: Bearer synthetic-bearer-sentinel
Cookie: session=synthetic-cookie-sentinel`
	candidates := ExtractCandidates("terminal", result)
	wantKeys := map[string]bool{
		"credential:analyst":  false,
		"credential:password": false,
		"credential:token":    false,
		"credential:api_key":  false,
		"credential:bearer":   false,
		"credential:cookie":   false,
	}
	for _, candidate := range candidates {
		if candidate.Type != EntityTypeCredential {
			continue
		}
		if strings.Contains(candidate.Key, "synthetic-") {
			t.Fatalf("credential key contains secret material")
		}
		if _, ok := wantKeys[candidate.Key]; ok {
			wantKeys[candidate.Key] = true
		}
	}
	for key, found := range wantKeys {
		if !found {
			t.Fatalf("missing stable credential identity %q", key)
		}
	}
}

func TestExtractSkipsKeywordFindingsAndCodeHosts(t *testing.T) {
	result := `Plan: Test for SQL injection, IDOR, RCE and authentication bypass.
See location.pathname, document.write, arguments.length, window.gtag,
index-ciwxgrft.js, favicon-16x16.ico, 404.html, next.js.
CDN: https://cdnjs.cloudflare.com/ajax/libs/fingerprintjs2/2.1.0/fingerprint2.min.js
Alive: https://demo.mokka.ru/ and https://backend.test.mokka.ru/
`
	cands := ExtractCandidates("terminal", result)
	sawDemo, sawBackend := false, false
	demoEndpoints := 0
	for _, c := range cands {
		switch {
		case strings.HasPrefix(c.Key, "finding:") && !strings.Contains(c.Key, "cve-"):
			t.Fatalf("keyword finding leaked: %s", c.Key)
		case c.Key == "host:location.pathname", c.Key == "host:document.write",
			c.Key == "host:arguments.length", c.Key == "host:window.gtag",
			c.Key == "host:index-ciwxgrft.js", c.Key == "host:favicon-16x16.ico",
			c.Key == "host:404.html", c.Key == "host:next.js",
			c.Key == "host:cdnjs.cloudflare.com":
			t.Fatalf("code/noise host leaked: %s", c.Key)
		case c.Key == "host:demo.mokka.ru":
			sawDemo = true
		case c.Key == "host:backend.test.mokka.ru":
			sawBackend = true
		}
		if c.Type == EntityTypeEndpoint && strings.Contains(c.Key, "demo.mokka.ru") {
			demoEndpoints++
		}
	}
	if !sawDemo || !sawBackend {
		t.Fatalf("expected mokka hosts, got %+v", keys(cands))
	}
	if demoEndpoints != 1 {
		t.Fatalf("expected one demo endpoint, got %d in %+v", demoEndpoints, keys(cands))
	}
}

func TestExtractKeepsCVEFindings(t *testing.T) {
	cands := ExtractCandidates("terminal", "Found CVE-2021-44228 on login")
	saw := false
	for _, c := range cands {
		if c.Key == "finding:cve-2021-44228" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected CVE finding, got %+v", keys(cands))
	}
}

func TestIngestionFailureFieldsSanitizeCredentialIdentifiers(t *testing.T) {
	fields := ingestionFailureFields(17, "credential:password:synthetic-password-sentinel", "terminal")
	if fields["entity_key"] != "credential:password" {
		t.Fatalf("unexpected ingestion entity key: %v", fields["entity_key"])
	}
}

func TestExtractSkipsInfrastructureNoise(t *testing.T) {
	result := `
Get: http://http.kali.org/kali pool/main
Hit: http://kali.download/kali
https://download.docker.com/linux/debian
https://discord.com/api
https://ogp.me/ns
https://ctlogs.dev/
https://us.mirror.ionos.com/linux/distributions/kali/kali
n.endswith css-j7o5z.fixed
`
	cands := ExtractCandidates("terminal", result)
	for _, c := range cands {
		switch {
		case strings.Contains(c.Key, "kali"), strings.Contains(c.Key, "docker.com"),
			strings.Contains(c.Key, "discord"), strings.Contains(c.Key, "ogp.me"),
			strings.Contains(c.Key, "ctlogs"), strings.Contains(c.Key, "ionos"),
			c.Key == "host:n.endswith", c.Key == "host:css-j7o5z.fixed":
			t.Fatalf("infra/code noise leaked: %s", c.Key)
		}
	}
}

func TestExtractStripsEscapedURLJunk(t *testing.T) {
	result := `{"url": "https://dh91a5qq5somh28whqwy.testapp.site/robots.txt.\\n"}`
	cands := ExtractCandidates("terminal", result)
	for _, c := range cands {
		if strings.ContainsAny(c.Key, "\\\n") {
			t.Fatalf("escaped junk in key: %q", c.Key)
		}
	}
}

func TestExtractSkipsRedactedCookieField(t *testing.T) {
	cands := ExtractCandidates("terminal", `cookie: [REDACTED]
cookie=[REDACTED]`)
	for _, c := range cands {
		if c.Type == EntityTypeCredential {
			t.Fatalf("redacted cookie became credential: %+v", c)
		}
	}
}

func TestExtractBrowserSkipsDockerBridgeIP(t *testing.T) {
	cands := ExtractCandidates("browser", `dial tcp 172.20.0.7:443: connect: connection refused`)
	for _, c := range cands {
		if c.Key == "host:172.20.0.7" {
			t.Fatalf("docker bridge IP ingested from browser: %+v", keys(cands))
		}
	}
}

func TestExtractTerminalKeepsLabDockerRangeIP(t *testing.T) {
	cands := ExtractCandidates("terminal", "ping 172.20.0.7")
	saw := false
	for _, c := range cands {
		if c.Key == "host:172.20.0.7" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected lab IP from terminal, got %+v", keys(cands))
	}
}

func TestSkipToolIngest(t *testing.T) {
	if !skipToolIngest("world_state_query") || !skipToolIngest("file") || !skipToolIngest("duckduckgo") {
		t.Fatal("expected query/file/search ingest to be skipped")
	}
	if skipToolIngest("terminal") || skipToolIngest("browser") {
		t.Fatal("expected terminal/browser ingest to remain enabled")
	}
}
