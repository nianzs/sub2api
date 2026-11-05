package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// Fish 钱包余额观测（账号 extra）。余额只作调度参考，不追求精确：两个实例
// 同时扣同一账号可能丢更新，下一次 402 或巡检会把它纠正回来。
const (
	// FishWalletBalanceExtraKey 是最近一次确认（或按出图扣减后）的钱包余额。
	FishWalletBalanceExtraKey = "fish_wallet_balance"
	// FishWalletBalanceAtExtraKey 是上一次查钱包的时间；出图扣减不刷新它，
	// 所以余额在信任时长后自然过期、交给巡检重查。
	FishWalletBalanceAtExtraKey = "fish_wallet_balance_at"
	// FishWalletBelowExtraKey 记下某一单 402 时的价格：余额确知低于它。
	// 查到精确余额后清除。
	FishWalletBelowExtraKey   = "fish_wallet_below"
	FishWalletBelowAtExtraKey = "fish_wallet_below_at"
)

// fishCreditsExhaustedReason 是「积分耗尽」冷却的原因文案（旧版 402 一律
// 写它；按余额调度开启后只在付不起最便宜一单时写）。
const fishCreditsExhaustedReason = "fish credits exhausted or quota exceeded"

// fishWalletWriteTimeout 是单次余额落库的预算（与冷却写入同级）。
const fishWalletWriteTimeout = 5 * time.Second

// FishWalletBalance 返回 fish 原生号最近观测的钱包余额与观测时间。
func (a *Account) FishWalletBalance() (balance int, observedAt time.Time, ok bool) {
	if a == nil || a.Extra == nil {
		return 0, time.Time{}, false
	}
	raw, exists := a.Extra[FishWalletBalanceExtraKey]
	if !exists || raw == nil {
		return 0, time.Time{}, false
	}
	return int(parseExtraFloat64(raw)), a.getExtraTime(FishWalletBalanceAtExtraKey), true
}

// FishWalletBelow 返回近期 402 记下的价格：余额确知低于它。
func (a *Account) FishWalletBelow() (credits int, observedAt time.Time, ok bool) {
	if a == nil || a.Extra == nil {
		return 0, time.Time{}, false
	}
	raw, exists := a.Extra[FishWalletBelowExtraKey]
	if !exists || raw == nil {
		return 0, time.Time{}, false
	}
	return int(parseExtraFloat64(raw)), a.getExtraTime(FishWalletBelowAtExtraKey), true
}

// fishWalletObservedAt 返回上一次查到钱包的时间；从没观测过的号返回零值
// （排在巡检最前）。
func fishWalletObservedAt(account *Account) time.Time {
	if _, observedAt, ok := account.FishWalletBalance(); ok {
		return observedAt
	}
	return time.Time{}
}

// FishCreditPolicy 是按钱包余额调度的开关与时长（gateway.fish.*）。零值 =
// 关闭：402 回到旧版冷却，选号与拆批都不看余额。
type FishCreditPolicy struct {
	Enabled                  bool
	BalanceTrust             time.Duration
	WalletRefreshMinInterval time.Duration
}

// FishCreditPolicy 返回网关配置里的按余额调度设置。
func (s *GatewayService) FishCreditPolicy() FishCreditPolicy {
	if s == nil || s.cfg == nil {
		return FishCreditPolicy{}
	}
	fishCfg := s.cfg.Gateway.Fish
	policy := FishCreditPolicy{
		Enabled:                  fishCfg.CreditAwareSchedulingEnabled,
		BalanceTrust:             6 * time.Hour,
		WalletRefreshMinInterval: 10 * time.Minute,
	}
	if fishCfg.BalanceTrustHours > 0 {
		policy.BalanceTrust = time.Duration(fishCfg.BalanceTrustHours) * time.Hour
	}
	if fishCfg.WalletRefreshMinIntervalSeconds > 0 {
		policy.WalletRefreshMinInterval = time.Duration(fishCfg.WalletRefreshMinIntervalSeconds) * time.Second
	}
	return policy
}

// trustedBalance 返回信任时长内观测到的余额；关闭、中转号、未观测或过期
// 一律视为未知。
func (p FishCreditPolicy) trustedBalance(account *Account, now time.Time) (int, bool) {
	if !p.Enabled || !isFishTokenAccount(account) {
		return 0, false
	}
	balance, observedAt, ok := account.FishWalletBalance()
	if !ok || observedAt.IsZero() || now.Sub(observedAt) > p.BalanceTrust {
		return 0, false
	}
	return balance, true
}

