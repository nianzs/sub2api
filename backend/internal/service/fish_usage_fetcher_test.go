package service

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
)

// fishUsageRepoStub 记录用量观察触发的冷却/凭据写入。嵌入接口让未实现的方法
// 保持 panic（不会被走到），冷却与 Update 走记录实现。
type fishUsageRepoStub struct {
	AccountRepository
	until   time.Time
	reason  string
	sets    int
	updates int
	extra   map[string]any
	clears  int
	account *Account
}

func (r *fishUsageRepoStub) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}

func (r *fishUsageRepoStub) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if r.extra == nil {
		r.extra = make(map[string]any)
	}
	maps.Copy(r.extra, updates)
	return nil
}

func (r *fishUsageRepoStub) ClearTempUnschedulable(_ context.Context, _ int64) error {
	r.clears++
	return nil
}

func (r *fishUsageRepoStub) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, reason string) error {
	r.sets++
	r.until, r.reason = until, reason
	return nil
}

func (r *fishUsageRepoStub) Update(_ context.Context, _ *Account) error {
	r.updates++
	return nil
}

func fishUsageServer(t *testing.T, entitlements string) *atomic.Int32 {
	t.Helper()
	return fishUsageServerWithWallet(t, `{"total":8000,"balance":6500,"type":"free"}`, entitlements)
}

func fishUsageServerWithWallet(t *testing.T, wallet, entitlements string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user/self":
			_, _ = w.Write([]byte(`{"_id":"uid-9","email":"a@b.c"}`))
		case "/wallet/uid-9/package":
			_, _ = w.Write([]byte(wallet))
		case "/tools/image-video/entitlements":
			_, _ = w.Write([]byte(entitlements))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	orig := fishUsageAPIBaseForTest
	fishUsageAPIBaseForTest = server.URL
	t.Cleanup(func() { fishUsageAPIBaseForTest = orig })
	return &hits
}

func TestGetFishUsageCoolsExhaustedDailyWindowAndBackfillsPlan(t *testing.T) {
	fishUsageServer(t, `{"plan":"pro","dailyImage":{"limit":3,"used":3,"remaining":0,"resetAt":"2099-03-14T00:00:00Z"}}`)
	repo := &fishUsageRepoStub{}
	svc := &AccountUsageService{cache: NewUsageCache(), accountRepo: repo}
	account := &Account{
		ID:          7,
		Platform:    PlatformFish,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{fishCredentialToken: "tok"},
	}

	if _, err := svc.getFishUsage(t.Context(), account, "active", true); err != nil {
		t.Fatalf("getFishUsage: %v", err)
	}
	if repo.sets != 1 {
		t.Fatalf("cooldown writes = %d, want 1", repo.sets)
	}
	if want := time.Date(2099, 3, 14, 0, 0, 0, 0, time.UTC); !repo.until.Equal(want) {
		t.Fatalf("until = %v, want %v", repo.until, want)
	}
	if repo.reason != fishDailyQuotaExhaustedReason {
		t.Fatalf("reason = %q", repo.reason)
	}
	if got, _ := account.Credentials[fishCredentialPlan].(string); got != "pro" {
		t.Fatalf("plan_type = %v, want pro backfilled", account.Credentials[fishCredentialPlan])
	}
}

func TestGetFishUsageDailyWindowWithRoomWritesNoCooldown(t *testing.T) {
	fishUsageServer(t, `{"plan":"free","dailyImage":{"limit":3,"used":1,"remaining":2,"resetAt":"2099-03-14T00:00:00Z"}}`)
	repo := &fishUsageRepoStub{}
	svc := &AccountUsageService{cache: NewUsageCache(), accountRepo: repo}
	account := &Account{
		ID:          7,
		Platform:    PlatformFish,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{fishCredentialToken: "tok"},
	}

	if _, err := svc.getFishUsage(t.Context(), account, "active", true); err != nil {
		t.Fatalf("getFishUsage: %v", err)
	}
	if repo.sets != 0 {
		t.Fatalf("cooldown writes = %d, want 0 (daily window not exhausted)", repo.sets)
	}
	if got, _ := account.Credentials[fishCredentialPlan].(string); got != "free" {
		t.Fatalf("plan_type = %v, want free backfilled", account.Credentials[fishCredentialPlan])
	}
}

