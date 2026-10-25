package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// fishRateLimitCooldown 是上游 429 后账号的冷却时长。
const fishRateLimitCooldown = 5 * time.Minute

// fishRateLimitCooldownReason 是 429 限流冷却的原因文案（请求路径与连通性
// 测试路径共用）。
const fishRateLimitCooldownReason = "fish rate limited"

// fishInsufficientCooldown 是 402 后立即落库的冷却。402 本身不带 resetAt；
// 后台补查确认 dailyImage 已耗尽时，冷却延长到重置时刻（resetAt 缺失或过期
// 时回落到下一个 UTC 零点）。free 号不补查：直接冷却到下一个 UTC 零点——
// 出图次数按 UTC 日重置，积分是长期总额、不随次数重置（分不够时次数重置了
// 也用不了），无论 402 因次数还是积分，零点前都不可能成功，零点一次探测
// 即可发现积分是否已补，2h 探测纯属浪费选号。钱包耗尽、查询失败保持 2h。
const fishInsufficientCooldown = 2 * time.Hour

// fishPlanFree 是 entitlements.plan 的免费档标识；连通性测试与用量拉取会把
// plan 回填到 credentials.plan_type（fishCredentialPlan），402 路径据此分流。
const fishPlanFree = "free"

// fishDailyQuotaExhaustedReason 是每日出图窗口耗尽、挂到 resetAt 时的冷却原因。
const fishDailyQuotaExhaustedReason = "fish daily image quota exhausted"

// fishDailyResetLookupTimeout 是 402 后台补查 entitlements 的预算。
// 补查不在换号路径上，只限制后台 goroutine 的寿命。测试可临时改短。
var fishDailyResetLookupTimeout = 5 * time.Second

// fish 失败原因，进入 UpstreamFailoverError.Reason 供 ops 归因。
const (
	fishFailureInsufficientCredits = GatewayFailureReason("fish_insufficient_credits")
	fishFailureAuth                = GatewayFailureReason("fish_auth")
	fishFailureForbidden           = GatewayFailureReason("fish_forbidden")
	fishFailureUpstream            = GatewayFailureReason("fish_upstream")
	fishFailureRequest             = GatewayFailureReason("fish_request")
	fishFailureGenerationFailed    = GatewayFailureReason("fish_generation_failed")
	fishFailureIndeterminate       = GatewayFailureReason("fish_indeterminate")
	fishFailureResultUnavailable   = GatewayFailureReason("fish_result_unavailable")
)

// fishFailure 是 pkg/fish 的错误翻译成网关语义后的结果（镜像 leonardoFailure）。
type fishFailure struct {
	Failover            *UpstreamFailoverError
	Cooldown            time.Duration
	CooldownReason      string
	// RateLimited 标记 429 限流（Reason 与 5xx 同为 fishFailureUpstream，
	// 靠本字段区分账号侧处置）。
	RateLimited         bool
	SetAccountError     bool
	AccountErrorMessage string
}

