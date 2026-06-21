package service

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

var (
	ErrDailyCheckinDisabled  = infraerrors.BadRequest("DAILY_CHECKIN_DISABLED", "daily check-in is disabled")
	ErrDailyCheckinAlready   = infraerrors.Conflict("DAILY_CHECKIN_ALREADY", "you have already checked in today")
	ErrDailyCheckinExhausted = infraerrors.Conflict(
		"DAILY_CHECKIN_EXHAUSTED",
		"daily check-in reward pool has been exhausted",
	)
	ErrDailyCheckinRechargeRequired = infraerrors.Forbidden(
		"DAILY_CHECKIN_RECHARGE_REQUIRED",
		"minimum recharge amount is required for daily check-in",
	)
)

type DailyCheckinStatus struct {
	Enabled           bool       `json:"enabled"`
	CheckedInToday    bool       `json:"checked_in_today"`
	TodayReward       float64    `json:"today_reward"`
	TodayTotalGranted float64    `json:"today_total_granted"`
	DailyTotalLimit   float64    `json:"daily_total_limit"`
	MinReward         float64    `json:"min_reward"`
	MaxReward         float64    `json:"max_reward"`
	MinRechargeAmount float64    `json:"min_recharge_amount"`
	TotalRecharged    float64    `json:"total_recharged"`
	RechargeEligible  bool       `json:"recharge_eligible"`
	CheckinDate       string     `json:"checkin_date"`
	LastCheckinAt     *time.Time `json:"last_checkin_at,omitempty"`
	LastReward        float64    `json:"last_reward,omitempty"`
	NextAvailableAt   time.Time  `json:"next_available_at"`
	RemainingToday    float64    `json:"remaining_today"`
	ExhaustedToday    bool       `json:"exhausted_today"`
}

type DailyCheckinResult struct {
	DailyCheckinStatus
	Reward  float64 `json:"reward"`
	Balance float64 `json:"balance"`
}

type DailyCheckinRecord struct {
	UserID    int64
	Date      string
	Reward    float64
	CreatedAt time.Time
}

type DailyCheckinAdminRecord struct {
	UserID      int64     `json:"user_id"`
	Email       string    `json:"email"`
	Username    string    `json:"username"`
	CheckinDate string    `json:"checkin_date"`
	Reward      float64   `json:"reward"`
	CreatedAt   time.Time `json:"created_at"`
}

type DailyCheckinAdminListFilter struct {
	Search    string
	UserID    int64
	StartDate string
	EndDate   string
	Page      int
	PageSize  int
	SortBy    string
	SortOrder string
}

type DailyCheckinClaimInput struct {
	UserID          int64
	Date            string
	Reward          float64
	DailyTotalLimit float64
	MinReward       float64
	GrantedSoFar    float64
}

type DailyCheckinClaimResult struct {
	Record            DailyCheckinRecord
	TodayTotalGranted float64
	Balance           float64
}

type DailyCheckinRepository interface {
	GetUserCheckin(ctx context.Context, userID int64, date string) (*DailyCheckinRecord, error)
	GetUserLatestCheckin(ctx context.Context, userID int64) (*DailyCheckinRecord, error)
	SumRewardsByDate(ctx context.Context, date string) (float64, error)
	Claim(ctx context.Context, input DailyCheckinClaimInput) (*DailyCheckinClaimResult, error)
	ListAdminRecords(ctx context.Context, filter DailyCheckinAdminListFilter) ([]DailyCheckinAdminRecord, int64, error)
}

type DailyCheckinUserRepository interface {
	GetByID(ctx context.Context, id int64) (*User, error)
}

type DailyCheckinService struct {
	repo                DailyCheckinRepository
	userRepo            DailyCheckinUserRepository
	cfg                 *config.Config
	billingCacheService *BillingCacheService
	settingService      *SettingService
}

func NewDailyCheckinService(repo DailyCheckinRepository, userRepo DailyCheckinUserRepository, cfg *config.Config, billingCacheService *BillingCacheService) *DailyCheckinService {
	return &DailyCheckinService{
		repo:                repo,
		userRepo:            userRepo,
		cfg:                 cfg,
		billingCacheService: billingCacheService,
	}
}

func (s *DailyCheckinService) SetSettingService(settingService *SettingService) {
	if s != nil {
		s.settingService = settingService
	}
}

func ProvideDailyCheckinService(repo DailyCheckinRepository, userRepo DailyCheckinUserRepository, cfg *config.Config, billingCacheService *BillingCacheService, settingService *SettingService) *DailyCheckinService {
	svc := NewDailyCheckinService(repo, userRepo, cfg, billingCacheService)
	svc.SetSettingService(settingService)
	return svc
}

