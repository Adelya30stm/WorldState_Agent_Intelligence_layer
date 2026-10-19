package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"pentagi/pkg/database"
	"pentagi/pkg/graphiti"
	"pentagi/pkg/server/logger"
	"pentagi/pkg/server/models"
	"pentagi/pkg/server/response"
	"pentagi/pkg/worldstate"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
)

// graphitiSearcher is the subset of graphiti.Client used by WorldStateService and AttackPathService.
type graphitiSearcher interface {
	IsEnabled() bool
	DiverseResultsSearch(ctx context.Context, req graphiti.DiverseSearchRequest) (*graphiti.DiverseSearchResponse, error)
	EntityByLabelSearch(ctx context.Context, req graphiti.EntityByLabelSearchRequest) (*graphiti.EntityByLabelSearchResponse, error)
	EntityRelationshipsSearch(ctx context.Context, req graphiti.EntityRelationshipSearchRequest) (*graphiti.EntityRelationshipSearchResponse, error)
}

// ─── Response types ───────────────────────────────────────────────────────────

type worldStateEntity struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Label            string            `json:"label"`
	Status           string            `json:"status,omitempty"`
	Metadata         map[string]string `json:"metadata"`
	RiskLevel        string            `json:"riskLevel"`
	AvailableActions []string          `json:"availableActions"`
}

type worldStateLink struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label,omitempty"`
	Type   string `json:"type"`
}

type worldStateResponse struct {
	Version   int                `json:"version"`
	FlowID    uint64             `json:"flowId"`
	UpdatedAt time.Time          `json:"updatedAt"`
	Entities  []worldStateEntity `json:"entities"`
	Links     []worldStateLink   `json:"links"`
}

// ─── Entity action definitions ────────────────────────────────────────────────

var entityActions = map[string][]string{
	"flow":          {"add-note"},
	"task":          {"add-note", "create-subflow"},
	"subtask":       {"add-note"},
	"tool":          {"add-note"},
	"domain":        {"safe-probe", "deep-scan", "enumerate-endpoints", "mark-high-priority", "add-note", "create-subflow"},
	"endpoint":      {"safe-probe", "deep-scan", "mark-high-priority", "add-note", "create-subflow"},
	"finding":       {"mark-high-priority", "add-note", "create-subflow"},
	"target":        {"safe-probe", "deep-scan", "enumerate-endpoints", "mark-high-priority", "add-note", "create-subflow"},
	"vulnerability": {"mark-high-priority", "add-note", "create-subflow"},
	"threat":        {"add-note", "create-subflow"},
}

// ─── Extraction helpers ───────────────────────────────────────────────────────

var (
	domainRE = regexp.MustCompile(`\b([a-zA-Z0-9](?:[a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z]{2,})+)\b`)
	ipv4RE   = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
	urlRE    = regexp.MustCompile(`https?://[^\s'"` + "`" + `,<>)]+`)
)

var noiseDomains = map[string]bool{
	"localhost": true, "example.com": true, "github.com": true,
	"npmjs.com": true, "docker.io": true, "golang.org": true,
	"pkg.go.dev": true, "ubuntu.com": true, "debian.org": true,
}

// Code method/attribute names that match the domain regex but are not real TLDs
var codeSuffixes = map[string]bool{
	"get": true, "set": true, "put": true, "delete": true, "post": true, "patch": true,
	"now": true, "utc": true, "parse": true, "format": true, "encode": true, "decode": true,
	"resolve": true, "resolver": true, "lookup": true, "query": true, "fetch": true,
	"insert": true, "append": true, "extend": true, "update": true, "remove": true,
	"open": true, "close": true, "read": true, "write": true, "load": true, "dump": true,
	"url": true, "path": true, "text": true, "json": true, "xml": true, "data": true,
	"status": true, "headers": true, "content": true, "body": true, "params": true,
	"error": true, "result": true, "response": true, "request": true, "handler": true,
	"method": true, "fields": true, "values": true, "items": true,
	"preference": true, "exchange": true, "gethostbyaddr": true, "getaddrinfo": true,
	"tool": true, "utils": true, "helper": true, "base": true, "core": true,
	"stdout": true, "stderr": true, "stdin": true,
}

