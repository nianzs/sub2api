//go:build unit

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type iqCheckTestRepo struct {
	*upstreamBillingProbeAccountRepo
	schedulableCalls []bool
}

func (r *iqCheckTestRepo) SetSchedulable(_ context.Context, id int64, schedulable bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accounts[id].Schedulable = schedulable
	r.schedulableCalls = append(r.schedulableCalls, schedulable)
	return nil
}

func (r *iqCheckTestRepo) account(t *testing.T, id int64) *Account {
	t.Helper()
	account, err := r.GetByID(context.Background(), id)
	require.NoError(t, err)
	return account
}

type iqCheckTestLock struct{ deny bool }

func (l *iqCheckTestLock) TryAcquireLeaderLock(context.Context, string, string, time.Duration) (bool, error) {
	return !l.deny, nil
}
func (l *iqCheckTestLock) ReleaseLeaderLock(context.Context, string, string) error { return nil }

// iqCheckHarness answers with the configured mode: "right", "wrong" or "error".
type iqCheckHarness struct {
	repo   *iqCheckTestRepo
	svc    *IQCheckService
	now    time.Time
	mode   atomic.Value
	probes atomic.Int32
}

var iqCheckTestQuestions = []iqCheckQuestion{
	{Family: "paths", Prompt: "path?", Expected: iqPathAnswer{Count: 5, First: "RRUU"}},
	{Family: "weighted_xor", Prompt: "xor?", Expected: iqXORAnswer{Penalty: 2, Assignments: []string{"000001"}}},
}

func newIQCheckHarness(t *testing.T, accounts ...*Account) *iqCheckHarness {
	t.Helper()
	stored := map[int64]*Account{}
	for _, account := range accounts {
		stored[account.ID] = account
	}
	h := &iqCheckHarness{
		repo: &iqCheckTestRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: stored}},
		now:  time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC),
	}
	h.mode.Store("right")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.svc = &IQCheckService{
		accounts:     h.repo,
		newQuestions: func() ([]iqCheckQuestion, error) { return iqCheckTestQuestions, nil },
		now:          func() time.Time { return h.now },
		slots:        make(chan struct{}, iqCheckMaxConcurrent),
		ctx:          ctx,
		cancel:       cancel,
	}
	h.svc.probe = func(_ context.Context, _ int64, _ string, prompt string) (string, error) {
		h.probes.Add(1)
		switch h.mode.Load().(string) {
		case "error":
			return "", errors.New("upstream 502: bad gateway")
		case "wrong":
			return `{"count":4,"first":"RRUU","penalty":1,"assignments":["000001"]}`, nil
		case "one":
			if prompt == "path?" {
				return `{"count":4,"first":"RRUU"}`, nil
			}
		}
		if prompt == "path?" {
			return `{"count":5,"first":"RRUU"}`, nil
		}
		return "```json\n{\"penalty\":2,\"assignments\":[\"000001\"]}\n```", nil
	}
	return h
}

// round runs one due cycle and waits for its checks, then moves past the interval.
func (h *iqCheckHarness) round(mode string) {
	h.mode.Store(mode)
	h.svc.RunDue(context.Background())
	h.svc.wg.Wait()
	h.now = h.now.Add(61 * time.Minute)
}

func iqCheckTestAccount(id int64) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{IQCheckEnabledExtraKey: true, IQCheckAutoDisableExtraKey: true}}
}

func (h *iqCheckHarness) state(t *testing.T) IQCheckState {
	t.Helper()
	return decodeIQCheckState(h.repo.account(t, 1).Extra)
}

func (h *iqCheckHarness) setExtra(key string, value any) {
	h.repo.mu.Lock()
	defer h.repo.mu.Unlock()
	if value == nil {
		delete(h.repo.accounts[1].Extra, key)
		return
	}
	h.repo.accounts[1].Extra[key] = value
}

