package repository

// Ported from ranxi2001/sub2api@f895ea743 (backend/internal/repository/account_quality.go).
// Trimmed to observe_only / disable_scheduling / remove_groups; model isolation,
// model cooldowns, BPS, 2FA admission and 5xx episodes are not ported.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func (r *scheduledTestPlanRepository) ListQualityPlans(ctx context.Context) ([]*service.ScheduledTestPlan, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT p.id, p.account_id, p.model_id, p.cron_expression, p.enabled, p.max_results, p.auto_recover, p.last_run_at, p.next_run_at, p.created_at, p.updated_at, p.pelican_config, p.running_until, a.name
 FROM scheduled_test_plans p JOIN accounts a ON a.id=p.account_id
 WHERE p.pelican_config->'quality' IS NOT NULL AND a.deleted_at IS NULL ORDER BY p.id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanPlans(rows, true)
}
func (r *scheduledTestPlanRepository) TriggerQuality(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `UPDATE scheduled_test_plans SET next_run_at=NOW(), updated_at=NOW(),
 pelican_config=jsonb_set(pelican_config, '{_manual_quality_run}', 'true'::jsonb)
 WHERE id=$1 AND enabled AND pelican_config->'quality' IS NOT NULL AND (running_until IS NULL OR running_until<NOW())
 AND NOT EXISTS (SELECT 1 FROM account_quality_states rest WHERE rest.plan_id=scheduled_test_plans.id
 AND COALESCE((pelican_config->'quality'->>'retest_rest_minutes')::int,0)>0
 AND (rest.state->>'retest_after')::timestamptz>NOW())`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("plan must be enabled, idle and outside its quality retest rest period")
	}
	return nil
}

type qualityGroup struct {
	AccountID int64     `json:"account_id"`
	GroupID   int64     `json:"group_id"`
	Priority  int       `json:"priority"`
	CreatedAt time.Time `json:"created_at"`
}
type qualityState struct {
	RetestAfter *time.Time `json:"retest_after,omitempty"`
	// Transaction-local evidence, copied to the result even when restore rolls
	// back. Never persist diagnostics as ownership or use them as a restore guard.
	RestoreDiagnostic *service.QualityRestoreDiagnostic `json:"-"`
	Action            string                            `json:"action"`
	AccountVersion    time.Time                         `json:"account_version"`
	Removed           []qualityGroup                    `json:"removed"`
	Remaining         json.RawMessage                   `json:"remaining"`
}

