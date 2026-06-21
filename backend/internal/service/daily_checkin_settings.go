package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type DailyCheckinSettings struct {
	Enabled           bool    `json:"enabled"`
	DailyTotalLimit   float64 `json:"daily_total_limit"`
	MinReward         float64 `json:"min_reward"`
	MaxReward         float64 `json:"max_reward"`
	MinRechargeAmount float64 `json:"min_recharge_amount"`
}

var dailyCheckinSettingKeys = []string{
	SettingKeyDailyCheckinEnabled,
	SettingKeyDailyCheckinDailyTotalLimit,
	SettingKeyDailyCheckinMinReward,
	SettingKeyDailyCheckinMaxReward,
	SettingKeyDailyCheckinMinRechargeAmount,
}

func (s *SettingService) GetDailyCheckinSettings(ctx context.Context) (DailyCheckinSettings, error) {
	result := s.defaultDailyCheckinSettings()
	if s == nil || s.settingRepo == nil {
		return result, nil
	}

	values, err := s.settingRepo.GetMultiple(ctx, dailyCheckinSettingKeys)
	if err != nil {
		return result, fmt.Errorf("get daily check-in settings: %w", err)
	}
	if raw, ok := values[SettingKeyDailyCheckinEnabled]; ok {
		if parsed, parseErr := strconv.ParseBool(strings.TrimSpace(raw)); parseErr == nil {
			result.Enabled = parsed
		}
	}
	if value, ok := parseDailyCheckinSettingFloat(values, SettingKeyDailyCheckinDailyTotalLimit); ok {
		result.DailyTotalLimit = value
	}
	if value, ok := parseDailyCheckinSettingFloat(values, SettingKeyDailyCheckinMinReward); ok {
		result.MinReward = value
	}
	if value, ok := parseDailyCheckinSettingFloat(values, SettingKeyDailyCheckinMaxReward); ok {
		result.MaxReward = value
	}
	if value, ok := parseDailyCheckinSettingFloat(values, SettingKeyDailyCheckinMinRechargeAmount); ok {
		result.MinRechargeAmount = value
	}

	return dailyCheckinSettingsFromConfig(dailyCheckinConfigFromSettings(result)), nil
}

func (s *SettingService) ResolveDailyCheckinConfig(ctx context.Context) config.DailyCheckinConfig {
	settings, err := s.GetDailyCheckinSettings(ctx)
	if err != nil {
		return dailyCheckinConfigFromSettings(s.defaultDailyCheckinSettings())
	}
	return dailyCheckinConfigFromSettings(settings)
}

func (s *SettingService) UpdateDailyCheckinSettings(ctx context.Context, input DailyCheckinSettings) (DailyCheckinSettings, error) {
	settings, err := validateDailyCheckinSettings(input)
	if err != nil {
		return DailyCheckinSettings{}, err
	}
	if s == nil || s.settingRepo == nil {
		return DailyCheckinSettings{}, fmt.Errorf("setting service is not configured")
	}

	updates := map[string]string{
		SettingKeyDailyCheckinEnabled:           strconv.FormatBool(settings.Enabled),
		SettingKeyDailyCheckinDailyTotalLimit:   strconv.FormatFloat(settings.DailyTotalLimit, 'f', 8, 64),
		SettingKeyDailyCheckinMinReward:         strconv.FormatFloat(settings.MinReward, 'f', 8, 64),
		SettingKeyDailyCheckinMaxReward:         strconv.FormatFloat(settings.MaxReward, 'f', 8, 64),
		SettingKeyDailyCheckinMinRechargeAmount: strconv.FormatFloat(settings.MinRechargeAmount, 'f', 8, 64),
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return DailyCheckinSettings{}, fmt.Errorf("update daily check-in settings: %w", err)
	}
	if s.onUpdate != nil {
		s.onUpdate()
	}
	return settings, nil
}

func (s *SettingService) defaultDailyCheckinSettings() DailyCheckinSettings {
	if s == nil || s.cfg == nil {
		return DailyCheckinSettings{}
	}
	return dailyCheckinSettingsFromConfig(s.cfg.DailyCheckin)
}