func (s *DailyCheckinService) GetStatus(ctx context.Context, userID int64) (*DailyCheckinStatus, error) {
	settings := s.settings(ctx)
	claimable := dailyCheckinClaimable(settings)
	today, nextAvailableAt := s.todayWindow()

	status := &DailyCheckinStatus{
		Enabled:           claimable,
		DailyTotalLimit:   settings.DailyTotalLimit,
		MinReward:         settings.MinReward,
		MaxReward:         settings.MaxReward,
		MinRechargeAmount: settings.MinRechargeAmount,
		RechargeEligible:  true,
		CheckinDate:       today,
		NextAvailableAt:   nextAvailableAt,
	}

	if s == nil || s.repo == nil || userID <= 0 {
		status.RechargeEligible = dailyCheckinRechargeEligible(status.TotalRecharged, settings.MinRechargeAmount)
		return status, nil
	}

	totalRecharged, err := s.userTotalRecharged(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get daily check-in recharge total: %w", err)
	}
	status.TotalRecharged = totalRecharged
	status.RechargeEligible = dailyCheckinRechargeEligible(totalRecharged, settings.MinRechargeAmount)

	if record, err := s.repo.GetUserCheckin(ctx, userID, today); err != nil {
		return nil, fmt.Errorf("get daily check-in status: %w", err)
	} else if record != nil {
		status.CheckedInToday = true
		status.TodayReward = roundCheckinReward(record.Reward)
		status.LastCheckinAt = &record.CreatedAt
		status.LastReward = roundCheckinReward(record.Reward)
	} else if latest, latestErr := s.repo.GetUserLatestCheckin(ctx, userID); latestErr != nil {
		return nil, fmt.Errorf("get latest daily check-in: %w", latestErr)
	} else if latest != nil {
		status.LastCheckinAt = &latest.CreatedAt
		status.LastReward = roundCheckinReward(latest.Reward)
	}

	if total, err := s.repo.SumRewardsByDate(ctx, today); err != nil {
		return nil, fmt.Errorf("sum daily check-in rewards: %w", err)
	} else {
		status.TodayTotalGranted = roundCheckinReward(total)
	}
	applyDailyCheckinRemaining(status)
	return status, nil
}

func (s *DailyCheckinService) Claim(ctx context.Context, userID int64) (*DailyCheckinResult, error) {
	settings := s.settings(ctx)
	if !dailyCheckinClaimable(settings) {
		return nil, ErrDailyCheckinDisabled
	}
	if userID <= 0 {
		return nil, ErrUserNotFound
	}
	totalRecharged, err := s.userTotalRecharged(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get daily check-in recharge total: %w", err)
	}
	if !dailyCheckinRechargeEligible(totalRecharged, settings.MinRechargeAmount) {
		return nil, ErrDailyCheckinRechargeRequired
	}

	today, nextAvailableAt := s.todayWindow()
	grantedSoFar, err := s.repo.SumRewardsByDate(ctx, today)
	if err != nil {
		return nil, fmt.Errorf("sum daily check-in rewards: %w", err)
	}
	remaining := settings.DailyTotalLimit - grantedSoFar
	if remaining <= 0 {
		return nil, ErrDailyCheckinExhausted
	}

	reward := randomDailyCheckinReward(settings.MinReward, settings.MaxReward, remaining)
	if reward <= 0 {
		return nil, ErrDailyCheckinExhausted
	}

	claimed, err := s.repo.Claim(ctx, DailyCheckinClaimInput{
		UserID:          userID,
		Date:            today,
		Reward:          reward,
		DailyTotalLimit: settings.DailyTotalLimit,
		MinReward:       settings.MinReward,
		GrantedSoFar:    grantedSoFar,
	})
	if err != nil {
		return nil, err
	}
	s.invalidateBalanceCache(userID)

	status := DailyCheckinStatus{
		Enabled:           dailyCheckinClaimable(settings),
		CheckedInToday:    true,
		TodayReward:       roundCheckinReward(claimed.Record.Reward),
		TodayTotalGranted: roundCheckinReward(claimed.TodayTotalGranted),
		DailyTotalLimit:   settings.DailyTotalLimit,
		MinReward:         settings.MinReward,
		MaxReward:         settings.MaxReward,
		MinRechargeAmount: settings.MinRechargeAmount,
		TotalRecharged:    totalRecharged,
		RechargeEligible:  true,
		CheckinDate:       today,
		LastCheckinAt:     &claimed.Record.CreatedAt,
		LastReward:        roundCheckinReward(claimed.Record.Reward),
		NextAvailableAt:   nextAvailableAt,
	}
	applyDailyCheckinRemaining(&status)

	return &DailyCheckinResult{
		DailyCheckinStatus: status,
		Reward:             roundCheckinReward(claimed.Record.Reward),
		Balance:            roundCheckinReward(claimed.Balance),
	}, nil
}

