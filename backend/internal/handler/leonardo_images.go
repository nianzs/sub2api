package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// LeonardoImages 处理 Leonardo 分组下的 /v1/images/generations 与 /v1/images/edits。
//
// 分组里可能同时有 Leonardo Cookie 原生号和 OpenAI 形中转号。选到 oauth 时把请求
// 翻译成 Leonardo GraphQL Generate；选到 apikey+base_url 时把同一份 OpenAI 请求
// 转发到 {base_url}（官方 OpenAI 或三方兼容端点），作为原生号全部故障时的兜底。
// Leonardo 账号不满足 account.IsOpenAICompatible()，走不了 OpenAI 调度器，故这里用
// 平台无关的 GatewayService.SelectAccountForModelWithExclusions 自建 failover。
func (h *GatewayHandler) LeonardoImages(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		leonardoImagesError(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		leonardoImagesError(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	if h.leonardoImageService == nil {
		leonardoImagesError(c, http.StatusNotFound, "not_found_error", "Leonardo image generation is not available")
		return
	}

	reqLog := requestLogger(
		c,
		"handler.gateway.leonardo_images",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if failure := pkghttputil.DescribeRequestBodyReadFailure(c.Request, err); failure.Kind == pkghttputil.RequestBodyReadErrorMaxBytes {
			leonardoImagesError(c, http.StatusRequestEntityTooLarge, "invalid_request_error",
				buildBodyTooLargeMessage(failure.MaxBytesLimit))
			return
		}
		leonardoImagesError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	parsed, err := h.openAIGatewayService.ParseOpenAIImagesRequest(c, body)
	if err != nil {
		leonardoImagesError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		leonardoImagesError(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
		return
	}

	// composite 分组在 middleware 已 resolve 出上游模型时，选号与渠道映射按它走。
	routingModel := parsed.Model
	if resolvedModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context()); ok {
		routingModel = resolvedModel
	}

	reqLog = reqLog.With(
		zap.String("model", parsed.Model),
		zap.String("routing_model", routingModel),
		zap.String("size", parsed.Size),
		zap.Bool("stream", parsed.Stream),
	)
	setOpsRequestContext(c, parsed.Model, false)
	setOpsEndpointContext(c, "", int16(service.RequestTypeSync))

	if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject,
		service.ContentModerationProtocolOpenAIImages, parsed.Model, parsed.ModerationBody()); decision != nil &&
		!decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}

	releaseAdmission, admitted := h.admitLeonardoImagesRequest(c, reqLog, apiKey, subject)
	if !admitted {
		return
	}
	defer releaseAdmission()

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, routingModel)
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	call := service.NewLeonardoImageCall(parsed, channelMapping.MappedModel)
	call.UsageTokensPerCredit = h.leonardoUsageTokensPerCredit()
	h.runLeonardoImagesFailover(c, reqLog, apiKey, subject, subscription, &leonardoImagesRequest{
		parsed:         parsed,
		body:           body,
		routingModel:   routingModel,
		channelMapping: channelMapping,
		call:           call,
	})
}

// leonardoUsageTokensPerCredit 是响应 usage 的估算比例
// （gateway.leonardo_usage_tokens_per_credit，默认 34；0 不返回 usage）。
func (h *GatewayHandler) leonardoUsageTokensPerCredit() float64 {
	if h.cfg == nil || h.cfg.Gateway.LeonardoUsageTokensPerCredit <= 0 {
		return 0
	}
	return h.cfg.Gateway.LeonardoUsageTokensPerCredit
}

// leonardoImagesRequest 汇总一次 Leonardo 出图请求在换号尝试之间共享的输入。
type leonardoImagesRequest struct {
	parsed *service.OpenAIImagesRequest
	body   []byte
	// routingModel 用于选号；channelMapping.MappedModel 用于转发与账号模型映射。
	routingModel   string
	channelMapping service.ChannelMappingResult
	// call 在换号之间复用已抓取的参考图。
	call *service.LeonardoImageCall
}