func dailyCheckinSettingsFromConfig(cfg config.DailyCheckinConfig) DailyCheckinSettings {
	cfg = normalizeDailyCheckinConfig(cfg)
	return DailyCheckinSettings{
		Enabled:           cfg.Enabled,
		DailyTotalLimit:   cfg.DailyTotalLimit,
		MinReward:         cfg.MinReward,
		MaxReward:         cfg.MaxReward,
		MinRechargeAmount: cfg.MinRechargeAmount,
	}
}

func dailyCheckinConfigFromSettings(settings DailyCheckinSettings) config.DailyCheckinConfig {
	return normalizeDailyCheckinConfig(config.DailyCheckinConfig{
		Enabled:           settings.Enabled,
		DailyTotalLimit:   settings.DailyTotalLimit,
		MinReward:         settings.MinReward,
		MaxReward:         settings.MaxReward,
		MinRechargeAmount: settings.MinRechargeAmount,
	})
}

func validateDailyCheckinSettings(input DailyCheckinSettings) (DailyCheckinSettings, error) {
	if !isFiniteNonNegativeFloat(input.DailyTotalLimit) {
		return DailyCheckinSettings{}, infraerrors.BadRequest("DAILY_CHECKIN_SETTINGS_INVALID", "daily_total_limit must be a finite non-negative number")
	}
	if !isFiniteNonNegativeFloat(input.MinReward) {
		return DailyCheckinSettings{}, infraerrors.BadRequest("DAILY_CHECKIN_SETTINGS_INVALID", "min_reward must be a finite non-negative number")
	}
	if !isFiniteNonNegativeFloat(input.MaxReward) {
		return DailyCheckinSettings{}, infraerrors.BadRequest("DAILY_CHECKIN_SETTINGS_INVALID", "max_reward must be a finite non-negative number")
	}
	if !isFiniteNonNegativeFloat(input.MinRechargeAmount) {
		return DailyCheckinSettings{}, infraerrors.BadRequest("DAILY_CHECKIN_SETTINGS_INVALID", "min_recharge_amount must be a finite non-negative number")
	}

	settings := DailyCheckinSettings{
		Enabled:           input.Enabled,
		DailyTotalLimit:   roundCheckinReward(input.DailyTotalLimit),
		MinReward:         roundCheckinReward(input.MinReward),
		MaxReward:         roundCheckinReward(input.MaxReward),
		MinRechargeAmount: roundCheckinReward(input.MinRechargeAmount),
	}
	if settings.MaxReward < settings.MinReward {
		return DailyCheckinSettings{}, infraerrors.BadRequest("DAILY_CHECKIN_SETTINGS_INVALID", "max_reward must be greater than or equal to min_reward")
	}
	if settings.Enabled && settings.DailyTotalLimit <= 0 {
		return DailyCheckinSettings{}, infraerrors.BadRequest("DAILY_CHECKIN_SETTINGS_INVALID", "daily_total_limit must be positive when daily check-in is enabled")
	}
	if settings.Enabled && settings.MaxReward <= 0 {
		return DailyCheckinSettings{}, infraerrors.BadRequest("DAILY_CHECKIN_SETTINGS_INVALID", "max_reward must be positive when daily check-in is enabled")
	}
	if settings.Enabled && settings.MinReward <= 0 {
		return DailyCheckinSettings{}, infraerrors.BadRequest("DAILY_CHECKIN_SETTINGS_INVALID", "min_reward must be positive when daily check-in is enabled")
	}
	if settings.Enabled && settings.MinReward > settings.DailyTotalLimit {
		return DailyCheckinSettings{}, infraerrors.BadRequest("DAILY_CHECKIN_SETTINGS_INVALID", "min_reward must be less than or equal to daily_total_limit when daily check-in is enabled")
	}
	return settings, nil
}

func parseDailyCheckinSettingFloat(values map[string]string, key string) (float64, bool) {
	raw, ok := values[key]
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || !isFiniteNonNegativeFloat(value) {
		return 0, false
	}
	return roundCheckinReward(value), true
}
