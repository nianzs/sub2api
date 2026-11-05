package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
)

func TestClassifyFishErrorAuth(t *testing.T) {
	failure := classifyFishError(&fish.AuthError{RequestError: fish.RequestError{Message: "Invalid token", StatusCode: 401}})
	if failure.Failover == nil || failure.Failover.Reason != fishFailureAuth {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if !failure.SetAccountError {
		t.Fatal("auth must set account error (fish token has no refresh)")
	}
	if failure.Cooldown > 0 {
		t.Fatal("auth should not cooldown")
	}
}

func TestClassifyFishErrorInsufficientCredits(t *testing.T) {
	failure := classifyFishError(&fish.InsufficientCreditsError{RequestError: fish.RequestError{Message: "insufficient credits", StatusCode: 402}})
	if failure.Failover == nil || failure.Failover.Reason != fishFailureInsufficientCredits {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Cooldown != 2*time.Hour {
		t.Fatalf("cooldown = %v, want 2h (quota class)", failure.Cooldown)
	}
	if failure.Failover.NextAccountAction != NextAccountRetry {
		t.Fatal("insufficient credits should retry next account")
	}
}

func TestClassifyFishErrorForbidden(t *testing.T) {
	native := `fish: GENERATION_UNAVAILABLE: {"code":"GENERATION_UNAVAILABLE"} (status 403)`
	failure := classifyFishError(&fish.ForbiddenError{RequestError: fish.RequestError{
		Message: `GENERATION_UNAVAILABLE: {"code":"GENERATION_UNAVAILABLE"}`, StatusCode: 403}})
	if failure.Failover == nil || failure.Failover.Reason != fishFailureForbidden {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Failover.NextAccountAction != NextAccountRetry {
		t.Fatal("forbidden should retry next account")
	}
	if failure.Failover.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", failure.Failover.StatusCode)
	}
	if !failure.SetAccountError {
		t.Fatal("forbidden must set account error (risk control)")
	}
	if failure.AccountErrorMessage != native {
		t.Fatalf("account error message = %q, want native %q", failure.AccountErrorMessage, native)
	}
	if failure.Cooldown > 0 {
		t.Fatal("forbidden should not cooldown (error state already unschedulable)")
	}
}

func TestClassifyFishErrorRateLimited(t *testing.T) {
	failure := classifyFishError(&fish.RateLimitedError{RequestError: fish.RequestError{StatusCode: 429}})
	if failure.Cooldown != fishRateLimitCooldown || failure.Failover.Reason != fishFailureUpstream {
		t.Fatalf("failure = %+v", failure)
	}
	if !failure.RateLimited {
		t.Fatal("rate-limited failure must carry the RateLimited marker")
	}
}

func TestClassifyFishErrorTemporary(t *testing.T) {
	failure := classifyFishError(&fish.UpstreamTemporaryError{RequestError: fish.RequestError{StatusCode: 503}})
	if failure.Failover == nil || failure.Failover.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Failover.NextAccountAction != NextAccountRetry || failure.Cooldown > 0 {
		t.Fatalf("temporary should retry without side effects: %+v", failure)
	}
}

func TestClassifyFishErrorRequestStops(t *testing.T) {
	failure := classifyFishError(&fish.InvalidCombinationError{RequestError: fish.RequestError{Message: "Pricing configuration must match exactly one rate: matches=0", StatusCode: 400}})
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountStop {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Failover.Reason != fishFailureRequest {
		t.Fatalf("reason = %q", failure.Failover.Reason)
	}
	if failure.Failover.ClientStatusCode != http.StatusBadRequest {
		t.Fatalf("client status = %d", failure.Failover.ClientStatusCode)
	}
}

func TestClassifyFishErrorIndeterminate(t *testing.T) {
	failure := classifyFishError(&fishSubmitIndeterminateError{err: errors.New("conn reset")})
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountStop {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Failover.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d", failure.Failover.StatusCode)
	}
}

func TestClassifyFishErrorResultUnavailable(t *testing.T) {
	// 内层是 auth：账号侧处置保留（SetAccountError），但对本请求不换号
	failure := classifyFishError(&fishResultUnavailableError{
		groupID: "g",
		err:     &fish.AuthError{RequestError: fish.RequestError{StatusCode: 401}},
	})
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountStop {
		t.Fatalf("failover = %+v", failure.Failover)
	}
	if failure.Failover.Reason != fishFailureResultUnavailable {
		t.Fatalf("reason = %q", failure.Failover.Reason)
	}
	if !failure.SetAccountError {
		t.Fatal("inner auth side effect must be preserved")
	}
}

func TestClassifyFishErrorGenerationFailedRetries(t *testing.T) {
	failure := classifyFishError(&fishGenerationFailedError{groupID: "g", reason: "moderation"})
	if failure.Failover == nil || failure.Failover.NextAccountAction != NextAccountRetry {
		t.Fatalf("failover = %+v", failure.Failover)
	}
}

func TestClassifyFishErrorForeign(t *testing.T) {
	if failure := classifyFishError(errors.New("context canceled")); failure.Failover != nil {
		t.Fatalf("foreign error must yield nil failover, got %+v", failure.Failover)
	}
	if failure := classifyFishError(nil); failure.Failover != nil {
		t.Fatalf("nil must yield nil failover")
	}
}

type fishCooldownRepo struct {
	account *Account
	getErr  error
	until   time.Time
	reason  string
	gets    int
	sets    int
	// writeCtxErrs 记录每次写入时 ctx.Err()：真实库写入在已过期的 ctx 上会失败。
	writeCtxErrs []error
}

func (r *fishCooldownRepo) GetByID(context.Context, int64) (*Account, error) {
	r.gets++
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.account, nil
}

// SetTempUnschedulable 镜像仓储 SQL：只延长（temp_unschedulable_until < $1 才更新）。
func (r *fishCooldownRepo) SetTempUnschedulable(ctx context.Context, _ int64, until time.Time, reason string) error {
	r.sets++
	r.writeCtxErrs = append(r.writeCtxErrs, ctx.Err())
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if r.until.Before(until) {
		r.until = until
		r.reason = reason
	}
	return nil
}

func (r *fishCooldownRepo) assertWritesHadLiveContext(t *testing.T) {
	t.Helper()
	for i, err := range r.writeCtxErrs {
		if err != nil {
			t.Fatalf("cooldown write %d ran on a dead context: %v", i, err)
		}
	}
}

func fishCooldownAccount() *Account {
	return &Account{
		ID:       7,
		Platform: PlatformFish,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			fishCredentialToken:    "tok",
			fishCredentialDeviceID: "dev-1",
		},
	}
}

func fishInsufficientFailure() fishFailure {
	return classifyFishError(&fish.InsufficientCreditsError{
		RequestError: fish.RequestError{Message: "insufficient credits", StatusCode: 402},
	})
}

// fishEntitlementServer 只提供 entitlements。其它路径记为 other，用来确认没打钱包。
func fishEntitlementServer(t *testing.T, status int, body string, block bool) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var entitlements, other atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tools/image-video/entitlements" {
			other.Add(1)
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		entitlements.Add(1)
		if block {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
				t.Error("entitlements request was not cancelled by the lookup timeout")
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	orig := fishUsageAPIBaseForTest
	fishUsageAPIBaseForTest = server.URL
	t.Cleanup(func() { fishUsageAPIBaseForTest = orig })
	// 后台补查改为同步，断言时补查已完成。
	origAsync := fishRunAsync
	fishRunAsync = func(fn func()) { fn() }
	t.Cleanup(func() { fishRunAsync = origAsync })
	return server, &entitlements, &other
}

func assertFishDurationCooldown(t *testing.T, repo *fishCooldownRepo, started time.Time, want time.Duration, reason string) {
	t.Helper()
	repo.assertWritesHadLiveContext(t)
	if repo.sets != 1 {
		t.Fatalf("cooldown writes = %d, want 1", repo.sets)
	}
	if repo.reason != reason {
		t.Fatalf("reason = %q, want %q", repo.reason, reason)
	}
	if repo.until.Before(started.Add(want)) || repo.until.After(time.Now().Add(want)) {
		t.Fatalf("until = %v, want about %s after %v", repo.until, want, started)
	}
}

func TestFishInsufficientCooldownFreePlanGoesStraightToMidnight(t *testing.T) {
	// free 号：不打 entitlements，单次写入冷却到下一个 UTC 零点。
	_, entitlements, other := fishEntitlementServer(t, http.StatusOK,
		`{"plan":"free","dailyImage":{"limit":3,"used":1,"remaining":2,"resetAt":"2099-03-14T00:00:00Z"}}`, false)
	account := fishCooldownAccount()
	account.Credentials[fishCredentialPlan] = fishPlanFree
	repo := &fishCooldownRepo{account: account}
	started := time.Now()
	applyFishCooldown(context.Background(), repo, 7, fishInsufficientFailure())

	repo.assertWritesHadLiveContext(t)
	if repo.sets != 1 {
		t.Fatalf("cooldown writes = %d, want 1 (free goes straight to midnight)", repo.sets)
	}
	if want := nextUTCMidnight(started); !repo.until.Equal(want) {
		t.Fatalf("until = %v, want next UTC midnight %v", repo.until, want)
	}
	if repo.reason != "fish credits exhausted or quota exceeded" {
		t.Fatalf("reason = %q", repo.reason)
	}
	if entitlements.Load() != 0 || other.Load() != 0 {
		t.Fatalf("entitlements=%d other=%d, want no upstream query", entitlements.Load(), other.Load())
	}
}

func TestFishInsufficientCooldownExtendsToMidnightWhenResetAtUnusable(t *testing.T) {
	// 日窗口确认耗尽但 resetAt 缺失/过期：兜底到下一个 UTC 零点而不是保持 2h。
	cases := []struct {
		name string
		body string
	}{
		{
			name: "resetAt missing",
			body: `{"plan":"pro","dailyImage":{"limit":3,"used":3,"remaining":0}}`,
		},
		{
			name: "resetAt expired",
			body: `{"plan":"pro","dailyImage":{"limit":3,"used":3,"remaining":0,"resetAt":"2000-01-01T00:00:00Z"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fishEntitlementServer(t, http.StatusOK, tc.body, false)
			repo := &fishCooldownRepo{account: fishCooldownAccount()}
			started := time.Now()
			applyFishCooldown(context.Background(), repo, 7, fishInsufficientFailure())

			repo.assertWritesHadLiveContext(t)
			if repo.sets != 2 {
				t.Fatalf("cooldown writes = %d, want 2 (2h then midnight)", repo.sets)
			}
			if want := nextUTCMidnight(started); !repo.until.Equal(want) {
				t.Fatalf("until = %v, want next UTC midnight %v", repo.until, want)
			}
			if repo.reason != fishDailyQuotaExhaustedReason {
				t.Fatalf("reason = %q", repo.reason)
			}
		})
	}
}

func TestNextUTCMidnight(t *testing.T) {
	cases := []struct {
		now  time.Time
		want time.Time
	}{
		{
			now:  time.Date(2026, 10, 25, 10, 30, 0, 0, time.UTC),
			want: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
		},
		{
			now:  time.Date(2026, 10, 25, 23, 59, 59, 999000000, time.UTC),
			want: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
		},
		{
			// 恰在零点：刚重置过，下一个零点是明天。
			now:  time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC),
			want: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
		},
		{
			// 带时区的入参换算到 UTC 后再取零点：CST 26 日 07 点 = UTC 25 日 23 点。
			now:  time.Date(2026, 10, 26, 7, 0, 0, 0, time.FixedZone("CST", 8*3600)),
			want: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		if got := nextUTCMidnight(tc.now); !got.Equal(tc.want) {
			t.Fatalf("nextUTCMidnight(%v) = %v, want %v", tc.now, got, tc.want)
		}
	}
}

func TestFishInsufficientCooldownUsesDailyResetAt(t *testing.T) {
	_, entitlements, other := fishEntitlementServer(t, http.StatusOK,
		`{"plan":"free","dailyImage":{"limit":3,"used":3,"remaining":0,"resetAt":"2099-03-14T00:00:00Z"}}`, false)
	repo := &fishCooldownRepo{account: fishCooldownAccount()}
	applyFishCooldown(context.Background(), repo, 7, fishInsufficientFailure())

	// 先落 2h，再由补查延长到 resetAt。
	repo.assertWritesHadLiveContext(t)
	if repo.sets != 2 {
		t.Fatalf("cooldown writes = %d, want 2 (2h then resetAt)", repo.sets)
	}
	want := time.Date(2099, 3, 14, 0, 0, 0, 0, time.UTC)
	if !repo.until.Equal(want) {
		t.Fatalf("until = %v, want %v", repo.until, want)
	}
	if repo.reason != fishDailyQuotaExhaustedReason {
		t.Fatalf("reason = %q", repo.reason)
	}
	if entitlements.Load() != 1 || other.Load() != 0 {
		t.Fatalf("entitlements=%d other=%d, want 1 and 0", entitlements.Load(), other.Load())
	}
}

func TestFishInsufficientCooldownIgnoresCallerCancel(t *testing.T) {
	_, entitlements, _ := fishEntitlementServer(t, http.StatusOK,
		`{"plan":"free","dailyImage":{"limit":3,"used":3,"remaining":0,"resetAt":"2099-03-14T00:00:00Z"}}`, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := &fishCooldownRepo{account: fishCooldownAccount()}
	applyFishCooldown(ctx, repo, 7, fishInsufficientFailure())
	repo.assertWritesHadLiveContext(t)
	want := time.Date(2099, 3, 14, 0, 0, 0, 0, time.UTC)
	if !repo.until.Equal(want) {
		t.Fatalf("cancelled caller still needs resetAt, until = %v", repo.until)
	}
	if entitlements.Load() != 1 {
		t.Fatalf("entitlements hits = %d, want 1", entitlements.Load())
	}
}

func TestFishInsufficientCooldownKeepsTwoHoursWhenDailyRemains(t *testing.T) {
	_, entitlements, other := fishEntitlementServer(t, http.StatusOK,
		`{"plan":"free","dailyImage":{"limit":3,"used":1,"remaining":2,"resetAt":"2099-03-14T00:00:00Z"}}`, false)
	repo := &fishCooldownRepo{account: fishCooldownAccount()}
	started := time.Now()
	applyFishCooldown(context.Background(), repo, 7, fishInsufficientFailure())
	assertFishDurationCooldown(t, repo, started, fishInsufficientCooldown, "fish credits exhausted or quota exceeded")
	if entitlements.Load() != 1 || other.Load() != 0 {
		t.Fatalf("entitlements=%d other=%d, want 1 and 0", entitlements.Load(), other.Load())
	}
}

func TestFishInsufficientCooldownKeepsTwoHoursWhenLookupUnusable(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		block  bool
	}{
		{
			name:   "upstream 5xx",
			status: http.StatusInternalServerError,
			body:   `{"message":"boom"}`,
		},
		{
			name:   "remaining omitted on a fresh window",
			status: http.StatusOK,
			body:   `{"plan":"free","dailyImage":{"limit":3,"used":0,"resetAt":"2099-03-14T00:00:00Z"}}`,
		},
		{
			// 钱包耗尽引起的 402：日窗口还有余量，只是 remaining 缺失被解成 0。
			name:   "remaining omitted with daily quota left",
			status: http.StatusOK,
			body:   `{"plan":"free","dailyImage":{"limit":100,"used":5,"resetAt":"2099-03-14T00:00:00Z"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fishEntitlementServer(t, tc.status, tc.body, tc.block)
			repo := &fishCooldownRepo{account: fishCooldownAccount()}
			started := time.Now()
			applyFishCooldown(context.Background(), repo, 7, fishInsufficientFailure())
			assertFishDurationCooldown(t, repo, started, fishInsufficientCooldown, "fish credits exhausted or quota exceeded")
		})
	}

	t.Run("lookup timeout", func(t *testing.T) {
		orig := fishDailyResetLookupTimeout
		fishDailyResetLookupTimeout = 30 * time.Millisecond
		t.Cleanup(func() { fishDailyResetLookupTimeout = orig })
		fishEntitlementServer(t, http.StatusOK, `{"dailyImage":{"limit":3,"used":3,"remaining":0,"resetAt":"2099-03-14T00:00:00Z"}}`, true)
		repo := &fishCooldownRepo{account: fishCooldownAccount()}
		started := time.Now()
		applyFishCooldown(context.Background(), repo, 7, fishInsufficientFailure())
		assertFishDurationCooldown(t, repo, started, fishInsufficientCooldown, "fish credits exhausted or quota exceeded")
	})
}

func TestFishRateLimitCooldownFreePlanGoesStraightToMidnight(t *testing.T) {
	// free 号 429：不打 entitlements，单次写入冷却到下一个 UTC 零点——
	// 持续 429 的 free 号当天基本废了，5 分钟回锅会虚增可用账号数。
	_, entitlements, other := fishEntitlementServer(t, http.StatusOK,
		`{"plan":"free","dailyImage":{"limit":3,"used":1,"remaining":2,"resetAt":"2099-03-14T00:00:00Z"}}`, false)
	account := fishCooldownAccount()
	account.Credentials[fishCredentialPlan] = fishPlanFree
	repo := &fishCooldownRepo{account: account}
	started := time.Now()
	failure := classifyFishError(&fish.RateLimitedError{RequestError: fish.RequestError{StatusCode: 429}})
	applyFishCooldown(context.Background(), repo, 7, failure)

	repo.assertWritesHadLiveContext(t)
	if repo.sets != 1 {
		t.Fatalf("cooldown writes = %d, want 1 (free goes straight to midnight)", repo.sets)
	}
	if want := nextUTCMidnight(started); !repo.until.Equal(want) {
		t.Fatalf("until = %v, want next UTC midnight %v", repo.until, want)
	}
	if repo.reason != fishRateLimitCooldownReason {
		t.Fatalf("reason = %q", repo.reason)
	}
	if entitlements.Load() != 0 || other.Load() != 0 {
		t.Fatalf("entitlements=%d other=%d, want no upstream query", entitlements.Load(), other.Load())
	}
}

func TestFishRateLimitCooldownDoesNotQueryEntitlements(t *testing.T) {
	_, entitlements, other := fishEntitlementServer(t, http.StatusOK,
		`{"plan":"free","dailyImage":{"limit":3,"used":3,"remaining":0,"resetAt":"2099-03-14T00:00:00Z"}}`, false)
	repo := &fishCooldownRepo{account: fishCooldownAccount()}
	started := time.Now()
	failure := classifyFishError(&fish.RateLimitedError{RequestError: fish.RequestError{StatusCode: 429}})
	applyFishCooldown(context.Background(), repo, 7, failure)
	assertFishDurationCooldown(t, repo, started, fishRateLimitCooldown, fishRateLimitCooldownReason)
	// plan 未知只做一次 GetByID 读 plan，不打 entitlements。
	if repo.gets != 1 || entitlements.Load() != 0 || other.Load() != 0 {
		t.Fatalf("gets=%d entitlements=%d other=%d, want plan check only", repo.gets, entitlements.Load(), other.Load())
	}
}

func TestFishFailoverCreditsShortCoolsDownOnlyWhenExhausted(t *testing.T) {
	outOfCredits := &fish.InsufficientCreditsError{RequestError: fish.RequestError{
		Message: "OUT_OF_CREDITS: Out of credits", StatusCode: 402, Code: fish.CodeOutOfCredits,
	}}
	upstream := func(need int) *fishCreditsShortError {
		return &fishCreditsShortError{need: need, balance: -1, cause: outOfCredits}
	}
	local := func(balance int) *fishCreditsShortError { return &fishCreditsShortError{need: 650, balance: balance} }
	const brokenWallet = `not-json`
	cases := []struct {
		name        string
		wallet      string
		observedAgo time.Duration // 0 = 从没查过钱包
		estimate    int           // 库里的估算余额
		short       *fishCreditsShortError
		wantSets    int
		wantBalance any // 补查后落库的余额；nil = 没有补查
		wantFetch   bool
	}{
		{"402 and the wallet is empty", `{"total":8000,"balance":0}`, 0, 0, upstream(650), 1, 0, true},
		{"402 but the wallet still covers the cheapest job", `{"total":8000,"balance":300}`, 0, 0, upstream(650), 0, 300, true},
		{"402 for the cheapest job and the wallet lookup fails", brokenWallet, 0, 0, upstream(150), 1, nil, true},
		{"402 although the wallet covers this job", `{"total":8000,"balance":6500}`, 0, 0, upstream(650), 1, 6500, true},
		{"402 contradicts a recent estimate", `{"total":8000,"balance":300}`, time.Minute, 6500, upstream(650), 0, 300, true},
		{"local estimate is stale and credits were refilled", `{"total":8000,"balance":6500}`, time.Hour, 100, local(100), 0, 6500, true},
		{"local estimate is stale and the wallet is empty", `{"total":8000,"balance":0}`, time.Hour, 100, local(100), 1, 0, true},
		{"local estimate was just observed", `{"total":8000,"balance":6500}`, time.Minute, 100, local(100), 1, nil, false},
		{"local estimate and the wallet lookup fails", brokenWallet, time.Hour, 100, local(100), 0, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFishPricesForTest(t)
			fishPrices.record(fishPriceKey{model: "gpt-image-2", variant: fish.VariantTextToImage, imageSize: "1024x1024", quality: "low"},
				&fish.PriceResult{PerItemCredits: 150}, 1, time.Now())
			origAsync := fishRunAsync
			fishRunAsync = func(fn func()) { fn() }
			t.Cleanup(func() { fishRunAsync = origAsync })
			hits := fishUsageServerWithWallet(t, tc.wallet, `{"plan":"free"}`)
			account := &Account{
				ID: 7, Platform: PlatformFish, Type: AccountTypeAPIKey,
				Credentials: map[string]any{fishCredentialToken: "tok", fishCredentialPlan: fishPlanFree},
			}
			if tc.observedAgo > 0 {
				account.Extra = map[string]any{
					FishWalletBalanceExtraKey:   tc.estimate,
					FishWalletBalanceAtExtraKey: time.Now().Add(-tc.observedAgo).UTC().Format(time.RFC3339),
				}
			}
			repo := &fishUsageRepoStub{account: account}
			svc := &GatewayService{accountRepo: repo, cfg: &config.Config{Gateway: config.GatewayConfig{
				Fish: config.GatewayFishConfig{CreditAwareSchedulingEnabled: true},
			}}}

			failover := svc.FishFailover(context.Background(), 7, "tok", tc.short)
			if failover == nil || failover.Reason != fishFailureCreditsShortForJob || !failover.ShouldRetryNextAccount() {
				t.Fatalf("failover = %+v, want retry with %q", failover, fishFailureCreditsShortForJob)
			}
			if repo.sets != tc.wantSets {
				t.Fatalf("cooldown writes = %d, want %d", repo.sets, tc.wantSets)
			}
			if tc.wantSets == 1 && repo.reason != fishCreditsExhaustedReason {
				t.Fatalf("cooldown reason = %q", repo.reason)
			}
			if fetched := hits.Load() > 0; fetched != tc.wantFetch {
				t.Fatalf("wallet fetched = %v, want %v", fetched, tc.wantFetch)
			}
			if got := repo.extra[FishWalletBalanceExtraKey]; got != tc.wantBalance {
				t.Fatalf("persisted balance = %v, want %v", got, tc.wantBalance)
			}
		})
	}
}

func TestFishFailoverKeepsTheTighterBelowHint(t *testing.T) {
	outOfCredits := &fish.InsufficientCreditsError{RequestError: fish.RequestError{
		Message: "OUT_OF_CREDITS: Out of credits", StatusCode: 402, Code: fish.CodeOutOfCredits,
	}}
	cases := []struct {
		name      string
		existing  int
		wantBelow any
	}{
		{"a smaller hint is kept", 400, nil},
		{"a larger hint is tightened", 900, 800},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFishPricesForTest(t)
			fishPrices.record(fishPriceKey{model: "gpt-image-2"}, &fish.PriceResult{PerItemCredits: 150}, 1, time.Now())
			origAsync := fishRunAsync
			fishRunAsync = func(fn func()) { fn() }
			t.Cleanup(func() { fishRunAsync = origAsync })
			hits := fishUsageServerWithWallet(t, `{"total":8000,"balance":300}`, `{"plan":"free"}`)
			observed := time.Now().UTC().Format(time.RFC3339)
			// 余额刚查过且与 402 一致（300 < 800）：不补查，提示留在库里。
			repo := &fishUsageRepoStub{account: &Account{
				ID: 7, Platform: PlatformFish, Type: AccountTypeAPIKey,
				Credentials: map[string]any{fishCredentialToken: "tok"},
				Extra: map[string]any{
					FishWalletBalanceExtraKey: 300, FishWalletBalanceAtExtraKey: observed,
					FishWalletBelowExtraKey: tc.existing, FishWalletBelowAtExtraKey: observed,
				},
			}}
			svc := &GatewayService{accountRepo: repo, cfg: &config.Config{Gateway: config.GatewayConfig{
				Fish: config.GatewayFishConfig{CreditAwareSchedulingEnabled: true},
			}}}

			svc.FishFailover(context.Background(), 7, "tok", &fishCreditsShortError{need: 800, balance: -1, cause: outOfCredits})
			if got := repo.extra[FishWalletBelowExtraKey]; got != tc.wantBelow {
				t.Fatalf("written below hint = %v, want %v", got, tc.wantBelow)
			}
			if hits.Load() != 0 || repo.sets != 0 {
				t.Fatalf("wallet hits = %d cooldowns = %d, want neither", hits.Load(), repo.sets)
			}
		})
	}
}