func TestIQCheckStopsAtThreeWrongInTwoRoundsAndRestoresOnCleanRound(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))

	h.round("wrong")
	require.Equal(t, IQCheckResultFailed, h.state(t).Result)
	require.Equal(t, 2, h.state(t).RecentWrong)
	require.True(t, h.repo.account(t, 1).Schedulable, "two wrong answers must not stop scheduling")

	h.round("one")
	require.Equal(t, 3, h.state(t).RecentWrong)
	require.True(t, h.state(t).DisabledByCheck)
	require.False(t, h.repo.account(t, 1).Schedulable)

	h.round("one")
	require.False(t, h.repo.account(t, 1).Schedulable, "restoring needs a fully correct round")

	h.round("right")
	state := h.state(t)
	require.Equal(t, IQCheckResultPassed, state.Result)
	require.False(t, state.DisabledByCheck)
	require.Zero(t, state.RecentWrong)
	require.Equal(t, 8, state.Answered)
	require.Equal(t, 4, state.AnsweredCorrect)
	require.True(t, h.repo.account(t, 1).Schedulable)
	require.Equal(t, []bool{false, true}, h.repo.schedulableCalls)
}

func TestIQCheckOneWrongAnswerPerRoundNeverStops(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	for i := 0; i < 4; i++ {
		h.round("one")
	}
	require.Equal(t, 2, h.state(t).RecentWrong)
	require.True(t, h.repo.account(t, 1).Schedulable)
	require.Empty(t, h.repo.schedulableCalls)
}

func TestIQCheckRecordOnlyCountsButNeverStops(t *testing.T) {
	account := iqCheckTestAccount(1)
	delete(account.Extra, IQCheckAutoDisableExtraKey)
	h := newIQCheckHarness(t, account)
	h.round("wrong")
	h.round("wrong")
	h.round("right")
	state := h.state(t)
	require.Equal(t, 2, state.RecentWrong)
	require.Equal(t, 6, state.Answered)
	require.Equal(t, 2, state.AnsweredCorrect)
	require.Empty(t, h.repo.schedulableCalls)
}

func TestIQCheckRestoresAfterSwitchingToRecordOnly(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	h.round("wrong")
	h.round("wrong")
	require.False(t, h.repo.account(t, 1).Schedulable)
	h.setExtra(IQCheckAutoDisableExtraKey, nil)
	h.round("right")
	require.True(t, h.repo.account(t, 1).Schedulable)
	require.False(t, h.state(t).DisabledByCheck)
}

func TestIQCheckRoundWithoutAnswersKeepsTheWindow(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	h.round("wrong")
	h.round("error")
	state := h.state(t)
	require.Equal(t, IQCheckResultUnknown, state.Result)
	require.Equal(t, "upstream 502: bad gateway", state.Error)
	require.Equal(t, 2, state.LastRoundWrong)
	require.Equal(t, 2, state.Answered)
	h.round("one")
	require.False(t, h.repo.account(t, 1).Schedulable)
}

func TestIQCheckStatsRestartWhenTheModelChanges(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	h.round("right")
	require.Equal(t, 2, h.state(t).Answered)
	h.setExtra(IQCheckModelExtraKey, "gpt-5.5")
	h.round("one")
	state := h.state(t)
	require.Equal(t, "gpt-5.5", state.StatsModel)
	require.Equal(t, 2, state.Answered)
	require.Equal(t, 1, state.AnsweredCorrect)
}

func TestIQCheckClaimsAccountBeforeChecking(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	h.svc.RunDue(context.Background())
	h.svc.wg.Wait()
	h.svc.RunDue(context.Background()) // Same minute: not due again.
	h.svc.wg.Wait()
	require.EqualValues(t, 2, h.probes.Load())
	require.Equal(t, h.now.Add(60*time.Minute), *h.state(t).NextCheckAt)
}