// classifyFishError 把 fish 链路的错误翻译成网关语义。
func classifyFishError(err error) fishFailure {
	if err == nil {
		return fishFailure{}
	}

	// 提交结果不明优先于一切：上游可能已受理并扣积分，绝不能换号重提。
	var indeterminate *fishSubmitIndeterminateError
	if errors.As(err, &indeterminate) {
		return fishFailure{Failover: fishIndeterminateFailure()}
	}

	// 已提交但取不回结果：同样绝不换号重提。账号侧处置按内层错误执行。
	var unavailable *fishResultUnavailableError
	if errors.As(err, &unavailable) {
		failure := classifyFishError(unavailable.err)
		failure.Failover = fishResultUnavailableFailure(unavailable.err)
		return failure
	}

	// 上游显式失败（积分已退）：可换号重试。
	var generationFailed *fishGenerationFailedError
	if errors.As(err, &generationFailed) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        http.StatusBadGateway,
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeProvider,
				Reason:            fishFailureGenerationFailed,
				NextAccountAction: NextAccountRetry,
			},
		}
	}

	var insufficientErr *fish.InsufficientCreditsError
	if errors.As(err, &insufficientErr) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(insufficientErr.StatusCode, http.StatusTooManyRequests),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeAccount,
				Reason:            fishFailureInsufficientCredits,
				NextAccountAction: NextAccountRetry,
			},
			Cooldown:       fishInsufficientCooldown,
			CooldownReason: "fish credits exhausted or quota exceeded",
		}
	}

	var authErr *fish.AuthError
	if errors.As(err, &authErr) {
		// fish token 即长期凭据（无 refresh）：401 = 账号失效，直接置 error
		// 让管理员换号，比冷却更诚实。
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(authErr.StatusCode, http.StatusUnauthorized),
				Stage:             GatewayFailureStageAccountAuth,
				Scope:             GatewayFailureScopeAccount,
				Reason:            fishFailureAuth,
				NextAccountAction: NextAccountRetry,
			},
			SetAccountError:     true,
			AccountErrorMessage: "fish token rejected by upstream (invalid token)",
		}
	}

	// 业务 API 403（实测 GENERATION_UNAVAILABLE，2026-10-24：edit 与 t2i 先后
	// 全池 403，账号级风控/封禁；token 鉴权本身仍通过）。换号重试；账号置
	// error，文案逐字写上游原生错误串。
	var forbiddenErr *fish.ForbiddenError
	if errors.As(err, &forbiddenErr) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(forbiddenErr.StatusCode, http.StatusForbidden),
				Stage:             GatewayFailureStageAccountAuth,
				Scope:             GatewayFailureScopeAccount,
				Reason:            fishFailureForbidden,
				NextAccountAction: NextAccountRetry,
			},
			SetAccountError:     true,
			AccountErrorMessage: forbiddenErr.Error(),
		}
	}

	var rateLimitedErr *fish.RateLimitedError
	if errors.As(err, &rateLimitedErr) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(rateLimitedErr.StatusCode, http.StatusTooManyRequests),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeProvider,
				Reason:            fishFailureUpstream,
				NextAccountAction: NextAccountRetry,
			},
			Cooldown:       fishRateLimitCooldown,
			CooldownReason: fishRateLimitCooldownReason,
			RateLimited:    true,
		}
	}

	var temporaryErr *fish.UpstreamTemporaryError
	if errors.As(err, &temporaryErr) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(temporaryErr.StatusCode, http.StatusBadGateway),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeProvider,
				Reason:            fishFailureUpstream,
				NextAccountAction: NextAccountRetry,
			},
		}
	}

	// 终态：请求参数问题（组合非法、模型不支持、尺寸越界等），换号无解。
	var requestErr *fish.RequestError
	if errors.As(err, &requestErr) {
		return fishFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(requestErr.StatusCode, http.StatusBadRequest),
				ClientStatusCode:  http.StatusBadRequest,
				ClientMessage:     requestErr.Error(),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeRequest,
				Reason:            fishFailureRequest,
				NextAccountAction: NextAccountStop,
			},
		}
	}

	// 非本包错误（ctx 取消、参数校验等）：不换号，调用方原样上抛。
	return fishFailure{}
}

// fishIndeterminateFailure 构造「提交结果不明」的 failover 决策：504、不换号。
func fishIndeterminateFailure() *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:        http.StatusGatewayTimeout,
		ClientStatusCode:  http.StatusGatewayTimeout,
		ClientMessage:     "fish generation submit result is unknown; not retrying to avoid double billing",
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeRequest,
		Reason:            fishFailureIndeterminate,
		NextAccountAction: NextAccountStop,
	}
}

// fishResultUnavailableFailure 构造「已提交但取不回结果」的 failover 决策：
// 超出脱钩时限报 504，其余报 502；均不换号。
func fishResultUnavailableFailure(cause error) *UpstreamFailoverError {
	status := http.StatusBadGateway
	message := "fish generation was submitted but its result could not be retrieved; not retrying to avoid double billing"
	if errors.Is(cause, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		message = "fish generation did not finish in time; not retrying to avoid double billing"
	}
	return &UpstreamFailoverError{
		StatusCode:        status,
		ClientStatusCode:  status,
		ClientMessage:     message,
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeRequest,
		Reason:            fishFailureResultUnavailable,
		NextAccountAction: NextAccountStop,
	}
}

// FishFailover 把 fish 链路的错误翻译成网关的 failover 决策，并按需冷却
// 账号、置 error。handler 层的唯一入口；返回 nil 表示错误不属于 fish 上游
// 语义，调用方应原样上抛。
func (s *GatewayService) FishFailover(ctx context.Context, accountID int64, _ string, err error) *UpstreamFailoverError {
	failure := classifyFishError(err)
	if failure.Failover == nil {
		return nil
	}
	if s != nil {
		applyFishCooldown(ctx, s.accountRepo, accountID, failure)
		applyFishAccountError(ctx, s.accountRepo, accountID, failure)
	}
	return failure.Failover
}

