package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/promocode"
	"github.com/Wei-Shaw/sub2api/ent/promocodeusage"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

type promoCodeRepository struct {
	client *dbent.Client
}

func NewPromoCodeRepository(client *dbent.Client) service.PromoCodeRepository {
	return &promoCodeRepository{client: client}
}

func (r *promoCodeRepository) placeholder(n int) string {
	if r.client != nil && r.client.Driver() != nil && r.client.Driver().Dialect() == dialect.Postgres {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

func scanPromoCode(rows interface {
	Scan(dest ...any) error
}) (*service.PromoCode, error) {
	var out service.PromoCode
	var expiresAt sql.NullTime
	var notes sql.NullString
	var firstRechargeBonus sql.NullFloat64
	var firstRechargeDiscount sql.NullFloat64
	var firstRechargeDiscountTimes sql.NullInt64
	if err := rows.Scan(
		&out.ID,
		&out.Code,
		&out.BonusAmount,
		&firstRechargeBonus,
		&firstRechargeDiscount,
		&firstRechargeDiscountTimes,
		&out.MaxUses,
		&out.UsedCount,
		&out.Status,
		&expiresAt,
		&notes,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if firstRechargeBonus.Valid {
		v := firstRechargeBonus.Float64
		out.FirstRechargeBonusAmount = &v
	}
	if firstRechargeDiscount.Valid {
		v := firstRechargeDiscount.Float64
		out.FirstRechargeDiscountPercent = &v
	}
	if firstRechargeDiscountTimes.Valid {
		out.FirstRechargeDiscountTimes = int(firstRechargeDiscountTimes.Int64)
	} else {
		out.FirstRechargeDiscountTimes = service.PromoRechargeDiscountTimesDefault
	}
	if expiresAt.Valid {
		out.ExpiresAt = &expiresAt.Time
	}
	if notes.Valid {
		out.Notes = notes.String
	}
	return &out, nil
}

func (r *promoCodeRepository) hydratePromoCodeFirstRechargeFields(ctx context.Context, codes []service.PromoCode) error {
	if len(codes) == 0 {
		return nil
	}
	client := clientFromContext(ctx, r.client)
	for i := range codes {
		query := fmt.Sprintf(`
SELECT first_recharge_bonus_amount, first_recharge_discount_percent, first_recharge_discount_times
FROM promo_codes
WHERE id = %s`, r.placeholder(1))
		rows, err := client.QueryContext(ctx, query, codes[i].ID)
		if err != nil {
			return err
		}
		var bonus sql.NullFloat64
		var discount sql.NullFloat64
		var discountTimes sql.NullInt64
		if rows.Next() {
			if err := rows.Scan(&bonus, &discount, &discountTimes); err != nil {
				_ = rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		if bonus.Valid {
			v := bonus.Float64
			codes[i].FirstRechargeBonusAmount = &v
		}
		if discount.Valid {
			v := discount.Float64
			codes[i].FirstRechargeDiscountPercent = &v
		}
		if discountTimes.Valid {
			codes[i].FirstRechargeDiscountTimes = int(discountTimes.Int64)
		} else {
			codes[i].FirstRechargeDiscountTimes = service.PromoRechargeDiscountTimesDefault
		}
	}
	return nil
}

func (r *promoCodeRepository) savePromoCodeFirstRechargeFields(ctx context.Context, code *service.PromoCode) error {
	client := clientFromContext(ctx, r.client)
	query := fmt.Sprintf(`
UPDATE promo_codes
SET first_recharge_bonus_amount = %s,
    first_recharge_discount_percent = %s,
    first_recharge_discount_times = %s
WHERE id = %s`, r.placeholder(1), r.placeholder(2), r.placeholder(3), r.placeholder(4))
	_, err := client.ExecContext(ctx, query, nullableFloatArg(code.FirstRechargeBonusAmount), nullableFloatArg(code.FirstRechargeDiscountPercent), code.FirstRechargeDiscountTimes, code.ID)
	return err
}

func (r *promoCodeRepository) Create(ctx context.Context, code *service.PromoCode) error {
	client := clientFromContext(ctx, r.client)
	builder := client.PromoCode.Create().
		SetCode(code.Code).
		SetBonusAmount(code.BonusAmount).
		SetMaxUses(code.MaxUses).
		SetUsedCount(code.UsedCount).
		SetStatus(code.Status).
		SetNotes(code.Notes)

	if code.ExpiresAt != nil {
		builder.SetExpiresAt(*code.ExpiresAt)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return err
	}

	code.ID = created.ID
	code.CreatedAt = created.CreatedAt
	code.UpdatedAt = created.UpdatedAt
	if err := r.savePromoCodeFirstRechargeFields(ctx, code); err != nil {
		return err
	}
	return nil
}

func (r *promoCodeRepository) GetByID(ctx context.Context, id int64) (*service.PromoCode, error) {
	m, err := r.client.PromoCode.Query().
		Where(promocode.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrPromoCodeNotFound
		}
		return nil, err
	}
	out := promoCodeEntityToService(m)
	if out == nil {
		return nil, nil
	}
	full, err := r.getByIDRaw(ctx, out.ID)
	if err != nil {
		return nil, err
	}
	return full, nil
}

func (r *promoCodeRepository) GetByCode(ctx context.Context, code string) (*service.PromoCode, error) {
	m, err := r.client.PromoCode.Query().
		Where(promocode.CodeEqualFold(code)).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrPromoCodeNotFound
		}
		return nil, err
	}
	return r.getByIDRaw(ctx, m.ID)
}

func (r *promoCodeRepository) GetByCodeForUpdate(ctx context.Context, code string) (*service.PromoCode, error) {
	client := clientFromContext(ctx, r.client)
	m, err := client.PromoCode.Query().
		Where(promocode.CodeEqualFold(code)).
		ForUpdate().
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrPromoCodeNotFound
		}
		return nil, err
	}
	return r.getByIDRaw(ctx, m.ID)
}

