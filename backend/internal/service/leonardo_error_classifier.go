package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	leonardo "github.com/Wei-Shaw/sub2api/internal/pkg/leonardo"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// leonardoRateLimitCooldown 是上游 429 后账号的冷却时长：get-session 与
// GraphQL 共享同一套限流，短冷却即可覆盖。
const leonardoRateLimitCooldown = 5 * time.Minute

// leonardoInsufficientCooldown 是积分不足时的冷却时长：余额靠订阅周期回补，
// 冷却只是避免同一个请求反复撞空账号，不宜过长。
const leonardoInsufficientCooldown = 10 * time.Minute

// Leonardo 失败原因，进入 UpstreamFailoverError.Reason 供 ops 归因。
const (
	leonardoFailureInsufficientTokens = GatewayFailureReason("leonardo_insufficient_tokens")
	leonardoFailureAuth               = GatewayFailureReason("leonardo_auth")
	leonardoFailureSuspended          = GatewayFailureReason("leonardo_suspended")
	leonardoFailureUpstream           = GatewayFailureReason("leonardo_upstream")
	leonardoFailureRequest            = GatewayFailureReason("leonardo_request")
	leonardoFailureContentRejected    = GatewayFailureReason("leonardo_content_rejected")
	// leonardoFailureProviderModeration 是上游模型方审核拦截（结果不稳定，可重试一次）。
	leonardoFailureProviderModeration = GatewayFailureReason("leonardo_provider_moderation")
	leonardoFailureIndeterminate      = GatewayFailureReason("leonardo_indeterminate")
	leonardoFailureResultUnavailable  = GatewayFailureReason("leonardo_result_unavailable")
)

// leonardoFailure 是 pkg/leonardo 的错误翻译成网关语义后的结果。
type leonardoFailure struct {
	// Failover 描述该错误对本次请求的影响（是否换号、给客户端什么状态码）。
	Failover *UpstreamFailoverError
	// Cooldown 大于 0 时，应把该账号临时摘出调度。
	Cooldown time.Duration
	// CooldownReason 写进账号的不可调度原因，便于管理端排查。
	CooldownReason string
	// InvalidateToken 为 true 时，本次使用的 JWT 已被上游明确拒绝，应从账号里
	// 清掉，让下次取 token 时走带锁刷新，而不是按 expires_at 继续复用它。
	InvalidateToken bool
	// SetAccountError 为 true 时，应把账号置为 error 态（封号）。
	SetAccountError bool
	// AccountErrorMessage 用于置 error 的文案。
	AccountErrorMessage string
}

