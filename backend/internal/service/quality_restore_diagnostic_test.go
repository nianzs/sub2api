//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qualityDiagnosticPlanRepo struct {
	pelicanPlanRepo
	action  string
	outcome string
}

func (r *qualityDiagnosticPlanRepo) ApplyQualityOutcome(_ context.Context, plan *ScheduledTestPlan, _ time.Time, outcome string) (string, error) {
	r.outcome = outcome
	plan.QualityRestoreDiagnostic = &QualityRestoreDiagnostic{Reason: "membership_already_present", Scope: "remove_groups", Disposition: "external_membership_present", OwnershipRetained: true}
	if r.action == "action_error" {
		return "", errors.New("synthetic transaction failure")
	}
	return r.action, nil
}

func TestQualityRestoreDiagnosticRoundSnapshotAndRejectedActions(t *testing.T) {
	for _, action := range []string{"restore_conflict", "stale_run", "action_conflict", "action_error"} {
		t.Run(action, func(t *testing.T) {
			plans := &qualityDiagnosticPlanRepo{action: action}
			results := &pelicanResults{}
			runner := &ScheduledTestRunnerService{planRepo: plans, scheduledSvc: NewScheduledTestService(plans, results)}
			runner.judgeQuality = func(context.Context, int64, *PelicanTestConfig, string) *QualityJudgment {
				return &QualityJudgment{Verdict: "correct"}
			}
			runner.runPelican = func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
				return &ScheduledTestResult{Status: "success", QualityJudgment: &QualityJudgment{Verdict: "correct"}}, nil
			}
			plan := pelicanPlan()
			plan.PelicanConfig.QuestionKind = "candy"
			plan.PelicanConfig.Quality = &QualityPolicy{ExpectedAnswer: "21", Action: "remove_groups", RemoveGroupIDs: []int64{21}, AutoRestore: true}
			originalConfig := plan.PelicanConfig
			runner.runOnePlan(context.Background(), plan)
			require.Equal(t, "passed", plans.outcome, "both samples must actually pass before the simulated restore")
			require.Len(t, results.results, 2)
			for _, result := range results.results {
				require.Equal(t, action, result.QualityAction)
				if action == "restore_conflict" {
					require.Equal(t, "external_membership_present", result.PelicanConfig.QualityRestoreDiagnostic.Disposition)
					// Stored in the existing result JSON, readable after a process restart.
					raw, err := json.Marshal(result.PelicanConfig)
					require.NoError(t, err)
					var reloaded PelicanTestConfig
					require.NoError(t, json.Unmarshal(raw, &reloaded))
					require.Equal(t, result.PelicanConfig.QualityRestoreDiagnostic, reloaded.QualityRestoreDiagnostic)
				} else {
					require.Nil(t, result.PelicanConfig.QualityRestoreDiagnostic)
				}
			}
			require.Nil(t, originalConfig.QualityRestoreDiagnostic, "shared rule config must not be contaminated")
		})
	}
}

func TestQualityRestoreDiagnosticCannotConfigureRuleOrRewriteOldHistory(t *testing.T) {
	plan := pelicanPlan()
	plan.PelicanConfig.QualityRestoreDiagnostic = &QualityRestoreDiagnostic{Reason: "fabricated"}
	_, err := nextPlanRun(plan, time.Now())
	require.NoError(t, err)
	require.Nil(t, plan.PelicanConfig.QualityRestoreDiagnostic)
	var old ScheduledTestResult
	require.NoError(t, json.Unmarshal([]byte(`{"quality_action":"restore_conflict","pelican_config":{"quality":{"action":"remove_groups"}}}`), &old))
	require.Nil(t, old.PelicanConfig.QualityRestoreDiagnostic, "a legacy conflict has an unknown cause, never infer it from current state")
}
