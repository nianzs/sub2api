package repository

// Quality-rule persistence ported from ranxi2001/sub2api@f895ea743
// (backend/internal/repository/scheduled_test_repo.go), without model isolation,
// BPS, 2FA admission, 5xx triggers, OAuth 401 cleanup and HTML Pelican history.

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// --- Plan Repository ---

type scheduledTestPlanRepository struct {
	db *sql.DB
}

func NewScheduledTestPlanRepository(db *sql.DB) service.ScheduledTestPlanRepository {
	return &scheduledTestPlanRepository{db: db}
}

func (r *scheduledTestPlanRepository) Create(ctx context.Context, plan *service.ScheduledTestPlan) (*service.ScheduledTestPlan, error) {
	return insertScheduledTestPlan(ctx, r.db, plan)
}

type planRowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// insertScheduledTestPlan is shared with group rules, which insert inside their own transaction.
func insertScheduledTestPlan(ctx context.Context, q planRowQuerier, plan *service.ScheduledTestPlan) (*service.ScheduledTestPlan, error) {
	if err := validateQualityGroupReferences(ctx, q, plan.PelicanConfig); err != nil {
		return nil, err
	}
	row := q.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_plans (account_id, model_id, cron_expression, enabled, max_results, auto_recover, next_run_at, created_at, updated_at, pelican_config)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), NOW(), $8)
		RETURNING id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, pelican_config, running_until
	`, plan.AccountID, plan.ModelID, plan.CronExpression, plan.Enabled, plan.MaxResults, plan.AutoRecover, plan.NextRunAt, marshalPelicanConfig(plan.PelicanConfig))
	return scanPlan(row)
}

func (r *scheduledTestPlanRepository) GetByID(ctx context.Context, id int64) (*service.ScheduledTestPlan, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, pelican_config, running_until
		FROM scheduled_test_plans WHERE id = $1
	`, id)
	return scanPlan(row)
}

func (r *scheduledTestPlanRepository) ListByAccountID(ctx context.Context, accountID int64) ([]*service.ScheduledTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, pelican_config, running_until
		FROM scheduled_test_plans WHERE account_id = $1
		ORDER BY created_at DESC, id DESC
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPlans(rows)
}

func (r *scheduledTestPlanRepository) ListDue(ctx context.Context, now time.Time) ([]*service.ScheduledTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, pelican_config, running_until
		FROM scheduled_test_plans
		WHERE enabled = true AND next_run_at <= $1
		ORDER BY next_run_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPlans(rows)
}

func (r *scheduledTestPlanRepository) Update(ctx context.Context, plan *service.ScheduledTestPlan) (*service.ScheduledTestPlan, error) {
	const query = `
		UPDATE scheduled_test_plans
		SET model_id = $2, cron_expression = $3, enabled = $4, max_results = $5, auto_recover = $6, next_run_at = $7, updated_at = NOW(), pelican_config = $8
		WHERE id = $1
		RETURNING id, account_id, model_id, cron_expression, enabled, max_results, auto_recover, last_run_at, next_run_at, created_at, updated_at, pelican_config, running_until
	`
	args := []any{plan.ID, plan.ModelID, plan.CronExpression, plan.Enabled, plan.MaxResults, plan.AutoRecover, plan.NextRunAt, marshalPelicanConfig(plan.PelicanConfig)}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	updated, err := scanPlan(tx.QueryRowContext(ctx, query, args...))
	if err != nil {
		return nil, err
	}
	if err = validateQualityGroupReferences(ctx, tx, updated.PelicanConfig); err != nil {
		return nil, err
	}
	if updated.PelicanConfig != nil && updated.PelicanConfig.Quality != nil && updated.PelicanConfig.Quality.Action == service.QualityActionObserveOnly {
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_quality_states WHERE plan_id=$1`, plan.ID); err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (r *scheduledTestPlanRepository) Delete(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var locked int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM scheduled_test_plans WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	if err := deleteScheduledTestPlanTx(ctx, tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

// The caller holds the plan lock. Deleting a rule never restores or modifies
// the account; any owned quarantine stays in place for the operator to review.
func deleteScheduledTestPlanTx(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM scheduled_test_plans WHERE id=$1`, id)
	return err
}

