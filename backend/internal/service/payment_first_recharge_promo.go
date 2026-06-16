package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/payment"
)

const (
	paymentFirstRechargePromoAction        = "FIRST_RECHARGE_PROMO_APPLIED"
	paymentFirstRechargePromoSkippedAction = "FIRST_RECHARGE_PROMO_SKIPPED"
)

type firstRechargePromoBalanceResult int

const (
	firstRechargePromoBalanceNone firstRechargePromoBalanceResult = iota
	firstRechargePromoBalanceApplied
	firstRechargePromoBalanceStale
)

type firstRechargePromo struct {
	PromoCodeID     int64
	PromoCode       string
	BonusAmount     float64
	DiscountPercent float64
	DiscountTimes   int
	DiscountSet     bool
}

type firstRechargeAmountPlan struct {
	PromoCodeID      int64
	PromoCode        string
	BaseCreditAmount float64
	BonusAmount      float64
	DiscountPercent  float64
	DiscountTimes    int
	DiscountSet      bool
	CreditAmount     float64
	PaymentAmount    float64
}

func (p firstRechargePromo) active() bool {
	return p.BonusAmount > 0 || (p.DiscountSet && p.DiscountPercent < 100)
}

func (s *PaymentService) resolveFirstRechargePromo(ctx context.Context, userID int64) (*firstRechargePromo, error) {
	if s == nil || s.promoRepo == nil {
		return nil, nil
	}
	firstRechargeAvailable := true
	if s.userRepo != nil {
		user, err := s.userRepo.GetByID(ctx, userID)
		if err != nil {
			return nil, err
		}
		if user.TotalRecharged > 0 {
			firstRechargeAvailable = false
		}
	}
	promoCode, err := s.promoRepo.GetFirstRechargePromoByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if promoCode == nil {
		return nil, nil
	}
	promo := &firstRechargePromo{
		PromoCodeID: promoCode.ID,
		PromoCode:   promoCode.Code,
	}
	if firstRechargeAvailable && promoCode.FirstRechargeBonusAmount != nil {
		promo.BonusAmount = roundTo(*promoCode.FirstRechargeBonusAmount, 8)
	}
	if promoCode.FirstRechargeDiscountPercent != nil {
		discountTimes := promoCode.FirstRechargeDiscountTimes
		discountUsed, err := countPromoRechargeDiscountOrders(ctx, s.entClient, userID, promoCode.ID)
		if err != nil {
			return nil, err
		}
		if discountTimes == 0 || discountUsed < discountTimes {
			promo.DiscountPercent = clampFirstRechargeDiscount(*promoCode.FirstRechargeDiscountPercent)
			promo.DiscountTimes = discountTimes
			promo.DiscountSet = true
		}
	}
	if !promo.active() {
		return nil, nil
	}
	return promo, nil
}

func clampFirstRechargeDiscount(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return roundTo(v, 8)
}

func buildFirstRechargeAmountPlan(requestAmount, baseCreditAmount float64, promo *firstRechargePromo) firstRechargeAmountPlan {
	plan := firstRechargeAmountPlan{
		BaseCreditAmount: baseCreditAmount,
		CreditAmount:     baseCreditAmount,
		PaymentAmount:    requestAmount,
	}
	if promo == nil || !promo.active() {
		return plan
	}
	plan.PromoCodeID = promo.PromoCodeID
	plan.PromoCode = promo.PromoCode
	plan.BonusAmount = math.Max(0, promo.BonusAmount)
	plan.DiscountPercent = clampFirstRechargeDiscount(promo.DiscountPercent)
	plan.DiscountTimes = promo.DiscountTimes
	plan.DiscountSet = promo.DiscountSet
	if plan.DiscountSet {
		plan.PaymentAmount = roundTo(requestAmount*(plan.DiscountPercent/100), 8)
	}
	plan.CreditAmount = roundTo(baseCreditAmount+plan.BonusAmount, 8)
	return plan
}

func (p firstRechargeAmountPlan) active() bool {
	return p.BonusAmount > 0 || (p.DiscountSet && p.DiscountPercent < 100)
}

func (p firstRechargeAmountPlan) normalCreditAmount(fallback float64) float64 {
	amount := p.BaseCreditAmount
	if amount <= 0 {
		amount = p.CreditAmount - p.BonusAmount
	}
	if amount <= 0 {
		amount = fallback
	}
	return roundTo(math.Max(0, amount), 8)
}