func isLikelyCodeRef(candidate string) bool {
	parts := strings.Split(candidate, ".")
	tld := strings.ToLower(parts[len(parts)-1])
	// TLDs > 8 chars are almost certainly code identifiers
	if len(tld) > 8 {
		return true
	}
	// Known code method/attribute names
	if codeSuffixes[tld] {
		return true
	}
	// More than 3 dot-separated segments is usually code (e.g. dns.resolver.resolve)
	if len(parts) > 3 {
		return true
	}
	// Underscores never appear in valid hostnames
	if strings.Contains(candidate, "_") {
		return true
	}
	return false
}

var toolPatterns = []struct {
	name string
	re   *regexp.Regexp
	risk string
}{
	{"nmap", regexp.MustCompile(`\bnmap\b`), "high"},
	{"gobuster", regexp.MustCompile(`\bgobuster\b`), "medium"},
	{"ffuf", regexp.MustCompile(`\bffuf\b`), "medium"},
	{"sqlmap", regexp.MustCompile(`\bsqlmap\b`), "critical"},
	{"nikto", regexp.MustCompile(`\bnikto\b`), "high"},
	{"hydra", regexp.MustCompile(`\bhydra\b`), "critical"},
	{"metasploit", regexp.MustCompile(`msfconsole|msfvenom`), "critical"},
	{"nuclei", regexp.MustCompile(`\bnuclei\b`), "high"},
	{"amass", regexp.MustCompile(`\bamass\b`), "low"},
	{"httpx", regexp.MustCompile(`\bhttpx\b`), "low"},
	{"sublist3r", regexp.MustCompile(`\bsublist3r\b`), "low"},
	{"curl", regexp.MustCompile(`\bcurl\b`), "low"},
	{"wget", regexp.MustCompile(`\bwget\b`), "low"},
	{"wapiti", regexp.MustCompile(`\bwapiti\b`), "high"},
	{"dirsearch", regexp.MustCompile(`\bdirsearch\b`), "medium"},
}