// Lease/version checks, account mutation, ownership and scheduler invalidation
// commit together. A paused, edited, deleted or expired run cannot change accounts.
func (r *scheduledTestPlanRepository) ApplyQualityOutcome(ctx context.Context, plan *service.ScheduledTestPlan, until time.Time, outcome string) (string, error) {
	plan.QualityRestoreDiagnostic = nil
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	commit := false
	action, err := applyQualityOutcomeTx(ctx, tx, plan, until, outcome, &commit)
	if err != nil || !commit {
		return action, err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return action, nil
}

func qualityActionRollsBack(action string) bool {
	switch action {
	case "action_conflict", "restore_conflict", "stale_run", "account_deleted":
		return true
	}
	return false
}

// Caller commits only complete actions; rejected partial transitions roll back.
func applyQualityOutcomeTx(ctx context.Context, tx *sql.Tx, plan *service.ScheduledTestPlan, until time.Time, outcome string, commit *bool) (string, error) {
	action, err := applyQualityOutcomeWithoutRestTx(ctx, tx, plan, until, outcome, commit)
	if err != nil || !*commit || qualityActionRollsBack(action) || plan.PelicanConfig == nil || plan.PelicanConfig.Quality == nil {
		return action, err
	}
	q := plan.PelicanConfig.Quality
	if plan.PelicanConfig.QuestionKind == service.QualityRandomQuestionKind && !plan.QualityBatchEvaluated {
		return action, nil
	}
	if outcome != "failed" || !q.UsesRetestRest() || q.RetestRestMinutes <= 0 {
		return action, nil
	}
	// The plan lock acquired by the action also fences claims on other nodes.
	// Only an actual owned quarantine starts the rest period. Unknown verdicts
	// and transport failures do not.
	_, err = tx.ExecContext(ctx, `WITH resting AS (
 UPDATE account_quality_states SET state=jsonb_set(state,'{retest_after}',
 to_jsonb(clock_timestamp()+make_interval(mins => $2::int)))
 WHERE plan_id=$1 AND state->>'action' IN ('remove_groups','disable_scheduling')
 RETURNING (state->>'retest_after')::timestamptz AS deadline
 ) UPDATE scheduled_test_plans SET next_run_at=GREATEST(next_run_at,resting.deadline)
 FROM resting WHERE id=$1`, plan.ID, q.RetestRestMinutes)
	if err != nil {
		*commit = false
	}
	return action, err
}

func applyQualityOutcomeWithoutRestTx(ctx context.Context, tx *sql.Tx, plan *service.ScheduledTestPlan, until time.Time, outcome string, commit *bool) (string, error) {
	plan.QualityRestoreDiagnostic = nil
	var valid bool
	var err error
	err = tx.QueryRowContext(ctx, `SELECT enabled AND updated_at=$2 AND running_until=$3 AND running_until>NOW()
 FROM scheduled_test_plans WHERE id=$1 FOR UPDATE`, plan.ID, plan.UpdatedAt, until).Scan(&valid)
	if err == sql.ErrNoRows || (err == nil && !valid) {
		return "stale_run", nil
	}
	if err != nil {
		return "", err
	}
	// A stale run needs only its lease/version, not an execution config. Keep
	// that guard before reading optional snapshots or touching account state.
	if plan.PelicanConfig == nil || plan.PelicanConfig.Quality == nil {
		return "inconclusive", nil
	}
	if plan.PelicanConfig.QuestionKind == service.QualityRandomQuestionKind && !plan.QualityBatchEvaluated && outcome != "pending" {
		outcome = "inconclusive"
		plan.QualityModelOutcomes = nil
	}

	state, err := readQualityState(ctx, tx, plan.ID)
	if err != nil {
		return "", err
	}

	if plan.PelicanConfig.Quality.Action == service.QualityActionObserveOnly {
		// Relinquish any former action without restoring or changing account settings.
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_quality_states WHERE plan_id=$1`, plan.ID); err != nil {
			return "", err
		}
		*commit = true
		return "observed", nil
	}
	var version time.Time
	var schedulable bool
	var status string
	err = tx.QueryRowContext(ctx, `SELECT updated_at, schedulable, status FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, plan.AccountID).Scan(&version, &schedulable, &status)
	if err == sql.ErrNoRows {
		return "account_deleted", nil
	}
	if err != nil {
		return "", err
	}
	if plan.QualityBatchEvaluated && len(plan.QualityBatchModels) > 0 {
		var mapping []byte
		account := &service.Account{}
		if err = tx.QueryRowContext(ctx, `SELECT platform,COALESCE(credentials->'model_mapping','{}'::jsonb) FROM accounts WHERE id=$1`, plan.AccountID).Scan(&account.Platform, &mapping); err != nil {
			return "", err
		}
		var models map[string]any
		if err = json.Unmarshal(mapping, &models); err != nil {
			return "", err
		}
		account.Credentials = map[string]any{"model_mapping": models}
		for model, mapped := range plan.QualityBatchModels {
			if mapped == "" || account.GetMappedModel(model) != mapped {
				return "stale_run", nil
			}
		}
	}
	defer func() {
		diagnostic := plan.QualityRestoreDiagnostic
		if diagnostic == nil {
			diagnostic = state.RestoreDiagnostic
		}
		if diagnostic == nil {
			return
		}
		diagnostic.Scope = state.Action
		diagnostic.AccountStatus = status
		diagnostic.Schedulable = &schedulable
		diagnostic.AccountVersion = &version
		if !state.AccountVersion.IsZero() {
			diagnostic.OwnedAccountVersion = &state.AccountVersion
		}
		plan.QualityRestoreDiagnostic = diagnostic
	}()
	q := plan.PelicanConfig.Quality
	// Changing a rule's action does not discard its previous ownership. While
	// holding the account lock, keep another rule from taking over that scope
	// until the previous owner's snapshot has been restored.
	if state.Action == "" {
		var owned bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM account_quality_states s JOIN scheduled_test_plans p ON p.id=s.plan_id
 WHERE p.account_id=$1 AND p.id<>$2 AND COALESCE(s.state->>'action','')<>'')`, plan.AccountID, plan.ID).Scan(&owned)
		if err != nil {
			return "", err
		}
		if owned {
			return "action_conflict", nil
		}
	}
	groups, err := qualityGroups(ctx, tx, plan.AccountID)
	if err != nil {
		return "", err
	}
	action := "no_change"
	changed := false
	switch {
	case outcome == "inconclusive":
		action = "inconclusive"
	case outcome == "failed" && state.Action != "":
		action = "already_quarantined"
	case outcome == "failed":
		state.Action = q.Action
		switch q.Action {
		case "disable_scheduling":
			if schedulable {
				_, err = tx.ExecContext(ctx, `UPDATE accounts SET schedulable=false, updated_at=clock_timestamp() WHERE id=$1`, plan.AccountID)
				changed = true
				action = "scheduling_disabled"
			}
		case "remove_groups":
			err = removeQualityGroups(ctx, tx, plan.AccountID, q.RemoveGroupIDs, &state)
			changed = len(state.Removed) > 0
			if changed {
				action = "groups_removed"
			}
		}
		if err != nil {
			return "", err
		}
		if changed {
			if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM accounts WHERE id=$1`, plan.AccountID).Scan(&state.AccountVersion); err != nil {
				return "", err
			}
			state.Remaining, err = qualityGroups(ctx, tx, plan.AccountID)
			if err != nil {
				return "", err
			}
			if err = qualityUpsertState(ctx, tx, plan.ID, state); err != nil {
				return "", err
			}
		}
	case outcome == "passed" && state.Action != "" && q.AutoRestore:
		// Restore only the mutation owned by this quality rule. Other account or
		// membership edits must not turn an enabled auto-restore rule into a
		// manual cleanup task. A non-active account is not safe to reactivate.
		if status != "active" {
			state.RestoreDiagnostic = qualityRestoreConflict("account_not_active")
			return "restore_conflict", nil
		}
		switch state.Action {
		case "disable_scheduling":
			_, err = tx.ExecContext(ctx, `UPDATE accounts SET schedulable=true, updated_at=clock_timestamp() WHERE id=$1 AND (expires_at IS NULL OR expires_at>NOW())`, plan.AccountID)
			if err == nil {
				var enabled bool
				err = tx.QueryRowContext(ctx, `SELECT schedulable FROM accounts WHERE id=$1`, plan.AccountID).Scan(&enabled)
				if err == nil && !enabled {
					state.RestoreDiagnostic = qualityRestoreConflict("account_expired")
					return "restore_conflict", nil
				}
			}
		case "remove_groups":
			var conflict bool
			conflict, err = restoreQualityGroups(ctx, tx, plan.AccountID, &state)
			if conflict {
				return "restore_conflict", nil
			}
		}
		if err != nil {
			return "", err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_quality_states WHERE plan_id=$1`, plan.ID); err != nil {
			return "", err
		}
		changed = true
		action = "restored"
	case outcome == "passed":
		action = "passed"
	}
	if changed {
		if err = qualityGroupsChanged(ctx, tx, plan.AccountID, groups, state.Removed); err != nil {
			return "", err
		}
	}

	*commit = true
	return action, nil
}
func qualityGroups(ctx context.Context, tx *sql.Tx, accountID int64) ([]byte, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(g) ORDER BY g.group_id),'[]'::jsonb) FROM account_groups g WHERE account_id=$1`, accountID).Scan(&raw)
	return raw, err
}

