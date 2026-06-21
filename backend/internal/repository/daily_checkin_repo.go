package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type dailyCheckinRepository struct {
	db *sql.DB
}

func NewDailyCheckinRepository(_ *dbent.Client, db *sql.DB) service.DailyCheckinRepository {
	return &dailyCheckinRepository{db: db}
}

func (r *dailyCheckinRepository) GetUserCheckin(ctx context.Context, userID int64, date string) (*service.DailyCheckinRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("daily check-in repository is not configured")
	}
	return scanDailyCheckinRecord(ctx, r.db, `
SELECT user_id, checkin_date::text, reward::double precision, created_at
FROM daily_checkins
WHERE user_id = $1 AND checkin_date = $2`, userID, date)
}

func (r *dailyCheckinRepository) GetUserLatestCheckin(ctx context.Context, userID int64) (*service.DailyCheckinRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("daily check-in repository is not configured")
	}
	return scanDailyCheckinRecord(ctx, r.db, `
SELECT user_id, checkin_date::text, reward::double precision, created_at
FROM daily_checkins
WHERE user_id = $1
ORDER BY checkin_date DESC
LIMIT 1`, userID)
}

func (r *dailyCheckinRepository) SumRewardsByDate(ctx context.Context, date string) (float64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("daily check-in repository is not configured")
	}
	var total float64
	err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(total_reward, 0)::double precision
FROM daily_checkin_totals
WHERE checkin_date = $1`, date).Scan(&total)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return total, nil
}

func (r *dailyCheckinRepository) Claim(ctx context.Context, input service.DailyCheckinClaimInput) (*service.DailyCheckinClaimResult, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("daily check-in repository is not configured")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin daily check-in transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO daily_checkin_totals (checkin_date, total_reward, created_at, updated_at)
VALUES ($1, 0, NOW(), NOW())
ON CONFLICT (checkin_date) DO NOTHING`, input.Date); err != nil {
		return nil, fmt.Errorf("ensure daily check-in total: %w", err)
	}

	var total float64
	if err := tx.QueryRowContext(ctx, `
SELECT total_reward::double precision
FROM daily_checkin_totals
WHERE checkin_date = $1
FOR UPDATE`, input.Date).Scan(&total); err != nil {
		return nil, fmt.Errorf("lock daily check-in total: %w", err)
	}
	if total >= input.DailyTotalLimit {
		return nil, service.ErrDailyCheckinExhausted
	}

	existing, err := scanDailyCheckinRecord(ctx, tx, `
SELECT user_id, checkin_date::text, reward::double precision, created_at
FROM daily_checkins
WHERE user_id = $1 AND checkin_date = $2`, input.UserID, input.Date)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, service.ErrDailyCheckinAlready
	}

	remaining := input.DailyTotalLimit - total
	reward := input.Reward
	if input.MinReward > 0 && remaining < input.MinReward {
		return nil, service.ErrDailyCheckinExhausted
	}
	if reward > remaining {
		reward = remaining
	}
	if reward <= 0 {
		return nil, service.ErrDailyCheckinExhausted
	}

	record := service.DailyCheckinRecord{}
	if err := tx.QueryRowContext(ctx, `
INSERT INTO daily_checkins (user_id, checkin_date, reward, created_at, updated_at)
VALUES ($1, $2, $3, NOW(), NOW())
RETURNING user_id, checkin_date::text, reward::double precision, created_at`,
		input.UserID, input.Date, reward,
	).Scan(&record.UserID, &record.Date, &record.Reward, &record.CreatedAt); err != nil {
		if isUniqueConstraintViolation(err) {
			return nil, service.ErrDailyCheckinAlready
		}
		return nil, fmt.Errorf("insert daily check-in: %w", err)
	}

	total += reward
	if _, err := tx.ExecContext(ctx, `
UPDATE daily_checkin_totals
SET total_reward = total_reward + $2, updated_at = NOW()
WHERE checkin_date = $1`, input.Date, reward); err != nil {
		return nil, fmt.Errorf("update daily check-in total: %w", err)
	}

	var balance float64
	if err := tx.QueryRowContext(ctx, `
UPDATE users
SET balance = balance + $2,
    updated_at = NOW()
WHERE id = $1 AND deleted_at IS NULL
RETURNING balance::double precision`, input.UserID, reward).Scan(&balance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrUserNotFound
		}
		return nil, fmt.Errorf("update daily check-in user balance: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit daily check-in transaction: %w", err)
	}
	return &service.DailyCheckinClaimResult{
		Record:            record,
		TodayTotalGranted: total,
		Balance:           balance,
	}, nil
}