// classifyLeonardoError 把 internal/pkg/leonardo 的类型化错误翻译成网关语义。
func classifyLeonardoError(err error) leonardoFailure {
	if err == nil {
		return leonardoFailure{}
	}

	// 提交结果不明优先于其它一切分类：上游可能已受理并扣积分，绝不能换号重提。
	var indeterminate *leonardoSubmitIndeterminateError
	if errors.As(err, &indeterminate) {
		return leonardoFailure{Failover: leonardoIndeterminateFailure(err)}
	}

	// 已提交但取不回结果：同样绝不换号重提。账号侧的处置（清 token、冷却、
	// 封号置错）仍按内层错误执行，只覆盖对本次请求的 failover 决策。
	var unavailable *leonardoResultUnavailableError
	if errors.As(err, &unavailable) {
		failure := classifyLeonardoError(unavailable.err)
		failure.Failover = leonardoResultUnavailableFailure(unavailable.err)
		return failure
	}

	var insufficientErr *leonardo.InsufficientTokensError
	if errors.As(err, &insufficientErr) {
		return leonardoFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(insufficientErr.StatusCode, http.StatusTooManyRequests),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeAccount,
				Reason:            leonardoFailureInsufficientTokens,
				NextAccountAction: NextAccountRetry,
			},
			Cooldown:       leonardoInsufficientCooldown,
			CooldownReason: "leonardo credits exhausted",
		}
	}

	var suspensionErr *leonardo.SuspensionError
	if errors.As(err, &suspensionErr) {
		return leonardoFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(suspensionErr.StatusCode, http.StatusForbidden),
				Stage:             GatewayFailureStageAccountAuth,
				Scope:             GatewayFailureScopeAccount,
				Reason:            leonardoFailureSuspended,
				NextAccountAction: NextAccountRetry,
			},
			SetAccountError:     true,
			AccountErrorMessage: "leonardo account suspended for violating the user agreement",
		}
	}

	var authErr *leonardo.AuthError
	if errors.As(err, &authErr) {
		// 不冷却账号：冷却只会让恢复变慢。JWT 被拒时清掉 token，下次取 token
		// 走带锁的 cookie 刷新；cookie 真的失效时，刷新路径会把账号置为 error。
		return leonardoFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(authErr.StatusCode, http.StatusUnauthorized),
				Stage:             GatewayFailureStageAccountAuth,
				Scope:             GatewayFailureScopeAccount,
				Reason:            leonardoFailureAuth,
				NextAccountAction: NextAccountRetry,
			},
			InvalidateToken: true,
		}
	}

	var rateLimitedErr *leonardo.RateLimitedError
	if errors.As(err, &rateLimitedErr) {
		return leonardoFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(rateLimitedErr.StatusCode, http.StatusTooManyRequests),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeProvider,
				Reason:            leonardoFailureUpstream,
				NextAccountAction: NextAccountRetry,
			},
			Cooldown:       leonardoRateLimitCooldown,
			CooldownReason: "leonardo rate limited",
		}
	}

	var contentErr *leonardo.ContentRejectedError
	if errors.As(err, &contentErr) {
		reason := leonardoFailureContentRejected
		if contentErr.ProviderModeration {
			reason = leonardoFailureProviderModeration
		}
		return leonardoFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(contentErr.StatusCode, http.StatusUnavailableForLegalReasons),
				ClientStatusCode:  http.StatusBadRequest,
				ClientMessage:     contentErr.User(),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeRequest,
				Reason:            reason,
				NextAccountAction: NextAccountRetry,
			},
		}
	}

	var temporaryErr *leonardo.UpstreamTemporaryError
	if errors.As(err, &temporaryErr) {
		return leonardoFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(temporaryErr.StatusCode, http.StatusBadGateway),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeProvider,
				Reason:            leonardoFailureUpstream,
				NextAccountAction: NextAccountRetry,
			},
		}
	}

	// 终态：请求本身有问题（参数校验失败、模型不支持、尺寸非法等），换号也救不了。
	var requestErr *leonardo.RequestError
	if errors.As(err, &requestErr) {
		return leonardoFailure{
			Failover: &UpstreamFailoverError{
				StatusCode:        statusOrDefault(requestErr.StatusCode, http.StatusBadRequest),
				ClientStatusCode:  http.StatusBadRequest,
				ClientMessage:     requestErr.User(),
				Stage:             GatewayFailureStageInference,
				Scope:             GatewayFailureScopeRequest,
				Reason:            leonardoFailureRequest,
				NextAccountAction: NextAccountStop,
			},
		}
	}

	// 非本包错误（ctx 取消、编解码失败等）：不换号，交由调用方原样上抛。
	return leonardoFailure{}
}

// applyLeonardoCooldown 按分类结果把账号临时摘出调度。失败只记日志——
// 冷却是优化项，摘不掉最多是下次请求再撞一次，不该让本次请求失败。
func applyLeonardoCooldown(ctx context.Context, repo AccountRepository, accountID int64, failure leonardoFailure) {
	if repo == nil || accountID <= 0 || failure.Cooldown <= 0 {
		return
	}
	until := time.Now().Add(failure.Cooldown)
	if err := repo.SetTempUnschedulable(ctx, accountID, until, failure.CooldownReason); err != nil {
		logger.L().With(zap.String("component", "service.leonardo")).Warn(
			"leonardo.cooldown_failed",
			zap.Int64("account_id", accountID),
			zap.String("reason", failure.CooldownReason),
			zap.Error(err),
		)
	}
}

// applyLeonardoTokenInvalidation 清掉被上游明确拒绝的 JWT。失败只记日志。
func applyLeonardoTokenInvalidation(ctx context.Context, repo AccountRepository, accountID int64, token string, failure leonardoFailure) {
	if !failure.InvalidateToken || accountID <= 0 || token == "" {
		return
	}
	invalidator, ok := repo.(LeonardoTokenInvalidationRepository)
	if !ok {
		return
	}
	if _, err := invalidator.InvalidateLeonardoAccessTokenIfUnchanged(ctx, accountID, token); err != nil {
		logger.L().With(zap.String("component", "service.leonardo")).Warn(
			"leonardo.token_invalidation_failed",
			zap.Int64("account_id", accountID),
			zap.Error(err),
		)
	}
}

