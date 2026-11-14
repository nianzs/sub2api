//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type sharedBatchPlanRepo struct {
	*qualityPlanRepo
	batch *QualitySharedBatch
	runs  []*QualityBatchRun
	saved *pelicanResults
	err   error
}

func (r *sharedBatchPlanRepo) StartQualityBatch(_ context.Context, candidate *QualitySharedBatch, _ *ScheduledTestPlan, _ time.Time) (*QualitySharedBatch, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.batch == nil {
		r.batch = candidate
	}
	return r.batch, nil
}

func (r *sharedBatchPlanRepo) CollectQualityBatch(_ context.Context, run *QualityBatchRun) error {
	r.runs = append(r.runs, run)
	if r.saved != nil {
		r.saved.results = append(r.saved.results, run.Results...)
	}
	return nil
}

func (r *sharedBatchPlanRepo) FinalizeQualityBatches(ctx context.Context) error {
	for _, run := range r.runs {
		if _, err := r.ApplyQualityOutcome(ctx, run.Plan, run.Until, EvaluateQualityBatch(run, r.runs)); err != nil {
			return err
		}
	}
	return nil
}

func sharedBatchFixture(t *testing.T, batch *QualitySharedBatch, account int64, verdict string) *QualityBatchRun {
	t.Helper()
	plan := multiModelQualityPlan(t, `["test-model"]`)
	plan.ID, plan.AccountID = account, account
	plan.PelicanConfig.QuestionKind = QualityRandomQuestionKind
	plan.PelicanConfig.ReasoningEffort = "medium"
	samples, err := sharedQualitySamples(plan.PelicanConfig, batch)
	require.NoError(t, err)
	samples[0].ModelID = "test-model"
	samples[0].QualityBatch.Route, samples[0].QualityBatch.MappedModel = "synthetic", "test-model"
	result := &ScheduledTestResult{Status: "success", StartedAt: batch.StartedAt.Add(10 * time.Second),
		FinishedAt: batch.StartedAt.Add(20 * time.Second), PelicanConfig: samples[0], QualityJudgment: &QualityJudgment{Verdict: verdict}}
	if verdict != "correct" {
		result.Status = "failed"
		result.ErrorMessage = "answer_mismatch"
	}
	return &QualityBatchRun{Apply: true, Plan: plan, Results: []*ScheduledTestResult{result}}
}

func TestSharedQualityAllWrongKeepsVerdictsButCannotIsolate(t *testing.T) {
	batch, err := newQualitySharedBatch(time.Now())
	require.NoError(t, err)
	a, b := sharedBatchFixture(t, batch, 1, "incorrect"), sharedBatchFixture(t, batch, 2, "incorrect")
	for _, run := range []*QualityBatchRun{a, b} {
		require.Equal(t, "inconclusive", EvaluateQualityBatch(run, []*QualityBatchRun{a, b}))
		require.Equal(t, "incorrect", run.Results[0].QualityJudgment.Verdict)
		require.Equal(t, "no_passing_peer", run.Results[0].PelicanConfig.QualityBatch.Decision)
		require.Equal(t, 1, run.Results[0].PelicanConfig.QualityBatch.PeerFailed)
		require.Equal(t, "inconclusive", run.Plan.QualityModelOutcomes["test-model"])
	}
}

func TestSharedQualityPassingControlDoesNotDependOnFinishOrder(t *testing.T) {
	batch, err := newQualitySharedBatch(time.Now())
	require.NoError(t, err)
	wrong, passed := sharedBatchFixture(t, batch, 1, "incorrect"), sharedBatchFixture(t, batch, 2, "correct")
	for _, peers := range [][]*QualityBatchRun{{wrong, passed}, {passed, wrong}} {
		require.Equal(t, "failed", EvaluateQualityBatch(wrong, peers))
		require.Equal(t, "peer_passed", wrong.Results[0].PelicanConfig.QualityBatch.Decision)
		require.Equal(t, "failed", wrong.Plan.QualityModelOutcomes["test-model"])
		require.Equal(t, "passed", EvaluateQualityBatch(passed, peers))
	}
}

