package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
)

// Fish token 号用量拉取的缓存与超时。成功 3 分钟、错误 1 分钟、singleflight，
// 与 Kiro 额度缓存同构：错误快照在 TTL 内即使 force 也不重打上游。
const (
	fishUsageCacheTTL     = apiCacheTTL
	fishUsageFetchTimeout = 20 * time.Second
)

// fishUsageAPIBaseForTest 非空时，用量拉取打到该基址（单测）。
var fishUsageAPIBaseForTest string

// fishUsageCache 缓存 Fish 积分与每日出图窗口快照。
type fishUsageCache struct {
	usageInfo *UsageInfo
	timestamp time.Time
}

func fishUsageCacheTTLFor(info *UsageInfo) time.Duration {
	if info != nil && (info.Error != "" || info.ErrorCode != "") {
		return fishUsageErrorTTL
	}
	return fishUsageCacheTTL
}

func (s *AccountUsageService) getCachedFishUsage(accountID int64) (*UsageInfo, bool) {
	if s == nil || s.cache == nil || accountID <= 0 {
		return nil, false
	}
	cached, ok := s.cache.fishUsageCache.Load(accountID)
	if !ok {
		return nil, false
	}
	entry, ok := cached.(*fishUsageCache)
	if !ok || entry.usageInfo == nil {
		return nil, false
	}
	if time.Since(entry.timestamp) >= fishUsageCacheTTLFor(entry.usageInfo) {
		return nil, false
	}
	return cloneUsageInfo(entry.usageInfo), true
}

func (s *AccountUsageService) storeFishUsageSnapshot(accountID int64, usage *UsageInfo) {
	if s == nil || s.cache == nil || accountID <= 0 || usage == nil {
		return
	}
	now := time.Now()
	if usage.UpdatedAt == nil {
		usage.UpdatedAt = &now
	}
	s.cache.fishUsageCache.Store(accountID, &fishUsageCache{
		usageInfo: cloneUsageInfo(usage),
		timestamp: now,
	})
}

// getFishUsage 拉取 Fish token 号的钱包进度、每日出图窗口和套餐。
// 中转号没有 Fish 钱包，调用方不应进入这里。
func (s *AccountUsageService) getFishUsage(ctx context.Context, account *Account, source string, forceRefresh bool) (*UsageInfo, error) {
	now := time.Now()
	if account == nil {
		return &UsageInfo{
			Source:    source,
			UpdatedAt: &now,
			Error:     "account is nil",
			ErrorCode: errorCodeNetworkError,
		}, nil
	}
	if !isFishTokenAccount(account) {
		return &UsageInfo{Source: source, UpdatedAt: &now}, nil
	}

	cached, hasCached := s.getCachedFishUsage(account.ID)
	if hasCached && (cached.Error != "" || cached.ErrorCode != "") {
		cached.Source = source
		return cached, nil
	}
	if !forceRefresh && hasCached {
		cached.Source = source
		return cached, nil
	}

	if s == nil || s.cache == nil {
		usage, err := s.fetchAndCacheFishUsage(ctx, account, source)
		if err != nil {
			degraded := buildFishDegradedUsage(err)
			degraded.Source = source
			return degraded, nil
		}
		usage.Source = source
		return usage, nil
	}

	flightKey := fmt.Sprintf("fish-usage:%d", account.ID)
	result, fetchErr, _ := s.cache.fishUsageFlight.Do(flightKey, func() (any, error) {
		if !forceRefresh {
			if usage, ok := s.getCachedFishUsage(account.ID); ok && usage.Error == "" && usage.ErrorCode == "" {
				return usage, nil
			}
		}
		return s.fetchAndCacheFishUsage(ctx, account, source)
	})
	if fetchErr == nil {
		if usage, ok := result.(*UsageInfo); ok && usage != nil {
			cloned := cloneUsageInfo(usage)
			cloned.Source = source
			if source == "active" && cloned.Error == "" {
				s.tryClearRecoverableAccountError(ctx, account)
			}
			return cloned, nil
		}
	}

	degraded := buildFishDegradedUsage(fetchErr)
	degraded.Source = source
	if hasCached {
		cached.Error = degraded.Error
		cached.ErrorCode = degraded.ErrorCode
		cached.NeedsReauth = degraded.NeedsReauth
		cached.Source = source
		return cached, nil
	}
	s.storeFishUsageSnapshot(account.ID, degraded)
	return degraded, nil
}