func (s *DailyCheckinService) AdminListRecords(ctx context.Context, filter DailyCheckinAdminListFilter) ([]DailyCheckinAdminRecord, int64, error) {
	if s == nil || s.repo == nil {
		return nil, 0, fmt.Errorf("daily check-in service is not configured")
	}
	filter.Search = strings.TrimSpace(filter.Search)
	filter.StartDate = strings.TrimSpace(filter.StartDate)
	filter.EndDate = strings.TrimSpace(filter.EndDate)
	filter.SortBy = strings.TrimSpace(filter.SortBy)
	if filter.SortBy == "" {
		filter.SortBy = "created_at"
	}
	filter.SortOrder = strings.ToLower(strings.TrimSpace(filter.SortOrder))
	if filter.SortOrder != "asc" {
		filter.SortOrder = "desc"
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 1000 {
		filter.PageSize = 1000
	}
	return s.repo.ListAdminRecords(ctx, filter)
}

func (s *DailyCheckinService) settings(ctx context.Context) config.DailyCheckinConfig {
	if s != nil && s.settingService != nil {
		return normalizeDailyCheckinConfig(s.settingService.ResolveDailyCheckinConfig(ctx))
	}
	if s == nil || s.cfg == nil {
		return config.DailyCheckinConfig{}
	}
	return normalizeDailyCheckinConfig(s.cfg.DailyCheckin)
}

func normalizeDailyCheckinConfig(settings config.DailyCheckinConfig) config.DailyCheckinConfig {
	if !isFiniteNonNegativeFloat(settings.DailyTotalLimit) {
		settings.DailyTotalLimit = 0
	}
	if !isFiniteNonNegativeFloat(settings.MinReward) {
		settings.MinReward = 0
	}
	if !isFiniteNonNegativeFloat(settings.MaxReward) {
		settings.MaxReward = 0
	}
	if !isFiniteNonNegativeFloat(settings.MinRechargeAmount) {
		settings.MinRechargeAmount = 0
	}
	if settings.MaxReward < settings.MinReward {
		settings.MaxReward = settings.MinReward
	}
	settings.DailyTotalLimit = roundCheckinReward(settings.DailyTotalLimit)
	settings.MinReward = roundCheckinReward(settings.MinReward)
	settings.MaxReward = roundCheckinReward(settings.MaxReward)
	settings.MinRechargeAmount = roundCheckinReward(settings.MinRechargeAmount)
	return settings
}

func dailyCheckinClaimable(settings config.DailyCheckinConfig) bool {
	return settings.Enabled &&
		settings.DailyTotalLimit > 0 &&
		settings.MaxReward > 0 &&
		settings.MinReward <= settings.DailyTotalLimit
}

func (s *DailyCheckinService) todayWindow() (string, time.Time) {
	loc := time.Local
	if s != nil && s.cfg != nil && s.cfg.Timezone != "" {
		if loaded, err := time.LoadLocation(s.cfg.Timezone); err == nil {
			loc = loaded
		}
	}
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	return start.Format("2006-01-02"), start.AddDate(0, 0, 1)
}

func (s *DailyCheckinService) invalidateBalanceCache(userID int64) {
	if s == nil || s.billingCacheService == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.billingCacheService.InvalidateUserBalance(ctx, userID); err != nil {
		logger.LegacyPrintf("service.daily_checkin", "failed to invalidate billing cache for user %d: %v", userID, err)
	}
}

func (s *DailyCheckinService) userTotalRecharged(ctx context.Context, userID int64) (float64, error) {
	if s == nil || s.userRepo == nil {
		return 0, nil
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return 0, err
	}
	return roundCheckinReward(user.TotalRecharged), nil
}

func dailyCheckinRechargeEligible(totalRecharged, minRechargeAmount float64) bool {
	minRechargeAmount = roundCheckinReward(minRechargeAmount)
	if minRechargeAmount <= 0 {
		return true
	}
	return roundCheckinReward(totalRecharged) >= minRechargeAmount
}

func randomDailyCheckinReward(minReward, maxReward, remaining float64) float64 {
	minReward = math.Max(roundCheckinReward(minReward), 0)
	maxReward = math.Max(roundCheckinReward(maxReward), minReward)
	remaining = roundCheckinReward(remaining)
	if remaining <= 0 {
		return 0
	}
	if minReward > 0 && remaining < minReward {
		return 0
	}
	if maxReward > remaining {
		maxReward = remaining
	}
	if maxReward <= 0 {
		return 0
	}
	if minReward > maxReward {
		minReward = maxReward
	}
	if maxReward == minReward {
		return roundCheckinReward(maxReward)
	}
	return roundCheckinReward(minReward + rand.Float64()*(maxReward-minReward))
}

func applyDailyCheckinRemaining(status *DailyCheckinStatus) {
	if status == nil {
		return
	}
	remaining := status.DailyTotalLimit - status.TodayTotalGranted
	if remaining < 0 {
		remaining = 0
	}
	status.RemainingToday = roundCheckinReward(remaining)
	status.ExhaustedToday = status.Enabled && status.DailyTotalLimit > 0 && status.RemainingToday <= 0
}

func roundCheckinReward(value float64) float64 {
	if !isFiniteNonNegativeFloat(math.Abs(value)) {
		return 0
	}
	return math.Round(value*1e8) / 1e8
}

func isFiniteNonNegativeFloat(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