func TestGetFishUsageCachesWalletAndDailyWindow(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user/self":
			_, _ = w.Write([]byte(`{"_id":"uid-9","email":"a@b.c"}`))
		case "/wallet/uid-9/package":
			_, _ = w.Write([]byte(`{"total":8000,"balance":6500,"type":"free"}`))
		case "/tools/image-video/entitlements":
			_, _ = w.Write([]byte(`{"plan":"free","maxImagesPerGeneration":2,"dailyImage":{"limit":3,"used":1,"remaining":2,"resetAt":"2099-03-14T00:00:00Z"}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	orig := fishUsageAPIBaseForTest
	fishUsageAPIBaseForTest = server.URL
	t.Cleanup(func() { fishUsageAPIBaseForTest = orig })

	svc := &AccountUsageService{cache: NewUsageCache()}
	account := &Account{
		ID:          7,
		Platform:    PlatformFish,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{fishCredentialToken: "tok"},
	}

	info, err := svc.getFishUsage(t.Context(), account, "passive", false)
	if err != nil {
		t.Fatalf("getFishUsage: %v", err)
	}
	if info.FishPlan != "free" {
		t.Fatalf("plan = %q, want free", info.FishPlan)
	}
	if info.FishCredits == nil || info.FishCredits.CurrentUsage != 1500 || info.FishCredits.UsageLimit != 8000 {
		t.Fatalf("credits = %+v, want used 1500 / 8000", info.FishCredits)
	}
	if info.FishDaily == nil || info.FishDaily.CurrentUsage != 1 || info.FishDaily.UsageLimit != 3 {
		t.Fatalf("daily = %+v, want 1 / 3", info.FishDaily)
	}
	if info.FishResetAt == nil || !info.FishResetAt.Equal(time.Date(2099, 3, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("reset = %v", info.FishResetAt)
	}
	firstHits := hits.Load()

	if _, err := svc.getFishUsage(t.Context(), account, "passive", false); err != nil {
		t.Fatalf("cached getFishUsage: %v", err)
	}
	if hits.Load() != firstHits {
		t.Fatalf("cache miss: hits %d, want %d", hits.Load(), firstHits)
	}

	if _, err := svc.getFishUsage(t.Context(), account, "active", true); err != nil {
		t.Fatalf("force getFishUsage: %v", err)
	}
	if hits.Load() == firstHits {
		t.Fatal("force refresh did not hit upstream")
	}
}

func TestGetFishUsageErrorCacheSkipsForce(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Invalid token"}`))
	}))
	t.Cleanup(server.Close)

	orig := fishUsageAPIBaseForTest
	fishUsageAPIBaseForTest = server.URL
	t.Cleanup(func() { fishUsageAPIBaseForTest = orig })

	svc := &AccountUsageService{cache: NewUsageCache()}
	account := &Account{
		ID:          8,
		Platform:    PlatformFish,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{fishCredentialToken: "tok"},
	}

	info, err := svc.getFishUsage(t.Context(), account, "active", true)
	if err != nil {
		t.Fatalf("getFishUsage: %v", err)
	}
	if !info.NeedsReauth || info.ErrorCode != errorCodeUnauthenticated {
		t.Fatalf("degraded = %+v", info)
	}
	firstHits := hits.Load()

	again, err := svc.getFishUsage(t.Context(), account, "active", true)
	if err != nil {
		t.Fatalf("cached error: %v", err)
	}
	if hits.Load() != firstHits {
		t.Fatalf("error cache bypassed on force: hits %d, want %d", hits.Load(), firstHits)
	}
	if again.Error == "" {
		t.Fatal("expected cached error")
	}
}

func TestGetFishUsagePersistsWalletBalanceIncludingZero(t *testing.T) {
	fishUsageServerWithWallet(t, `{"total":8000,"balance":0,"type":"free"}`,
		`{"plan":"free","dailyImage":{"limit":3,"used":1,"remaining":2,"resetAt":"2099-03-14T00:00:00Z"}}`)
	repo := &fishUsageRepoStub{}
	svc := &AccountUsageService{cache: NewUsageCache(), accountRepo: repo}
	account := &Account{ID: 7, Platform: PlatformFish, Type: AccountTypeAPIKey, Credentials: map[string]any{fishCredentialToken: "tok"}}

	if _, err := svc.getFishUsage(t.Context(), account, "active", true); err != nil {
		t.Fatalf("getFishUsage: %v", err)
	}
	if got, ok := repo.extra[FishWalletBalanceExtraKey]; !ok || got != 0 {
		t.Fatalf("balance = %v (present %v), want 0 persisted", got, ok)
	}
	if at, _ := repo.extra[FishWalletBalanceAtExtraKey].(string); at == "" {
		t.Fatal("balance_at not written")
	}
	if got, ok := repo.extra[FishWalletBelowExtraKey]; !ok || got != nil {
		t.Fatalf("below = %v (present %v), want cleared to nil", got, ok)
	}
	if repo.clears != 0 {
		t.Fatalf("clears = %d, want 0 for a zero balance", repo.clears)
	}
}

func TestGetFishUsageRecoversCreditCooldownOnlyWhenItCanStillPay(t *testing.T) {
	future := time.Now().Add(12 * time.Hour)
	room := `{"plan":"free","dailyImage":{"limit":3,"used":1,"remaining":2}}`
	cases := []struct {
		name         string
		reason       string
		entitlements string
		priceKnown   bool
		// currentReason 是清除前重读到的冷却原因；空 = 与巡检开始时相同。
		currentReason string
		wantClears    int
	}{
		{"credits cooldown with daily room", fishCreditsExhaustedReason, room, true, "", 1},
		{"credits cooldown but no price observed yet", fishCreditsExhaustedReason, room, false, "", 0},
		{"credits cooldown but daily exhausted", fishCreditsExhaustedReason, `{"plan":"free","dailyImage":{"limit":3,"used":3,"remaining":0}}`, true, "", 0},
		{"another subsystem's cooldown", fishRateLimitCooldownReason, room, true, "", 0},
		{"cooldown replaced while the sweep ran", fishCreditsExhaustedReason, room, true, fishRateLimitCooldownReason, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFishPricesForTest(t)
			if tc.priceKnown {
				fishPrices.record(fishPriceKey{model: "gpt-image-2"}, &fish.PriceResult{PerItemCredits: 150}, 1, time.Now())
			}
			fishUsageServer(t, tc.entitlements)
			account := &Account{
				ID: 7, Platform: PlatformFish, Type: AccountTypeAPIKey,
				Credentials:             map[string]any{fishCredentialToken: "tok"},
				TempUnschedulableUntil:  &future,
				TempUnschedulableReason: tc.reason,
			}
			current := *account
			if tc.currentReason != "" {
				current.TempUnschedulableReason = tc.currentReason
			}
			repo := &fishUsageRepoStub{account: &current}
			svc := &AccountUsageService{cache: NewUsageCache(), accountRepo: repo}
			if _, err := svc.getFishUsage(t.Context(), account, "active", true); err != nil {
				t.Fatalf("getFishUsage: %v", err)
			}
			if repo.clears != tc.wantClears {
				t.Fatalf("clears = %d, want %d", repo.clears, tc.wantClears)
			}
		})
	}
}
