//go:build unit

package service

// Ported from ranxi2001/sub2api@f895ea743. Judge candidates must be OpenAI
// accounts in this fork because only that test path carries the grading prompt.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type judgeAccountsStub struct {
	accounts []Account
	group    int64
}

func (s *judgeAccountsStub) ListSchedulableByGroupID(_ context.Context, id int64) ([]Account, error) {
	s.group = id
	return s.accounts, nil
}

type judgeGroupsStub struct{ group *Group }

func (s judgeGroupsStub) GetByID(context.Context, int64) (*Group, error) { return s.group, nil }

type judgeSlotsStub struct {
	acquired, released []int64
	busy               map[int64]bool
}

func (s *judgeSlotsStub) AcquireAccountSlot(_ context.Context, id int64, _ int) (*AcquireResult, error) {
	s.acquired = append(s.acquired, id)
	if s.busy[id] {
		return &AcquireResult{}, nil
	}
	return &AcquireResult{Acquired: true, ReleaseFunc: func() { s.released = append(s.released, id) }}, nil
}
func judgeAccount(id int64) Account {
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true}
}

func judgeFixture() (*QualityJudgeService, *PelicanTestConfig, *judgeAccountsStub, *judgeSlotsStub) {
	accounts := &judgeAccountsStub{accounts: []Account{judgeAccount(1), judgeAccount(2), judgeAccount(3)}}
	slots := &judgeSlotsStub{busy: map[int64]bool{}}
	svc := &QualityJudgeService{accounts: accounts, groups: judgeGroupsStub{&Group{ID: 8, Status: StatusActive}}, slots: slots}
	cfg := &PelicanTestConfig{QuestionKind: "candy", Prompt: "question", Quality: &QualityPolicy{ExpectedAnswer: "21", Judge: &QualityJudgeConfig{GroupID: 8, ModelID: "operator-selected-model", Prompt: "operator rubric"}}}
	return svc, cfg, accounts, slots
}
func TestQualityJudgeSemanticAnswerAndConfiguredGroupModel(t *testing.T) {
	svc, cfg, accounts, slots := judgeFixture()
	svc.request = func(_ context.Context, id int64, model, prompt string) (string, error) {
		require.Equal(t, int64(2), id, "never self-grade")
		require.Equal(t, "operator-selected-model", model)
		require.Contains(t, prompt, `"candidate_answer":"21个。"`)
		require.Contains(t, prompt, "operator rubric")
		return `{"verdict":"correct","reason":"21个与21含义一致"}`, nil
	}
	judged := svc.Judge(context.Background(), 1, cfg, "21个。")
	require.Equal(t, "correct", judged.Verdict)
	require.Equal(t, int64(8), accounts.group)
	require.Equal(t, slots.acquired, slots.released)
	result := &ScheduledTestResult{ResponseText: "21个。"}
	applyQualityJudgment(result, judged)
	require.Equal(t, "passed", qualityOutcome([]*ScheduledTestResult{result}))
	require.Equal(t, "21个。", result.ResponseText)
}
func TestQualityJudgeFailoverAndFailClosed(t *testing.T) {
	svc, cfg, _, slots := judgeFixture()
	calls := 0
	svc.request = func(context.Context, int64, string, string) (string, error) {
		calls++
		if calls == 1 {
			return "", fmt.Errorf("timeout")
		}
		return `{"verdict":"incorrect","reason":"different conclusion"}`, nil
	}
	judged := svc.Judge(context.Background(), 1, cfg, "wrong")
	require.Equal(t, "incorrect", judged.Verdict)
	require.Equal(t, int64(3), judged.AccountID)
	require.Equal(t, slots.acquired, slots.released)
	svc.request = func(context.Context, int64, string, string) (string, error) { return "21", nil }
	judged = svc.Judge(context.Background(), 1, cfg, "21")
	require.Equal(t, "unknown", judged.Verdict)
	result := &ScheduledTestResult{Status: "success"}
	applyQualityJudgment(result, judged)
	require.Equal(t, "inconclusive", qualityOutcome([]*ScheduledTestResult{result}))
	cfg.Quality.Judge = nil
	require.Equal(t, "judge_not_configured", svc.Judge(context.Background(), 1, cfg, "21").Reason)
	require.Equal(t, "inconclusive", qualityOutcome([]*ScheduledTestResult{{Status: "success"}}), "unjudged answers must never restore")
}
func TestQualityJudgeSkipsUnavailableOrUnsupportedAccounts(t *testing.T) {
	svc, cfg, accounts, slots := judgeFixture()
	accounts.accounts[1].Schedulable = false
	slots.busy[3] = true
	svc.request = func(context.Context, int64, string, string) (string, error) {
		t.Fatal("must not issue request")
		return "", nil
	}
	require.Equal(t, "judge_no_available_account", svc.Judge(context.Background(), 1, cfg, "answer").Reason)
	require.Equal(t, []int64{3}, slots.acquired)
}
func TestQualityJudgeRejectsAmbiguousOutput(t *testing.T) {
	for _, output := range []string{`{"verdict":"correct"}`, `{"verdict":true,"reason":"ok"}`, `{"verdict":"correct","verdict":"incorrect","reason":"contradiction"}`, `{"verdict":"incorrect","reason":"no"} trailing`, "```json\n{}\n```", `{"verdict":"correct","reason":"ok","other":true}`, strings.Repeat("x", 8001)} {
		_, err := parseQualityJudgment(output)
		require.Error(t, err)
	}
}
func TestQualityJudgeUnknownDoesNotMaskIncorrectOrPermitRecovery(t *testing.T) {
	correct := &ScheduledTestResult{}
	applyQualityJudgment(correct, &QualityJudgment{Verdict: "correct"})
	unknown := &ScheduledTestResult{}
	applyQualityJudgment(unknown, &QualityJudgment{Verdict: "unknown"})
	incorrect := &ScheduledTestResult{}
	applyQualityJudgment(incorrect, &QualityJudgment{Verdict: "incorrect"})
	require.Equal(t, "inconclusive", qualityOutcome([]*ScheduledTestResult{correct, unknown}))
	require.Equal(t, "failed", qualityOutcome([]*ScheduledTestResult{incorrect, unknown}))
}