func TestIQCheckSkipsIneligibleAccounts(t *testing.T) {
	disabled := iqCheckTestAccount(2)
	disabled.Extra[IQCheckEnabledExtraKey] = false
	inactive := iqCheckTestAccount(3)
	inactive.Status = StatusError
	other := iqCheckTestAccount(4)
	other.Platform = PlatformAnthropic
	h := newIQCheckHarness(t, disabled, inactive, other)
	h.round("wrong")
	require.Zero(t, h.probes.Load())

	locked := newIQCheckHarness(t, iqCheckTestAccount(1))
	locked.svc.lockCache = &iqCheckTestLock{deny: true}
	locked.round("wrong")
	require.Zero(t, locked.probes.Load())
}

func TestIQCheckLeavesManuallyDisabledAccountsAlone(t *testing.T) {
	account := iqCheckTestAccount(1)
	account.Schedulable = false
	h := newIQCheckHarness(t, account)
	h.round("wrong")
	h.round("wrong")
	h.round("right")
	require.False(t, h.repo.account(t, 1).Schedulable)
	require.Empty(t, h.repo.schedulableCalls)
	// An account turned off by hand is not probed at all.
	require.Zero(t, h.probes.Load())
}

func TestIQCheckSkipsTemporarilyUnavailableAccounts(t *testing.T) {
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	rateLimited := iqCheckTestAccount(1)
	rateLimited.RateLimitResetAt = &future
	overloaded := iqCheckTestAccount(2)
	overloaded.OverloadUntil = &future
	coolingDown := iqCheckTestAccount(3)
	coolingDown.TempUnschedulableUntil = &future
	expired := iqCheckTestAccount(4)
	expired.AutoPauseOnExpired = true
	expired.ExpiresAt = &past
	h := newIQCheckHarness(t, rateLimited, overloaded, coolingDown, expired)

	h.round("wrong")

	require.Zero(t, h.probes.Load())
	for id := int64(1); id <= 4; id++ {
		// Not claimed: checked on the first tick after it is available again.
		require.Nil(t, decodeIQCheckState(h.repo.account(t, id).Extra).NextCheckAt, "account %d", id)
	}
}

func TestIQCheckKeepsCheckingAnAccountItStopped(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	h.round("wrong")
	h.round("wrong")
	require.False(t, h.repo.account(t, 1).Schedulable)
	probes := h.probes.Load()

	h.round("right")

	require.Greater(t, h.probes.Load(), probes)
	require.True(t, h.repo.account(t, 1).Schedulable)
}

func TestIQCheckQuestionPanicCountsAsFailedRequest(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	answer := h.svc.probe
	h.svc.probe = func(ctx context.Context, id int64, model, prompt string) (string, error) {
		if prompt == "path?" {
			panic("boom")
		}
		return answer(ctx, id, model, prompt)
	}

	require.NotPanics(t, func() { h.round("right") })

	state := h.state(t)
	require.Contains(t, state.Error, "probe panicked: boom")
	require.Equal(t, 1, state.Answered)
	require.True(t, h.repo.account(t, 1).Schedulable)
}

func TestIQCheckPrunesDeletedAccountDrawingsHourly(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	drawings := &iqCheckTestDrawings{}
	h.svc.drawings = drawings

	h.svc.RunDue(context.Background())
	h.svc.wg.Wait()
	h.svc.RunDue(context.Background())
	h.svc.wg.Wait()
	require.Equal(t, 1, drawings.prunes)

	h.now = h.now.Add(iqCheckDrawingPruneInterval)
	h.svc.RunDue(context.Background())
	h.svc.wg.Wait()
	require.Equal(t, 2, drawings.prunes)

	// Only the instance that holds the cycle lock prunes.
	h.now = h.now.Add(iqCheckDrawingPruneInterval)
	h.svc.lockCache = &iqCheckTestLock{deny: true}
	h.svc.RunDue(context.Background())
	require.Equal(t, 2, drawings.prunes)
}

