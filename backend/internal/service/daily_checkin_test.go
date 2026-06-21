//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type fakeDailyCheckinRepo struct {
	checkin       *DailyCheckinRecord
	latest        *DailyCheckinRecord
	total         float64
	claimResult   *DailyCheckinClaimResult
	claimErr      error
	claimInputs   []DailyCheckinClaimInput
	sumCallCount  int
	userCallCount int
}

type fakeDailyCheckinUserRepo struct {
	user     *User
	getErr   error
	getCalls int
}

type fakeDailyCheckinSettingRepo struct {
	values  map[string]string
	updates map[string]string
}

func (r *fakeDailyCheckinSettingRepo) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}

func (r *fakeDailyCheckinSettingRepo) GetValue(context.Context, string) (string, error) {
	panic("unexpected GetValue call")
}

func (r *fakeDailyCheckinSettingRepo) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (r *fakeDailyCheckinSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *fakeDailyCheckinSettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	r.updates = make(map[string]string, len(settings))
	for key, value := range settings {
		r.updates[key] = value
		r.values[key] = value
	}
	return nil
}

func (r *fakeDailyCheckinSettingRepo) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (r *fakeDailyCheckinSettingRepo) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func (r *fakeDailyCheckinRepo) GetUserCheckin(context.Context, int64, string) (*DailyCheckinRecord, error) {
	r.userCallCount++
	return r.checkin, nil
}

func (r *fakeDailyCheckinRepo) GetUserLatestCheckin(context.Context, int64) (*DailyCheckinRecord, error) {
	return r.latest, nil
}

func (r *fakeDailyCheckinRepo) SumRewardsByDate(context.Context, string) (float64, error) {
	r.sumCallCount++
	return r.total, nil
}

func (r *fakeDailyCheckinRepo) Claim(_ context.Context, input DailyCheckinClaimInput) (*DailyCheckinClaimResult, error) {
	r.claimInputs = append(r.claimInputs, input)
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	if r.claimResult != nil {
		return r.claimResult, nil
	}
	return &DailyCheckinClaimResult{
		Record: DailyCheckinRecord{
			UserID:    input.UserID,
			Date:      input.Date,
			Reward:    input.Reward,
			CreatedAt: time.Now(),
		},
		TodayTotalGranted: r.total + input.Reward,
		Balance:           10 + input.Reward,
	}, nil
}

func (r *fakeDailyCheckinRepo) ListAdminRecords(context.Context, DailyCheckinAdminListFilter) ([]DailyCheckinAdminRecord, int64, error) {
	return nil, 0, nil
}

func (r *fakeDailyCheckinUserRepo) Create(context.Context, *User) error {
	panic("unexpected Create call")
}

func (r *fakeDailyCheckinUserRepo) GetByID(context.Context, int64) (*User, error) {
	r.getCalls++
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.user != nil {
		return r.user, nil
	}
	return &User{ID: 1}, nil
}

func TestDailyCheckinGetStatusDisabledWhenConfigNotClaimable(t *testing.T) {
	repo := &fakeDailyCheckinRepo{}
	userRepo := &fakeDailyCheckinUserRepo{}
	svc := NewDailyCheckinService(repo, userRepo, &config.Config{
		DailyCheckin: config.DailyCheckinConfig{
			Enabled:         true,
			DailyTotalLimit: 0,
			MinReward:       0,
			MaxReward:       1,
		},
	}, nil)

	status, err := svc.GetStatus(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, status.Enabled)
	require.Equal(t, 0.0, status.DailyTotalLimit)
	require.Equal(t, 1.0, status.MaxReward)
	require.True(t, status.RechargeEligible)
	require.Equal(t, 1, repo.userCallCount)
	require.Equal(t, 1, repo.sumCallCount)
	require.Equal(t, 1, userRepo.getCalls)
}

func TestDailyCheckinClaimDisabledWhenConfigNotClaimable(t *testing.T) {
	repo := &fakeDailyCheckinRepo{}
	userRepo := &fakeDailyCheckinUserRepo{}
	svc := NewDailyCheckinService(repo, userRepo, &config.Config{
		DailyCheckin: config.DailyCheckinConfig{
			Enabled:         true,
			DailyTotalLimit: 1,
			MinReward:       2,
			MaxReward:       2,
		},
	}, nil)

	result, err := svc.Claim(context.Background(), 1)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrDailyCheckinDisabled)
	require.Empty(t, repo.claimInputs)
	require.Equal(t, 0, repo.sumCallCount)
	require.Equal(t, 0, userRepo.getCalls)
}