func TestQualityRunnerUsesJudgeBeforeMutatingAccount(t *testing.T) {
	plans := &qualityPlanRepo{}
	results := &pelicanResults{}
	runner := &ScheduledTestRunnerService{planRepo: plans, scheduledSvc: NewScheduledTestService(plans, results)}
	runner.runPelican = func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
		return &ScheduledTestResult{Status: "success", ResponseText: "21个。"}, nil
	}
	runner.judgeQuality = func(_ context.Context, _ int64, _ *PelicanTestConfig, answer string) *QualityJudgment {
		require.Equal(t, "21个。", answer)
		return &QualityJudgment{Verdict: "correct", Reason: "equivalent"}
	}
	plan := pelicanPlan()
	plan.PelicanConfig.QuestionKind = "candy"
	plan.PelicanConfig.Quality = &QualityPolicy{ExpectedAnswer: "21", Action: "remove_groups", RemoveGroupIDs: []int64{21}}
	runner.runOnePlan(context.Background(), plan)
	require.Equal(t, []string{"passed"}, plans.outcomes)
	require.Equal(t, "success", results.results[0].Status)
	require.NotEmpty(t, results.results[0].QualityRoundID)
	require.Equal(t, results.results[0].QualityRoundID, results.results[1].QualityRoundID)
}

// Reproduce two overloaded credentials followed by an unsupported route,
// with a usable fourth account that the old three-attempt limit never reached.
func TestQualityJudgeContinuesPastRejectedRoutes(t *testing.T) {
	svc, cfg, accounts, slots := judgeFixture()
	accounts.accounts = nil
	for id := int64(1); id <= 5; id++ {
		accounts.accounts = append(accounts.accounts, judgeAccount(id))
	}
	var called []int64
	svc.request = func(_ context.Context, id int64, _, _ string) (string, error) {
		called = append(called, id)
		if id == 4 {
			return "", fmt.Errorf("API returned 404: model is not available for this group")
		}
		if id < 5 {
			return "", fmt.Errorf("servers overloaded")
		}
		return `{"verdict":"correct","reason":"equivalent"}`, nil
	}
	result := svc.Judge(context.Background(), 1, cfg, "21")
	require.Equal(t, "correct", result.Verdict)
	require.Equal(t, []int64{2, 3, 4, 5}, called)
	require.Equal(t, called, slots.released)
}

