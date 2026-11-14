//go:build integration

package repository

// Ported from ranxi2001/sub2api@f895ea743. account_groups has no allowed_models
// column here, and this fork deduplicates identical pending outbox events.

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestQualityActionsRestoreOwnershipAndStaleRuns(t *testing.T) {
	ctx := context.Background()
	for _, action := range []string{"remove_groups", "disable_scheduling"} {
		t.Run(action, func(t *testing.T) {
			var account, group, other int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,status,schedulable) VALUES('quality','openai','apikey','active',true) RETURNING id`).Scan(&account))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES('quality-target','openai') RETURNING id`).Scan(&group))
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform) VALUES('quality-other','openai') RETURNING id`).Scan(&other))
			defer func() {
				_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, account)
				_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, account)
				_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id IN ($1,$2)`, group, other)
			}()
			_, err := integrationDB.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,7),($1,$3,50)`, account, group, other)
			require.NoError(t, err)
			plans := NewScheduledTestPlanRepository(integrationDB)
			results := NewScheduledTestResultRepository(integrationDB)
			svc := service.NewScheduledTestService(plans, results)
			plan, err := svc.CreatePlan(ctx, &service.ScheduledTestPlan{AccountID: account, ModelID: "gpt-test", CronExpression: "*/30 * * * *", Enabled: true, MaxResults: 100, PelicanConfig: &service.PelicanTestConfig{QuestionKind: "candy", Prompt: "test", ReasoningEffort: "high", ParallelCount: 1, Quality: &service.QualityPolicy{ExpectedAnswer: "42", Action: action, RemoveGroupIDs: []int64{group}, AutoRestore: true}}})
			require.NoError(t, err)
			_, err = svc.CreatePlan(ctx, &service.ScheduledTestPlan{AccountID: account, ModelID: plan.ModelID, CronExpression: plan.CronExpression, Enabled: true, PelicanConfig: plan.PelicanConfig})
			require.Error(t, err, "one quarantine owner per account")
			listed, err := plans.ListQualityPlans(ctx)
			require.NoError(t, err)
			require.NotEmpty(t, listed)
			for _, item := range listed {
				if item.ID == plan.ID {
					require.Equal(t, "quality", item.AccountName)
				}
			}
			require.NoError(t, plans.TriggerQuality(ctx, plan.ID))
			plan, err = plans.GetByID(ctx, plan.ID)
			require.NoError(t, err)
			now := time.Now().Truncate(time.Microsecond)
			// The database and test host clocks may differ slightly. Test a due plan
			// using its saved due time instead of depending on clock synchronization.
			if plan.NextRunAt != nil && plan.NextRunAt.After(now) {
				now = *plan.NextRunAt
			}
			until := now.Add(15 * time.Minute)
			ok, err := plans.ClaimPelican(ctx, plan, now, until, now.Add(30*time.Minute))
			require.NoError(t, err)
			require.True(t, ok)
			apply := func(outcome string) string {
				t.Helper()
				got, err := plans.ApplyQualityOutcome(ctx, plan, until, outcome)
				require.NoError(t, err)
				return got
			}
			require.Equal(t, "inconclusive", apply("inconclusive"))
			want := "groups_removed"
			if action == "disable_scheduling" {
				want = "scheduling_disabled"
			}
			require.Equal(t, want, apply("failed"))
			require.Equal(t, "already_quarantined", apply("failed"))
			var count int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_groups WHERE account_id=$1 AND group_id=$2`, account, other).Scan(&count))
			require.Equal(t, 1, count)
			require.Equal(t, "restored", apply("passed"))
			var priority int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT priority FROM account_groups WHERE account_id=$1 AND group_id=$2`, account, group).Scan(&priority))
			require.Equal(t, 7, priority)
			require.Equal(t, want, apply("failed"))
			_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET updated_at=clock_timestamp(),name='manually edited' WHERE id=$1`, account)
			require.NoError(t, err)
			// An unrelated account edit does not cancel restoration of the
			// mutation owned by this quality rule.
			require.Equal(t, "restored", apply("passed"))
			_, err = integrationDB.ExecContext(ctx, `UPDATE scheduled_test_plans SET enabled=false WHERE id=$1`, plan.ID)
			require.NoError(t, err)
			require.Equal(t, "stale_run", apply("failed"))
			result := &service.ScheduledTestResult{QualityRoundID: "round-one", QualityJudgment: &service.QualityJudgment{Verdict: "incorrect", Reason: "different answer", GroupID: group, ModelID: "chosen-judge"}, Status: "failed", ErrorMessage: "answer_mismatch", QualityAction: want, StartedAt: now, FinishedAt: now, PelicanConfig: plan.PelicanConfig}
			require.NoError(t, svc.SaveResult(ctx, plan.ID, 100, result))
			saved, err := svc.ListResults(ctx, plan.ID, 100)
			require.NoError(t, err)
			require.Len(t, saved, 1)
			require.Equal(t, want, saved[0].QualityAction)
			single, err := svc.GetResult(ctx, plan.ID, saved[0].ID)
			require.NoError(t, err)
			require.Equal(t, want, single.QualityAction)
			require.Equal(t, "incorrect", single.QualityJudgment.Verdict)
			require.Equal(t, "round-one", single.QualityRoundID)
			second := *result
			second.Status = "success"
			second.ErrorMessage = ""
			second.QualityJudgment = &service.QualityJudgment{Verdict: "correct", Reason: "equivalent answer"}
			require.NoError(t, svc.SaveResult(ctx, plan.ID, 100, &second))
			operations, err := results.ListQualityHistory(ctx, 0, 1)
			require.NoError(t, err)
			require.Len(t, operations, 1)
			require.Equal(t, 1, operations[0].PassedCount)
			require.Equal(t, 2, operations[0].TotalCount)
			require.Equal(t, "answer_mismatch", operations[0].ErrorMessage)
			require.Len(t, operations[0].ResultIDs, 2)
			require.Empty(t, operations[0].ResponseText, "summary does not include generated output")
			firstRoundID := operations[0].ID
			third := second
			third.QualityRoundID = "round-two"
			third.QualityAction = "restored"
			require.NoError(t, svc.SaveResult(ctx, plan.ID, 100, &third))
			operations, err = results.ListQualityHistory(ctx, 0, 1)
			require.NoError(t, err)
			require.Equal(t, "restored", operations[0].QualityAction)
			previous, err := results.ListQualityHistory(ctx, operations[0].ID, 1)
			require.NoError(t, err)
			require.Len(t, previous, 1)
			require.Equal(t, firstRoundID, previous[0].ID)

			var events int
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type='account_groups_changed'`, account).Scan(&events))
			require.Positive(t, events, "every account change invalidates the scheduler snapshot")
		})
	}
}