// cannotAfford 报告账号是否确知付不起 perItem：近期 402 记下的「余额 < X」且
// X <= perItem，或可信余额 < perItem。402 提示优先：精确观测会清除它，所以
// 它一定比余额更新，而余额可能是丢过更新的估算值。
func (p FishCreditPolicy) cannotAfford(account *Account, perItem int, now time.Time) bool {
	if !p.Enabled || !isFishTokenAccount(account) {
		return false
	}
	if below, observedAt, ok := account.FishWalletBelow(); ok && !observedAt.IsZero() &&
		now.Sub(observedAt) <= p.BalanceTrust && below <= perItem {
		return true
	}
	balance, ok := p.trustedBalance(account, now)
	return ok && balance < perItem
}

// fishAffordableBatch 返回余额 balance 在单价 perItem 下最多能出的张数（不超过
// want）。单价未知（<=0）时不限制。
func fishAffordableBatch(want, balance, perItem int) int {
	if perItem <= 0 {
		return want
	}
	return min(want, balance/perItem)
}

// fishCreditsShortError 表示账号付不起下一批：要么上游返回 402
// OUT_OF_CREDITS（cause 非空，余额未知），要么按可信余额本地判定（cause 为
// 空，不提交）。它只针对这一单：换号重试，不冷却。
type fishCreditsShortError struct {
	// need 是付不起的价格：上游 402 时为该批总价，本地判定时为单价。0 = 未知
	//（402 发生在计价环节）。
	need int
	// balance 是本地判定依据的余额；上游 402 时为 -1。
	balance int
	cause   error
}

func (e *fishCreditsShortError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return fmt.Sprintf("fish: wallet balance %d cannot cover the next image (%d credits)", e.balance, e.need)
}

func (e *fishCreditsShortError) Unwrap() error { return e.cause }

// fishCreditAwareError 在开关开启时把 402 OUT_OF_CREDITS 包成只针对本单的
// fishCreditsShortError；其他错误原样返回。
func fishCreditAwareError(policy FishCreditPolicy, err error, need int) error {
	if !policy.Enabled || !fish.IsOutOfCredits(err) {
		return err
	}
	return &fishCreditsShortError{need: need, balance: -1, cause: err}
}

// fishWalletStore 是余额观测需要的账号读写面（AccountRepository 实现）。
type fishWalletStore interface {
	fishAccountCooldownStore
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
	ClearTempUnschedulable(ctx context.Context, id int64) error
}

func writeFishWalletExtra(ctx context.Context, store fishWalletStore, accountID int64, updates map[string]any) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fishWalletWriteTimeout)
	defer cancel()
	if err := store.UpdateExtra(writeCtx, accountID, updates); err != nil {
		logger.L().With(zap.String("component", "service.fish")).Warn(
			"fish.wallet_write_failed",
			zap.Int64("account_id", accountID),
			zap.Error(err),
		)
	}
}

// applyFishWalletObservation 落库一次查到的精确余额（含 0），并清除 402
// 留下的「余额 < X」提示。
func applyFishWalletObservation(ctx context.Context, store fishWalletStore, accountID int64, balance int, now time.Time) {
	if store == nil || accountID <= 0 {
		return
	}
	writeFishWalletExtra(ctx, store, accountID, map[string]any{
		FishWalletBalanceExtraKey:   max(balance, 0),
		FishWalletBalanceAtExtraKey: now.UTC().Format(time.RFC3339),
		FishWalletBelowExtraKey:     nil,
		FishWalletBelowAtExtraKey:   nil,
	})
}

// recoverFishCreditCooldown 解除被「积分耗尽」冷却、但余额仍够最便宜一单的
// 账号（旧版把 402 一律当耗尽，冷却到 UTC 零点）。只碰本原因写入的冷却，
// 每日出图次数还有剩余、且本进程已观测到真实单价时才解除——刚启动时单价
// 未知，按「余额 > 0」放出来的号多半付不起任何一单；单价表只在按余额调度
// 开启时学习，所以关闭开关后这里不会解除任何冷却（否则 402 冷却与巡检解除
// 会循环）。account 是巡检开始时读到的快照，清除前重读一次，期间被别的原因
// （403、429 等）改写的冷却不动。
func recoverFishCreditCooldown(ctx context.Context, store fishWalletStore, account *Account, balance int, dailyAvailable bool, now time.Time) {
	if store == nil || account == nil || !dailyAvailable {
		return
	}
	if cheapest, ok := fishPrices.minPerItem(now); !ok || balance < cheapest {
		return
	}
	if !fishCreditCooldownActive(account, now) {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fishWalletWriteTimeout)
	defer cancel()
	current, err := store.GetByID(writeCtx, account.ID)
	if err != nil || !fishCreditCooldownActive(current, now) {
		return
	}
	if err := store.ClearTempUnschedulable(writeCtx, account.ID); err != nil {
		logger.L().With(zap.String("component", "service.fish")).Warn(
			"fish.credit_cooldown_clear_failed",
			zap.Int64("account_id", account.ID),
			zap.Error(err),
		)
		return
	}
	logger.L().With(zap.String("component", "service.fish")).Info(
		"fish.credit_cooldown_cleared",
		zap.Int64("account_id", account.ID),
		zap.Int("balance", balance),
	)
}