// admitLeonardoImagesRequest 在选号前完成与 OpenAI/Grok 出图一致的准入：
// 全局出图并发槽 → 用户并发槽 → 计费资格。
func (h *GatewayHandler) admitLeonardoImagesRequest(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
) (release func(), admitted bool) {
	var releases []func()
	release = func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}

	imageRelease, acquired := h.acquireLeonardoImageSlot(c)
	if !acquired {
		return nil, false
	}
	if imageRelease != nil {
		releases = append(releases, imageRelease)
	}

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	streamStarted := false
	userRelease, err := h.concurrencyHelper.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, false, &streamStarted)
	if err != nil {
		reqLog.Warn("leonardo_images.user_slot_acquire_failed", zap.Error(err))
		release()
		status, errType, _, message := concurrencyErrorResponse(err, "user")
		leonardoImagesError(c, status, errType, message)
		return nil, false
	}
	// 不用 wrapReleaseOnDone：提交之后上游与客户端连接脱钩，客户端断开时生成任务
	// 仍在跑。断开即释放会让用户靠「提交后立刻断开」绕过并发上限；槽位持有到
	// handler 返回，由 Redis 槽位 TTL 兜底异常路径。
	if userRelease = leonardoReleaseOnce(userRelease); userRelease != nil {
		releases = append(releases, userRelease)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("leonardo_images.billing_eligibility_check_failed", zap.Error(err))
		release()
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		leonardoImagesError(c, status, code, message)
		return nil, false
	}
	return release, true
}

// acquireLeonardoImageSlot 占用与 OpenAI/Grok 出图共享的全局出图并发槽。
func (h *GatewayHandler) acquireLeonardoImageSlot(c *gin.Context) (func(), bool) {
	release, acquired := acquireImageConcurrencySlot(c.Request.Context(), h.cfg, h.imageLimiter)
	if acquired {
		return release, true
	}
	leonardoImagesError(c, http.StatusTooManyRequests, "rate_limit_error", "Image generation concurrency limit exceeded, please retry later")
	return nil, false
}

// leonardoImageStreamKeepaliveInterval 是假流式的 SSE 保活间隔
// （gateway.image_stream_keepalive_interval，默认 10 秒；0 关闭）。
func (h *GatewayHandler) leonardoImageStreamKeepaliveInterval() time.Duration {
	if h.cfg == nil || h.cfg.Gateway.ImageStreamKeepaliveInterval <= 0 {
		return 0
	}
	return time.Duration(h.cfg.Gateway.ImageStreamKeepaliveInterval) * time.Second
}

// leonardoImageNonstreamKeepaliveInterval 是非流式 JSON 空白保活间隔
// （gateway.image_nonstream_keepalive_interval，默认 0 关闭）。
func (h *GatewayHandler) leonardoImageNonstreamKeepaliveInterval() time.Duration {
	if h.cfg == nil || h.cfg.Gateway.ImageNonstreamKeepaliveInterval <= 0 {
		return 0
	}
	return time.Duration(h.cfg.Gateway.ImageNonstreamKeepaliveInterval) * time.Second
}

// leonardoProviderModerationRetryDelay 是上游审核拦截后重提前的等待（与 leo_proxy 一致）。
var leonardoProviderModerationRetryDelay = 2 * time.Second

// leonardoShouldRetryProviderModeration 报告这次内容拒绝是否值得再提一次：
// 只有上游模型方审核拦截（结果不稳定）且本请求尚未重试过。Leonardo 自身的
// NSFW 拦截与参考图审核是确定性的，直接跳过其余原生号。
func leonardoShouldRetryProviderModeration(failover *service.UpstreamFailoverError, retried bool) bool {
	return !retried && service.IsLeonardoProviderModeration(failover)
}