// fishAccountCooldownStore 是冷却决策需要的账号读写面。
// AccountRepository 的方法集覆盖它，测试只需实现这两个方法。
type fishAccountCooldownStore interface {
	GetByID(ctx context.Context, id int64) (*Account, error)
	SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error
}

// fishCooldownWriteTimeout 是单次冷却落库的预算，与 entitlements 补查各自独立。
const fishCooldownWriteTimeout = 5 * time.Second

// fishRunAsync 执行 402 后的后台补查。测试替换为同步执行。
var fishRunAsync = func(fn func()) { go fn() }

// fishDailyResetSF 合并同一账号并发 402 触发的 entitlements 补查。
var fishDailyResetSF singleflight.Group

// applyFishCooldown 先按固定时长落库冷却（不等补查，换号不被拖住，查询失败
// 也一定有冷却）。两类例外直接落 UTC 零点、不补查：
//   - 402 free 号：次数按 UTC 日重置、积分是长期总额，两个维度零点前都无
//     恢复可能，补查 dailyImage 区分不了；
//   - 429 free 号：实测被 fish 持续 429 的 free 号当天基本废了，5 分钟回锅
//     会虚增可用账号数、挡住自动补号。plus/pro 保持 5 分钟（更可能是瞬时限流）。
//
// 其余 402（paid/plan 未知）再在后台补查每日出图窗口：确认耗尽时把冷却延长
// 到重置时刻——SetTempUnschedulable 只延长、不缩短。
func applyFishCooldown(ctx context.Context, repo fishAccountCooldownStore, accountID int64, failure fishFailure) {
	if repo == nil || accountID <= 0 || failure.Cooldown <= 0 {
		return
	}
	if failure.RateLimited {
		if fishFreePlanCooldownUntilMidnight(ctx, repo, accountID, failure.CooldownReason) {
			return
		}
		writeFishCooldown(ctx, repo, accountID, time.Now().Add(failure.Cooldown), failure.CooldownReason)
		return
	}
	if failure.Failover == nil || failure.Failover.Reason != fishFailureInsufficientCredits {
		writeFishCooldown(ctx, repo, accountID, time.Now().Add(failure.Cooldown), failure.CooldownReason)
		return
	}
	if fishFreePlanCooldownUntilMidnight(ctx, repo, accountID, failure.CooldownReason) {
		return
	}
	writeFishCooldown(ctx, repo, accountID, time.Now().Add(failure.Cooldown), failure.CooldownReason)
	bgCtx := context.WithoutCancel(ctx)
	fishRunAsync(func() {
		_, _, _ = fishDailyResetSF.Do(strconv.FormatInt(accountID, 10), func() (any, error) {
			lookupCtx, cancel := context.WithTimeout(bgCtx, fishDailyResetLookupTimeout)
			resetAt, ok := lookupFishDailyReset(lookupCtx, repo, accountID, time.Now())
			cancel()
			if ok {
				writeFishCooldown(bgCtx, repo, accountID, resetAt, fishDailyQuotaExhaustedReason)
			}
			return nil, nil
		})
	})
}

// fishFreePlanCooldownUntilMidnight 对 free 号的 402/429 直接把冷却挂到下一个
// UTC 零点，不打 entitlements。free 的积分是长期总额、次数按 UTC 日重置：
// 402 无论因次数耗尽还是积分不够，零点前都不可能成功（分不够时次数重置了
// 也用不了）；429 实测当天基本废了。零点后一次探测即可发现是否恢复。
// 返回 true 表示已落库。plan 未知走老路径（402 补查 / 429 五分钟）：
// 用量拉取/巡检/连通性测试会回填 plan_type，下一轮即进入本分支。
func fishFreePlanCooldownUntilMidnight(ctx context.Context, repo fishAccountCooldownStore, accountID int64, reason string) bool {
	account, err := repo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		if err != nil {
			logger.L().With(zap.String("component", "service.fish")).Warn(
				"fish.free_plan_check_failed",
				zap.Int64("account_id", accountID),
				zap.Error(err),
			)
		}
		return false
	}
	if !isFishTokenAccount(account) || !fishAccountPlanIsFree(account) {
		return false
	}
	writeFishCooldown(ctx, repo, accountID, nextUTCMidnight(time.Now()), reason)
	return true
}

// fishAccountPlanIsFree 按 credentials.plan_type 判断 free 档。
func fishAccountPlanIsFree(account *Account) bool {
	return strings.EqualFold(strings.TrimSpace(account.GetCredential(fishCredentialPlan)), fishPlanFree)
}