// applyLeonardoAccountError 把封号结论落到账号 error（无条件写：封禁串是上游
// 的明确终态结论，不随凭据轮转变化）。失败只记日志。
func applyLeonardoAccountError(ctx context.Context, repo AccountRepository, accountID int64, failure leonardoFailure) {
	if !failure.SetAccountError || repo == nil || accountID <= 0 {
		return
	}
	if err := repo.SetError(ctx, accountID, failure.AccountErrorMessage); err != nil {
		logger.L().With(zap.String("component", "service.leonardo")).Warn(
			"leonardo.account_error_failed",
			zap.Int64("account_id", accountID),
			zap.Error(err),
		)
	}
}

// LeonardoFailover 把 internal/pkg/leonardo 的错误翻译成网关的 failover 决策，
// 并按需冷却账号、清 token、置 error。
//
// 这是 handler 层的唯一入口：分类结果（leonardoFailure）刻意不导出。
// 返回 nil 表示该错误不属于 Leonardo 上游语义（如 ctx 取消），调用方应原样上抛。
// token 是本次请求发给上游的 JWT，用于在上游明确拒绝时按值清掉它。
func (s *GatewayService) LeonardoFailover(ctx context.Context, accountID int64, token string, err error) *UpstreamFailoverError {
	failure := classifyLeonardoError(err)
	if failure.Failover == nil {
		return nil
	}
	if s != nil {
		applyLeonardoCooldown(ctx, s.accountRepo, accountID, failure)
		applyLeonardoTokenInvalidation(ctx, s.accountRepo, accountID, token, failure)
		applyLeonardoAccountError(ctx, s.accountRepo, accountID, failure)
	}
	return failure.Failover
}

// IsLeonardoProviderModeration 报告这次 failover 是否为上游模型方审核拦截
// （PROVIDER_MODERATION_ERROR）。这类结果不稳定，handler 会先重试一次。
func IsLeonardoProviderModeration(err *UpstreamFailoverError) bool {
	return err != nil && err.Reason == leonardoFailureProviderModeration
}

// IsLeonardoContentRejected 报告这次 failover 是否来自 Leonardo 内容安全拒绝。
// Handler 用它决定「跳过其余 Cookie 号，只试组内 OpenAI 形中转号」。
func IsLeonardoContentRejected(err *UpstreamFailoverError) bool {
	return err != nil && (err.Reason == leonardoFailureContentRejected || err.Reason == leonardoFailureProviderModeration)
}

// IsLeonardoIndeterminate 报告错误是否为「提交结果不明」：网络错误发生在
// Generate 提交阶段时，无法判断上游是否已受理并扣除积分（上游无幂等键），
// 自动重提可能双扣。调用方应终止请求并返回 504，不换号、不重试。
func IsLeonardoIndeterminate(err *UpstreamFailoverError) bool {
	return err != nil && err.Reason == leonardoFailureIndeterminate
}

// leonardoIndeterminateFailure 构造提交结果不明的 failover 决策。
func leonardoIndeterminateFailure(err error) *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:        http.StatusGatewayTimeout,
		ClientStatusCode:  http.StatusGatewayTimeout,
		ClientMessage:     "leonardo generation submit result is unknown; not retrying to avoid double billing",
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeRequest,
		Reason:            leonardoFailureIndeterminate,
		NextAccountAction: NextAccountStop,
	}
}

// leonardoResultUnavailableFailure 构造「已提交但取不回结果」的 failover 决策：
// 超出脱钩时限报 504，其余报 502；均不换号。
func leonardoResultUnavailableFailure(cause error) *UpstreamFailoverError {
	status := http.StatusBadGateway
	message := "leonardo generation was submitted but its result could not be retrieved; not retrying to avoid double billing"
	if errors.Is(cause, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		message = "leonardo generation did not finish in time; not retrying to avoid double billing"
	}
	return &UpstreamFailoverError{
		StatusCode:        status,
		ClientStatusCode:  status,
		ClientMessage:     message,
		Stage:             GatewayFailureStageInference,
		Scope:             GatewayFailureScopeRequest,
		Reason:            leonardoFailureResultUnavailable,
		NextAccountAction: NextAccountStop,
	}
}

// statusOrDefault 取错误携带的上游状态码，缺失时用 fallback。