// leonardoWait 等待 d；ctx 先结束（客户端离开）时返回 false。
func leonardoWait(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// leonardoImagesMaxSelectionRounds 是单个请求内选号轮数的硬上限。
const leonardoImagesMaxSelectionRounds = 128

// runLeonardoImagesFailover 逐个账号尝试出图，直到成功或没有可换的账号。
//
// 请求携带原生路径不支持的特性（mask / n>8）时只选中转号；stream=true 在原生号上走假流式；
// 组内没有可用中转时返回明确的 400，不降级到原生（降级会产生错误结果）。
func (h *GatewayHandler) runLeonardoImagesFailover(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	req *leonardoImagesRequest,
) {
	parsed := req.parsed
	requestCtx := c.Request.Context()
	failedAccountIDs := make(map[int64]struct{})
	var lastFailover *service.UpstreamFailoverError
	skipLeonardoNative := false
	// providerModerationRetried 记录本请求是否已为上游审核拦截重试过（至多一次）。
	providerModerationRetried := false
	requireRelay := service.LeonardoRequiresRelay(parsed)
	// 选号按 routingModel，转发与账号映射按渠道映射后的模型；两者不一致时逐个
	// 账号再校验一次，防止渠道映射把请求改写成平台未实现的模型。
	forwardModel := req.routingModel
	if mapped := strings.TrimSpace(req.channelMapping.MappedModel); mapped != "" {
		forwardModel = mapped
	}
	deferredNatives := make(map[int64]struct{})
	// busyAccountIDs 记录本请求内并发槽已满的账号：只从选号中排除，不算失败。
	busyAccountIDs := make(map[int64]struct{})

	// 保活：stream=true 走假流式（SSE 注释保活，出图后写 completed 事件）；
	// 非流式在 JSON 前写空白保活（与 OpenAI 出图一致，配置为 0 时关闭）。
	var fakeStream *leonardoFakeStream
	if parsed.Stream {
		fakeStream = startLeonardoFakeStream(c, h.leonardoImageStreamKeepaliveInterval())
		defer fakeStream.stop()
	} else {
		stopJSONKeepalive := service.StartOpenAIImagesJSONKeepalive(c, h.leonardoImageNonstreamKeepaliveInterval())
		defer stopJSONKeepalive()
	}

	// attempts 只统计真正打到账号上的尝试。busy / 推迟 / 内容拒绝后跳过的账号
	// 只加入排除集合，不消耗换号预算；round 上限只是防御性死循环保护。
	attempts := 0
	for round := 0; attempts <= h.maxAccountSwitches && round < leonardoImagesMaxSelectionRounds; round++ {
		if failoverClientGone(c) {
			return
		}

		unavailable := mergeLeonardoAccountIDSets(failedAccountIDs, busyAccountIDs)
		excluded := service.LeonardoRelaySelectionExclusions(unavailable, requireRelay, deferredNatives)
		if requireRelay {
			h.gatewayService.ExcludeLeonardoNativeAccounts(requestCtx, apiKey.GroupID, excluded)
		}

		account, err := h.gatewayService.SelectAccountForModelWithExclusions(
			requestCtx, apiKey.GroupID, "", req.routingModel, excluded)
		if err != nil || account == nil {
			if requireRelay && len(failedAccountIDs) == 0 {
				// 组内没有可用的中转号：明确拒绝，不降级到原生（mask 会被忽略、
				// n>8 与 webp 原生不支持，降级只会产生错误结果）。
				reqLog.Warn("leonardo_images.relay_required_unavailable",
					zap.String("model", parsed.Model),
					zap.Bool("mask", parsed.HasMask),
					zap.Int("n", parsed.N),
					zap.String("output_format", parsed.OutputFormat),
					zap.Error(err),
				)
				leonardoImagesError(c, http.StatusBadRequest, "invalid_request_error",
					"This request uses parameters that Leonardo native accounts do not support (mask, n>8 or output_format=webp); configure an OpenAI-compatible relay account (base_url + api_key) in this group to serve it")
				return
			}
			if lastFailover == nil && len(busyAccountIDs) > 0 {
				leonardoImagesAccountsBusyError(c, reqLog, busyAccountIDs)
				return
			}
			h.finishLeonardoImagesWithoutAccount(c, reqLog, apiKey, parsed, failedAccountIDs, lastFailover, err)
			return
		}
		setOpsSelectedAccount(c, account.ID, account.Platform)

		if forwardModel != req.routingModel && !account.IsModelSupported(forwardModel) {
			reqLog.Warn("leonardo_images.channel_mapped_model_unsupported",
				zap.Int64("account_id", account.ID),
				zap.String("forward_model", forwardModel))
			failedAccountIDs[account.ID] = struct{}{}
			continue
		}

		if requireRelay && !service.IsLeonardoRelayAccount(account) {
			// listing 漏网的原生号：推迟处理（requireRelay 下不会真的执行它们）。
			deferredNatives[account.ID] = struct{}{}
			continue
		}

		if skipLeonardoNative && !service.IsLeonardoRelayAccount(account) {
			// 内容安全拒绝后绝不再把同一 prompt 打到其它原生号。
			failedAccountIDs[account.ID] = struct{}{}
			continue
		}

		if fakeStream != nil && fakeStream.committed() && service.IsLeonardoRelayAccount(account) {
			// 假流式已提交 SSE 响应头：中转转发会自写响应头与响应体，无法接管
			// 已打开的流，只能继续在原生号之间换。
			failedAccountIDs[account.ID] = struct{}{}
			continue
		}

		releaseAccount, acquired, err := h.concurrencyHelper.TryAcquireAccountSlot(requestCtx, account.ID, account.Concurrency)
		if err != nil {
			reqLog.Warn("leonardo_images.account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			status, errType, _, message := concurrencyErrorResponse(err, "account")
			leonardoImagesError(c, status, errType, message)
			return
		}
		if !acquired {
			busyAccountIDs[account.ID] = struct{}{}
			continue
		}
		// 每轮尝试结束就释放账号槽，不能 defer 到函数返回，否则失败的账号会一直被占着。
		releaseAccount = leonardoReleaseOnce(releaseAccount)
		if releaseAccount == nil {
			releaseAccount = func() {}
		}

		attempts++
		if service.IsLeonardoRelayAccount(account) {
			// 中转号对 stream=true 是真流式、自写响应头：转发期间暂停假流式保活。
			if fakeStream != nil {
				fakeStream.pause()
			}
			_, done := h.tryLeonardoImagesRelay(
				c, reqLog, apiKey, subject, subscription, account, req, failedAccountIDs, &lastFailover, attempts)
			releaseAccount()
			if done {
				return
			}
			if fakeStream != nil {
				fakeStream.resume()
			}
			continue
		}

		token, _, err := h.gatewayService.GetAccessToken(requestCtx, account)
		if err != nil {
			releaseAccount()
			// 账号缺 token（刷新器还没跑到，或 cookie 已失效）：换下一个。
			reqLog.Warn("leonardo_images.token_unavailable",
				zap.Int64("account_id", account.ID), zap.Error(err))
			failedAccountIDs[account.ID] = struct{}{}
			continue
		}

		// 提交之后的阶段在 service 内脱离客户端连接：拿到产物就一定记账。
		result, err := h.leonardoImageService.GenerateCall(requestCtx, account, token, req.call)
		releaseAccount()
		if err == nil {
			h.finishLeonardoImagesSuccess(c, reqLog, apiKey, subject, subscription, account, result, req)
			return
		}

		if errors.Is(err, context.Canceled) || failoverClientGone(c) {
			reqLog.Info("leonardo_images.aborted_client_disconnected", zap.Int64("account_id", account.ID))
			return
		}

		failover := h.gatewayService.LeonardoFailover(requestCtx, account.ID, token, err)
		if failover == nil {
			// 不属于 Leonardo 上游语义（编解码错误等）：没有换号的依据，直接上抛。
			reqLog.Error("leonardo_images.generate_failed",
				zap.Int64("account_id", account.ID), zap.Error(err))
			leonardoImagesError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
			return
		}
		if service.IsLeonardoContentRejected(failover) && !service.IsLeonardoRelayAccount(account) {
			if leonardoShouldRetryProviderModeration(failover, providerModerationRetried) {
				// 上游模型方审核结果不稳定（同一提示词前后两次可能一过一拦），失败
				// 的生成会退积分：不排除该号，稍等后再选一次号重提。
				providerModerationRetried = true
				lastFailover = failover
				reqLog.Warn("leonardo_images.provider_moderation_retry",
					zap.Int64("account_id", account.ID),
					zap.Int("attempts", attempts),
					zap.Error(err),
				)
				if !leonardoWait(requestCtx, leonardoProviderModerationRetryDelay) {
					return
				}
				continue
			}
			skipLeonardoNative = true
			h.gatewayService.ExcludeLeonardoNativeAccounts(requestCtx, apiKey.GroupID, failedAccountIDs)
			reqLog.Warn("leonardo_images.content_rejected_skip_native",
				zap.Int64("account_id", account.ID),
				zap.Int("attempts", attempts),
				zap.Error(err),
			)
			lastFailover = failover
			continue
		}
		if !failover.ShouldRetryNextAccount() {
			// 请求本身的问题（含提交结果不明），换号也救不了。原始上游报错完整
			// 记日志，便于针对新的错误串补分类。
			reqLog.Warn("leonardo_images.request_failed",
				zap.Int64("account_id", account.ID),
				zap.String("reason", string(failover.Reason)),
				zap.Int("upstream_status", failover.StatusCode),
				zap.Int("attempts", attempts),
				zap.Error(err),
			)
			leonardoImagesFailoverError(c, failover)
			return
		}

		reqLog.Warn("leonardo_images.account_failover",
			zap.Int64("account_id", account.ID),
			zap.String("reason", string(failover.Reason)),
			zap.Int("upstream_status", failover.StatusCode),
			zap.Int("attempts", attempts),
			zap.Error(err),
		)
		failedAccountIDs[account.ID] = struct{}{}
		lastFailover = failover
	}

	// 换号预算用尽。
	if lastFailover != nil {
		leonardoImagesFailoverError(c, lastFailover)
		return
	}
	if len(busyAccountIDs) > 0 {
		leonardoImagesAccountsBusyError(c, reqLog, busyAccountIDs)
		return
	}
	leonardoImagesError(c, http.StatusServiceUnavailable, "api_error", "No available Leonardo accounts")
}

// tryLeonardoImagesRelay 把 OpenAI 形状的出图请求转到中转号的 base_url。
// done=true 表示已经给客户端写了成功或终态错误；switched=true 表示应换下一个账号。
func (h *GatewayHandler) tryLeonardoImagesRelay(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	account *service.Account,
	req *leonardoImagesRequest,
	failedAccountIDs map[int64]struct{},
	lastFailover **service.UpstreamFailoverError,
	attempts int,
) (switched bool, done bool) {
	if h.openAIGatewayService == nil {
		reqLog.Error("leonardo_images.relay_unavailable", zap.Int64("account_id", account.ID))
		leonardoImagesError(c, http.StatusBadGateway, "api_error", "Leonardo relay forwarding is not available")
		return false, true
	}

	result, err := h.openAIGatewayService.ForwardImages(
		c.Request.Context(), c, account, req.body, req.parsed, req.channelMapping.MappedModel)
	if err == nil {
		h.finishLeonardoImagesRelaySuccess(c, reqLog, apiKey, subject, subscription, account, result, req)
		return false, true
	}

	// 中转流式中途断开时已经把部分图片交给了客户端：先按实际张数记账，再处理错误。
	partial := result != nil && result.ImageCount > 0
	if partial {
		reqLog.Warn("leonardo_images.relay_partial_error_with_image_result",
			zap.Int64("account_id", account.ID),
			zap.Int("image_count", result.ImageCount),
			zap.Error(err))
		h.recordLeonardoImagesUsage(c, apiKey, subject, subscription, account, result, req)
	}

	if errors.Is(err, context.Canceled) || failoverClientGone(c) {
		reqLog.Info("leonardo_images.relay_aborted_client_disconnected", zap.Int64("account_id", account.ID))
		return false, true
	}

	if partial || service.IsResponseCommitted(c) {
		reqLog.Warn("leonardo_images.relay_failed_after_flush",
			zap.Int64("account_id", account.ID), zap.Error(err))
		return false, true
	}

	var failover *service.UpstreamFailoverError
	if errors.As(err, &failover) && failover.ShouldRetryNextAccount() {
		reqLog.Warn("leonardo_images.relay_failover",
			zap.Int64("account_id", account.ID),
			zap.Int("upstream_status", failover.StatusCode),
			zap.Int("attempts", attempts),
		)
		failedAccountIDs[account.ID] = struct{}{}
		*lastFailover = failover
		return true, false
	}

	reqLog.Error("leonardo_images.relay_failed",
		zap.Int64("account_id", account.ID), zap.Error(err))
	if failover != nil {
		leonardoImagesFailoverError(c, failover)
		return false, true
	}
	var upErr *service.OpenAIImagesUpstreamError
	if errors.As(err, &upErr) && upErr != nil {
		status := upErr.StatusCode
		if status <= 0 {
			status = http.StatusBadGateway
		}
		message := strings.TrimSpace(upErr.Message)
		if message == "" {
			message = "Upstream request failed"
		}
		errType := strings.TrimSpace(upErr.ErrorType)
		if errType == "" {
			errType = "api_error"
		}
		leonardoImagesError(c, status, errType, message)
		return false, true
	}
	leonardoImagesError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
	return false, true
}

// finishLeonardoImagesWithoutAccount 处理「选不出账号」：首轮无候选说明分组本身
// 没有可用账号，后续轮次说明候选都已失败，此时应上报最后一次的上游错误。
func (h *GatewayHandler) finishLeonardoImagesWithoutAccount(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	parsed *service.OpenAIImagesRequest,
	failedAccountIDs map[int64]struct{},
	lastFailover *service.UpstreamFailoverError,
	selectErr error,
) {
	if failoverClientGone(c) {
		return
	}
	reqLog.Warn("leonardo_images.account_select_failed",
		zap.Int("excluded_account_count", len(failedAccountIDs)),
		zap.Error(selectErr))

	if lastFailover != nil {
		leonardoImagesFailoverError(c, lastFailover)
		return
	}
	markOpsRoutingCapacityLimitedIfNoAvailable(c, selectErr)
	cls := classifyNoAccountErrorFromGin(c, h.openAIGatewayService, apiKey, parsed.Model, parsed.Model, service.PlatformLeonardo)
	leonardoImagesError(c, cls.Status, cls.ErrType, cls.Message)
}

// finishLeonardoImagesSuccess 写响应并记账。
func (h *GatewayHandler) finishLeonardoImagesSuccess(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	account *service.Account,
	result *service.LeonardoImageResult,
	req *leonardoImagesRequest,
) {
	if result == nil {
		reqLog.Error("leonardo_images.empty_result", zap.Int64("account_id", account.ID))
		leonardoImagesError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
		return
	}
	if fakeStream := leonardoFakeStreamFromContext(c); fakeStream != nil {
		// 假流式：每张图一条 image_generation.completed / image_edit.completed 事件。
		fakeStream.writeEvents(service.LeonardoImagesStreamEvents(req.parsed, result))
	} else {
		c.Data(http.StatusOK, "application/json", result.Body)
	}
	h.recordLeonardoImagesUsage(c, apiKey, subject, subscription, account, result.Forward, req)
	reqLog.Debug("leonardo_images.request_completed",
		zap.Int64("account_id", account.ID),
		zap.String("upstream_model", result.Forward.UpstreamModel),
		zap.Int("api_credit_cost", result.APICreditCost),
		zap.String("generation_id", result.GenerationID),
	)
}

// finishLeonardoImagesRelaySuccess 记账。响应体已由 ForwardImages 写入 gin，不能再 c.Data。
func (h *GatewayHandler) finishLeonardoImagesRelaySuccess(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	account *service.Account,
	result *service.OpenAIForwardResult,
	req *leonardoImagesRequest,
) {
	h.recordLeonardoImagesUsage(c, apiKey, subject, subscription, account, result, req)
	upstreamModel := ""
	if result != nil {
		upstreamModel = result.UpstreamModel
	}
	reqLog.Debug("leonardo_images.relay_completed",
		zap.Int64("account_id", account.ID),
		zap.String("upstream_model", upstreamModel),
	)
}

func (h *GatewayHandler) recordLeonardoImagesUsage(
	c *gin.Context,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	account *service.Account,
	result *service.OpenAIForwardResult,
	req *leonardoImagesRequest,
) {
	if result == nil {
		return
	}
	parsed := req.parsed
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	sessionID := service.ExtractClientSessionID(c)
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
	quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
	channelUsageFields := clientRequestedUsageFields(c, req.channelMapping, parsed.Model, result.UpstreamModel)
	requestPayloadHash := service.HashUsageRequestPayload(req.body)
	if parsed.Multipart {
		requestPayloadHash = service.HashUsageRequestPayload([]byte(parsed.StickySessionSeed()))
	}

	h.submitMandatoryUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := h.openAIGatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result:             result,
			APIKey:             apiKey,
			User:               apiKey.User,
			Account:            account,
			Subscription:       subscription,
			InboundEndpoint:    inboundEndpoint,
			UpstreamEndpoint:   upstreamEndpoint,
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			QuotaPlatform:      quotaPlatform,
			SessionID:          sessionID,
			ChannelUsageFields: channelUsageFields,
		}); err != nil {
			logger.L().With(
				zap.String("component", "handler.gateway.leonardo_images"),
				zap.Int64("user_id", subject.UserID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Int64("account_id", account.ID),
			).Error("leonardo_images.record_usage_failed", zap.Error(err))
		}
	})
}

