//go:build integration

package repository

// Ported from ranxi2001/sub2api@f895ea743 with an LLM judge instead of Jev and
// without the account performance monitor assertion.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Disposable migrated PostgreSQL only. No real model calls or production data.
func TestQualitySharedBatchPostgresFinalizationAndRestart(t *testing.T) {
	for _, scenario := range []string{"all-wrong", "passing-peer-finishes-last", "peer-paused", "target-edited", "mapping-edited", "late-peer"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Microsecond)
			start := now.Add(-time.Minute)
			batch := &service.QualitySharedBatch{ID: "synthetic-" + uuid.NewString(), StartedAt: start,
				StartDeadline: now.Add(time.Minute), ClosesAt: now.Add(11 * time.Minute),
				Questions: []service.QualityBatchQuestion{{Prompt: "Synthetic question", Expected: "42",
					Question: &service.QualityRandomQuestion{ID: "synthetic-common", Family: "paths", Version: "fixture"}}}}
			defer func() {
				_, _ = integrationDB.ExecContext(ctx, `DELETE FROM quality_probe_batches WHERE id=$1`, batch.ID)
			}()
			plans := NewScheduledTestPlanRepository(integrationDB)
			repo := plans.(*scheduledTestPlanRepository)
			var runs []*service.QualityBatchRun
			for i := 0; i < 2; i++ {
				var account int64
				require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable)
 VALUES('shared-batch-fixture','openai','apikey','active',true) RETURNING id`).Scan(&account))
				defer func() {
					_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, account)
					_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, account)
				}()
				due := now.Add(-time.Minute)
				plan, err := plans.Create(ctx, &service.ScheduledTestPlan{AccountID: account, ModelID: "test-model",
					CronExpression: "*/5 * * * *", Enabled: true, MaxResults: 100, NextRunAt: &due,
					PelicanConfig: &service.PelicanTestConfig{QuestionKind: service.QualityRandomQuestionKind,
						ReasoningEffort: "medium", ParallelCount: 1,
						Quality: &service.QualityPolicy{Action: "disable_scheduling", Judge: &service.QualityJudgeConfig{GroupID: 1, ModelID: "judge-model", Prompt: "Compare answers"}}}})
				require.NoError(t, err)
				until := now.Add(15 * time.Minute)
				ok, err := plans.ClaimPelican(ctx, plan, now, until, now.Add(5*time.Minute))
				require.NoError(t, err)
				require.True(t, ok)
				// A second primary proposes a different question; the persisted batch wins.
				candidate := *batch
				if i == 1 {
					candidate.Questions = []service.QualityBatchQuestion{{Prompt: "must not replace the first question", Expected: "99"}}
				}
				actual, err := repo.StartQualityBatch(ctx, &candidate, plan, until)
				require.NoError(t, err)
				require.Equal(t, batch.Questions, actual.Questions)
				cfg, policy := *plan.PelicanConfig, *plan.PelicanConfig.Quality
				cfg.Quality = &policy
				cfg.ModelID, cfg.Prompt, cfg.Quality.ExpectedAnswer, cfg.RandomQuestion = "test-model", actual.Questions[0].Prompt, actual.Questions[0].Expected, actual.Questions[0].Question
				cfg.QualityBatch = &service.QualityBatchEvidence{ID: batch.ID, StartedAt: batch.StartedAt,
					StartDeadline: batch.StartDeadline, ClosesAt: batch.ClosesAt, Decision: "pending", Route: "synthetic", MappedModel: "test-model"}
				sampleStarted := time.Now().UTC().Truncate(time.Microsecond)
				result := &service.ScheduledTestResult{PlanID: plan.ID, Status: "failed", ErrorMessage: "answer_mismatch", ResponseText: "41",
					StartedAt: sampleStarted, FinishedAt: sampleStarted.Add(time.Millisecond), QualityRoundID: until.Format(time.RFC3339Nano), QualityAction: "comparison_pending",
					PelicanConfig: &cfg, QualityJudgment: &service.QualityJudgment{Verdict: "incorrect"}}
				if i == 1 && scenario != "all-wrong" {
					result.Status, result.ErrorMessage, result.ResponseText, result.QualityJudgment.Verdict = "success", "", "42", "correct"
				}
				if i == 1 && scenario == "late-peer" {
					result.StartedAt, result.FinishedAt = batch.StartDeadline, batch.StartDeadline.Add(time.Second)
				}
				run := &service.QualityBatchRun{Apply: true, Plan: plan, Until: until, Results: []*service.ScheduledTestResult{result}}
				runs = append(runs, run)
				require.NoError(t, repo.CollectQualityBatch(ctx, run))
				require.NoError(t, repo.CollectQualityBatch(ctx, run), "collection is idempotent")
				require.NoError(t, repo.FinalizeQualityBatches(ctx), "start window still open")
				var enabled bool
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT schedulable FROM accounts WHERE id=$1`, runs[0].Plan.AccountID).Scan(&enabled))
				require.True(t, enabled, "a fast wrong answer must not be isolated before controls finish")
			}
			if scenario == "peer-paused" {
				_, err := integrationDB.ExecContext(ctx, `UPDATE scheduled_test_plans SET enabled=false WHERE id=$1`, runs[1].Plan.ID)
				require.NoError(t, err)
			}
			if scenario == "target-edited" {
				_, err := integrationDB.ExecContext(ctx, `UPDATE scheduled_test_plans SET updated_at=clock_timestamp() WHERE id=$1`, runs[0].Plan.ID)
				require.NoError(t, err)
			}
			if scenario == "mapping-edited" {
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials='{"model_mapping":{"test-model":"different-target"}}'::jsonb WHERE id=$1`, runs[0].Plan.AccountID)
				require.NoError(t, err)
			}
			// Simulate closing the start window without sleeping or making requests.
			_, err := integrationDB.ExecContext(ctx, `UPDATE quality_probe_batches SET start_deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, batch.ID)
			require.NoError(t, err)
			restarted := NewScheduledTestPlanRepository(integrationDB).(*scheduledTestPlanRepository)
			require.NoError(t, restarted.FinalizeQualityBatches(ctx))
			require.NoError(t, restarted.FinalizeQualityBatches(ctx), "finalization is idempotent")
			var enabled bool
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT schedulable FROM accounts WHERE id=$1`, runs[0].Plan.AccountID).Scan(&enabled))
			require.Equal(t, scenario != "passing-peer-finishes-last", enabled)
			var status, verdict, action string
			var raw []byte
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status,quality_judgment->>'verdict',quality_action,pelican_config FROM scheduled_test_results WHERE plan_id=$1`, runs[0].Plan.ID).Scan(&status, &verdict, &action, &raw))
			require.Equal(t, "failed", status)
			require.Equal(t, "incorrect", verdict, "raw verdicts are never rewritten as passes")
			var cfg service.PelicanTestConfig
			require.NoError(t, json.Unmarshal(raw, &cfg))
			if scenario == "passing-peer-finishes-last" {
				require.Equal(t, "scheduling_disabled", action)
				require.Equal(t, "peer_passed", cfg.QualityBatch.Decision)
			} else if scenario == "target-edited" || scenario == "mapping-edited" {
				require.Equal(t, "stale_run", action)
			} else {
				require.Equal(t, "inconclusive", action)
				require.Equal(t, "no_passing_peer", cfg.QualityBatch.Decision)
			}
		})
	}
}