func (r *scheduledTestPlanRepository) UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE scheduled_test_plans SET last_run_at = $2, next_run_at = $3, updated_at = NOW() WHERE id = $1
	`, id, lastRunAt, nextRunAt)
	return err
}

// --- Result Repository ---

type scheduledTestResultRepository struct {
	db *sql.DB
}

func NewScheduledTestResultRepository(db *sql.DB) service.ScheduledTestResultRepository {
	return &scheduledTestResultRepository{db: db}
}

func (r *scheduledTestResultRepository) Create(ctx context.Context, result *service.ScheduledTestResult) (*service.ScheduledTestResult, error) {
	return insertScheduledTestResult(ctx, r.db, result)
}

func insertScheduledTestResult(ctx context.Context, q planRowQuerier, result *service.ScheduledTestResult) (*service.ScheduledTestResult, error) {
	row := q.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_results (plan_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at, pelican_config, quality_action, quality_judgment, quality_round_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW(), $8, $9, $10, $11)
		RETURNING id, plan_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at, pelican_config, quality_action, quality_judgment, quality_round_id
	`, result.PlanID, result.Status, result.ResponseText, result.ErrorMessage, result.LatencyMs, result.StartedAt, result.FinishedAt, marshalPelicanConfig(result.PelicanConfig), result.QualityAction, marshalQualityJudgment(result.QualityJudgment), result.QualityRoundID)

	out := &service.ScheduledTestResult{}
	var judgment []byte
	var config []byte
	if err := row.Scan(
		&out.ID, &out.PlanID, &out.Status, &out.ResponseText, &out.ErrorMessage,
		&out.LatencyMs, &out.StartedAt, &out.FinishedAt, &out.CreatedAt, &config, &out.QualityAction, &judgment, &out.QualityRoundID,
	); err != nil {
		return nil, err
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &out.PelicanConfig); err != nil {
			return nil, err
		}
	}
	if len(judgment) > 0 {
		if err := json.Unmarshal(judgment, &out.QualityJudgment); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *scheduledTestResultRepository) ListByPlanID(ctx context.Context, planID int64, limit int, includeContent ...bool) ([]*service.ScheduledTestResult, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, plan_id, status, CASE WHEN $3 THEN response_text ELSE '' END, error_message, latency_ms, started_at, finished_at, created_at, pelican_config, quality_action, quality_judgment, quality_round_id
		FROM scheduled_test_results
		WHERE plan_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2
	`, planID, limit, len(includeContent) == 0 || includeContent[0])
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []*service.ScheduledTestResult
	for rows.Next() {
		r := &service.ScheduledTestResult{}
		var judgment []byte
		var config []byte
		if err := rows.Scan(
			&r.ID, &r.PlanID, &r.Status, &r.ResponseText, &r.ErrorMessage,
			&r.LatencyMs, &r.StartedAt, &r.FinishedAt, &r.CreatedAt, &config, &r.QualityAction, &judgment, &r.QualityRoundID,
		); err != nil {
			return nil, err
		}
		if len(config) > 0 {
			if err := json.Unmarshal(config, &r.PelicanConfig); err != nil {
				return nil, err
			}
		}
		if len(judgment) > 0 {
			if err := json.Unmarshal(judgment, &r.QualityJudgment); err != nil {
				return nil, err
			}
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (r *scheduledTestResultRepository) PruneOldResults(ctx context.Context, planID int64, keepCount int) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM scheduled_test_results
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (PARTITION BY plan_id ORDER BY created_at DESC, id DESC) AS rn
				FROM scheduled_test_results
				WHERE plan_id = $1
			) ranked
			WHERE rn > $2
		)
	`, planID, keepCount)
	return err
}

// --- scan helpers ---

type scannable interface {
	Scan(dest ...any) error
}

func scanPlan(row scannable, withAccountName ...bool) (*service.ScheduledTestPlan, error) {
	p := &service.ScheduledTestPlan{}
	var config []byte
	dest := []any{
		&p.ID, &p.AccountID, &p.ModelID, &p.CronExpression, &p.Enabled, &p.MaxResults, &p.AutoRecover,
		&p.LastRunAt, &p.NextRunAt, &p.CreatedAt, &p.UpdatedAt, &config, &p.RunningUntil,
	}
	if len(withAccountName) > 0 && withAccountName[0] {
		dest = append(dest, &p.AccountName)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &p.PelicanConfig); err != nil {
			return nil, err
		}
		var internal struct {
			ManualQualityRun bool `json:"_manual_quality_run"`
		}
		if err := json.Unmarshal(config, &internal); err != nil {
			return nil, err
		}
		p.ManualQualityRun = internal.ManualQualityRun
	}
	return p, nil
}