// leonardoImagesAccountsBusyError 处理「候选账号并发槽都满了」：可重试的容量
// 问题返回 429 而非 503。
func leonardoImagesAccountsBusyError(c *gin.Context, reqLog *zap.Logger, busyAccountIDs map[int64]struct{}) {
	reqLog.Info("leonardo_images.accounts_busy", zap.Int("busy_account_count", len(busyAccountIDs)))
	markOpsRoutingCapacityLimited(c)
	leonardoImagesError(c, http.StatusTooManyRequests, "rate_limit_error",
		"Too many concurrent requests for available accounts, please retry later")
}

// leonardoReleaseOnce 把槽位释放函数包成幂等的；不随请求 ctx 取消而提前释放。
func leonardoReleaseOnce(release func()) func() {
	if release == nil {
		return nil
	}
	var once sync.Once
	return func() { once.Do(release) }
}

// mergeLeonardoAccountIDSets 返回若干账号集合的并集副本，不改写入参。
func mergeLeonardoAccountIDSets(sets ...map[int64]struct{}) map[int64]struct{} {
	size := 0
	for _, set := range sets {
		size += len(set)
	}
	out := make(map[int64]struct{}, size)
	for _, set := range sets {
		for id := range set {
			out[id] = struct{}{}
		}
	}
	return out
}