func TestQualityJudgeDiversifiesRoutesAndRetainsPeerCredentials(t *testing.T) {
	svc, cfg, accounts, slots := judgeFixture()
	accounts.accounts = nil
	for i, endpoint := range []string{"https://a.example/v1", "https://a.example/v1/", "https://b.example/v1", "https://c.example/v1"} {
		accounts.accounts = append(accounts.accounts, Account{
			ID: int64(i + 10), Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Priority: i,
			Credentials: map[string]any{"base_url": endpoint},
		})
	}
	var called []int64
	svc.request = func(_ context.Context, id int64, _, _ string) (string, error) {
		called = append(called, id)
		if id != 11 {
			return "", fmt.Errorf("model unavailable")
		}
		return `{"verdict":"correct","reason":"equivalent"}`, nil
	}
	result := svc.Judge(context.Background(), 1, cfg, "21")
	require.Equal(t, "correct", result.Verdict)
	require.Equal(t, []int64{10, 12, 13, 11}, called)
	require.Equal(t, called, slots.released)
	require.Equal(t, int64(11), accounts.accounts[1].ID, "do not reorder repository-owned storage")
}

func TestQualityJudgeExhaustionAttemptsEachAccountOnce(t *testing.T) {
	svc, cfg, accounts, slots := judgeFixture()
	accounts.accounts = append(accounts.accounts, accounts.accounts[1])
	svc.request = func(context.Context, int64, string, string) (string, error) {
		return "", fmt.Errorf("model unavailable")
	}
	result := svc.Judge(context.Background(), 1, cfg, "21")
	require.Equal(t, "unknown", result.Verdict)
	require.Equal(t, "judge_request_failed", result.Reason)
	require.Equal(t, []int64{2, 3}, slots.acquired)
	require.Equal(t, slots.acquired, slots.released)
}

func TestQualityJudgeCancellationAndAttemptDeadline(t *testing.T) {
	t.Run("already cancelled", func(t *testing.T) {
		svc, cfg, _, slots := judgeFixture()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		svc.request = func(context.Context, int64, string, string) (string, error) {
			t.Fatal("cancelled judge must not send requests")
			return "", nil
		}
		require.Equal(t, "unknown", svc.Judge(ctx, 1, cfg, "21").Verdict)
		require.Empty(t, slots.acquired)
	})
	t.Run("cancel during request", func(t *testing.T) {
		svc, cfg, _, slots := judgeFixture()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var attempt context.Context
		svc.request = func(ctx context.Context, _ int64, _, _ string) (string, error) {
			attempt = ctx
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.LessOrEqual(t, time.Until(deadline), 30*time.Second)
			cancel()
			return "", ctx.Err()
		}
		require.Equal(t, "unknown", svc.Judge(ctx, 1, cfg, "21").Verdict)
		require.Equal(t, []int64{2}, slots.acquired)
		require.Equal(t, slots.acquired, slots.released)
		require.ErrorIs(t, attempt.Err(), context.Canceled)
	})
}

func TestQualityJudgeSkipsAccountsWithoutPromptPath(t *testing.T) {
	svc, cfg, accounts, slots := judgeFixture()
	accounts.accounts[1].Platform = PlatformAnthropic
	var called []int64
	svc.request = func(_ context.Context, id int64, _, _ string) (string, error) {
		called = append(called, id)
		return `{"verdict":"correct","reason":"equivalent"}`, nil
	}
	require.Equal(t, "correct", svc.Judge(context.Background(), 1, cfg, "21").Verdict)
	require.Equal(t, []int64{3}, called, "a non-OpenAI judge would answer the connectivity probe")
	require.Equal(t, []int64{3}, slots.acquired)
}
