//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	mathrand "math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRandomQualityBatchHasFreshBalancedQuestionsWithoutMutatingRule(t *testing.T) {
	cfg := &PelicanTestConfig{QuestionKind: QualityRandomQuestionKind, ParallelCount: 8, Prompt: "stale question",
		Quality: &QualityPolicy{ExpectedAnswer: "stale answer", Action: "disable_scheduling"}}
	samples, err := generateQualityRandomSamples(cfg, strings.NewReader("abcdefghABCDEFGH"))
	require.NoError(t, err)
	require.Len(t, samples, 8)
	families, seen := map[string]int{}, map[string]bool{}
	for _, sample := range samples {
		require.NotEmpty(t, sample.Prompt)
		require.True(t, json.Valid([]byte(sample.Quality.ExpectedAnswer)))
		require.NotNil(t, sample.RandomQuestion)
		require.False(t, seen[sample.RandomQuestion.ID])
		seen[sample.RandomQuestion.ID] = true
		families[sample.RandomQuestion.Family]++
		require.NotContains(t, sample.Prompt, sample.Quality.ExpectedAnswer)
	}
	require.Equal(t, map[string]int{"paths": 4, "weighted_xor": 4}, families)
	require.Equal(t, "stale question", cfg.Prompt)
	require.Equal(t, "stale answer", cfg.Quality.ExpectedAnswer)
	require.Nil(t, cfg.RandomQuestion)
	other, err := generateQualityRandomSamples(cfg, strings.NewReader("ijklmnopIJKLMNOP"))
	require.NoError(t, err)
	require.NotEqual(t, samples[0].RandomQuestion.ID, other[0].RandomQuestion.ID)
	_, err = generateQualityRandomSamples(cfg, strings.NewReader("short"))
	require.ErrorContains(t, err, "entropy unavailable")
}

// Compare the state-counting solver against a separate whole-walk enumeration
// on a small synthetic grid, not a stored production question/answer fixture.
func TestRandomQualityPathOracleAgainstWalkEnumeration(t *testing.T) {
	for turns := 0; turns < 8; turns++ {
		p := qualityPathProblem{Width: 4, Height: 3, Turns: turns,
			Blocked:     [3]qualityGridPoint{{1, 1}, {2, 2}, {3, 0}},
			Checkpoints: [2]qualityGridPoint{{1, 0}, {0, 2}}}
		expected := qualityPathAnswer{}
		for mask := 0; mask < 1<<7; mask++ {
			x, y, hits, changes := 0, 0, 0, 0
			var word strings.Builder
			valid := true
			for i := 0; i < 7; i++ {
				if mask>>i&1 != 0 {
					x++
					_ = word.WriteByte('R')
				} else {
					y++
					_ = word.WriteByte('U')
				}
				for _, point := range p.Blocked {
					if point == (qualityGridPoint{x, y}) {
						valid = false
					}
				}
				for _, point := range p.Checkpoints {
					if point == (qualityGridPoint{x, y}) {
						hits++
					}
				}
			}
			path := word.String()
			for i := 1; i < len(path); i++ {
				if path[i] != path[i-1] {
					changes++
				}
			}
			if valid && x == p.Width && y == p.Height && hits == 1 && changes == turns && !strings.Contains(path, "RRR") {
				expected.Count++
				if expected.First == "" || path < expected.First {
					expected.First = path
				}
			}
		}
		require.Equal(t, expected, solveQualityPath(p))
	}
}

func TestRandomQualityXOROracleFindsAllMinimumWeightRepairs(t *testing.T) {
	rows := []qualityXORObservation{{[]int{0}, 0, 2}, {[]int{0}, 1, 5}, {[]int{1}, 0, 3}}
	answer := solveQualityXOR(rows)
	require.Equal(t, 2, answer.Penalty)
	var expected []string
	for i := 0; i < 16; i++ {
		expected = append(expected, fmt.Sprintf("10%04b", i))
	}
	require.Equal(t, expected, answer.Assignments)
	// Generated references must be nontrivial and bounded without publishing a
	// golden production question or relying on a real model as an oracle.
	rng := mathrand.New(mathrand.NewPCG(7, 11)) //nolint:gosec
	for i := 0; i < 8; i++ {
		_, reference, err := generateQualityXORQuestion(rng)
		require.NoError(t, err)
		value, ok := reference.(qualityXORAnswer)
		require.True(t, ok)
		require.Positive(t, value.Penalty)
		require.NotEmpty(t, value.Assignments)
		require.LessOrEqual(t, len(value.Assignments), 4)
	}
}