func TestDailyCheckinClaimExhaustedWhenRemainingBelowMinimum(t *testing.T) {
	repo := &fakeDailyCheckinRepo{total: 0.95}
	userRepo := &fakeDailyCheckinUserRepo{}
	svc := NewDailyCheckinService(repo, userRepo, &config.Config{
		DailyCheckin: config.DailyCheckinConfig{
			Enabled:         true,
			DailyTotalLimit: 1,
			MinReward:       0.1,
			MaxReward:       0.2,
		},
	}, nil)

	result, err := svc.Claim(context.Background(), 1)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrDailyCheckinExhausted)
	require.Empty(t, repo.claimInputs)
	require.Equal(t, 1, repo.sumCallCount)
	require.Equal(t, 1, userRepo.getCalls)
}

func TestDailyCheckinClaimPropagatesAlreadyCheckedIn(t *testing.T) {
	repo := &fakeDailyCheckinRepo{
		total:    0.2,
		claimErr: ErrDailyCheckinAlready,
	}
	svc := NewDailyCheckinService(repo, &fakeDailyCheckinUserRepo{}, &config.Config{
		DailyCheckin: config.DailyCheckinConfig{
			Enabled:         true,
			DailyTotalLimit: 1,
			MinReward:       0.1,
			MaxReward:       0.2,
		},
	}, nil)

	result, err := svc.Claim(context.Background(), 1)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrDailyCheckinAlready)
	require.Len(t, repo.claimInputs, 1)
}

func TestDailyCheckinClaimRewardWithinRangeAndUpdatesStatus(t *testing.T) {
	repo := &fakeDailyCheckinRepo{total: 0.2}
	svc := NewDailyCheckinService(repo, &fakeDailyCheckinUserRepo{user: &User{ID: 42, TotalRecharged: 5}}, &config.Config{
		Timezone: "Asia/Shanghai",
		DailyCheckin: config.DailyCheckinConfig{
			Enabled:           true,
			DailyTotalLimit:   1,
			MinReward:         0.1,
			MaxReward:         0.2,
			MinRechargeAmount: 3,
		},
	}, nil)

	result, err := svc.Claim(context.Background(), 42)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Enabled)
	require.True(t, result.CheckedInToday)
	require.GreaterOrEqual(t, result.Reward, 0.1)
	require.LessOrEqual(t, result.Reward, 0.2)
	require.Equal(t, result.Reward, result.TodayReward)
	require.Equal(t, roundCheckinReward(0.2+result.Reward), result.TodayTotalGranted)
	require.Equal(t, roundCheckinReward(10+result.Reward), result.Balance)
	require.Equal(t, 3.0, result.MinRechargeAmount)
	require.Equal(t, 5.0, result.TotalRecharged)
	require.True(t, result.RechargeEligible)
	require.Len(t, repo.claimInputs, 1)
	require.Equal(t, int64(42), repo.claimInputs[0].UserID)
	require.Equal(t, 0.2, repo.claimInputs[0].GrantedSoFar)
}

func TestDailyCheckinClaimRequiresMinimumRechargeAmount(t *testing.T) {
	repo := &fakeDailyCheckinRepo{total: 0.2}
	svc := NewDailyCheckinService(repo, &fakeDailyCheckinUserRepo{user: &User{ID: 42, TotalRecharged: 4.99}}, &config.Config{
		DailyCheckin: config.DailyCheckinConfig{
			Enabled:           true,
			DailyTotalLimit:   1,
			MinReward:         0.1,
			MaxReward:         0.2,
			MinRechargeAmount: 5,
		},
	}, nil)

	result, err := svc.Claim(context.Background(), 42)
	require.Nil(t, result)
	require.ErrorIs(t, err, ErrDailyCheckinRechargeRequired)
	require.Empty(t, repo.claimInputs)
	require.Equal(t, 0, repo.sumCallCount)
}

func TestDailyCheckinStatusIncludesRechargeEligibility(t *testing.T) {
	repo := &fakeDailyCheckinRepo{}
	svc := NewDailyCheckinService(repo, &fakeDailyCheckinUserRepo{user: &User{ID: 42, TotalRecharged: 4.99}}, &config.Config{
		DailyCheckin: config.DailyCheckinConfig{
			Enabled:           true,
			DailyTotalLimit:   1,
			MinReward:         0.1,
			MaxReward:         0.2,
			MinRechargeAmount: 5,
		},
	}, nil)

	status, err := svc.GetStatus(context.Background(), 42)
	require.NoError(t, err)
	require.True(t, status.Enabled)
	require.Equal(t, 5.0, status.MinRechargeAmount)
	require.Equal(t, 4.99, status.TotalRecharged)
	require.False(t, status.RechargeEligible)
}

func TestDailyCheckinClaimWrapsUnexpectedRepositoryError(t *testing.T) {
	repoErr := errors.New("repository unavailable")
	repo := &fakeDailyCheckinRepo{claimErr: repoErr}
	svc := NewDailyCheckinService(repo, &fakeDailyCheckinUserRepo{}, &config.Config{
		DailyCheckin: config.DailyCheckinConfig{
			Enabled:         true,
			DailyTotalLimit: 1,
			MinReward:       0.1,
			MaxReward:       0.2,
		},
	}, nil)

	result, err := svc.Claim(context.Background(), 1)
	require.Nil(t, result)
	require.ErrorIs(t, err, repoErr)
}