func TestSharedQualityRejectsIncomparableOrIncompleteControls(t *testing.T) {
	changes := map[string]func(*QualityBatchRun){
		"same-account-alias": func(p *QualityBatchRun) { p.Plan.AccountID = 1 },
		"different-model":    func(p *QualityBatchRun) { p.Results[0].PelicanConfig.ModelID = "other" },
		"different-mapping":  func(p *QualityBatchRun) { p.Results[0].PelicanConfig.QualityBatch.MappedModel = "other" },
		"different-route":    func(p *QualityBatchRun) { p.Results[0].PelicanConfig.QualityBatch.Route = "openai:bps" },
		"unknown-route":      func(p *QualityBatchRun) { p.Results[0].PelicanConfig.QualityBatch.Route = "" },
		"stale-signal":       func(p *QualityBatchRun) { p.Apply = false },
		"different-effort":   func(p *QualityBatchRun) { p.Results[0].PelicanConfig.ReasoningEffort = "high" },
		"different-rubric": func(p *QualityBatchRun) {
			p.Results[0].PelicanConfig.Quality.Judge = &QualityJudgeConfig{Prompt: "other rubric"}
		},
		"different-batch":     func(p *QualityBatchRun) { p.Results[0].PelicanConfig.QualityBatch.ID = "other" },
		"different-question":  func(p *QualityBatchRun) { p.Results[0].PelicanConfig.Prompt += " different" },
		"different-reference": func(p *QualityBatchRun) { p.Results[0].PelicanConfig.Quality.ExpectedAnswer = "other" },
		"late-start": func(p *QualityBatchRun) {
			p.Results[0].StartedAt = p.Results[0].PelicanConfig.QualityBatch.StartDeadline
		},
		"late-completion": func(p *QualityBatchRun) {
			p.Results[0].FinishedAt = p.Results[0].PelicanConfig.QualityBatch.ClosesAt.Add(time.Second)
		},
		"transport-error": func(p *QualityBatchRun) { p.Results[0].Status = "failed"; p.Results[0].QualityJudgment = nil },
		"judge-unknown":   func(p *QualityBatchRun) { p.Results[0].QualityJudgment.Verdict = "unknown" },
		"skipped":         func(p *QualityBatchRun) { p.Results[0].Status = "skipped" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			batch, err := newQualitySharedBatch(time.Now())
			require.NoError(t, err)
			wrong, control := sharedBatchFixture(t, batch, 1, "incorrect"), sharedBatchFixture(t, batch, 2, "correct")
			change(control)
			require.Equal(t, "inconclusive", EvaluateQualityBatch(wrong, []*QualityBatchRun{wrong, control}))
			require.Zero(t, wrong.Results[0].PelicanConfig.QualityBatch.PeerPassed)
		})
	}
}

func TestSharedQualityBatchStorageFailureNeverFallsBackToIndependentProbes(t *testing.T) {
	plan := multiModelQualityPlan(t, `["test-model"]`)
	plan.PelicanConfig.QuestionKind = QualityRandomQuestionKind
	plans := &sharedBatchPlanRepo{qualityPlanRepo: &qualityPlanRepo{}, err: errors.New("storage unavailable")}
	saved := &pelicanResults{}
	runner := &ScheduledTestRunnerService{planRepo: plans, scheduledSvc: NewScheduledTestService(plans, saved)}
	runner.runPelican = func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		t.Fatal("no model request may be sent without shared batch storage")
		return nil, nil
	}
	require.True(t, runner.runOnePlan(context.Background(), plan))
	require.Equal(t, []string{"inconclusive"}, plans.outcomes)
	require.Len(t, saved.results, 1)
	require.Equal(t, "unknown", saved.results[0].QualityJudgment.Verdict)
}

func TestSharedQualityRuleValidationDiscardsClientComparisonEvidence(t *testing.T) {
	plan := multiModelQualityPlan(t, `["test-model"]`)
	plan.PelicanConfig.QuestionKind = QualityRandomQuestionKind
	plan.PelicanConfig.QualityBatch = &QualityBatchEvidence{Decision: "peer_passed", PeerPassed: 100}
	_, err := nextPlanRun(plan, time.Now())
	require.NoError(t, err)
	require.Nil(t, plan.PelicanConfig.QualityBatch)
}

func TestSharedQualityExpiredStartDoesNotSendAnotherRequest(t *testing.T) {
	plan := qualityAnswerRetryPlan()
	plan.PelicanConfig.QualityBatch = &QualityBatchEvidence{StartDeadline: time.Now().Add(-time.Second)}
	runner := &ScheduledTestRunnerService{runPelican: func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		t.Fatal("an expired batch cannot send a new request or retry")
		return nil, nil
	}}
	result := runner.runPelicanSample(context.Background(), plan)
	require.Equal(t, "failed", result.Status)
	require.Nil(t, result.QualityJudgment)
}
