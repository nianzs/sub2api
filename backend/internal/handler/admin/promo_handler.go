package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type PromoHandler struct {
	promoService *service.PromoService
}

func NewPromoHandler(promoService *service.PromoService) *PromoHandler {
	return &PromoHandler{promoService: promoService}
}

type CreatePromoCodeRequest struct {
	Code                         string   `json:"code"`
	BonusAmount                  float64  `json:"bonus_amount" binding:"required,min=0"`
	FirstRechargeBonusAmount     *float64 `json:"first_recharge_bonus_amount" binding:"omitempty,min=0"`
	FirstRechargeDiscountPercent *float64 `json:"first_recharge_discount_percent" binding:"omitempty,min=0.01,max=100"`
	FirstRechargeDiscountTimes   *int     `json:"first_recharge_discount_times" binding:"omitempty,min=0"`
	MaxUses                      int      `json:"max_uses" binding:"min=0"`
	ExpiresAt                    *int64   `json:"expires_at"`
	Notes                        string   `json:"notes"`
}

type UpdatePromoCodeRequest struct {
	Code                         *string  `json:"code"`
	BonusAmount                  *float64 `json:"bonus_amount" binding:"omitempty,min=0"`
	FirstRechargeBonusAmount     *float64 `json:"first_recharge_bonus_amount" binding:"omitempty,min=0"`
	FirstRechargeDiscountPercent *float64 `json:"first_recharge_discount_percent" binding:"omitempty,min=0.01,max=100"`
	FirstRechargeDiscountTimes   *int     `json:"first_recharge_discount_times" binding:"omitempty,min=0"`
	ClearFirstRechargeBonus      bool     `json:"clear_first_recharge_bonus"`
	ClearFirstRechargeDiscount   bool     `json:"clear_first_recharge_discount"`
	MaxUses                      *int     `json:"max_uses" binding:"omitempty,min=0"`
	Status                       *string  `json:"status" binding:"omitempty,oneof=active disabled"`
	ExpiresAt                    *int64   `json:"expires_at"`
	Notes                        *string  `json:"notes"`
}

func (h *PromoHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	status := c.Query("status")
	search := strings.TrimSpace(c.Query("search"))
	if len(search) > 100 {
		search = search[:100]
	}

	params := pagination.PaginationParams{
		Page:      page,
		PageSize:  pageSize,
		SortBy:    c.DefaultQuery("sort_by", "created_at"),
		SortOrder: c.DefaultQuery("sort_order", "desc"),
	}

	codes, paginationResult, err := h.promoService.List(c.Request.Context(), params, status, search)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.PromoCode, 0, len(codes))
	for i := range codes {
		out = append(out, *dto.PromoCodeFromService(&codes[i]))
	}
	response.Paginated(c, out, paginationResult.Total, page, pageSize)
}

func (h *PromoHandler) GetByID(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid promo code ID")
		return
	}

	code, err := h.promoService.GetByID(c.Request.Context(), codeID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.PromoCodeFromService(code))
}

func (h *PromoHandler) Create(c *gin.Context) {
	var req CreatePromoCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	input := &service.CreatePromoCodeInput{
		Code:                         req.Code,
		BonusAmount:                  req.BonusAmount,
		FirstRechargeBonusAmount:     req.FirstRechargeBonusAmount,
		FirstRechargeDiscountPercent: req.FirstRechargeDiscountPercent,
		FirstRechargeDiscountTimes:   req.FirstRechargeDiscountTimes,
		MaxUses:                      req.MaxUses,
		Notes:                        req.Notes,
	}
	if req.ExpiresAt != nil {
		t := time.Unix(*req.ExpiresAt, 0)
		input.ExpiresAt = &t
	}

	code, err := h.promoService.Create(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.PromoCodeFromService(code))
}

func (h *PromoHandler) Update(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid promo code ID")
		return
	}

	var req UpdatePromoCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	input := &service.UpdatePromoCodeInput{
		Code:                         req.Code,
		BonusAmount:                  req.BonusAmount,
		FirstRechargeBonusAmount:     req.FirstRechargeBonusAmount,
		ClearFirstRechargeBonus:      req.ClearFirstRechargeBonus,
		FirstRechargeDiscountPercent: req.FirstRechargeDiscountPercent,
		FirstRechargeDiscountTimes:   req.FirstRechargeDiscountTimes,
		ClearFirstRechargeDiscount:   req.ClearFirstRechargeDiscount,
		MaxUses:                      req.MaxUses,
		Status:                       req.Status,
		Notes:                        req.Notes,
	}
	if req.ExpiresAt != nil {
		if *req.ExpiresAt == 0 {
			input.ClearExpiresAt = true
		} else {
			t := time.Unix(*req.ExpiresAt, 0)
			input.ExpiresAt = &t
		}
	}

	code, err := h.promoService.Update(c.Request.Context(), codeID, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.PromoCodeFromService(code))
}

func (h *PromoHandler) Delete(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid promo code ID")
		return
	}

	if err := h.promoService.Delete(c.Request.Context(), codeID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"message": "Promo code deleted successfully"})
}

func (h *PromoHandler) GetUsages(c *gin.Context) {
	codeID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid promo code ID")
		return
	}

	page, pageSize := response.ParsePagination(c)
	params := pagination.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	}

	usages, paginationResult, err := h.promoService.ListUsages(c.Request.Context(), codeID, params)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]dto.PromoCodeUsage, 0, len(usages))
	for i := range usages {
		out = append(out, *dto.PromoCodeUsageFromService(&usages[i]))
	}
	response.Paginated(c, out, paginationResult.Total, page, pageSize)
}
