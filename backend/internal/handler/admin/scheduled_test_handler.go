package admin

import (
	"net/http"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ScheduledTestHandler handles admin scheduled-test-plan management. Quality-rule
// endpoints are ported from ranxi2001/sub2api@f895ea743 without observer scopes,
// ownership reconciliation or HTML Pelican history.
type ScheduledTestHandler struct {
	scheduledTestSvc *service.ScheduledTestService
}

// NewScheduledTestHandler creates a new ScheduledTestHandler.
func NewScheduledTestHandler(scheduledTestSvc *service.ScheduledTestService) *ScheduledTestHandler {
	return &ScheduledTestHandler{scheduledTestSvc: scheduledTestSvc}
}

type createScheduledTestPlanRequest struct {
	PelicanConfig  *service.PelicanTestConfig `json:"pelican_config"`
	AccountID      int64                      `json:"account_id" binding:"required"`
	ModelID        string                     `json:"model_id"`
	CronExpression string                     `json:"cron_expression" binding:"required"`
	Enabled        *bool                      `json:"enabled"`
	MaxResults     int                        `json:"max_results"`
	AutoRecover    *bool                      `json:"auto_recover"`
}

type updateScheduledTestPlanRequest struct {
	PelicanConfig  *service.PelicanTestConfig `json:"pelican_config"`
	ModelID        string                     `json:"model_id"`
	CronExpression string                     `json:"cron_expression"`
	Enabled        *bool                      `json:"enabled"`
	MaxResults     int                        `json:"max_results"`
	AutoRecover    *bool                      `json:"auto_recover"`
}

// ListByAccount GET /admin/accounts/:id/scheduled-test-plans
func (h *ScheduledTestHandler) ListByAccount(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid account id")
		return
	}

	plans, err := h.scheduledTestSvc.ListPlansByAccount(c.Request.Context(), accountID)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, plans)
}

// Create POST /admin/scheduled-test-plans
func (h *ScheduledTestHandler) Create(c *gin.Context) {
	var req createScheduledTestPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	plan := &service.ScheduledTestPlan{
		AccountID:      req.AccountID,
		PelicanConfig:  req.PelicanConfig,
		ModelID:        req.ModelID,
		CronExpression: req.CronExpression,
		Enabled:        true,
		MaxResults:     req.MaxResults,
	}
	if req.Enabled != nil {
		plan.Enabled = *req.Enabled
	}
	if req.AutoRecover != nil {
		plan.AutoRecover = *req.AutoRecover
	}

	created, err := h.scheduledTestSvc.CreatePlan(c.Request.Context(), plan)
	if err != nil {
		if infraerrors.Reason(err) == "QUALITY_MODEL_NOT_ALLOWED" || infraerrors.Reason(err) == "QUALITY_MODEL_CATALOG_UNKNOWN" || infraerrors.Reason(err) == "OPENAI_CODEX_MODELS_CREDENTIALS_INVALID" {
			response.ErrorFrom(c, err)
			return
		}
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, created)
}

// Update PUT /admin/scheduled-test-plans/:id
func (h *ScheduledTestHandler) Update(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}

	existing, err := h.scheduledTestSvc.GetPlan(c.Request.Context(), planID)
	if err != nil {
		response.NotFound(c, "plan not found")
		return
	}
	var req updateScheduledTestPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if req.PelicanConfig != nil {
		if existing.PelicanConfig == nil {
			response.BadRequest(c, "cannot change test type")
			return
		}
		if (existing.PelicanConfig.Quality == nil) != (req.PelicanConfig.Quality == nil) {
			response.BadRequest(c, "cannot change quality test type")
			return
		}
		existing.PelicanConfig = req.PelicanConfig
	}
	if req.ModelID != "" {
		if req.PelicanConfig == nil && existing.PelicanConfig != nil {
			for i, model := range existing.PelicanConfig.ModelIDs {
				if model == existing.ModelID {
					existing.PelicanConfig.ModelIDs[i] = req.ModelID
				}
			}
		}
		existing.ModelID = req.ModelID
	}
	if req.CronExpression != "" {
		existing.CronExpression = req.CronExpression
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if req.MaxResults > 0 {
		existing.MaxResults = req.MaxResults
	}
	if req.AutoRecover != nil {
		existing.AutoRecover = *req.AutoRecover
	}

	updated, err := h.scheduledTestSvc.UpdatePlan(c.Request.Context(), existing)
	if err != nil {
		if infraerrors.Reason(err) == service.QualityScopeConflictReason || infraerrors.Reason(err) == "QUALITY_MODEL_NOT_ALLOWED" || infraerrors.Reason(err) == "QUALITY_MODEL_CATALOG_UNKNOWN" || infraerrors.Reason(err) == "OPENAI_CODEX_MODELS_CREDENTIALS_INVALID" {
			response.ErrorFrom(c, err)
			return
		}
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, updated)
}

// Delete DELETE /admin/scheduled-test-plans/:id
func (h *ScheduledTestHandler) Delete(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}

	if err := h.scheduledTestSvc.DeletePlan(c.Request.Context(), planID); err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// ListResults GET /admin/scheduled-test-plans/:id/results
func (h *ScheduledTestHandler) ListResults(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}

	limit := 50
	if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 {
		limit = l
	}

	if limit > 100 {
		limit = 100
	}
	results, err := h.scheduledTestSvc.ListResults(c.Request.Context(), planID, limit, c.Query("include_content") != "false")
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, results)
}

func (h *ScheduledTestHandler) GetResult(c *gin.Context) {
	planID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid plan id")
		return
	}
	resultID, err := strconv.ParseInt(c.Param("resultID"), 10, 64)
	if err != nil {
		response.BadRequest(c, "invalid result id")
		return
	}
	result, err := h.scheduledTestSvc.GetResult(c.Request.Context(), planID, resultID)
	if err != nil {
		response.NotFound(c, "result not found or expired")
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ScheduledTestHandler) ListQualityPlans(c *gin.Context) {
	plans, err := h.scheduledTestSvc.ListQualityPlans(c.Request.Context())
	if err != nil {
		response.InternalError(c, "Failed to load quality plans")
		return
	}
	if plans == nil {
		plans = []*service.ScheduledTestPlan{}
	}
	c.JSON(http.StatusOK, plans)
}
func (h *ScheduledTestHandler) TriggerQuality(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid plan id")
		return
	}
	if err = h.scheduledTestSvc.TriggerQuality(c.Request.Context(), id); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "queued"})
}

func (h *ScheduledTestHandler) ListQualityHistory(c *gin.Context) {
	beforeID, err := strconv.ParseInt(c.DefaultQuery("before_id", "0"), 10, 64)
	if err != nil || beforeID < 0 {
		response.BadRequest(c, "invalid before_id")
		return
	}
	page, err := h.scheduledTestSvc.ListQualityHistory(c.Request.Context(), beforeID)
	if err != nil {
		response.InternalError(c, "Failed to load quality operation history")
		return
	}
	c.JSON(http.StatusOK, page)
}