func appendFirstRechargePromoSnapshot(snapshot map[string]any, plan firstRechargeAmountPlan) map[string]any {
	if !plan.active() {
		return snapshot
	}
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	snapshot["first_recharge_promo"] = map[string]any{
		"promo_code_id":    plan.PromoCodeID,
		"promo_code":       plan.PromoCode,
		"base_amount":      plan.BaseCreditAmount,
		"bonus_amount":     plan.BonusAmount,
		"discount_percent": plan.DiscountPercent,
		"discount_times":   plan.DiscountTimes,
		"discount_set":     plan.DiscountSet,
		"credited_amount":  plan.CreditAmount,
		"payment_amount":   plan.PaymentAmount,
	}
	return snapshot
}

func firstRechargeAmountPlanFromSnapshot(snapshot map[string]any) (firstRechargeAmountPlan, bool) {
	raw, ok := snapshot["first_recharge_promo"]
	if !ok {
		return firstRechargeAmountPlan{}, false
	}
	data, ok := raw.(map[string]any)
	if !ok {
		return firstRechargeAmountPlan{}, false
	}
	_, hasDiscountPercent := data["discount_percent"]
	_, hasDiscountSet := data["discount_set"]
	discountSet := boolFromSnapshot(data["discount_set"])
	if !hasDiscountSet {
		discountSet = hasDiscountPercent
	}
	plan := firstRechargeAmountPlan{
		PromoCodeID:      int64(numberFromSnapshot(data["promo_code_id"])),
		PromoCode:        stringFromSnapshot(data["promo_code"]),
		BaseCreditAmount: numberFromSnapshot(data["base_amount"]),
		BonusAmount:      numberFromSnapshot(data["bonus_amount"]),
		DiscountPercent:  clampFirstRechargeDiscount(numberFromSnapshot(data["discount_percent"])),
		DiscountTimes:    int(numberFromSnapshot(data["discount_times"])),
		DiscountSet:      discountSet,
		CreditAmount:     numberFromSnapshot(data["credited_amount"]),
		PaymentAmount:    numberFromSnapshot(data["payment_amount"]),
	}
	plan.BonusAmount = roundTo(math.Max(0, plan.BonusAmount), 8)
	plan.BaseCreditAmount = roundTo(math.Max(0, plan.BaseCreditAmount), 8)
	plan.CreditAmount = roundTo(math.Max(0, plan.CreditAmount), 8)
	plan.PaymentAmount = roundTo(math.Max(0, plan.PaymentAmount), 8)
	if !plan.active() || plan.CreditAmount <= 0 {
		return firstRechargeAmountPlan{}, false
	}
	return plan, true
}

func stringFromSnapshot(v any) string {
	switch s := v.(type) {
	case string:
		return s
	default:
		return ""
	}
}

func numberFromSnapshot(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		out, _ := n.Float64()
		return out
	default:
		return 0
	}
}

func boolFromSnapshot(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	default:
		return false
	}
}

func firstRechargePromoPlanForOrder(o *dbent.PaymentOrder) (firstRechargeAmountPlan, bool) {
	if o == nil || o.OrderType != payment.OrderTypeBalance {
		return firstRechargeAmountPlan{}, false
	}
	return firstRechargeAmountPlanFromSnapshot(o.ProviderSnapshot)
}

func countPromoRechargeDiscountOrders(ctx context.Context, client *dbent.Client, userID, promoCodeID int64) (int, error) {
	if client == nil || promoCodeID <= 0 {
		return 0, nil
	}
	orders, err := client.PaymentOrder.Query().
		Where(
			paymentorder.UserIDEQ(userID),
			paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
			paymentorder.StatusIn(
				OrderStatusPending,
				OrderStatusPaid,
				OrderStatusRecharging,
				OrderStatusCompleted,
				OrderStatusRefundRequested,
				OrderStatusRefunding,
				OrderStatusPartiallyRefunded,
				OrderStatusRefunded,
				OrderStatusRefundFailed,
			),
			paymentorder.ProviderSnapshotNotNil(),
		).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("count promo recharge discount orders: %w", err)
	}
	count := 0
	for _, order := range orders {
		plan, ok := firstRechargePromoPlanForOrder(order)
		if ok && plan.PromoCodeID == promoCodeID && plan.DiscountSet && plan.DiscountPercent < 100 {
			count++
		}
	}
	return count, nil
}

