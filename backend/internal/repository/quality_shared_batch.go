package repository

// Ported from ranxi2001/sub2api@f895ea743 (backend/internal/repository/quality_shared_batch.go)
// without 5xx episodes and OAuth 401 cleanup.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.QualitySharedBatchRepository = (*scheduledTestPlanRepository)(nil)

func (r *scheduledTestPlanRepository) StartQualityBatch(ctx context.Context, candidate *service.QualitySharedBatch, plan *service.ScheduledTestPlan, until time.Time) (*service.QualitySharedBatch, error) {
	questions, err := json.Marshal(candidate.Questions)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO quality_probe_batches(id,started_at,start_deadline,closes_at,questions)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING`, candidate.ID, candidate.StartedAt, candidate.StartDeadline, candidate.ClosesAt, string(questions)); err != nil {
		return nil, err
	}
	batch := &service.QualitySharedBatch{}
	var raw []byte
	// Finalization takes the same batch lock before checking for pending runs.
	err = tx.QueryRowContext(ctx, `SELECT id,started_at,start_deadline,closes_at,questions FROM quality_probe_batches
 WHERE id=$1 AND clock_timestamp()>=started_at AND clock_timestamp()<start_deadline FOR UPDATE`, candidate.ID).
		Scan(&batch.ID, &batch.StartedAt, &batch.StartDeadline, &batch.ClosesAt, &raw)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &batch.Questions); err != nil {
		return nil, err
	}
	run := &service.QualityBatchRun{Plan: plan, Until: until}
	snapshot, err := json.Marshal(run)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO quality_probe_batch_runs(batch_id,plan_id,account_id,lease_until,snapshot)
 SELECT $1,id,account_id,$3,$4 FROM scheduled_test_plans
 WHERE id=$2 AND enabled AND updated_at=$5 AND running_until=$3
 ON CONFLICT(batch_id,plan_id) DO NOTHING`, batch.ID, plan.ID, until, string(snapshot), plan.UpdatedAt)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return nil, fmt.Errorf("quality batch run unavailable")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return batch, nil
}

func (r *scheduledTestPlanRepository) CollectQualityBatch(ctx context.Context, run *service.QualityBatchRun) error {
	if len(run.Results) == 0 || run.Results[0].PelicanConfig == nil || run.Results[0].PelicanConfig.QualityBatch == nil {
		return fmt.Errorf("quality batch results unavailable")
	}
	batchID := run.Results[0].PelicanConfig.QualityBatch.ID
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Same plan -> run lock order as finalization and the existing quality paths.
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM scheduled_test_plans WHERE id=$1 FOR UPDATE`, run.Plan.ID).Scan(&id); err != nil {
		return err
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM quality_probe_batch_runs WHERE batch_id=$1 AND plan_id=$2 AND lease_until=$3 FOR UPDATE`, batchID, run.Plan.ID, run.Until).Scan(&status); err != nil {
		return err
	}
	if status != "pending" {
		return nil // The complete collection was already committed; never replay it.
	}
	stored := *run
	stored.Results = nil
	for _, sample := range run.Results {
		if sample.PelicanConfig == nil || sample.PelicanConfig.QualityBatch == nil || sample.PelicanConfig.QualityBatch.ID != batchID {
			return fmt.Errorf("quality batch sample mismatch")
		}
		created, insertErr := insertScheduledTestResult(ctx, tx, sample)
		if insertErr != nil {
			return insertErr
		}
		// Keep the comparison ledger independent of admin-history pruning, without
		// duplicating model output or provider error bodies in the runtime ledger.
		copy := *sample
		copy.ID, copy.ResponseText = created.ID, ""
		if copy.ErrorMessage != "answer_mismatch" {
			copy.ErrorMessage = ""
		}
		stored.Results = append(stored.Results, &copy)
	}
	var beforeClose bool
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()<=closes_at FROM quality_probe_batches WHERE id=$1`, batchID).Scan(&beforeClose); err != nil {
		return err
	}
	stored.Apply = stored.Apply && beforeClose
	snapshot, err := json.Marshal(&stored)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE quality_probe_batch_runs SET snapshot=$3,status='collected' WHERE batch_id=$1 AND plan_id=$2`, batchID, run.Plan.ID, string(snapshot))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func readQualityBatchResults(ctx context.Context, tx *sql.Tx, batchID string, planID int64) ([]*service.QualityBatchRun, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.snapshot FROM quality_probe_batch_runs r
 JOIN scheduled_test_plans p ON p.id=r.plan_id JOIN accounts a ON a.id=p.account_id
 WHERE r.batch_id=$1 AND r.status IN ('collected','finalized')
 AND (r.plan_id=$2 OR (p.enabled AND p.updated_at=(r.snapshot->'plan'->>'updated_at')::timestamptz
 AND a.deleted_at IS NULL AND a.status='active' AND r.snapshot->>'apply'='true')) ORDER BY r.plan_id`, batchID, planID)
	if err != nil {
		return nil, err
	}
	var runs []*service.QualityBatchRun
	for rows.Next() {
		var raw []byte
		var run service.QualityBatchRun
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &run)
		}
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		if run.Plan == nil || run.Plan.PelicanConfig == nil || run.Plan.PelicanConfig.Quality == nil || len(run.Results) == 0 || len(run.Results) > 100 {
			_ = rows.Close()
			return nil, fmt.Errorf("quality batch snapshot incomplete")
		}
		for _, sample := range run.Results {
			if sample == nil || sample.PelicanConfig == nil || sample.PelicanConfig.QualityBatch == nil {
				_ = rows.Close()
				return nil, fmt.Errorf("quality batch sample incomplete")
			}
		}
		runs = append(runs, &run)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	return runs, nil
}

func (r *scheduledTestPlanRepository) FinalizeQualityBatches(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT r.batch_id,r.plan_id FROM quality_probe_batch_runs r
 JOIN quality_probe_batches b ON b.id=r.batch_id WHERE r.status='collected' AND b.start_deadline<=clock_timestamp()
 AND (b.closes_at<=clock_timestamp() OR NOT EXISTS(SELECT 1 FROM quality_probe_batch_runs p WHERE p.batch_id=b.id AND p.status='pending'))
 ORDER BY b.started_at,r.plan_id LIMIT 100`)
	if err != nil {
		return err
	}
	type target struct {
		batch string
		plan  int64
	}
	var targets []target
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.batch, &t.plan); err != nil {
			_ = rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, t := range targets {
		if err = r.finalizeQualityBatchRun(ctx, t.batch, t.plan); err != nil {
			return err
		}
	}
	_, err = r.db.ExecContext(ctx, `DELETE FROM quality_probe_batches WHERE id IN
 (SELECT id FROM quality_probe_batches WHERE closes_at<clock_timestamp()-interval '7 days' ORDER BY closes_at LIMIT 1000)`)
	return err
}

