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
		Extra: map[string]any{IQCheckEnabledExtraKey: true}}
}

func TestIQCheckStopsSchedulingAfterTwoFailedRoundsAndRestoresOnPass(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))

	h.round("wrong")
	state := decodeIQCheckState(h.repo.account(t, 1).Extra)
	require.Equal(t, IQCheckResultFailed, state.Result)
	require.Equal(t, 1, state.ConsecutiveFailures)
	require.True(t, h.repo.account(t, 1).Schedulable, "one failed round must not stop scheduling")

	h.round("wrong")
	state = decodeIQCheckState(h.repo.account(t, 1).Extra)
	require.Equal(t, 2, state.ConsecutiveFailures)
	require.True(t, state.DisabledByCheck)
	require.False(t, h.repo.account(t, 1).Schedulable)

	h.round("right")
	state = decodeIQCheckState(h.repo.account(t, 1).Extra)
	require.Equal(t, IQCheckResultPassed, state.Result)
	require.Equal(t, 2, state.Correct)
	require.Equal(t, 2, state.Total)
	require.Zero(t, state.ConsecutiveFailures)
	require.False(t, state.DisabledByCheck)
	require.True(t, h.repo.account(t, 1).Schedulable)
	require.Equal(t, []bool{false, true}, h.repo.schedulableCalls)
}

func TestIQCheckUnknownRoundNeitherCountsNorResets(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	h.round("wrong")
	h.round("error")
	state := decodeIQCheckState(h.repo.account(t, 1).Extra)
	require.Equal(t, IQCheckResultUnknown, state.Result)
	require.Equal(t, "upstream 502: bad gateway", state.Error)
	require.Equal(t, 1, state.ConsecutiveFailures)
	h.round("wrong")
	require.False(t, h.repo.account(t, 1).Schedulable)
}

func TestIQCheckClaimsAccountBeforeChecking(t *testing.T) {
	h := newIQCheckHarness(t, iqCheckTestAccount(1))
	h.svc.RunDue(context.Background())
	h.svc.wg.Wait()
	h.svc.RunDue(context.Background()) // Same minute: not due again.
	h.svc.wg.Wait()
	require.EqualValues(t, 2, h.probes.Load())
	state := decodeIQCheckState(h.repo.account(t, 1).Extra)
	require.Equal(t, h.now.Add(60*time.Minute), *state.NextCheckAt)
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
	require.False(t, decodeIQCheckState(h.repo.account(t, 1).Extra).DisabledByCheck)
	h.round("right")
	require.False(t, h.repo.account(t, 1).Schedulable)

	// Turned back on by hand, it needs two new failed rounds to stop again.
	_, err = admin.SetAccountSchedulable(context.Background(), 1, true)
	require.NoError(t, err)
	h.round("wrong")
	require.True(t, h.repo.account(t, 1).Schedulable)
	h.round("wrong")
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
	h.round("wrong")
	state := decodeIQCheckState(h.repo.account(t, 1).Extra)
	require.False(t, state.DisabledByCheck)
	require.Equal(t, 1, state.ConsecutiveFailures)
	require.True(t, h.repo.account(t, 1).Schedulable)
}

func TestDecideIQCheckDoesNotRestoreExpiredAccounts(t *testing.T) {
	state := IQCheckState{Result: IQCheckResultPassed, DisabledByCheck: true, ConsecutiveFailures: 2}
	require.Equal(t, iqCheckNoChange, decideIQCheck(&state, false, true))
	require.True(t, state.DisabledByCheck)
	require.Zero(t, state.ConsecutiveFailures)
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