func (r *promoCodeRepository) Update(ctx context.Context, code *service.PromoCode) error {
	client := clientFromContext(ctx, r.client)
	builder := client.PromoCode.UpdateOneID(code.ID).
		SetCode(code.Code).
		SetBonusAmount(code.BonusAmount).
		SetMaxUses(code.MaxUses).
		SetUsedCount(code.UsedCount).
		SetStatus(code.Status).
		SetNotes(code.Notes)

	if code.ExpiresAt != nil {
		builder.SetExpiresAt(*code.ExpiresAt)
	} else {
		builder.ClearExpiresAt()
	}

	updated, err := builder.Save(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return service.ErrPromoCodeNotFound
		}
		return err
	}

	code.UpdatedAt = updated.UpdatedAt
	return r.savePromoCodeFirstRechargeFields(ctx, code)
}

func (r *promoCodeRepository) Delete(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.PromoCode.Delete().Where(promocode.IDEQ(id)).Exec(ctx)
	return err
}

func (r *promoCodeRepository) List(ctx context.Context, params pagination.PaginationParams) ([]service.PromoCode, *pagination.PaginationResult, error) {
	return r.ListWithFilters(ctx, params, "", "")
}

func (r *promoCodeRepository) ListWithFilters(ctx context.Context, params pagination.PaginationParams, status, search string) ([]service.PromoCode, *pagination.PaginationResult, error) {
	q := r.client.PromoCode.Query()

	if status != "" {
		q = q.Where(promocode.StatusEQ(status))
	}
	if search != "" {
		q = q.Where(promocode.CodeContainsFold(search))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	codesQuery := q.
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range promoCodeListOrder(params) {
		codesQuery = codesQuery.Order(order)
	}

	codes, err := codesQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outCodes := promoCodeEntitiesToService(codes)
	if err := r.hydratePromoCodeFirstRechargeFields(ctx, outCodes); err != nil {
		return nil, nil, err
	}

	return outCodes, paginationResultFromTotal(int64(total), params), nil
}

func (r *promoCodeRepository) getByIDRaw(ctx context.Context, id int64) (*service.PromoCode, error) {
	client := clientFromContext(ctx, r.client)
	query := fmt.Sprintf(`
SELECT id,
       code,
       bonus_amount,
       first_recharge_bonus_amount,
       first_recharge_discount_percent,
       first_recharge_discount_times,
       max_uses,
       used_count,
       status,
       expires_at,
       notes,
       created_at,
       updated_at
FROM promo_codes
WHERE id = %s`, r.placeholder(1))
	rows, err := client.QueryContext(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrPromoCodeNotFound
	}
	out, err := scanPromoCode(rows)
	if err != nil {
		return nil, err
	}
	return out, rows.Err()
}

func promoCodeListOrder(params pagination.PaginationParams) []func(*entsql.Selector) {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)

	var field string
	switch sortBy {
	case "bonus_amount":
		field = promocode.FieldBonusAmount
	case "status":
		field = promocode.FieldStatus
	case "expires_at":
		field = promocode.FieldExpiresAt
	case "created_at":
		field = promocode.FieldCreatedAt
	case "code":
		field = promocode.FieldCode
	default:
		field = promocode.FieldID
	}

	if sortOrder == pagination.SortOrderAsc {
		return []func(*entsql.Selector){dbent.Asc(field), dbent.Asc(promocode.FieldID)}
	}
	return []func(*entsql.Selector){dbent.Desc(field), dbent.Desc(promocode.FieldID)}
}

