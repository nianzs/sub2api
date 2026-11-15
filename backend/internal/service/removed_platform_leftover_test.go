//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// removedPlatformsForTest were once registered, so rows may survive in existing
// databases (old migrations still allow adobe in the DB CHECK constraints).
var removedPlatformsForTest = []string{"adobe"}

// A leftover OAuth account on a removed platform must behave as if it did not
// exist: it is not treated as a concrete platform, does not get a default
// model mapping, is never sent to another provider's API, and new accounts on
// that platform are rejected.
func TestRemovedPlatformLeftoverAccountIsInert(t *testing.T) {
	for _, platform := range removedPlatformsForTest {
		t.Run(platform, func(t *testing.T) {
			testRemovedPlatformLeftoverAccountIsInert(t, platform)
		})
	}
}

func testRemovedPlatformLeftoverAccountIsInert(t *testing.T, removedPlatformForTest string) {
	leftover := &Account{
		ID:          9301,
		Platform:    removedPlatformForTest,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"cookie": "aux_sid=x", "access_token": "stale"},
	}
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{leftover.ID: leftover}}

	t.Run("not a concrete platform", func(t *testing.T) {
		require.False(t, isConcreteRequestPlatform(leftover.Platform))
		require.NotContains(t, AllowedQuotaPlatforms, leftover.Platform)
		require.NotContains(t, schedulerSnapshotPlatforms(), leftover.Platform)
	})

	t.Run("no default model mapping", func(t *testing.T) {
		require.Empty(t, leftover.GetModelMapping())
	})

	t.Run("usage degrades without contacting an upstream", func(t *testing.T) {
		svc := NewAccountUsageService(repo, nil, nil, nil, nil, nil, nil, nil, NewUsageCache(), nil, nil)
		usage, err := svc.GetUsage(context.Background(), leftover.ID)
		require.NoError(t, err)
		require.NotNil(t, usage)
		require.Contains(t, usage.Error, "unsupported account platform")
	})

	t.Run("connection test is refused", func(t *testing.T) {
		svc := &AccountTestService{accountRepo: &shadowSkipTestRepo{account: leftover}}
		err := svc.TestAccountConnection(newShadowTestGinCtx(), leftover.ID, "", "", "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "Unsupported account platform")
	})

	t.Run("token refresh never selects it", func(t *testing.T) {
		svc := NewTokenRefreshService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		require.NotContains(t, svc.eligiblePlatforms(), leftover.Platform)
	})

	t.Run("creating a new account is rejected", func(t *testing.T) {
		createRepo := &createAccountKiroCreditRepoStub{}
		admin := &adminServiceImpl{accountRepo: createRepo}
		_, err := admin.CreateAccount(context.Background(), &CreateAccountInput{
			Name:                 "leftover",
			Platform:             removedPlatformForTest,
			Type:                 AccountTypeOAuth,
			SkipDefaultGroupBind: true,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported account platform")
		require.Zero(t, createRepo.createCalls)
	})
}