func slugify(s string) string {
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = strings.ToLower(s)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func extractDomains(text string) []string {
	seen := map[string]bool{}
	var out []string

	for _, m := range domainRE.FindAllStringSubmatch(text, -1) {
		v := strings.ToLower(m[1])
		if !noiseDomains[v] && strings.Contains(v, ".") && !isLikelyCodeRef(v) && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, m := range ipv4RE.FindAllStringSubmatch(text, -1) {
		v := m[1]
		if !strings.HasPrefix(v, "127.") && !strings.HasPrefix(v, "0.") && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func extractURLs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range urlRE.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".,'\"`")
		if len(u) > 12 && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

func detectTool(cmd string) (name, risk string) {
	for _, p := range toolPatterns {
		if p.re.MatchString(cmd) {
			return p.name, p.risk
		}
	}
	return "", ""
}

// ─── Service ──────────────────────────────────────────────────────────────────

type WorldStateService struct {
	db       *gorm.DB
	graphiti graphitiSearcher
}

func NewWorldStateService(db *gorm.DB, graphitiClient graphitiSearcher) *WorldStateService {
	return &WorldStateService{db: db, graphiti: graphitiClient}
}

// GetModel returns the persisted object/check/run model for a flow. Legacy flows
// return their schema version without fabricating checks from old graph data.
// @Summary Get persisted World State objects, checks and runs
// @Tags WorldState
// @Produce json
// @Security BearerAuth
// @Param flowID path int true "flow id" minimum(1)
// @Success 200 {object} response.successResp{data=worldstate.ModelSnapshot}
// @Failure 403 {object} response.errorResp
// @Failure 404 {object} response.errorResp
// @Failure 500 {object} response.errorResp
// @Router /flows/{flowID}/worldstate/model [get]
func (s *WorldStateService) GetModel(c *gin.Context) {
	flowID, err := strconv.ParseInt(c.Param("flowID"), 10, 64)
	if err != nil || flowID <= 0 {
		response.Error(c, response.ErrFlowsInvalidRequest, err)
		return
	}

	uid := c.GetUint64("uid")
	privs := c.GetStringSlice("prm")
	var flow models.Flow
	var scope func(db *gorm.DB) *gorm.DB
	if slices.Contains(privs, "flows.admin") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ?", flowID) }
	} else if slices.Contains(privs, "flows.view") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ? AND user_id = ?", flowID, uid) }
	} else {
		response.Error(c, response.ErrNotPermitted, nil)
		return
	}
	if err = s.db.Model(&flow).Scopes(scope).Take(&flow).Error; err != nil {
		if gorm.IsRecordNotFoundError(err) {
			response.Error(c, response.ErrFlowsNotFound, err)
		} else {
			response.Error(c, response.ErrInternal, err)
		}
		return
	}

	queries := database.New(s.db.DB())
	version, err := worldstate.ModelVersion(c.Request.Context(), queries, flowID)
	if err != nil {
		logger.FromContext(c).WithError(err).Error("world state schema version query failed")
		response.Error(c, response.ErrInternal, err)
		return
	}
	if version == 1 {
		response.Success(c, http.StatusOK, worldstate.ModelSnapshot{
			SchemaVersion: version, FlowID: flowID, Objects: []worldstate.Object{},
		})
		return
	}

	snapshot, err := worldstate.NewModelStore(queries).ReadSnapshot(c.Request.Context(), flowID)
	if err != nil {
		logger.FromContext(c).WithError(err).Error("world state model query failed")
		response.Error(c, response.ErrInternal, err)
		return
	}
	response.Success(c, http.StatusOK, snapshot)
}

// GetWorldState aggregates flow data into a normalized World State
// @Summary Get World State for a flow
// @Tags WorldState
// @Produce json
// @Security BearerAuth
// @Param flowID path int true "flow id" minimum(0)
// @Success 200 {object} response.successResp{data=worldStateResponse} "world state received successfully"
// @Failure 403 {object} response.errorResp "not permitted"
// @Failure 404 {object} response.errorResp "flow not found"
// @Failure 500 {object} response.errorResp "internal error"
// @Router /flows/{flowID}/worldstate [get]
func (s *WorldStateService) GetWorldState(c *gin.Context) {
	flowID, err := strconv.ParseUint(c.Param("flowID"), 10, 64)
	if err != nil {
		response.Error(c, response.ErrFlowsInvalidRequest, err)
		return
	}

	uid := c.GetUint64("uid")
	privs := c.GetStringSlice("prm")

	var flow models.Flow
	var scope func(db *gorm.DB) *gorm.DB
	if slices.Contains(privs, "flows.admin") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ?", flowID) }
	} else if slices.Contains(privs, "flows.view") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ? AND user_id = ?", flowID, uid) }
	} else {
		response.Error(c, response.ErrNotPermitted, nil)
		return
	}

	if err = s.db.Model(&flow).Scopes(scope).Take(&flow).Error; err != nil {
		if gorm.IsRecordNotFoundError(err) {
			response.Error(c, response.ErrFlowsNotFound, err)
		} else {
			response.Error(c, response.ErrInternal, err)
		}
		return
	}

	// Fetch tasks
	var tasks []models.Task
	if err = s.db.Where("flow_id = ?", flowID).Find(&tasks).Error; err != nil {
		logger.FromContext(c).WithError(err).Errorf("error fetching tasks for world state")
		response.Error(c, response.ErrInternal, err)
		return
	}

	// Fetch subtasks (join through tasks to scope by flowID)
	var subtasks []models.Subtask
	if err = s.db.
		Joins("INNER JOIN tasks t ON t.id = subtasks.task_id").
		Where("t.flow_id = ?", flowID).
		Find(&subtasks).Error; err != nil {
		logger.FromContext(c).WithError(err).Errorf("error fetching subtasks for world state")
		response.Error(c, response.ErrInternal, err)
		return
	}

	// Fetch stdin terminal logs
	var termlogs []models.Termlog
	if err = s.db.Where("flow_id = ? AND type = ?", flowID, models.TermlogTypeStdin).
		Limit(500).Find(&termlogs).Error; err != nil {
		logger.FromContext(c).WithError(err).Errorf("error fetching termlogs for world state")
		response.Error(c, response.ErrInternal, err)
		return
	}

	// ── Build world state ─────────────────────────────────────────────────────
	entities := []worldStateEntity{}
	links := []worldStateLink{}
	seenIDs := map[string]bool{}
	seenDomains := map[string]bool{}
	seenURLs := map[string]bool{}
	seenTools := map[string]bool{}

	addEntity := func(e worldStateEntity) {
		if !seenIDs[e.ID] {
			seenIDs[e.ID] = true
			entities = append(entities, e)
		}
	}

	// Flow entity
	flowEntityID := "flow-" + strconv.FormatUint(flowID, 10)
	addEntity(worldStateEntity{
		ID:               flowEntityID,
		Type:             "flow",
		Label:            flow.Title,
		Status:           string(flow.Status),
		Metadata:         map[string]string{"flowId": strconv.FormatUint(flowID, 10)},
		RiskLevel:        "none",
		AvailableActions: entityActions["flow"],
	})

	// Tasks
	for _, task := range tasks {
		taskID := "task-" + strconv.FormatUint(task.ID, 10)
		addEntity(worldStateEntity{
			ID:               taskID,
			Type:             "task",
			Label:            task.Title,
			Status:           string(task.Status),
			Metadata:         map[string]string{"taskId": strconv.FormatUint(task.ID, 10)},
			RiskLevel:        "none",
			AvailableActions: entityActions["task"],
		})
		links = append(links, worldStateLink{
			ID:     "fl-tk-" + strconv.FormatUint(task.ID, 10),
			Source: flowEntityID,
			Target: taskID,
			Type:   "contains",
		})
	}

	// Subtasks
	for _, st := range subtasks {
		stID := "subtask-" + strconv.FormatUint(st.ID, 10)
		parentID := "task-" + strconv.FormatUint(st.TaskID, 10)
		addEntity(worldStateEntity{
			ID:               stID,
			Type:             "subtask",
			Label:            st.Title,
			Status:           string(st.Status),
			Metadata:         map[string]string{"subtaskId": strconv.FormatUint(st.ID, 10)},
			RiskLevel:        "none",
			AvailableActions: entityActions["subtask"],
		})
		if seenIDs[parentID] {
			links = append(links, worldStateLink{
				ID:     "tk-st-" + strconv.FormatUint(st.ID, 10),
				Source: parentID,
				Target: stID,
				Type:   "contains",
			})
		}
	}

	// Terminal logs → tools + domains + endpoints
	for _, log := range termlogs {
		cmd := strings.TrimSpace(log.Text)
		if len(cmd) < 3 {
			continue
		}

		toolName, toolRisk := detectTool(cmd)
		var toolNodeID string
		if toolName != "" && !seenTools[toolName] {
			seenTools[toolName] = true
			toolNodeID = "tool-" + toolName
			addEntity(worldStateEntity{
				ID:               toolNodeID,
				Type:             "tool",
				Label:            toolName,
				Metadata:         map[string]string{"tool": toolName},
				RiskLevel:        toolRisk,
				AvailableActions: entityActions["tool"],
			})
		} else if toolName != "" {
			toolNodeID = "tool-" + toolName
		}

		for _, domain := range extractDomains(cmd) {
			if seenDomains[domain] {
				continue
			}
			seenDomains[domain] = true
			domID := "domain-" + slugify(domain)
			addEntity(worldStateEntity{
				ID:               domID,
				Type:             "domain",
				Label:            domain,
				Metadata:         map[string]string{"domain": domain},
				RiskLevel:        "medium",
				AvailableActions: entityActions["domain"],
			})
			if toolNodeID != "" {
				links = append(links, worldStateLink{
					ID:     "tl-dm-" + toolName + "-" + slugify(domain),
					Source: toolNodeID,
					Target: domID,
					Type:   "discovered",
				})
			}
		}

		for _, u := range extractURLs(cmd) {
			epID := "endpoint-" + slugify(u)
			if seenURLs[epID] {
				continue
			}
			seenURLs[epID] = true
			label := u
			if len(label) > 60 {
				label = label[:57] + "…"
			}
			addEntity(worldStateEntity{
				ID:               epID,
				Type:             "endpoint",
				Label:            label,
				Metadata:         map[string]string{"url": u},
				RiskLevel:        "medium",
				AvailableActions: entityActions["endpoint"],
			})
		}
	}

	// ── Neo4j/Graphiti entities ───────────────────────────────────────────────
	if s.graphiti != nil && s.graphiti.IsEnabled() {
		groupID := fmt.Sprintf("flow-%d", flowID)
		log := logger.FromContext(c)

		// Search for pentest-relevant labeled nodes only.
		// We run separate searches per label family and merge by UUID.
		labelFamilies := [][]string{
			{"Target"},
			{"Vulnerability"},
			{"AttackTechnique"},
			{"TechnicalFinding"},
		}
		for _, labels := range labelFamilies {
			resp, err := s.graphiti.EntityByLabelSearch(c.Request.Context(), graphiti.EntityByLabelSearchRequest{
				Query:      "target host endpoint vulnerability attack threat finding",
				GroupID:    &groupID,
				NodeLabels: labels,
				MaxResults: 40,
			})
			if err != nil {
				log.WithError(err).Warnf("graphiti label search failed for %v", labels)
				continue
			}
			for _, node := range resp.Nodes {
				entityID := "neo4j-" + node.UUID
				if seenIDs[entityID] {
					continue
				}
				// Skip agent artifacts and tool/file noise.
				if isNoisyNeo4jNode(node.Name, node.Labels) {
					continue
				}
				seenIDs[entityID] = true
				etype := neo4jLabelToType(node.Labels)
				entities = append(entities, worldStateEntity{
					ID:    entityID,
					Type:  etype,
					Label: node.Name,
					Metadata: map[string]string{
						"uuid":    node.UUID,
						"summary": node.Summary,
						"labels":  strings.Join(node.Labels, ","),
						"source":  "neo4j",
					},
					RiskLevel:        riskFromNeo4jLabels(node.Labels, node.Name),
					AvailableActions: entityActions[etype],
				})
			}
			for _, edge := range resp.Edges {
				srcID := "neo4j-" + edge.SourceNodeUUID
				tgtID := "neo4j-" + edge.TargetNodeUUID
				links = append(links, worldStateLink{
					ID:     "neo4j-edge-" + edge.UUID,
					Source: srcID,
					Target: tgtID,
					Label:  edge.Name,
					Type:   "references",
				})
			}
		}
	}

	resp := worldStateResponse{
		Version:   1,
		FlowID:    flowID,
		UpdatedAt: time.Now(),
		Entities:  entities,
		Links:     links,
	}

	response.Success(c, http.StatusOK, resp)
}