func (r *promoCodeRepository) CreateUsage(ctx context.Context, usage *service.PromoCodeUsage) error {
	client := clientFromContext(ctx, r.client)
	created, err := client.PromoCodeUsage.Create().
		SetPromoCodeID(usage.PromoCodeID).
		SetUserID(usage.UserID).
		SetBonusAmount(usage.BonusAmount).
		SetUsedAt(usage.UsedAt).
		Save(ctx)
	if err != nil {
		return err
	}

	usage.ID = created.ID
	return nil
}

func (r *promoCodeRepository) GetUsageByPromoCodeAndUser(ctx context.Context, promoCodeID, userID int64) (*service.PromoCodeUsage, error) {
	m, err := r.client.PromoCodeUsage.Query().
		Where(
			promocodeusage.PromoCodeIDEQ(promoCodeID),
			promocodeusage.UserIDEQ(userID),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return promoCodeUsageEntityToService(m), nil
}

func (r *promoCodeRepository) GetFirstRechargePromoByUser(ctx context.Context, userID int64) (*service.PromoCode, error) {
	client := clientFromContext(ctx, r.client)
	query := fmt.Sprintf(`
SELECT pc.id,
       pc.code,
       pc.bonus_amount,
       pc.first_recharge_bonus_amount,
       pc.first_recharge_discount_percent,
       pc.first_recharge_discount_times,
       pc.max_uses,
       pc.used_count,
       pc.status,
       pc.expires_at,
       pc.notes,
       pc.created_at,
       pc.updated_at
FROM promo_code_usages pcu
JOIN promo_codes pc ON pc.id = pcu.promo_code_id
WHERE pcu.user_id = %s
  AND (pc.first_recharge_bonus_amount IS NOT NULL
       OR pc.first_recharge_discount_percent IS NOT NULL)
ORDER BY pcu.used_at ASC, pcu.id ASC
LIMIT 1`, r.placeholder(1))
	rows, err := client.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	out, err := scanPromoCode(rows)
	if err != nil {
		return nil, err
	}
	return out, rows.Err()
}

func (r *promoCodeRepository) ListUsagesByPromoCode(ctx context.Context, promoCodeID int64, params pagination.PaginationParams) ([]service.PromoCodeUsage, *pagination.PaginationResult, error) {
	q := r.client.PromoCodeUsage.Query().
		Where(promocodeusage.PromoCodeIDEQ(promoCodeID))

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	usages, err := q.
		WithUser().
		Offset(params.Offset()).
		Limit(params.Limit()).
		Order(dbent.Desc(promocodeusage.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outUsages := promoCodeUsageEntitiesToService(usages)

	return outUsages, paginationResultFromTotal(int64(total), params), nil
}

func (r *promoCodeRepository) IncrementUsedCount(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)
	_, err := client.PromoCode.UpdateOneID(id).
		AddUsedCount(1).
		Save(ctx)
	return err
}

// Entity to Service conversions

func promoCodeEntityToService(m *dbent.PromoCode) *service.PromoCode {
	if m == nil {
		return nil
	}
	return &service.PromoCode{
		ID:          m.ID,
		Code:        m.Code,
		BonusAmount: m.BonusAmount,
		MaxUses:     m.MaxUses,
		UsedCount:   m.UsedCount,
		Status:      m.Status,
		ExpiresAt:   m.ExpiresAt,
		Notes:       derefString(m.Notes),
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

func nullableFloatArg(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func promoCodeEntitiesToService(models []*dbent.PromoCode) []service.PromoCode {
	out := make([]service.PromoCode, 0, len(models))
	for i := range models {
		if s := promoCodeEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

func promoCodeUsageEntityToService(m *dbent.PromoCodeUsage) *service.PromoCodeUsage {
	if m == nil {
		return nil
	}
	out := &service.PromoCodeUsage{
		ID:          m.ID,
		PromoCodeID: m.PromoCodeID,
		UserID:      m.UserID,
		BonusAmount: m.BonusAmount,
		UsedAt:      m.UsedAt,
	}
	if m.Edges.User != nil {
		out.User = userEntityToService(m.Edges.User)
	}
	return out
}

func promoCodeUsageEntitiesToService(models []*dbent.PromoCodeUsage) []service.PromoCodeUsage {
	out := make([]service.PromoCodeUsage, 0, len(models))
	for i := range models {
		if s := promoCodeUsageEntityToService(models[i]); s != nil {
			out = append(out, *s)
		}
	}
	return out
}
