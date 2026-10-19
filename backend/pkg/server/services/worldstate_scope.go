package services

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"pentagi/pkg/database"
	"pentagi/pkg/server/logger"
	"pentagi/pkg/server/models"
	"pentagi/pkg/server/response"
	"pentagi/pkg/worldstate"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
)

type scopePayload struct {
	Domains []string `json:"domains"`
	CIDRs   []string `json:"cidrs"`
}

type scopeResponse struct {
	FlowID uint64       `json:"flowId"`
	Scope  scopePayload `json:"scope"`
}

type candidatePayload struct {
	ID         int64           `json:"id"`
	EntityKey  string          `json:"entityKey"`
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
	Reason     string          `json:"reason"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type candidatesResponse struct {
	FlowID     uint64             `json:"flowId"`
	Scope      scopePayload       `json:"scope"`
	Candidates []candidatePayload `json:"candidates"`
}

type candidateReviewRequest struct {
	Accept []string `json:"accept"`
	Reject []string `json:"reject"`
}

type candidateReviewResponse struct {
	Accepted int `json:"accepted"`
	Rejected int `json:"rejected"`
}

func (s *WorldStateService) sqlc() (*database.Queries, bool) {
	sqlDB := s.db.DB()
	if sqlDB == nil {
		return nil, false
	}
	return database.New(sqlDB), true
}

func (s *WorldStateService) permittedFlow(c *gin.Context, write bool) (uint64, bool) {
	flowID, err := strconv.ParseUint(c.Param("flowID"), 10, 64)
	if err != nil {
		response.Error(c, response.ErrFlowsInvalidRequest, err)
		return 0, false
	}
	uid := c.GetUint64("uid")
	privs := c.GetStringSlice("prm")

	var flow models.Flow
	var scope func(db *gorm.DB) *gorm.DB
	if slices.Contains(privs, "flows.admin") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ?", flowID) }
	} else if write && slices.Contains(privs, "flows.edit") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ? AND user_id = ?", flowID, uid) }
	} else if !write && slices.Contains(privs, "flows.view") {
		scope = func(db *gorm.DB) *gorm.DB { return db.Where("id = ? AND user_id = ?", flowID, uid) }
	} else {
		response.Error(c, response.ErrNotPermitted, nil)
		return 0, false
	}
	if err = s.db.Model(&flow).Scopes(scope).Take(&flow).Error; err != nil {
		if gorm.IsRecordNotFoundError(err) {
			response.Error(c, response.ErrFlowsNotFound, err)
		} else {
			response.Error(c, response.ErrInternal, err)
		}
		return 0, false
	}
	return flowID, true
}

func scopeToPayload(s worldstate.Scope) scopePayload {
	domains := s.Domains
	if domains == nil {
		domains = []string{}
	}
	cidrs := s.CIDRs
	if cidrs == nil {
		cidrs = []string{}
	}
	return scopePayload{Domains: domains, CIDRs: cidrs}
}

// GetScope returns the ingest allow-list for a flow.
// @Summary Get World State scope
// @Tags WorldState
// @Produce json
// @Security BearerAuth
// @Param flowID path int true "flow id" minimum(0)
// @Success 200 {object} response.successResp{data=scopeResponse}
// @Failure 403 {object} response.errorResp
// @Failure 404 {object} response.errorResp
// @Router /flows/{flowID}/worldstate/scope [get]
func (s *WorldStateService) GetScope(c *gin.Context) {
	flowID, ok := s.permittedFlow(c, false)
	if !ok {
		return
	}
	q, ok := s.sqlc()
	if !ok {
		response.Error(c, response.ErrInternal, nil)
		return
	}
	scope, err := worldstate.LoadScope(c.Request.Context(), q, int64(flowID))
	if err != nil {
		logger.FromContext(c).WithError(err).Error("load world state scope failed")
		response.Error(c, response.ErrInternal, err)
		return
	}
	response.Success(c, http.StatusOK, scopeResponse{FlowID: flowID, Scope: scopeToPayload(scope)})
}

// PutScope replaces the ingest allow-list for a flow.
// @Summary Set World State scope
// @Tags WorldState
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param flowID path int true "flow id" minimum(0)
// @Success 200 {object} response.successResp{data=scopeResponse}
// @Failure 403 {object} response.errorResp
// @Failure 404 {object} response.errorResp
// @Router /flows/{flowID}/worldstate/scope [put]
func (s *WorldStateService) PutScope(c *gin.Context) {
	flowID, ok := s.permittedFlow(c, true)
	if !ok {
		return
	}
	var body scopePayload
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, response.ErrFlowsInvalidRequest, err)
		return
	}
	q, ok := s.sqlc()
	if !ok {
		response.Error(c, response.ErrInternal, nil)
		return
	}
	scope := worldstate.NormalizeScope(worldstate.Scope{Domains: body.Domains, CIDRs: body.CIDRs})
	if err := worldstate.SaveScope(c.Request.Context(), q, int64(flowID), scope); err != nil {
		logger.FromContext(c).WithError(err).Error("save world state scope failed")
		response.Error(c, response.ErrInternal, err)
		return
	}
	response.Success(c, http.StatusOK, scopeResponse{FlowID: flowID, Scope: scopeToPayload(scope)})
}

// GetCandidates returns grey-zone hosts waiting for human review.
// @Summary List pending World State candidates
// @Tags WorldState
// @Produce json
// @Security BearerAuth
// @Param flowID path int true "flow id" minimum(0)
// @Success 200 {object} response.successResp{data=candidatesResponse}
// @Failure 403 {object} response.errorResp
// @Failure 404 {object} response.errorResp
// @Router /flows/{flowID}/worldstate/candidates [get]
func (s *WorldStateService) GetCandidates(c *gin.Context) {
	flowID, ok := s.permittedFlow(c, false)
	if !ok {
		return
	}
	q, ok := s.sqlc()
	if !ok {
		response.Error(c, response.ErrInternal, nil)
		return
	}
	ctx := c.Request.Context()
	scope, err := worldstate.LoadScope(ctx, q, int64(flowID))
	if err != nil {
		logger.FromContext(c).WithError(err).Error("load world state scope failed")
		response.Error(c, response.ErrInternal, err)
		return
	}
	rows, err := worldstate.ListPendingCandidates(ctx, q, int64(flowID))
	if err != nil {
		logger.FromContext(c).WithError(err).Error("list world state candidates failed")
		response.Error(c, response.ErrInternal, err)
		return
	}
	out := make([]candidatePayload, 0, len(rows))
	for _, r := range rows {
		out = append(out, candidatePayload{
			ID: r.ID, EntityKey: r.EntityKey, Type: r.Type, Properties: r.Properties,
			Reason: r.Reason, Status: r.Status, CreatedAt: r.CreatedAt,
		})
	}
	response.Success(c, http.StatusOK, candidatesResponse{
		FlowID:     flowID,
		Scope:      scopeToPayload(scope),
		Candidates: out,
	})
}

// ReviewCandidates accepts or rejects a batch of grey-zone hosts.
// @Summary Review World State candidates
// @Tags WorldState
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param flowID path int true "flow id" minimum(0)
// @Success 200 {object} response.successResp{data=candidateReviewResponse}
// @Failure 403 {object} response.errorResp
// @Failure 404 {object} response.errorResp
// @Router /flows/{flowID}/worldstate/candidates/review [post]
func (s *WorldStateService) ReviewCandidates(c *gin.Context) {
	flowID, ok := s.permittedFlow(c, true)
	if !ok {
		return
	}
	var body candidateReviewRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		response.Error(c, response.ErrFlowsInvalidRequest, err)
		return
	}
	q, ok := s.sqlc()
	if !ok {
		response.Error(c, response.ErrInternal, nil)
		return
	}
	ctx := c.Request.Context()
	store := worldstate.NewStore(q)
	var extraHosts []string
	accepted := 0
	for _, key := range body.Accept {
		cand, err := worldstate.GetPendingCandidate(ctx, q, int64(flowID), key)
		if err != nil {
			if err != sql.ErrNoRows {
				logger.FromContext(c).WithError(err).WithField("entity_key", key).
					Error("load pending candidate failed")
			}
			continue
		}
		props := map[string]any{}
		if len(cand.Properties) > 0 {
			_ = json.Unmarshal(cand.Properties, &props)
		}
		if _, err := store.Observe(ctx, int64(flowID), cand.Type, cand.EntityKey, worldstate.AgentHuman, props, map[string]any{
			"source": "candidate_review",
			"reason": cand.Reason,
		}); err != nil {
			logger.FromContext(c).WithError(err).WithField("entity_key", key).
				Error("observe accepted candidate failed")
			continue
		}
		if ok, err := worldstate.SetCandidateStatus(ctx, q, int64(flowID), key, "accepted"); err != nil || !ok {
			if err != nil {
				logger.FromContext(c).WithError(err).Error("mark candidate accepted failed")
			}
			continue
		}
		if host := worldstate.CandidateHost(cand); host != "" {
			extraHosts = append(extraHosts, host)
		}
		accepted++
	}
	if err := worldstate.MergeScopeHosts(ctx, q, int64(flowID), extraHosts); err != nil {
		logger.FromContext(c).WithError(err).Error("merge accepted hosts into scope failed")
		response.Error(c, response.ErrInternal, err)
		return
	}
	rejected := 0
	for _, key := range body.Reject {
		ok, err := worldstate.SetCandidateStatus(ctx, q, int64(flowID), key, "rejected")
		if err != nil {
			logger.FromContext(c).WithError(err).WithField("entity_key", key).
				Error("mark candidate rejected failed")
			continue
		}
		if ok {
			rejected++
		}
	}
	response.Success(c, http.StatusOK, candidateReviewResponse{Accepted: accepted, Rejected: rejected})
}