func TestIQCheckManualToggleTakesOverScheduling(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	h.round("wrong")
	h.round("wrong")
	require.False(t, h.repo.account(t, 1).Schedulable)

	// The operator keeps it off by toggling: a later pass must not turn it on.
	admin := &adminServiceImpl{accountRepo: h.repo}
	_, err := admin.SetAccountSchedulable(context.Background(), 1, false)
	require.NoError(t, err)
	require.False(t, h.state(t).DisabledByCheck)
	require.Zero(t, h.state(t).RecentWrong)
	h.round("right")
	require.False(t, h.repo.account(t, 1).Schedulable)

	// Turned back on by hand, it starts a fresh window.
	_, err = admin.SetAccountSchedulable(context.Background(), 1, true)
	require.NoError(t, err)
	h.round("wrong")
	require.True(t, h.repo.account(t, 1).Schedulable)
	h.round("one")
	require.False(t, h.repo.account(t, 1).Schedulable)
}

func TestIQCheckClearsOwnershipWhenSchedulingWasTurnedOnElsewhere(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	h.round("wrong")
	h.round("wrong")
	// Re-enabled through a path that does not release the check (e.g. bulk edit).
	require.NoError(t, h.repo.upstreamBillingProbeAccountRepo.Update(context.Background(), func() *Account {
		account := h.repo.account(t, 1)
		account.Schedulable = true
		return account
	}()))
	h.round("one")
	state := h.state(t)
	require.False(t, state.DisabledByCheck)
	require.Equal(t, 1, state.RecentWrong)
	require.True(t, h.repo.account(t, 1).Schedulable)
}

func TestDecideIQCheckDoesNotRestoreExpiredAccounts(t *testing.T) {
	state := IQCheckState{DisabledByCheck: true, LastRoundWrong: 2, RecentWrong: 4}
	round := iqCheckRound{result: IQCheckResultPassed, total: 2, answered: 2, correct: 2}
	require.Equal(t, iqCheckNoChange, decideIQCheck(&state, round, true, false, true))
	require.True(t, state.DisabledByCheck)
}

func TestIQCheckIntervalAndModelDefaults(t *testing.T) {
	require.Equal(t, 60, iqCheckIntervalMinutes(nil))
	require.Equal(t, 10, iqCheckIntervalMinutes(map[string]any{IQCheckIntervalExtraKey: float64(3)}))
	require.Equal(t, 1440, iqCheckIntervalMinutes(map[string]any{IQCheckIntervalExtraKey: float64(5000)}))
	require.Equal(t, 30, iqCheckIntervalMinutes(map[string]any{IQCheckIntervalExtraKey: float64(30)}))
	require.NotEmpty(t, iqCheckModel(nil))
	require.Equal(t, "gpt-5.5", iqCheckModel(map[string]any{IQCheckModelExtraKey: " gpt-5.5 "}))
}

func TestUpdateAccountKeepsIQCheckState(t *testing.T) {
	account := iqCheckTestAccount(1)
	account.Extra[IQCheckStateExtraKey] = map[string]any{"result": "failed", "disabled_by_check": true}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{1: account}}
	svc := &adminServiceImpl{accountRepo: repo}
	updated, err := svc.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: map[string]any{
		IQCheckEnabledExtraKey: true,
		IQCheckModelExtraKey:   "gpt-5.5",
		IQCheckStateExtraKey:   map[string]any{"result": "passed"},
	}})
	require.NoError(t, err)
	require.Equal(t, "gpt-5.5", updated.Extra[IQCheckModelExtraKey])
	state := decodeIQCheckState(updated.Extra)
	require.Equal(t, IQCheckResultFailed, state.Result)
	require.True(t, state.DisabledByCheck)
}