// ─── Neo4j classification helpers ─────────────────────────────────────────────

var (
	neo4jFindingKW  = []string{"vulnerability", "vuln", "cve-", "sqli", "xss", "rce", "injection", "exploit", "leak", "exposed", "finding"}
	neo4jDomainKW   = []string{"domain", "host", "target", "ip", "server", "network", "dns", "cdn", "subdomain"}
	neo4jEndpointKW = []string{"/api/", "/v1/", "/v2/", "https://", "http://", "endpoint", "url", "path", "route"}
	neo4jToolKW     = []string{"tool", "framework", "stride", "mitre", "owasp", "scanner", "nmap", "nuclei", "burp"}

	// Agent/planning artifacts to filter out from the pentest view.
	agentArtifactKW = []string{
		"subtask", "sub_task", " agent", "pen-test team", "pen test team",
		"it ops", "planner", "refiner", "coder", "executor",
		"primary_agent", "simple agent", "generator agent",
		"business/context summary",
	}

	// File/tool paths that indicate internal agent artifacts, not real targets.
	noisyPathPrefixes = []string{
		"/usr/share/", "/root/", "/work/", "/tmp/", "/home/",
		"tech_fingerprint", "inventory.csv", "wordlist",
	}
)

func containsAny(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// neo4jLabelToType maps real Neo4j labels (set by Graphiti) to our entity type taxonomy.
func neo4jLabelToType(labels []string) string {
	for _, l := range labels {
		switch l {
		case "Vulnerability":
			return "vulnerability"
		case "AttackTechnique":
			return "threat"
		case "TechnicalFinding":
			return "finding"
		case "Target":
			return "target"
		}
	}
	return "domain"
}

// isNoisyNeo4jNode returns true for agent-internal nodes that should not appear
// in the pentest-facing World State view.
func isNoisyNeo4jNode(name string, labels []string) bool {
	lower := strings.ToLower(name)
	// Skip internal tool/test-phase labels.
	for _, l := range labels {
		if l == "Tool" || l == "TestPhase" || l == "DatabaseMetadata" {
			return true
		}
	}
	// Skip agent artifact names.
	if containsAny(lower, agentArtifactKW) {
		return true
	}
	// Skip file paths, tool scripts, byte-count artifacts.
	for _, p := range noisyPathPrefixes {
		if strings.HasPrefix(lower, strings.ToLower(p)) {
			return true
		}
	}
	// Skip pure numeric / size artifacts (e.g. "6037 remote objects", "666.00 MiB transferred").
	if len(name) > 0 && (name[0] >= '0' && name[0] <= '9') {
		return true
	}
	// Skip very long names that are clearly not human-readable entity names.
	if len(name) > 120 {
		return true
	}
	return false
}

func riskFromNeo4jLabels(labels []string, name string) string {
	// Label-based risk first.
	for _, l := range labels {
		switch l {
		case "Vulnerability":
			// Check name for severity hints.
			combined := strings.ToLower(name)
			if containsAny(combined, []string{"rce", "remote code", "account takeover", "critical"}) {
				return "critical"
			}
			if containsAny(combined, []string{"sqli", "sql injection", "xss", "ssrf", "idor", "high"}) {
				return "high"
			}
			return "medium"
		case "AttackTechnique":
			combined := strings.ToLower(name)
			if containsAny(combined, []string{"credential stuffing", "brute", "injection", "rce"}) {
				return "high"
			}
			return "medium"
		case "TechnicalFinding":
			return "low"
		case "Target":
			return "none"
		}
	}
	combined := strings.ToLower(name)
	if containsAny(combined, []string{"critical", "rce", "remote code"}) {
		return "critical"
	}
	if containsAny(combined, []string{"high", "sqli", "xss", "ssrf", "idor"}) {
		return "high"
	}
	if containsAny(combined, []string{"medium", "vulnerability", "vuln", "cve-"}) {
		return "medium"
	}
	return "none"
}

// ─── Persisted lifecycle (PG world_state_*) ───────────────────────────────────

type lifecycleEntity struct {
	ID         int64           `json:"id"`
	EntityKey  string          `json:"entityKey"`
	Type       string          `json:"type"`
	State      string          `json:"state"`
	Properties json.RawMessage `json:"properties"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

type lifecycleTransition struct {
	ID        int64           `json:"id"`
	EntityID  int64           `json:"entityId"`
	EntityKey string          `json:"entityKey"`
	FromState string          `json:"fromState"`
	ToState   string          `json:"toState"`
	Agent     string          `json:"agent"`
	Evidence  json.RawMessage `json:"evidence"`
	CreatedAt time.Time       `json:"createdAt"`
}

type lifecycleResponse struct {
	FlowID      uint64                `json:"flowId"`
	Entities    []lifecycleEntity     `json:"entities"`
	Transitions []lifecycleTransition `json:"transitions"`
	Snapshot    string                `json:"snapshot"`
	Counts      map[string]int        `json:"counts"`
}

// GetLifecycle returns persisted World State entities + transition audit trail.
// @Summary Get persisted World State lifecycle
// @Tags WorldState
// @Produce json
// @Security BearerAuth
// @Param flowID path int true "flow id" minimum(0)
// @Success 200 {object} response.successResp{data=lifecycleResponse}
// @Failure 403 {object} response.errorResp
// @Failure 404 {object} response.errorResp
// @Router /flows/{flowID}/worldstate/lifecycle [get]
func (s *WorldStateService) GetLifecycle(c *gin.Context) {
	flowID, err := strconv.ParseUint(c.Param("flowID"), 10, 64)
	if err != nil {
		response.Error(c, response.ErrFlowsInvalidRequest, err)
		return
	}

	uid := c.GetUint64("uid")
	privs := c.GetStringSlice("prm")

	var flow models.Flow
	var scope func(db *gorm.DB) *gorm.DB
	if slices.Contains(privs, "flows.admin") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ?", flowID) }
	} else if slices.Contains(privs, "flows.view") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ? AND user_id = ?", flowID, uid) }
	} else {
		response.Error(c, response.ErrNotPermitted, nil)
		return
	}

	if err = s.db.Model(&flow).Scopes(scope).Take(&flow).Error; err != nil {
		if gorm.IsRecordNotFoundError(err) {
			response.Error(c, response.ErrFlowsNotFound, err)
		} else {
			response.Error(c, response.ErrInternal, err)
		}
		return
	}

	type entRow struct {
		ID         int64           `gorm:"column:id"`
		EntityKey  string          `gorm:"column:entity_key"`
		Type       string          `gorm:"column:type"`
		State      string          `gorm:"column:state"`
		Properties json.RawMessage `gorm:"column:properties"`
		UpdatedAt  time.Time       `gorm:"column:updated_at"`
	}
	var rows []entRow
	if err = s.db.Raw(`
		SELECT id, entity_key, type, state::text AS state, properties, updated_at
		FROM world_state_entities WHERE flow_id = ? ORDER BY updated_at DESC`, flowID).
		Scan(&rows).Error; err != nil {
		logger.FromContext(c).WithError(err).Error("lifecycle entities query failed")
		response.Error(c, response.ErrInternal, err)
		return
	}
	entities := make([]lifecycleEntity, 0, len(rows))
	counts := map[string]int{}
	for _, r := range rows {
		entities = append(entities, lifecycleEntity{
			ID: r.ID, EntityKey: r.EntityKey, Type: r.Type, State: r.State,
			Properties: r.Properties, UpdatedAt: r.UpdatedAt,
		})
		counts[r.State]++
	}

	type trRow struct {
		ID        int64           `gorm:"column:id"`
		EntityID  int64           `gorm:"column:entity_id"`
		EntityKey string          `gorm:"column:entity_key"`
		FromState string          `gorm:"column:from_state"`
		ToState   string          `gorm:"column:to_state"`
		Agent     string          `gorm:"column:agent"`
		Evidence  json.RawMessage `gorm:"column:evidence"`
		CreatedAt time.Time       `gorm:"column:created_at"`
	}
	var trows []trRow
	if err = s.db.Raw(`
		SELECT t.id, t.entity_id, e.entity_key,
		       t.from_state::text AS from_state, t.to_state::text AS to_state,
		       t.agent, t.evidence, t.created_at
		FROM world_state_transitions t
		JOIN world_state_entities e ON e.id = t.entity_id
		WHERE e.flow_id = ?
		ORDER BY t.created_at DESC
		LIMIT 100`, flowID).Scan(&trows).Error; err != nil {
		logger.FromContext(c).WithError(err).Error("lifecycle transitions query failed")
		response.Error(c, response.ErrInternal, err)
		return
	}
	transitions := make([]lifecycleTransition, 0, len(trows))
	for _, r := range trows {
		transitions = append(transitions, lifecycleTransition{
			ID: r.ID, EntityID: r.EntityID, EntityKey: r.EntityKey,
			FromState: r.FromState, ToState: r.ToState, Agent: r.Agent,
			Evidence: r.Evidence, CreatedAt: r.CreatedAt,
		})
	}

	snapshot := ""
	if sqlDB := s.db.DB(); sqlDB != nil {
		q := database.New(sqlDB)
		if proj, err := worldstate.BuildProjection(c.Request.Context(), q, int64(flowID)); err == nil {
			snapshot = proj.Text()
		}
	}

	response.Success(c, http.StatusOK, lifecycleResponse{
		FlowID:      flowID,
		Entities:    entities,
		Transitions: transitions,
		Snapshot:    snapshot,
		Counts:      counts,
	})
}

// ─── World State tool-call feed (agent read/write evidence) ───────────────────

type worldStateToolCall struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	Args      json.RawMessage `json:"args"`
	Result    string          `json:"result"`
	CreatedAt time.Time       `json:"createdAt"`
}

type toolCallsResponse struct {
	FlowID    uint64               `json:"flowId"`
	ToolCalls []worldStateToolCall `json:"toolCalls"`
}

// GetToolCalls returns recent world_state_query / world_state_update invocations
// so the UI can show, live, that agents are actually reading/writing World State.
// @Summary Get recent World State tool calls for a flow
// @Tags WorldState
// @Produce json
// @Security BearerAuth
// @Param flowID path int true "flow id" minimum(0)
// @Success 200 {object} response.successResp{data=toolCallsResponse}
// @Failure 403 {object} response.errorResp
// @Failure 404 {object} response.errorResp
// @Router /flows/{flowID}/worldstate/toolcalls [get]
func (s *WorldStateService) GetToolCalls(c *gin.Context) {
	flowID, err := strconv.ParseUint(c.Param("flowID"), 10, 64)
	if err != nil {
		response.Error(c, response.ErrFlowsInvalidRequest, err)
		return
	}

	uid := c.GetUint64("uid")
	privs := c.GetStringSlice("prm")

	var flow models.Flow
	var scope func(db *gorm.DB) *gorm.DB
	if slices.Contains(privs, "flows.admin") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ?", flowID) }
	} else if slices.Contains(privs, "flows.view") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ? AND user_id = ?", flowID, uid) }
	} else {
		response.Error(c, response.ErrNotPermitted, nil)
		return
	}

	if err = s.db.Model(&flow).Scopes(scope).Take(&flow).Error; err != nil {
		if gorm.IsRecordNotFoundError(err) {
			response.Error(c, response.ErrFlowsNotFound, err)
		} else {
			response.Error(c, response.ErrInternal, err)
		}
		return
	}

	type tcRow struct {
		ID        int64           `gorm:"column:id"`
		Name      string          `gorm:"column:name"`
		Status    string          `gorm:"column:status"`
		Args      json.RawMessage `gorm:"column:args"`
		Result    string          `gorm:"column:result"`
		CreatedAt time.Time       `gorm:"column:created_at"`
	}
	var rows []tcRow
	if err = s.db.Raw(`
		SELECT id, name, status::text AS status, args, result, created_at
		FROM toolcalls
		WHERE flow_id = ? AND name IN ('world_state_query', 'world_state_update')
		ORDER BY created_at DESC
		LIMIT 50`, flowID).Scan(&rows).Error; err != nil {
		logger.FromContext(c).WithError(err).Error("world state toolcalls query failed")
		response.Error(c, response.ErrInternal, err)
		return
	}

	calls := make([]worldStateToolCall, 0, len(rows))
	for _, r := range rows {
		args := r.Args
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		calls = append(calls, worldStateToolCall{
			ID: r.ID, Name: r.Name, Status: r.Status,
			Args: args, Result: r.Result, CreatedAt: r.CreatedAt,
		})
	}

	response.Success(c, http.StatusOK, toolCallsResponse{
		FlowID:    flowID,
		ToolCalls: calls,
	})
}