// nextUTCMidnight 返回 now 之后的下一个 UTC 零点（fish 日配额重置时刻）。
func nextUTCMidnight(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	if !midnight.After(now) {
		midnight = midnight.AddDate(0, 0, 1)
	}
	return midnight
}

// writeFishCooldown 用独立超时落库冷却；调用方取消（客户端断开）不影响写入。
func writeFishCooldown(ctx context.Context, repo fishAccountCooldownStore, accountID int64, until time.Time, reason string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fishCooldownWriteTimeout)
	defer cancel()
	if err := repo.SetTempUnschedulable(writeCtx, accountID, until, reason); err != nil {
		logger.L().With(zap.String("component", "service.fish")).Warn(
			"fish.cooldown_failed",
			zap.Int64("account_id", accountID),
			zap.String("reason", reason),
			zap.Error(err),
		)
	}
}

// lookupFishDailyReset 补查 entitlements。只有每日出图窗口耗尽且 resetAt
// 在未来时返回该时刻。中转号、无 token、查库失败、查询失败都不打到冷却决策上。
func lookupFishDailyReset(ctx context.Context, repo fishAccountCooldownStore, accountID int64, now time.Time) (time.Time, bool) {
	account, err := repo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		if err != nil {
			logger.L().With(zap.String("component", "service.fish")).Warn(
				"fish.daily_reset_lookup_failed",
				zap.Int64("account_id", accountID),
				zap.Error(err),
			)
		}
		return time.Time{}, false
	}
	resetAt, ok, err := fishDailyImageResetAt(ctx, account, now)
	if err != nil {
		logger.L().With(zap.String("component", "service.fish")).Warn(
			"fish.daily_reset_lookup_failed",
			zap.Int64("account_id", accountID),
			zap.Error(err),
		)
		return time.Time{}, false
	}
	return resetAt, ok
}

// fishDailyImageResetAt 只查每日出图窗口，不查钱包。device_id 为空时临时生成，不落库。
func fishDailyImageResetAt(ctx context.Context, account *Account, now time.Time) (time.Time, bool, error) {
	if !isFishTokenAccount(account) {
		return time.Time{}, false, nil
	}
	token := strings.TrimSpace(account.GetCredential(fishCredentialToken))
	if token == "" {
		return time.Time{}, false, nil
	}
	deviceID := strings.TrimSpace(account.GetCredential(fishCredentialDeviceID))
	if deviceID == "" {
		deviceID = fish.NewDeviceID()
	}
	client := fish.NewClient(fish.Options{ProxyURL: accountProxyURL(account), APIBase: fishUsageAPIBaseForTest})
	entitlements, err := client.QueryEntitlements(ctx, token, fish.Identity{DeviceID: deviceID})
	if err != nil {
		return time.Time{}, false, err
	}
	if entitlements == nil {
		return time.Time{}, false, nil
	}
	resetAt, ok := fishDailyResetFromQuota(entitlements.DailyImage, now)
	return resetAt, ok, nil
}

// fishDailyResetFromQuota 在每日出图窗口耗尽时返回冷却截止：resetAt 有效且
// 在未来时用 resetAt；缺失或已过期时回落到下一个 UTC 零点（日窗口按 UTC 天
// 重置，耗尽是事实，重置时刻兜底到零点比 2h 反复探测更省）。
func fishDailyResetFromQuota(daily *fish.DailyImageQuota, now time.Time) (time.Time, bool) {
	if !fishDailyImageExhausted(daily) {
		return time.Time{}, false
	}
	if resetAt := strings.TrimSpace(daily.ResetAt); resetAt != "" {
		if parsed, err := time.Parse(time.RFC3339, resetAt); err == nil && parsed.After(now) {
			return parsed, true
		}
	}
	return nextUTCMidnight(now), true
}

// fishDailyImageExhausted 要求 used 已达 limit：remaining 缺失会被解成 0，
// 单看 remaining 会把还有余量的账号（如钱包耗尽引起的 402）误判为日窗口耗尽。
func fishDailyImageExhausted(daily *fish.DailyImageQuota) bool {
	return daily != nil && daily.Limit > 0 && daily.Remaining <= 0 && daily.Used >= daily.Limit
}

func applyFishAccountError(ctx context.Context, repo AccountRepository, accountID int64, failure fishFailure) {
	if !failure.SetAccountError || repo == nil || accountID <= 0 {
		return
	}
	if err := repo.SetError(ctx, accountID, failure.AccountErrorMessage); err != nil {
		logger.L().With(zap.String("component", "service.fish")).Warn(
			"fish.account_error_failed",
			zap.Int64("account_id", accountID),
			zap.Error(err),
		)
	}
}