// fishCreditCooldownActive 报告账号是否正处于「积分耗尽」冷却中。
func fishCreditCooldownActive(account *Account, now time.Time) bool {
	return account != nil && account.TempUnschedulableUntil != nil &&
		now.Before(*account.TempUnschedulableUntil) &&
		account.TempUnschedulableReason == fishCreditsExhaustedReason
}

// RecordFishSpend 把一次尝试里已受理批次的积分从已知余额里扣掉（余额未知
// 时不写，等巡检或 402 补查）。观测时间不变，余额照常按信任时长过期。
//
// account 是选号时的快照，出图要几十秒，期间同账号的其他请求可能已经扣过、
// 巡检也可能写了新余额，所以扣减前重读库里的当前值：观测时间变了说明有新的
// 精确余额（可能已含这笔花费），以它为准、不再扣。重读到写入之间仍可能与
// 别的实例互相覆盖，窗口只有毫秒级，误差由下一次 402 或巡检纠正。
func (s *GatewayService) RecordFishSpend(ctx context.Context, account *Account, spent int) {
	if s == nil || s.accountRepo == nil || spent <= 0 || !isFishTokenAccount(account) {
		return
	}
	_, selectedAt, ok := account.FishWalletBalance()
	if !ok {
		return
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fishWalletWriteTimeout)
	current, err := s.accountRepo.GetByID(readCtx, account.ID)
	cancel()
	if err != nil || current == nil {
		return
	}
	balance, observedAt, ok := current.FishWalletBalance()
	if !ok || !observedAt.Equal(selectedAt) {
		return
	}
	writeFishWalletExtra(ctx, s.accountRepo, account.ID, map[string]any{
		FishWalletBalanceExtraKey: max(balance-spent, 0),
	})
}

// TouchFishAccountSelected 选中 fish 账号后立刻把调度缓存里的 last_used 往
// 前推：选号按「最久没用」排序，而失败从不更新 LastUsedAt，不推的话并发请求
// 会挤到同一个账号上。
func (s *GatewayService) TouchFishAccountSelected(ctx context.Context, accountID int64) {
	if s == nil || s.schedulerSnapshot == nil || accountID <= 0 {
		return
	}
	touchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fishWalletWriteTimeout)
	defer cancel()
	if err := s.schedulerSnapshot.TouchLastUsed(touchCtx, accountID, time.Now()); err != nil {
		logger.L().With(zap.String("component", "service.fish")).Debug(
			"fish.touch_last_used_failed",
			zap.Int64("account_id", accountID),
			zap.Error(err),
		)
	}
}

// fishWalletRefreshSF 合并同一账号并发 402 触发的钱包补查。
var fishWalletRefreshSF singleflight.Group

// applyFishCreditShortfall 处理「余额不够这一单」：当场只跳过、不冷却。上游
// 402 时先记下「余额 < 这单价格」（本地判定依据的余额已在库里，不必再记）。
// 两种情况都在后台补查钱包，确认连最便宜一单都付不起才冷却——本地判定用的
// 是估算余额，可能已经过时（例如付费号月度重置补了积分），不能直接据此冷却。
func applyFishCreditShortfall(ctx context.Context, store fishWalletStore, accountID int64, short *fishCreditsShortError, policy FishCreditPolicy) {
	if store == nil || accountID <= 0 || short == nil {
		return
	}
	upstreamRejected := short.cause != nil
	if upstreamRejected && short.need > 0 && !fishBelowHintAtMost(ctx, store, accountID, short.need, policy) {
		writeFishWalletExtra(ctx, store, accountID, map[string]any{
			FishWalletBelowExtraKey:   short.need,
			FishWalletBelowAtExtraKey: time.Now().UTC().Format(time.RFC3339),
		})
	}
	bgCtx := context.WithoutCancel(ctx)
	fishRunAsync(func() {
		_, _, _ = fishWalletRefreshSF.Do(strconv.FormatInt(accountID, 10), func() (any, error) {
			refreshFishWalletAfterShortfall(bgCtx, store, accountID, short.need, upstreamRejected, policy)
			return nil, nil
		})
	})
}