func (r *scheduledTestPlanRepository) finalizeQualityBatchRun(ctx context.Context, batchID string, planID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var ready bool
	// Serialize with the final possible joining account before reading readiness.
	if err = tx.QueryRowContext(ctx, `SELECT start_deadline<=clock_timestamp() FROM quality_probe_batches WHERE id=$1 FOR UPDATE`, batchID).Scan(&ready); err != nil || !ready {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT b.closes_at<=clock_timestamp() OR NOT EXISTS
 (SELECT 1 FROM quality_probe_batch_runs r WHERE r.batch_id=b.id AND r.status='pending') FROM quality_probe_batches b WHERE b.id=$1`, batchID).Scan(&ready); err != nil || !ready {
		return err
	}
	var id int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM scheduled_test_plans WHERE id=$1 FOR UPDATE`, planID).Scan(&id); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM quality_probe_batch_runs WHERE batch_id=$1 AND plan_id=$2 FOR UPDATE`, batchID, planID).Scan(&status); err == sql.ErrNoRows {
		return nil
	} else if err != nil || status != "collected" {
		return err
	}
	peers, err := readQualityBatchResults(ctx, tx, batchID, planID)
	if err != nil {
		return err
	}
	var run *service.QualityBatchRun
	for _, peer := range peers {
		if peer.Plan.ID == planID {
			run = peer
		}
	}
	if run == nil {
		return fmt.Errorf("quality batch run missing")
	}
	outcome := service.EvaluateQualityBatch(run, peers)
	if _, err = tx.ExecContext(ctx, `SAVEPOINT quality_batch_action`); err != nil {
		return err
	}
	action := "inconclusive"
	commitAction := false
	if run.Apply {
		action, err = applyQualityOutcomeTx(ctx, tx, run.Plan, run.Until, outcome, &commitAction)
		if err != nil {
			return err
		}
	}
	if !commitAction {
		if _, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT quality_batch_action`); err != nil {
			return err
		}
	}
	for _, result := range run.Results {
		cfg := result.PelicanConfig
		cfg.QualityModelOutcomes = run.Plan.QualityModelOutcomes
		if action == "restore_conflict" {
			cfg.QualityRestoreDiagnostic = run.Plan.QualityRestoreDiagnostic
		}
		if _, err = tx.ExecContext(ctx, `UPDATE scheduled_test_results SET quality_action=$2,pelican_config=$3 WHERE id=$1`, result.ID, action, marshalPelicanConfig(cfg)); err != nil {
			return err
		}
	}
	finished := run.Results[0].FinishedAt
	for _, sample := range run.Results {
		if sample.FinishedAt.After(finished) {
			finished = sample.FinishedAt
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scheduled_test_plans SET running_until=NULL,last_run_at=$3
 WHERE id=$1 AND running_until=$2`, planID, run.Until, finished); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE quality_probe_batch_runs SET status='finalized' WHERE batch_id=$1 AND plan_id=$2`, batchID, planID); err != nil {
		return err
	}
	keep := max(1, run.Plan.MaxResults)
	if _, err = tx.ExecContext(ctx, `DELETE FROM scheduled_test_results WHERE plan_id=$1 AND id NOT IN
 (SELECT id FROM scheduled_test_results WHERE plan_id=$1 ORDER BY id DESC LIMIT $2)`, planID, keep); err != nil {
		return err
	}
	return tx.Commit()
}
