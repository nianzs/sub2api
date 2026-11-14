//go:build unit

package service

// Test doubles ported from ranxi2001/sub2api@f895ea743
// (pelican_scheduled_test.go and account_quality_test.go).

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pelicanPlan returns a quality rule; this fork only runs quality rules through pelican_config.
func pelicanPlan() *ScheduledTestPlan {
	return &ScheduledTestPlan{ID: 1, AccountID: 42, ModelID: "gpt-6-astra", CronExpression: "*/30 * * * *", Enabled: true, MaxResults: 50,
		PelicanConfig: &PelicanTestConfig{QuestionKind: "candy", Prompt: "question", ReasoningEffort: "medium", ParallelCount: 2,
			Quality: &QualityPolicy{ExpectedAnswer: "21", Action: QualityActionObserveOnly}}}
}

type pelicanPlanRepo struct {
	ScheduledTestPlanRepository
	mu       sync.Mutex
	claimed  bool
	finished bool
}

func (r *pelicanPlanRepo) ClaimPelican(context.Context, *ScheduledTestPlan, time.Time, time.Time, time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed {
		return false, nil
	}
	r.claimed = true
	return true, nil
}
func (r *pelicanPlanRepo) FinishPelican(context.Context, int64, time.Time, time.Time) error {
	r.finished = true
	return nil
}

type pelicanResults struct {
	ScheduledTestResultRepository
	results []*ScheduledTestResult
	pruned  int
}

func (r *pelicanResults) Create(ctx context.Context, result *ScheduledTestResult) (*ScheduledTestResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.results = append(r.results, result)
	return result, nil
}
func (r *pelicanResults) PruneOldResults(_ context.Context, _ int64, count int) error {
	r.pruned = count
	return nil
}

type qualityPlanRepo struct {
	pelicanPlanRepo
	outcomes []string
}

func (r *qualityPlanRepo) ApplyQualityOutcome(_ context.Context, _ *ScheduledTestPlan, _ time.Time, outcome string) (string, error) {
	r.outcomes = append(r.outcomes, outcome)
	return "groups_removed", nil
}

func multiModelQualityPlan(t *testing.T, models string) *ScheduledTestPlan {
	t.Helper()
	plan := pelicanPlan()
	plan.PelicanConfig.ParallelCount = 1
	plan.PelicanConfig.QuestionKind = "candy"
	plan.PelicanConfig.Quality = &QualityPolicy{ExpectedAnswer: "21", Action: "disable_scheduling"}
	require.NoError(t, json.Unmarshal([]byte(`{"model_ids":`+models+`}`), plan.PelicanConfig))
	return plan
}