// fishBelowHintAtMost 报告库里是否已有不超过 need 的近期「余额 < X」提示。
// 已有更小的上界时保留它（「< 400」比「< 800」信息更多），不被大批次的 402
// 覆盖。读库失败按没有处理。
func fishBelowHintAtMost(ctx context.Context, store fishWalletStore, accountID int64, need int, policy FishCreditPolicy) bool {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fishWalletWriteTimeout)
	defer cancel()
	account, err := store.GetByID(readCtx, accountID)
	if err != nil || account == nil {
		return false
	}
	below, observedAt, ok := account.FishWalletBelow()
	return ok && !observedAt.IsZero() && time.Since(observedAt) <= policy.BalanceTrust && below <= need
}

// refreshFishWalletAfterShortfall 补查钱包并决定是否冷却。最近查过（刷新
// 间隔内）就不再打上游，按已知余额判断；但上游 402 而已知余额却够这一单时，
// 已知余额不可信，照样重查。查询失败时只有上游 402 才兜底：402 是上游对该
// 价格的确证，「余额 < X」已低于最便宜一单就冷却；本地估算没有这份确证，查
// 不到就不冷却（选号仍按估算余额过滤，过期后由真实请求或巡检纠正）。
//
// 冷却有两种：
//   - 余额连最便宜一单都付不起：积分耗尽；
//   - 上游 402、查到的余额却够这一单：拒绝原因不是余额（402 也可能是每日出图
//     次数用完），按旧版 402 规则冷却（free 号到 UTC 零点，其余 2 小时并补查
//     每日窗口），免得账号回到候选池后反复 402。
func refreshFishWalletAfterShortfall(ctx context.Context, store fishWalletStore, accountID int64, need int, upstreamRejected bool, policy FishCreditPolicy) {
	lookupCtx, cancel := context.WithTimeout(ctx, fishUsageFetchTimeout)
	defer cancel()
	account, err := store.GetByID(lookupCtx, accountID)
	if err != nil || account == nil || !isFishTokenAccount(account) {
		return
	}
	now := time.Now()
	exhaustedBelow := fishExhaustedBelow(now)
	balance, observedAt, known := account.FishWalletBalance()
	contradicted := upstreamRejected && need > 0 && known && balance >= need
	if !known || observedAt.IsZero() || now.Sub(observedAt) >= policy.WalletRefreshMinInterval || contradicted {
		wallet, err := queryFishWallet(lookupCtx, account)
		if err != nil {
			logger.L().With(zap.String("component", "service.fish")).Warn(
				"fish.wallet_refresh_failed",
				zap.Int64("account_id", accountID),
				zap.Error(err),
			)
			if upstreamRejected && need > 0 && need <= exhaustedBelow {
				applyFishCooldown(ctx, store, accountID, fishInsufficientCreditsFailure(0))
			}
			return
		}
		balance = wallet.Balance
		applyFishWalletObservation(ctx, store, accountID, balance, now)
	}
	switch {
	case balance < exhaustedBelow:
		applyFishCooldown(ctx, store, accountID, fishInsufficientCreditsFailure(0))
	case upstreamRejected && need > 0 && balance >= need:
		logger.L().With(zap.String("component", "service.fish")).Info(
			"fish.out_of_credits_with_balance",
			zap.Int64("account_id", accountID),
			zap.Int("balance", balance),
			zap.Int("need", need),
		)
		applyFishCooldown(ctx, store, accountID, fishInsufficientCreditsFailure(0))
	}
}

// queryFishWallet 查询一个 fish token 号的钱包（team_id 缺失时经 UserSelf
// 解析，与用量拉取同一套凭据与代理路由）。
func queryFishWallet(ctx context.Context, account *Account) (*fish.Wallet, error) {
	token := strings.TrimSpace(account.GetCredential(fishCredentialToken))
	if token == "" {
		return nil, fmt.Errorf("fish account has no api_key")
	}
	deviceID := strings.TrimSpace(account.GetCredential(fishCredentialDeviceID))
	if deviceID == "" {
		deviceID = fish.NewDeviceID()
	}
	identity := fish.Identity{DeviceID: deviceID}
	client := fish.NewClient(fish.Options{ProxyURL: accountProxyURL(account), APIBase: fishUsageAPIBaseForTest})
	return client.QueryWallet(ctx, token, identity, account.GetCredential(fishCredentialTeamID))
}