func qualityUpsertState(ctx context.Context, tx *sql.Tx, planID int64, state qualityState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_quality_states(plan_id,state) VALUES($1,$2)
 ON CONFLICT (plan_id) DO UPDATE SET state=EXCLUDED.state`, planID, string(data))
	return err
}

func readQualityState(ctx context.Context, tx *sql.Tx, planID int64) (qualityState, error) {
	var state qualityState
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT state FROM account_quality_states WHERE plan_id=$1`, planID).Scan(&raw)
	if err == sql.ErrNoRows {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(raw, &state)
	return state, err
}

// Global operation history is cursor-paginated independently of account/rule selection.
// Response bodies are loaded through the existing per-result detail endpoint.

func (r *scheduledTestResultRepository) ListQualityHistory(ctx context.Context, beforeID int64, limit int) ([]*service.QualityHistoryResult, error) {
	rows, err := r.db.QueryContext(ctx, `WITH rounds AS (
 SELECT r.*, a.id AS account_id,a.name AS account_name,
 row_number() OVER round_window AS row_in_round,
 count(*) FILTER (WHERE r.status='success') OVER round_window AS passed_count,
 count(*) FILTER (WHERE r.status='skipped') OVER round_window AS skipped_count,
 GREATEST(count(*) OVER round_window,COALESCE((r.pelican_config->>'parallel_count')::int,0)) AS total_count,
 array_agg(r.id) OVER round_window AS result_ids,
 min(r.started_at) OVER round_window AS round_started_at,
 max(r.finished_at) OVER round_window AS round_finished_at,
 bool_and(r.status='success') OVER round_window AS all_passed,
 bool_or(r.error_message='answer_mismatch') OVER round_window AS any_wrong
 FROM scheduled_test_results r JOIN scheduled_test_plans p ON p.id=r.plan_id JOIN accounts a ON a.id=p.account_id
 WHERE r.pelican_config->'quality' IS NOT NULL AND a.deleted_at IS NULL
 WINDOW round_window AS (PARTITION BY r.plan_id,COALESCE(NULLIF(r.quality_round_id,''),r.id::text) ORDER BY r.id DESC ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)
 ) SELECT id,plan_id,CASE WHEN all_passed THEN 'success' ELSE 'failed' END,
 CASE WHEN any_wrong THEN 'answer_mismatch' ELSE error_message END,
 latency_ms,round_started_at,round_finished_at,created_at,pelican_config,quality_action,quality_judgment,account_id,account_name,passed_count,total_count,skipped_count,result_ids
 FROM rounds WHERE row_in_round=1 AND ($1::bigint=0 OR id<$1) ORDER BY id DESC LIMIT $2`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]*service.QualityHistoryResult, 0)
	for rows.Next() {
		item := &service.QualityHistoryResult{}
		var cfg, judgment []byte
		if err := rows.Scan(&item.ID, &item.PlanID, &item.Status, &item.ErrorMessage, &item.LatencyMs, &item.StartedAt, &item.FinishedAt, &item.CreatedAt, &cfg, &item.QualityAction, &judgment, &item.AccountID, &item.AccountName, &item.PassedCount, &item.TotalCount, &item.SkippedCount, pq.Array(&item.ResultIDs)); err != nil {
			return nil, err
		}
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &item.PelicanConfig); err != nil {
				return nil, err
			}
		}
		if len(judgment) > 0 {
			if err := json.Unmarshal(judgment, &item.QualityJudgment); err != nil {
				return nil, err
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
