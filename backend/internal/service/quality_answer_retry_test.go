//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func qualityAnswerRetryPlan() *ScheduledTestPlan {
	plan := pelicanPlan()
	plan.PelicanConfig.QuestionKind = "candy"
	plan.PelicanConfig.Quality = &QualityPolicy{ExpectedAnswer: "21", Action: QualityActionObserveOnly}
	return plan
}

func TestQualityAnswerRetriesSamePreparedRandomQuestionBeforeGrading(t *testing.T) {
	for _, verdict := range []string{"correct", "incorrect"} {
		t.Run(verdict, func(t *testing.T) {
			plan := qualityAnswerRetryPlan()
			plan.PelicanConfig.QuestionKind = QualityRandomQuestionKind
			samples, err := generateQualityRandomSamples(plan.PelicanConfig, strings.NewReader("abcdefghABCDEFGH"))
			require.NoError(t, err)
			plan.PelicanConfig = samples[0]
			prepared := *plan.PelicanConfig
			policy := *prepared.Quality
			question := *prepared.RandomQuestion
			calls, grades := 0, 0
			runner := &ScheduledTestRunnerService{}
			runner.runPelican = func(_ context.Context, account int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
				calls++
				require.Equal(t, plan.AccountID, account)
				require.Equal(t, plan.ModelID, model)
				require.Same(t, plan.PelicanConfig, cfg)
				require.Equal(t, prepared, *cfg)
				require.Equal(t, policy, *cfg.Quality)
				require.Equal(t, question, *cfg.RandomQuestion)
				require.Zero(t, grades)
				if calls == 1 {
					return &ScheduledTestResult{Status: "failed", ErrorMessage: `API returned 503: {"error":{"code":"local_capacity_exhausted"}}`}, nil
				}
				now := time.Now()
				return &ScheduledTestResult{Status: "success", ResponseText: "candidate answer", StartedAt: now, FinishedAt: now}, nil
			}
			runner.judgeQuality = func(_ context.Context, account int64, cfg *PelicanTestConfig, answer string) *QualityJudgment {
				grades++
				require.Equal(t, plan.AccountID, account)
				require.Same(t, plan.PelicanConfig, cfg)
				require.Equal(t, "candidate answer", answer)
				return &QualityJudgment{Verdict: verdict}
			}
			started := time.Now()
			result := runner.runPelicanSample(context.Background(), plan)
			require.Equal(t, 2, calls)
			require.Equal(t, 1, grades)
			require.Equal(t, verdict, result.QualityJudgment.Verdict)
			require.False(t, result.StartedAt.Before(started))
			require.GreaterOrEqual(t, result.LatencyMs, int64(1000), "latency includes retry backoff")
			require.Equal(t, result.FinishedAt.Sub(result.StartedAt).Milliseconds(), result.LatencyMs)
			if verdict == "correct" {
				require.Equal(t, "passed", qualityRoundOutcome([]*ScheduledTestResult{result}))
			} else {
				require.Equal(t, "answer_mismatch", result.ErrorMessage)
				require.Equal(t, "failed", qualityRoundOutcome([]*ScheduledTestResult{result}))
			}
		})
	}
}

func TestQualityAnswerRetryExhaustionStaysInconclusive(t *testing.T) {
	calls := 0
	runner := &ScheduledTestRunnerService{}
	runner.runPelican = func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		calls++
		return &ScheduledTestResult{Status: "failed", ErrorMessage: fmt.Sprintf("API returned 503: busy attempt %d", calls)}, nil
	}
	runner.judgeQuality = func(context.Context, int64, *PelicanTestConfig, string) *QualityJudgment {
		t.Fatal("HTTP rejection must not be graded")
		return nil
	}
	result := runner.runPelicanSample(context.Background(), qualityAnswerRetryPlan())
	require.Equal(t, 3, calls)
	require.Equal(t, "API returned 503: busy attempt 3", result.ErrorMessage)
	require.Nil(t, result.QualityJudgment)
	require.GreaterOrEqual(t, result.LatencyMs, int64(3000))
	passed := &ScheduledTestResult{Status: "success", QualityJudgment: &QualityJudgment{Verdict: "correct"}}
	require.Equal(t, "inconclusive", qualityRoundOutcome([]*ScheduledTestResult{passed, result}))
}