func scanPlans(rows *sql.Rows, withAccountName ...bool) ([]*service.ScheduledTestPlan, error) {
	var plans []*service.ScheduledTestPlan
	for rows.Next() {
		p, err := scanPlan(rows, withAccountName...)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func marshalPelicanConfig(config *service.PelicanTestConfig) any {
	if config == nil {
		return nil
	}
	data, _ := json.Marshal(config)
	return string(data)
}

// Compare the saved version as well as the due time: a concurrent pause/edit wins.
func (r *scheduledTestPlanRepository) ClaimPelican(ctx context.Context, plan *service.ScheduledTestPlan, now, until, next time.Time) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('pelican-account:' || $1::text, 0))`, plan.AccountID).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_test_plans
 SET running_until = $3, next_run_at = $4, pelican_config = pelican_config - '_manual_quality_run'
 WHERE id = $1 AND enabled = true AND next_run_at <= $2
 AND (running_until IS NULL OR running_until < $2) AND updated_at = $5 AND next_run_at = $6
 AND NOT EXISTS (SELECT 1 FROM account_quality_states rest WHERE rest.plan_id=scheduled_test_plans.id
 AND COALESCE((pelican_config->'quality'->>'retest_rest_minutes')::int,0)>0
 AND (rest.state->>'retest_after')::timestamptz>$2)
 AND EXISTS (SELECT 1 FROM accounts WHERE accounts.id = account_id AND deleted_at IS NULL)
 AND NOT EXISTS (SELECT 1 FROM scheduled_test_plans other WHERE other.account_id = scheduled_test_plans.account_id
 AND other.id <> scheduled_test_plans.id AND other.pelican_config IS NOT NULL AND other.running_until > $2)`, plan.ID, now, until, next, plan.UpdatedAt, plan.NextRunAt)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}

func (r *scheduledTestPlanRepository) FinishPelican(ctx context.Context, id int64, until, finished time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE scheduled_test_plans SET running_until = NULL, last_run_at = $3
 WHERE id = $1 AND running_until = $2`, id, until, finished)
	return err
}

// Also prunes paused plans; bounded batches avoid long transactions on large histories.
func (r *scheduledTestResultRepository) PruneExpiredPelican(ctx context.Context, before time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM scheduled_test_results WHERE id IN (
 SELECT results.id FROM scheduled_test_results results
 JOIN scheduled_test_plans plans ON plans.id = results.plan_id
 WHERE plans.pelican_config IS NOT NULL AND results.created_at < $1
 ORDER BY results.created_at LIMIT 1000)`, before)
	return err
}

func (r *scheduledTestResultRepository) GetResult(ctx context.Context, planID, resultID int64) (*service.ScheduledTestResult, error) {
	out := &service.ScheduledTestResult{}
	var judgment []byte
	var config []byte
	err := r.db.QueryRowContext(ctx, `SELECT id, plan_id, status, response_text, error_message, latency_ms, started_at, finished_at, created_at, pelican_config, quality_action, quality_judgment, quality_round_id
 FROM scheduled_test_results WHERE plan_id = $1 AND id = $2`, planID, resultID).Scan(
		&out.ID, &out.PlanID, &out.Status, &out.ResponseText, &out.ErrorMessage,
		&out.LatencyMs, &out.StartedAt, &out.FinishedAt, &out.CreatedAt, &config, &out.QualityAction, &judgment, &out.QualityRoundID)
	if err != nil {
		return nil, err
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &out.PelicanConfig); err != nil {
			return nil, err
		}
	}
	if len(judgment) > 0 {
		if err := json.Unmarshal(judgment, &out.QualityJudgment); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func marshalQualityJudgment(judgment *service.QualityJudgment) any {
	if judgment == nil {
		return nil
	}
	data, _ := json.Marshal(judgment)
	return string(data)
}
