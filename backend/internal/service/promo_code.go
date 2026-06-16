package service

import "time"

type PromoCode struct {
	ID                           int64
	Code                         string
	BonusAmount                  float64
	FirstRechargeBonusAmount     *float64
	FirstRechargeDiscountPercent *float64
	FirstRechargeDiscountTimes   int
	MaxUses                      int
	UsedCount                    int
	Status                       string
	ExpiresAt                    *time.Time
	Notes                        string
	CreatedAt                    time.Time
	UpdatedAt                    time.Time

	UsageRecords []PromoCodeUsage
}

type PromoCodeUsage struct {
	ID          int64
	PromoCodeID int64
	UserID      int64
	BonusAmount float64
	UsedAt      time.Time

	PromoCode *PromoCode
	User      *User
}

func (p *PromoCode) CanUse() bool {
	if p.Status != PromoCodeStatusActive {
		return false
	}
	if p.ExpiresAt != nil && time.Now().After(*p.ExpiresAt) {
		return false
	}
	if p.MaxUses > 0 && p.UsedCount >= p.MaxUses {
		return false
	}
	return true
}

func (p *PromoCode) IsExpired() bool {
	return p.ExpiresAt != nil && time.Now().After(*p.ExpiresAt)
}

type CreatePromoCodeInput struct {
	Code                         string
	BonusAmount                  float64
	FirstRechargeBonusAmount     *float64
	FirstRechargeDiscountPercent *float64
	FirstRechargeDiscountTimes   *int
	MaxUses                      int
	ExpiresAt                    *time.Time
	Notes                        string
}

type UpdatePromoCodeInput struct {
	Code                         *string
	BonusAmount                  *float64
	FirstRechargeBonusAmount     *float64
	ClearFirstRechargeBonus      bool
	FirstRechargeDiscountPercent *float64
	ClearFirstRechargeDiscount   bool
	FirstRechargeDiscountTimes   *int
	MaxUses                      *int
	Status                       *string
	ExpiresAt                    *time.Time
	ClearExpiresAt               bool
	Notes                        *string
}