func TestQualityAnswerRetryHonorsCancellation(t *testing.T) {
	for _, preCanceled := range []bool{false, true} {
		t.Run(fmt.Sprint(preCanceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if preCanceled {
				cancel()
			}
			calls := 0
			runner := &ScheduledTestRunnerService{runPelican: func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
				calls++
				cancel()
				return &ScheduledTestResult{Status: "failed", ErrorMessage: "API returned 503: busy"}, nil
			}}
			result := runner.runPelicanSample(ctx, qualityAnswerRetryPlan())
			if preCanceled {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
			require.Equal(t, context.Canceled.Error(), result.ErrorMessage)
			require.Nil(t, result.QualityJudgment)
		})
	}
}

func TestQualityAnswerRetryStopsAtDeadlineDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	calls := 0
	runner := &ScheduledTestRunnerService{runPelican: func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		calls++
		return &ScheduledTestResult{Status: "failed", ErrorMessage: "API returned 503: busy"}, nil
	}}
	result := runner.runPelicanSample(ctx, qualityAnswerRetryPlan())
	require.Equal(t, 1, calls)
	require.Equal(t, context.DeadlineExceeded.Error(), result.ErrorMessage)
}

func TestQualityAnswerDoesNotReplayOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ScheduledTestPlan)
		result *ScheduledTestResult
		err    error
	}{
		{name: "partial_output", result: &ScheduledTestResult{Status: "failed", ResponseText: "partial", ErrorMessage: "API returned 503: busy"}},
		{name: "whitespace_output", result: &ScheduledTestResult{Status: "failed", ResponseText: " ", ErrorMessage: "API returned 503: busy"}},
		{name: "timeout", result: &ScheduledTestResult{Status: "failed", ErrorMessage: "Request failed: context deadline exceeded"}},
		{name: "EOF", result: &ScheduledTestResult{Status: "failed", ErrorMessage: "Request failed: EOF"}},
		{name: "request_error", err: errors.New("request failed")},
		{name: "nil_result"},
		{name: "unauthorized", result: &ScheduledTestResult{Status: "failed", ErrorMessage: "API returned 401: unauthorized"}},
		{name: "unsupported_model", result: &ScheduledTestResult{Status: "failed", ErrorMessage: "API returned 400: model_not_supported"}},
		{name: "embedded_status", result: &ScheduledTestResult{Status: "failed", ErrorMessage: "API returned 400: API returned 503: busy"}},
		{name: "judged_incorrect", result: &ScheduledTestResult{Status: "failed", ErrorMessage: "answer_mismatch", QualityJudgment: &QualityJudgment{Verdict: "incorrect"}}},
		{name: "judged_unknown", result: &ScheduledTestResult{Status: "failed", ErrorMessage: "judge_inconclusive", QualityJudgment: &QualityJudgment{Verdict: "unknown"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := qualityAnswerRetryPlan()
			if tc.change != nil {
				tc.change(plan)
			}
			calls := 0
			runner := &ScheduledTestRunnerService{runPelican: func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
				calls++
				return tc.result, tc.err
			}}
			result := runner.runPelicanSample(context.Background(), plan)
			require.Equal(t, 1, calls)
			require.Equal(t, "failed", result.Status)
		})
	}
}

func TestQualityAnswerRetryRecognizesExplicitHTTPRejections(t *testing.T) {
	cfg := qualityAnswerRetryPlan().PelicanConfig
	for _, prefix := range []string{"API returned ", "Chat Completions API (/v1/chat/completions) returned "} {
		for _, status := range []int{400, 401, 403, 404, 408, 429, 500, 501, 502, 503, 504, 529} {
			result := &ScheduledTestResult{Status: "failed", ErrorMessage: fmt.Sprintf("%s%d: busy", prefix, status)}
			want := status == 429 || status == 500 || status == 502 || status == 503 || status == 504
			require.Equal(t, want, retryableQualityAnswer(cfg, result), result.ErrorMessage)
		}
	}
	for _, message := range []string{"API returned 5030: busy", "API returned bad: busy", "API returned 503", "stream error: API returned 503: busy"} {
		require.False(t, retryableQualityAnswer(cfg, &ScheduledTestResult{Status: "failed", ErrorMessage: message}))
	}
}