func TestDailyCheckinServiceUsesRuntimeSettingsOverConfig(t *testing.T) {
	repo := &fakeDailyCheckinRepo{total: 0.2}
	settingRepo := &fakeDailyCheckinSettingRepo{values: map[string]string{
		SettingKeyDailyCheckinEnabled:           "true",
		SettingKeyDailyCheckinDailyTotalLimit:   "1",
		SettingKeyDailyCheckinMinReward:         "0.1",
		SettingKeyDailyCheckinMaxReward:         "0.2",
		SettingKeyDailyCheckinMinRechargeAmount: "0.5",
	}}
	settingService := NewSettingService(settingRepo, &config.Config{
		DailyCheckin: config.DailyCheckinConfig{Enabled: false},
	})
	svc := ProvideDailyCheckinService(repo, &fakeDailyCheckinUserRepo{user: &User{ID: 7, TotalRecharged: 1}}, &config.Config{
		DailyCheckin: config.DailyCheckinConfig{Enabled: false},
	}, nil, settingService)

	result, err := svc.Claim(context.Background(), 7)
	require.NoError(t, err)
	require.True(t, result.Enabled)
	require.Len(t, repo.claimInputs, 1)
	require.Equal(t, 1.0, repo.claimInputs[0].DailyTotalLimit)
	require.Equal(t, 0.1, repo.claimInputs[0].MinReward)
	require.Equal(t, 0.5, result.MinRechargeAmount)
}

func TestSettingServiceUpdateDailyCheckinSettingsPersistsKeys(t *testing.T) {
	repo := &fakeDailyCheckinSettingRepo{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})

	settings, err := svc.UpdateDailyCheckinSettings(context.Background(), DailyCheckinSettings{
		Enabled:           true,
		DailyTotalLimit:   1.234567891,
		MinReward:         0.1,
		MaxReward:         0.2,
		MinRechargeAmount: 3.456789123,
	})
	require.NoError(t, err)
	require.Equal(t, 1.23456789, settings.DailyTotalLimit)
	require.Equal(t, 3.45678912, settings.MinRechargeAmount)
	require.Equal(t, "true", repo.updates[SettingKeyDailyCheckinEnabled])
	require.Equal(t, "1.23456789", repo.updates[SettingKeyDailyCheckinDailyTotalLimit])
	require.Equal(t, "0.10000000", repo.updates[SettingKeyDailyCheckinMinReward])
	require.Equal(t, "0.20000000", repo.updates[SettingKeyDailyCheckinMaxReward])
	require.Equal(t, "3.45678912", repo.updates[SettingKeyDailyCheckinMinRechargeAmount])
}

func TestSettingServiceUpdateDailyCheckinSettingsRejectsInvalidEnabledConfig(t *testing.T) {
	repo := &fakeDailyCheckinSettingRepo{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})

	_, err := svc.UpdateDailyCheckinSettings(context.Background(), DailyCheckinSettings{
		Enabled:         true,
		DailyTotalLimit: 0,
		MinReward:       0.1,
		MaxReward:       0.2,
	})
	require.Error(t, err)
	require.Equal(t, "DAILY_CHECKIN_SETTINGS_INVALID", infraerrors.Reason(err))
	require.Nil(t, repo.updates)
}

func TestSettingServiceUpdateDailyCheckinSettingsRejectsRoundedZeroEnabledConfig(t *testing.T) {
	tests := []struct {
		name  string
		input DailyCheckinSettings
	}{
		{
			name: "daily total limit rounds to zero",
			input: DailyCheckinSettings{
				Enabled:         true,
				DailyTotalLimit: 0.000000001,
				MinReward:       0.00000001,
				MaxReward:       0.00000001,
			},
		},
		{
			name: "min reward is zero",
			input: DailyCheckinSettings{
				Enabled:         true,
				DailyTotalLimit: 1,
				MinReward:       0,
				MaxReward:       0.00000001,
			},
		},
		{
			name: "min reward rounds to zero",
			input: DailyCheckinSettings{
				Enabled:         true,
				DailyTotalLimit: 1,
				MinReward:       0.000000001,
				MaxReward:       0.1,
			},
		},
		{
			name: "max reward rounds to zero",
			input: DailyCheckinSettings{
				Enabled:         true,
				DailyTotalLimit: 1,
				MinReward:       0,
				MaxReward:       0.000000001,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeDailyCheckinSettingRepo{values: map[string]string{}}
			svc := NewSettingService(repo, &config.Config{})

			_, err := svc.UpdateDailyCheckinSettings(context.Background(), tt.input)

			require.Error(t, err)
			require.Equal(t, "DAILY_CHECKIN_SETTINGS_INVALID", infraerrors.Reason(err))
			require.Nil(t, repo.updates)
		})
	}
}
