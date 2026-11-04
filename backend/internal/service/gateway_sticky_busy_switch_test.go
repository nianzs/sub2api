//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// 粘性号槽位满时：Kiro 直接换到有空槽的号（会话改绑），只有全满才兜底排队；
// 其它平台保持原行为，在粘性号上排队等。
func TestSelectAccountWithLoadAwareness_StickyBusy(t *testing.T) {
	const (
		sessionHash = "sticky-busy"
		model       = "claude-sonnet-4-6"
	)

	cases := []struct {
		name          string
		platform      string
		routed        bool
		acquire       map[int64]bool
		wantAccountID int64
		wantWait      bool
		wantFallback  bool // 等待计划用兜底超时而不是粘性超时
	}{
		{name: "kiro_switches_to_free_account", platform: PlatformKiro, acquire: map[int64]bool{1: false, 2: true}, wantAccountID: 2},
		{name: "kiro_routed_switches_to_free_account", platform: PlatformKiro, routed: true, acquire: map[int64]bool{1: false, 2: true}, wantAccountID: 2},
		{name: "kiro_all_busy_falls_back_to_queue", platform: PlatformKiro, acquire: map[int64]bool{1: false, 2: false}, wantWait: true, wantFallback: true},
		{name: "anthropic_still_waits_on_sticky", platform: PlatformAnthropic, acquire: map[int64]bool{1: false, 2: true}, wantAccountID: 1, wantWait: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &mockAccountRepoForPlatform{
				accounts: []Account{
					{ID: 1, Platform: tc.platform, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 1},
					{ID: 2, Platform: tc.platform, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 1},
				},
				accountsByID: map[int64]*Account{},
			}
			for i := range repo.accounts {
				repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
			}
			cache := &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{sessionHash: 1}}
			cfg := testConfig()
			cfg.Gateway.Scheduling.LoadBatchEnabled = true
			cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
			cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
			cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
			cfg.Gateway.Scheduling.FallbackMaxWaiting = 100

			svc := &GatewayService{
				accountRepo:         repo,
				cache:               cache,
				cfg:                 cfg,
				concurrencyService:  NewConcurrencyService(&mockConcurrencyCache{acquireResults: tc.acquire}),
				tlsFPProfileService: &TLSFingerprintProfileService{},
			}

			ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, tc.platform)
			var groupID *int64
			if tc.routed {
				id := int64(30)
				groupID = &id
				svc.groupRepo = &mockGroupRepoForGateway{groups: map[int64]*Group{
					id: {
						ID: id, Platform: tc.platform, Status: StatusActive, Hydrated: true,
						ModelRoutingEnabled: true,
						ModelRouting:        map[string][]int64{model: {1, 2}},
					},
				}}
				ctx = context.Background()
			}

			result, err := svc.SelectAccountWithLoadAwareness(ctx, groupID, sessionHash, model, nil, "", 0)
			require.NoError(t, err)
			require.NotNil(t, result)

			if !tc.wantWait {
				require.True(t, result.Acquired)
				require.Nil(t, result.WaitPlan)
				require.Equal(t, tc.wantAccountID, result.Account.ID)
				require.Equal(t, tc.wantAccountID, cache.sessionBindings[sessionHash], "session should be rebound to the new account")
				return
			}
			require.False(t, result.Acquired)
			require.NotNil(t, result.WaitPlan)
			if tc.wantAccountID != 0 {
				require.Equal(t, tc.wantAccountID, result.Account.ID)
			}
			if tc.wantFallback {
				require.Equal(t, cfg.Gateway.Scheduling.FallbackWaitTimeout, result.WaitPlan.Timeout)
			} else {
				require.Equal(t, cfg.Gateway.Scheduling.StickySessionWaitTimeout, result.WaitPlan.Timeout)
			}
		})
	}
}