// leonardoImagesFailoverError 把 failover 错误渲染给客户端，优先用错误自带的
// 对外状态码与文案。
func leonardoImagesFailoverError(c *gin.Context, failover *service.UpstreamFailoverError) {
	status := failover.ClientStatusCode
	if status <= 0 {
		status = failover.StatusCode
	}
	if status <= 0 {
		status = http.StatusBadGateway
	}
	message := failover.ClientMessage
	if message == "" {
		message = "Upstream request failed"
	}
	// failover.Reason 是内部分类（leonardo_request 等），只进日志；对外按 OpenAI
	// 错误类型渲染。内容安全拒绝与 OpenAI gpt-image 一致：
	// image_generation_user_error + moderation_blocked。
	if service.IsLeonardoContentRejected(failover) {
		leonardoImagesErrorWithCode(c, status, "image_generation_user_error", "moderation_blocked", message)
		return
	}
	leonardoImagesError(c, status, openAIErrorTypeForStatus(status), message)
}

// openAIErrorTypeForStatus 按状态码给出 OpenAI 错误对象的 type。
func openAIErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

// leonardoImagesError 渲染 OpenAI 形状的错误体——images 端点的客户端按 OpenAI 协议解析。
// 假流式已提交 SSE 响应头时改写 SSE error 事件（状态码已是 200）。
func leonardoImagesError(c *gin.Context, status int, errType, message string) {
	leonardoImagesErrorWithCode(c, status, errType, "", message)
}

// leonardoImagesErrorWithCode 同 leonardoImagesError，额外带 error.code。与 OpenAI
// 错误对象一致，param / code 两个键始终存在（无值时为 null）。
func leonardoImagesErrorWithCode(c *gin.Context, status int, errType, code, message string) {
	if fakeStream := leonardoFakeStreamFromContext(c); fakeStream != nil && fakeStream.failIfCommitted(errType, code, message) {
		return
	}
	if service.IsResponseCommitted(c) {
		return
	}
	var codeValue any
	if code = strings.TrimSpace(code); code != "" {
		codeValue = code
	}
	c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": message, "param": nil, "code": codeValue}})
}