func TestRandomQualityValidationDiscardsClientQuestion(t *testing.T) {
	plan := multiModelQualityPlan(t, `["model-a","model-b"]`)
	cfg := plan.PelicanConfig
	cfg.QuestionKind = QualityRandomQuestionKind
	cfg.Prompt = "client cannot choose the next generated question"
	cfg.RandomQuestion = &QualityRandomQuestion{ID: "untrusted"}
	cfg.Quality.Judge = &QualityJudgeConfig{GroupID: 8, ModelID: "judge-model", Prompt: "Compare values"}
	_, err := nextPlanRun(plan, time.Now())
	require.NoError(t, err)
	require.Empty(t, cfg.Prompt)
	require.Empty(t, cfg.Quality.ExpectedAnswer)
	require.Nil(t, cfg.RandomQuestion)
	plan.PelicanConfig.Quality = nil
	_, err = nextPlanRun(plan, time.Now())
	require.Error(t, err)
}

func TestRandomQualityRoundSharesQuestionsAndPreservesUnknownVerdicts(t *testing.T) {
	for _, verdict := range []string{"", "unknown", "correct", "incorrect"} {
		t.Run("judge_"+verdict, func(t *testing.T) {
			plan := multiModelQualityPlan(t, `["model-a","model-b"]`)
			plan.PelicanConfig.QuestionKind = QualityRandomQuestionKind
			plan.PelicanConfig.ParallelCount = 2
			saved := &pelicanResults{}
			plans := &sharedBatchPlanRepo{qualityPlanRepo: &qualityPlanRepo{}, saved: saved}
			runner := &ScheduledTestRunnerService{planRepo: plans, scheduledSvc: NewScheduledTestService(plans, saved)}
			var mu sync.Mutex
			questions := map[string]map[string]string{}
			runner.runPelican = func(_ context.Context, _ int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
				mu.Lock()
				defer mu.Unlock()
				if questions[cfg.RandomQuestion.ID] == nil {
					questions[cfg.RandomQuestion.ID] = map[string]string{}
				}
				questions[cfg.RandomQuestion.ID][model] = cfg.Quality.ExpectedAnswer
				cfg.QualityBatch.Route, cfg.QualityBatch.MappedModel = "synthetic", model
				return &ScheduledTestResult{Status: "success", ResponseText: cfg.Quality.ExpectedAnswer, StartedAt: time.Now(), FinishedAt: time.Now()}, nil
			}
			if verdict != "" {
				runner.judgeQuality = func(context.Context, int64, *PelicanTestConfig, string) *QualityJudgment {
					return &QualityJudgment{Verdict: verdict}
				}
			}
			require.True(t, runner.runOnePlan(context.Background(), plan))
			require.Empty(t, plans.outcomes, "collection must release workers before applying outcomes")
			require.NoError(t, plans.FinalizeQualityBatches(context.Background()))
			require.Len(t, questions, 2)
			for _, references := range questions {
				require.Len(t, references, 2)
				require.Equal(t, references["model-a"], references["model-b"])
			}
			expected := "inconclusive"
			switch verdict {
			case "correct":
				expected = "passed"
			case "incorrect":
				expected = "inconclusive" // No other account supplied a passing control.
			}
			require.Equal(t, []string{expected}, plans.outcomes)
			require.Len(t, saved.results, 4)
			for _, result := range saved.results {
				require.NotNil(t, result.PelicanConfig.RandomQuestion)
				require.NotEmpty(t, result.PelicanConfig.Prompt)
				require.Equal(t, result.PelicanConfig.ModelID, result.PelicanConfig.QualityBatch.MappedModel, "each model owns separate mutable route evidence")
			}
			require.Empty(t, plan.PelicanConfig.Prompt)
			require.Empty(t, plan.PelicanConfig.Quality.ExpectedAnswer)
		})
	}
}

func TestQualityRoundNeverPromotesUncertainResultsToWrongAnswers(t *testing.T) {
	passed := &ScheduledTestResult{Status: "success", QualityJudgment: &QualityJudgment{Verdict: "correct"}}
	unknown := &ScheduledTestResult{Status: "failed", QualityJudgment: &QualityJudgment{Verdict: "unknown"}}
	transport := &ScheduledTestResult{Status: "failed", ErrorMessage: "timeout"}
	require.Equal(t, "inconclusive", qualityRoundOutcome([]*ScheduledTestResult{passed, unknown}))
	require.Equal(t, "inconclusive", qualityRoundOutcome([]*ScheduledTestResult{passed, transport}))
}