func (r *dailyCheckinRepository) ListAdminRecords(ctx context.Context, filter service.DailyCheckinAdminListFilter) ([]service.DailyCheckinAdminRecord, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, fmt.Errorf("daily check-in repository is not configured")
	}

	whereSQL, args := buildDailyCheckinAdminWhere(filter)
	countQuery := `
SELECT COUNT(*)
FROM daily_checkins dc
LEFT JOIN users u ON u.id = dc.user_id
` + whereSQL

	var total int64
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count daily check-in records: %w", err)
	}
	if total == 0 {
		return []service.DailyCheckinAdminRecord{}, 0, nil
	}

	sortExpr := dailyCheckinAdminSortExpression(filter.SortBy)
	sortOrder := "DESC"
	if strings.EqualFold(filter.SortOrder, "asc") {
		sortOrder = "ASC"
	}
	limitParam := len(args) + 1
	offsetParam := len(args) + 2
	listArgs := append(append([]any{}, args...), filter.PageSize, (filter.Page-1)*filter.PageSize)
	query := fmt.Sprintf(`
SELECT dc.user_id,
       COALESCE(u.email, ''),
       COALESCE(u.username, ''),
       dc.checkin_date::text,
       dc.reward::double precision,
       dc.created_at
FROM daily_checkins dc
LEFT JOIN users u ON u.id = dc.user_id
%s
ORDER BY %s %s, dc.created_at DESC, dc.user_id ASC
LIMIT $%d OFFSET $%d`, whereSQL, sortExpr, sortOrder, limitParam, offsetParam)

	rows, err := r.db.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list daily check-in records: %w", err)
	}
	defer rows.Close()

	records := make([]service.DailyCheckinAdminRecord, 0, filter.PageSize)
	for rows.Next() {
		var record service.DailyCheckinAdminRecord
		if err := rows.Scan(
			&record.UserID,
			&record.Email,
			&record.Username,
			&record.CheckinDate,
			&record.Reward,
			&record.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan daily check-in record: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate daily check-in records: %w", err)
	}
	return records, total, nil
}

func buildDailyCheckinAdminWhere(filter service.DailyCheckinAdminListFilter) (string, []any) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 4)
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}

	if filter.UserID > 0 {
		clauses = append(clauses, "dc.user_id = "+addArg(filter.UserID))
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		placeholder := addArg("%" + search + "%")
		clauses = append(clauses, fmt.Sprintf(
			"(u.email ILIKE %s OR u.username ILIKE %s OR dc.user_id::text ILIKE %s)",
			placeholder,
			placeholder,
			placeholder,
		))
	}
	if startDate := strings.TrimSpace(filter.StartDate); startDate != "" {
		clauses = append(clauses, "dc.checkin_date >= "+addArg(startDate)+"::date")
	}
	if endDate := strings.TrimSpace(filter.EndDate); endDate != "" {
		clauses = append(clauses, "dc.checkin_date <= "+addArg(endDate)+"::date")
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func dailyCheckinAdminSortExpression(sortBy string) string {
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "user", "email":
		return "u.email"
	case "username":
		return "u.username"
	case "user_id":
		return "dc.user_id"
	case "checkin_date":
		return "dc.checkin_date"
	case "reward":
		return "dc.reward"
	case "created_at":
		return "dc.created_at"
	default:
		return "dc.created_at"
	}
}

func scanDailyCheckinRecord(ctx context.Context, q sqlQueryer, query string, args ...any) (*service.DailyCheckinRecord, error) {
	var record service.DailyCheckinRecord
	err := scanSingleRow(ctx, q, query, args, &record.UserID, &record.Date, &record.Reward, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}
