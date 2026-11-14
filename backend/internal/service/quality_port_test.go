//go:build unit

package service

// Tests for the fork-specific parts of the quality-rule port: the OpenAI answer
// hook, the platform guard and the quality-only pelican_config contract.

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func qualityProbeOpenAIAccount() *Account {
	return &Account{ID: 91, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"}}
}

func completedOpenAIStream() *http.Response {
	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"))
	return resp
}

func TestQualityProbeSendsPromptEffortAndRecordsRoute(t *testing.T) {
	c, recorder := newTestContext()
	evidence := &QualityBatchEvidence{}
	c.Request = c.Request.WithContext(withPelicanTestOptions(c.Request.Context(),
		pelicanTestOptions{qualityBatch: evidence, prompt: "How many?", reasoningEffort: "high"}))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{completedOpenAIStream()}}
	svc := &AccountTestService{httpUpstream: upstream}

	require.NoError(t, svc.testOpenAIAccountConnection(c, qualityProbeOpenAIAccount(), "gpt-5.4", "How many?", ""))
	require.Len(t, upstream.requests, 1)
	body, err := io.ReadAll(upstream.requests[0].Body)
	require.NoError(t, err)
	require.Equal(t, "How many?", gjson.GetBytes(body, "input.0.content.0.text").String())
	require.Equal(t, "high", gjson.GetBytes(body, "reasoning.effort").String())
	require.False(t, gjson.GetBytes(body, "tools").Exists(), "quality probes never offer tools")
	require.Equal(t, "openai:account", evidence.Route, "without route evidence a batch comparison is always inconclusive")
	require.Equal(t, "gpt-5.4", evidence.MappedModel)
	output, message := parsePelicanOutput(recorder.Body.String())
	require.Empty(t, message)
	require.Equal(t, "21", output)
}

func TestAccountTestWithoutQualityOptionsKeepsConnectivityProbe(t *testing.T) {
	c, _ := newTestContext()
	upstream := &queuedHTTPUpstream{responses: []*http.Response{completedOpenAIStream()}}
	svc := &AccountTestService{httpUpstream: upstream}

	require.NoError(t, svc.testOpenAIAccountConnection(c, qualityProbeOpenAIAccount(), "gpt-5.4", "", ""))
	body, err := io.ReadAll(upstream.requests[0].Body)
	require.NoError(t, err)
	require.Equal(t, "hi", gjson.GetBytes(body, "input.0.content.0.text").String())
	require.False(t, gjson.GetBytes(body, "reasoning").Exists())
}

type qualityPlatformRepo struct {
	AccountRepository
	account *Account
}

func (r *qualityPlatformRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func TestQualityProbeSkipsAccountsWithoutPromptPath(t *testing.T) {
	cfg := pelicanPlan().PelicanConfig
	for _, account := range []*Account{
		{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
		{ID: 8, Platform: PlatformGemini, Type: AccountTypeAPIKey},
	} {
		svc := &AccountTestService{accountRepo: &qualityPlatformRepo{account: account}}
		result, err := svc.RunPelicanBackground(context.Background(), account.ID, "any-model", cfg)
		require.NoError(t, err)
		require.Equal(t, "skipped", result.Status, account.Platform)
		require.Equal(t, qualityErrPlatformUnsupported, result.ErrorMessage)
		require.Equal(t, "any-model", result.PelicanConfig.ModelID)
	}
	require.True(t, qualityProbeSupported(qualityProbeOpenAIAccount()))
	require.False(t, qualityProbeSupported(nil))
}

func TestQualityRunnerNeverGradesSkippedPlatforms(t *testing.T) {
	plans := &qualityPlanRepo{}
	results := &pelicanResults{}
	runner := &ScheduledTestRunnerService{planRepo: plans, scheduledSvc: NewScheduledTestService(plans, results)}
	runner.runPelican = func(_ context.Context, _ int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
		snapshot := *cfg
		snapshot.ModelID = model
		return &ScheduledTestResult{Status: "skipped", ErrorMessage: qualityErrPlatformUnsupported, PelicanConfig: &snapshot}, nil
	}
	runner.judgeQuality = func(context.Context, int64, *PelicanTestConfig, string) *QualityJudgment {
		t.Fatal("skipped samples must not be graded")
		return nil
	}
	plan := pelicanPlan()
	plan.PelicanConfig.Quality = &QualityPolicy{ExpectedAnswer: "21", Action: "disable_scheduling"}
	runner.runOnePlan(context.Background(), plan)
	require.Empty(t, plans.outcomes, "an all-skipped round never reaches the account action")
	require.Len(t, results.results, 2)
	for _, result := range results.results {
		require.Equal(t, "inconclusive", result.QualityAction)
	}
}

func TestPelicanConfigRequiresSupportedQualityRule(t *testing.T) {
	plan := pelicanPlan()
	plan.PelicanConfig.Quality = nil
	_, err := nextPlanRun(plan, time.Now())
	require.ErrorContains(t, err, "requires a quality rule")

	for _, action := range []string{"isolate_models", "remove_models", "enable_bps", "openai_two_fa_admission"} {
		plan = pelicanPlan()
		plan.PelicanConfig.Quality.Action = action
		_, err = nextPlanRun(plan, time.Now())
		require.ErrorContains(t, err, "invalid quality action", action)
	}

	plan = pelicanPlan()
	plan.PelicanConfig.Quality.Judge = &QualityJudgeConfig{Kind: "jev", Prompt: "rubric"}
	_, err = nextPlanRun(plan, time.Now())
	require.ErrorContains(t, err, "unsupported quality judge kind")

	plan = pelicanPlan()
	plan.PelicanConfig.Quality = &QualityPolicy{ExpectedAnswer: "21", Action: "remove_groups"}
	_, err = nextPlanRun(plan, time.Now())
	require.ErrorContains(t, err, "select at least one group")

	plan = pelicanPlan()
	plan.PelicanConfig.Quality = &QualityPolicy{ExpectedAnswer: "21", Action: QualityActionObserveOnly, AutoRestore: true, RemoveGroupIDs: []int64{3}, RetestRestMinutes: 60}
	_, err = nextPlanRun(plan, time.Now())
	require.NoError(t, err)
	require.False(t, plan.PelicanConfig.Quality.AutoRestore, "observation never restores")
	require.Empty(t, plan.PelicanConfig.Quality.RemoveGroupIDs)
	require.Zero(t, plan.PelicanConfig.Quality.RetestRestMinutes, "observation never rests")
}

func TestQualityQuotaAvailable(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	limited := qualityProbeOpenAIAccount()
	limited.RateLimitResetAt = &future
	require.False(t, QualityQuotaAvailable(limited, now))
	require.True(t, QualityQuotaAvailable(qualityProbeOpenAIAccount(), now), "unknown usage is treated as available")
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, RateLimitResetAt: &future}
	require.True(t, QualityQuotaAvailable(apiKey, now), "only OpenAI OAuth quota is inspected")

	runner := &ScheduledTestRunnerService{accountTestSvc: &AccountTestService{accountRepo: &qualityPlatformRepo{account: limited}}}
	plan := pelicanPlan()
	available, err := runner.qualityQuotaAvailable(context.Background(), plan)
	require.NoError(t, err)
	require.False(t, available)
	plan.ManualQualityRun = true
	available, err = runner.qualityQuotaAvailable(context.Background(), plan)
	require.NoError(t, err)
	require.True(t, available, "an explicit run-now request is not skipped")
}