func firstRechargePromoDiscountLimitReached(ctx context.Context, client *dbent.Client, userID int64, plan firstRechargeAmountPlan) (bool, error) {
	if !plan.DiscountSet || plan.DiscountTimes <= 0 || plan.PromoCodeID <= 0 {
		return false, nil
	}
	used, err := countPromoRechargeDiscountOrders(ctx, client, userID, plan.PromoCodeID)
	if err != nil {
		return false, err
	}
	return used >= plan.DiscountTimes, nil
}

func firstRechargePromoFallbackCreditAmount(o *dbent.PaymentOrder) (float64, bool) {
	plan, ok := firstRechargePromoPlanForOrder(o)
	if !ok {
		return 0, false
	}
	amount := plan.normalCreditAmount(o.Amount)
	if amount <= 0 {
		return 0, false
	}
	return amount, true
}

func affiliateRebateBaseAmountForOrder(o *dbent.PaymentOrder) float64 {
	if o == nil {
		return 0
	}
	if amount, ok := firstRechargePromoFallbackCreditAmount(o); ok {
		return amount
	}
	return o.Amount
}

func (s *PaymentService) applyFirstRechargePromoBalance(ctx context.Context, tx *dbent.Tx, o *dbent.PaymentOrder) (firstRechargePromoBalanceResult, error) {
	if s == nil || tx == nil || o == nil || o.OrderType != payment.OrderTypeBalance || o.Amount <= 0 {
		return firstRechargePromoBalanceNone, nil
	}
	if s.hasAuditLog(ctx, o.ID, paymentFirstRechargePromoAction) {
		return firstRechargePromoBalanceApplied, nil
	}
	plan, ok := firstRechargePromoPlanForOrder(o)
	if !ok {
		return firstRechargePromoBalanceNone, nil
	}
	if plan.BonusAmount <= 0 {
		return firstRechargePromoBalanceNone, nil
	}

	userQuery := tx.User.Query().Where(user.IDEQ(o.UserID))
	if paymentTxSupportsForUpdate(tx) {
		userQuery = userQuery.ForUpdate()
	}
	locked, err := userQuery.Only(ctx)
	if err != nil {
		return firstRechargePromoBalanceNone, fmt.Errorf("lock user for first recharge promo: %w", err)
	}
	if locked.TotalRecharged > 0 {
		return firstRechargePromoBalanceStale, nil
	}

	baseAmount := plan.normalCreditAmount(o.Amount)
	creditAmount := o.Amount
	_, err = tx.User.UpdateOneID(o.UserID).
		AddBalance(creditAmount).
		AddTotalRecharged(baseAmount).
		Save(ctx)
	if err != nil {
		return firstRechargePromoBalanceNone, fmt.Errorf("credit first recharge promo balance: %w", err)
	}

	if err := s.writeAuditLogWithClient(ctx, tx.Client(), o.ID, paymentFirstRechargePromoAction, "system", map[string]any{
		"promo_code_id":    plan.PromoCodeID,
		"promo_code":       plan.PromoCode,
		"base_amount":      baseAmount,
		"bonus_amount":     plan.BonusAmount,
		"discount_percent": plan.DiscountPercent,
		"discount_times":   plan.DiscountTimes,
		"credited_amount":  creditAmount,
		"pay_amount":       o.PayAmount,
		"recharge_code":    o.RechargeCode,
	}); err != nil {
		return firstRechargePromoBalanceNone, err
	}
	return firstRechargePromoBalanceApplied, nil
}

func (s *PaymentService) writeAuditLogWithClient(ctx context.Context, client *dbent.Client, oid int64, action, op string, detail map[string]any) error {
	if client == nil {
		return fmt.Errorf("nil payment audit client")
	}
	dj, _ := json.Marshal(detail)
	_, err := client.PaymentAuditLog.Create().
		SetOrderID(fmt.Sprintf("%d", oid)).
		SetAction(action).
		SetDetail(string(dj)).
		SetOperator(op).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("write audit log %s: %w", action, err)
	}
	return nil
}