func (s *AccountUsageService) fetchAndCacheFishUsage(ctx context.Context, account *Account, source string) (*UsageInfo, error) {
	token := strings.TrimSpace(account.GetCredential(fishCredentialToken))
	if token == "" {
		return nil, fmt.Errorf("fish account has no api_key")
	}
	deviceID := strings.TrimSpace(account.GetCredential(fishCredentialDeviceID))
	if deviceID == "" {
		deviceID = fish.NewDeviceID()
	}
	identity := fish.Identity{DeviceID: deviceID}
	teamID := strings.TrimSpace(account.GetCredential(fishCredentialTeamID))

	client := fish.NewClient(fish.Options{ProxyURL: accountProxyURL(account), APIBase: fishUsageAPIBaseForTest})
	fetchCtx, cancel := context.WithTimeout(ctx, fishUsageFetchTimeout)
	defer cancel()

	if teamID == "" {
		self, err := client.UserSelf(fetchCtx, token, identity)
		if err != nil {
			return nil, err
		}
		teamID = strings.TrimSpace(self.ID)
	}

	now := time.Now()
	info := &UsageInfo{Source: source, UpdatedAt: &now}

	wallet, walletErr := client.QueryWallet(fetchCtx, token, identity, teamID)
	entitlements, entErr := client.QueryEntitlements(fetchCtx, token, identity)
	if walletErr != nil && entErr != nil {
		return nil, walletErr
	}

	if wallet != nil && (wallet.Total > 0 || wallet.Balance > 0) {
		used := wallet.Total - wallet.Balance
		if used < 0 {
			used = 0
		}
		info.FishCredits = &FishCreditProgress{
			CurrentUsage:   float64(used),
			UsageLimit:     float64(wallet.Total),
			PercentageUsed: percentageOrZero(float64(used), float64(wallet.Total)),
		}
	}
	if entitlements != nil {
		info.FishPlan = strings.TrimSpace(entitlements.Plan)
		if daily := entitlements.DailyImage; daily != nil && daily.Limit > 0 {
			used := daily.Used
			if used < 0 {
				used = 0
			}
			info.FishDaily = &FishCreditProgress{
				CurrentUsage:   float64(used),
				UsageLimit:     float64(daily.Limit),
				PercentageUsed: percentageOrZero(float64(used), float64(daily.Limit)),
			}
			if resetAt := strings.TrimSpace(daily.ResetAt); resetAt != "" {
				if parsed, err := time.Parse(time.RFC3339, resetAt); err == nil {
					info.FishResetAt = &parsed
				}
			}
		}
		s.applyFishDailyObservation(ctx, account, entitlements)
	}
	// 钱包余额落库（含 0，选号据此排除付不起的账号）；顺带解除被旧版「402
	// 一律耗尽」误冷却、实际还够最便宜一单的账号。每日次数状态未知时不解除。
	if walletErr == nil && wallet != nil && s.accountRepo != nil {
		applyFishWalletObservation(ctx, s.accountRepo, account.ID, wallet.Balance, now)
		dailyAvailable := entErr == nil && entitlements != nil && !fishDailyImageExhausted(entitlements.DailyImage)
		recoverFishCreditCooldown(ctx, s.accountRepo, account, wallet.Balance, dailyAvailable, now)
	}
	s.storeFishUsageSnapshot(account.ID, info)
	return info, nil
}

// applyFishDailyObservation 把 entitlements 观察落到账号侧（纯反应式冷却之外
// 的主动通道：没被流量打到的耗尽号在这里也能挂上冷却）：
//   - dailyImage 已耗尽 → SetTempUnschedulable 到重置时刻（resetAt 缺失/过期
//     回落下一个 UTC 零点；SQL 只延长不缩短，不影响更长的既有冷却）；
//   - plan 回填 credentials.plan_type——402 冷却路径据此把 free 号直挂 UTC 零点。
//
// 失败只降级精度（冷却/回填缺失），不影响用量展示。
func (s *AccountUsageService) applyFishDailyObservation(ctx context.Context, account *Account, entitlements *fish.Entitlements) {
	if s == nil || s.accountRepo == nil || account == nil || account.ID <= 0 || entitlements == nil || !isFishTokenAccount(account) {
		return
	}
	if until, ok := fishDailyResetFromQuota(entitlements.DailyImage, time.Now()); ok {
		writeFishCooldown(ctx, s.accountRepo, account.ID, until, fishDailyQuotaExhaustedReason)
	}
	if plan := strings.TrimSpace(entitlements.Plan); plan != "" && strings.TrimSpace(account.GetCredential(fishCredentialPlan)) != plan {
		credentials := shallowCopyMap(account.Credentials)
		credentials[fishCredentialPlan] = plan
		if err := persistAccountCredentials(ctx, s.accountRepo, account, credentials); err != nil {
			slog.Warn("fish: persist plan_type failed", "account_id", account.ID, "error", err)
		}
	}
}

func buildFishDegradedUsage(err error) *UsageInfo {
	now := time.Now()
	info := &UsageInfo{
		UpdatedAt: &now,
		Error:     "failed to fetch Fish usage",
		ErrorCode: errorCodeNetworkError,
	}
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		info.Error = err.Error()
	}
	var authErr *fish.AuthError
	if errors.As(err, &authErr) {
		info.NeedsReauth = true
		info.ErrorCode = errorCodeUnauthenticated
	}
	return info
}