func TestRecordFishSpendDeductsFromTheCurrentBalance(t *testing.T) {
	selectedAt := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	newerAt := time.Now().UTC().Format(time.RFC3339)
	cases := []struct {
		name       string
		current    map[string]any
		wantWrite  bool
		wantRemain int
	}{
		{"another request already spent", map[string]any{FishWalletBalanceExtraKey: 1000, FishWalletBalanceAtExtraKey: selectedAt}, true, 700},
		{"a newer wallet observation landed", map[string]any{FishWalletBalanceExtraKey: 6500, FishWalletBalanceAtExtraKey: newerAt}, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selected := &Account{
				ID: 7, Platform: PlatformFish, Type: AccountTypeAPIKey,
				Credentials: map[string]any{fishCredentialToken: "tok"},
				Extra:       map[string]any{FishWalletBalanceExtraKey: 2000, FishWalletBalanceAtExtraKey: selectedAt},
			}
			current := *selected
			current.Extra = tc.current
			repo := &fishUsageRepoStub{account: &current}
			svc := &GatewayService{accountRepo: repo}

			svc.RecordFishSpend(context.Background(), selected, 300)
			got, written := repo.extra[FishWalletBalanceExtraKey]
			if written != tc.wantWrite || (tc.wantWrite && got != tc.wantRemain) {
				t.Fatalf("written balance = %v (%v), want %v (%v)", got, written, tc.wantRemain, tc.wantWrite)
			}
		})
	}
}