func TestApplyIQCheckResponsesPayload(t *testing.T) {
	payload := createOpenAITestPayload("gpt-5.4", true)
	applyIQCheckResponsesPayload(context.Background(), payload)
	require.NotContains(t, payload, "reasoning", "the plain connectivity test stays unchanged")

	ctx := context.WithValue(context.Background(), iqCheckProbeContextKey{}, iqCheckProbeOptions{prompt: "q?", reasoningEffort: "high"})
	applyIQCheckResponsesPayload(ctx, payload)
	input := payload["input"].([]map[string]any)
	require.Equal(t, "q?", input[0]["content"].([]map[string]any)[0]["text"])
	require.Equal(t, map[string]any{"effort": "high"}, payload["reasoning"])
}

// BulkUpdate also applies the schedulable flag so bulk toggles can be exercised.
func (r *iqCheckTestRepo) BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	n, err := r.upstreamBillingProbeAccountRepo.BulkUpdate(ctx, ids, updates)
	if err != nil || updates.Schedulable == nil {
		return n, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if account, ok := r.accounts[id]; ok {
			account.Schedulable = *updates.Schedulable
		}
	}
	return n, nil
}

func TestIQCheckBulkToggleTakesOverScheduling(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	h.round("wrong")
	h.round("wrong")
	require.False(t, h.repo.account(t, 1).Schedulable)
	require.True(t, h.state(t).DisabledByCheck)

	// Bulk-disabling a stopped account keeps it off: a later pass must not turn it on.
	admin := &adminServiceImpl{accountRepo: h.repo}
	off, on := false, true
	_, err := admin.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Schedulable: &off})
	require.NoError(t, err)
	require.False(t, h.state(t).DisabledByCheck)
	require.Zero(t, h.state(t).RecentWrong)
	h.round("right")
	require.False(t, h.repo.account(t, 1).Schedulable)

	// Bulk-enabled by hand, it starts a fresh window.
	_, err = admin.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Schedulable: &on})
	require.NoError(t, err)
	h.round("wrong")
	require.True(t, h.repo.account(t, 1).Schedulable)
	h.round("one")
	require.False(t, h.repo.account(t, 1).Schedulable)
}

func TestBulkUpdateAccountsDropsIQCheckState(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	admin := &adminServiceImpl{accountRepo: h.repo}
	forged, err := iqCheckStateValue(IQCheckState{DisabledByCheck: true})
	require.NoError(t, err)

	_, err = admin.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{1},
		Extra:      map[string]any{IQCheckStateExtraKey: forged, IQCheckEnabledExtraKey: false},
	})

	require.NoError(t, err)
	require.Len(t, h.repo.bulkUpdates, 1)
	require.NotContains(t, h.repo.bulkUpdates[0].Extra, IQCheckStateExtraKey)
	require.Equal(t, false, h.repo.bulkUpdates[0].Extra[IQCheckEnabledExtraKey])
}

func TestDuplicateAccountStartsWithFreshIQCheckState(t *testing.T) {
	ctx := context.Background()
	repo := &codexSeedDuplicateRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{accounts: make(map[int64]*Account)}}
	svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}
	stopped, err := iqCheckStateValue(IQCheckState{DisabledByCheck: true, LastRoundWrong: 2, RecentWrong: 3, Answered: 4})
	require.NoError(t, err)
	source := &Account{
		Name:     "source",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			IQCheckEnabledExtraKey:     true,
			IQCheckAutoDisableExtraKey: true,
			IQCheckStateExtraKey:       stopped,
		},
	}
	require.NoError(t, repo.Create(ctx, source))

	duplicate, err := svc.DuplicateAccount(ctx, source.ID, "admin:1", "")

	require.NoError(t, err)
	require.False(t, duplicate.Schedulable)
	require.NotContains(t, duplicate.Extra, IQCheckStateExtraKey)
	require.Equal(t, true, duplicate.Extra[IQCheckEnabledExtraKey])
	require.Equal(t, true, duplicate.Extra[IQCheckAutoDisableExtraKey])
}
