package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// FishImages 处理 fish.audio 分组下的 /v1/images/generations 与 /v1/images/edits。
//
// 结构照 LeonardoImages，但去掉中转分支——fish 上游不是 OpenAI 线协议，
// 全部账号都是 token 原生号。选号走平台无关的
// SelectAccountForModelWithExclusions 自建 failover。
func (h *GatewayHandler) FishImages(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		fishImagesError(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		fishImagesError(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	if h.fishImageService == nil {
		fishImagesError(c, http.StatusNotFound, "not_found_error", "Fish image generation is not available")
		return
	}

	reqLog := requestLogger(
		c,
		"handler.gateway.fish_images",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if failure := pkghttputil.DescribeRequestBodyReadFailure(c.Request, err); failure.Kind == pkghttputil.RequestBodyReadErrorMaxBytes {
			fishImagesError(c, http.StatusRequestEntityTooLarge, "invalid_request_error",
				buildBodyTooLargeMessage(failure.MaxBytesLimit))
			return
		}
		fishImagesError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	parsed, err := h.openAIGatewayService.ParseOpenAIImagesRequest(c, body)
	if err != nil {
		fishImagesError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		fishImagesError(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
		return
	}

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

	releaseAdmission, admitted := h.admitFishImagesRequest(c, reqLog, apiKey, subject)
	if !admitted {
		return
	}
	defer releaseAdmission()

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, routingModel)
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	// 动态尺寸规划阈值每请求读一次，随 call 下传保证换号续跑规划一致。
	fitLimits := h.fishImageFitLimits(c.Request.Context())
	call := service.NewFishImageCall(parsed, channelMapping.MappedModel, fitLimits)
	h.runFishImagesFailover(c, reqLog, apiKey, subject, subscription, &fishImagesRequest{
		parsed:         parsed,
		body:           body,
		routingModel:   routingModel,
		channelMapping: channelMapping,
		call:           call,
	})
}

// fishImagesRequest 汇总一次 fish 出图请求在换号尝试之间共享的输入。
type fishImagesRequest struct {
	parsed *service.OpenAIImagesRequest
	body   []byte
	// routingModel 用于选号；channelMapping.MappedModel 用于上游请求。
	routingModel   string
	channelMapping service.ChannelMappingResult
	// call 在换号之间复用已抓取的参考图，并携带本次的尺寸规划阈值。
	call *service.FishImageCall
}

// fishImageFitLimits 读取 fish 原生号动态尺寸规划阈值（系统设置-网关）。
// settingService 未注入（部分测试）时取默认值。
func (h *GatewayHandler) fishImageFitLimits(ctx context.Context) domain.FishFitLimits {
	if h.settingService == nil {
		return domain.DefaultFishFitLimits()
	}
	return h.settingService.GetFishImageFitLimits(ctx)
}

// admitFishImagesRequest 与 Leonardo 出图一致的准入：全局出图并发槽 →
// 用户并发槽 → 计费资格。
func (h *GatewayHandler) admitFishImagesRequest(
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

	imageRelease, acquired := h.acquireFishImageSlot(c)
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
		reqLog.Warn("fish_images.user_slot_acquire_failed", zap.Error(err))
		release()
		status, errType, _, message := concurrencyErrorResponse(err, "user")
		fishImagesError(c, status, errType, message)
		return nil, false
	}
	// 槽位持有到 handler 返回（提交后上游与客户端脱钩），由 Redis TTL 兜底。
	if userRelease = leonardoReleaseOnce(userRelease); userRelease != nil {
		releases = append(releases, userRelease)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("fish_images.billing_eligibility_check_failed", zap.Error(err))
		release()
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", http.StatusText(retryAfter))
		}
		fishImagesError(c, status, code, message)
		return nil, false
	}
	return release, true
}

func (h *GatewayHandler) acquireFishImageSlot(c *gin.Context) (func(), bool) {
	release, acquired := acquireImageConcurrencySlot(c.Request.Context(), h.cfg, h.imageLimiter)
	if acquired {
		return release, true
	}
	fishImagesError(c, http.StatusTooManyRequests, "rate_limit_error", "Image generation concurrency limit exceeded, please retry later")
	return nil, false
}

// fishImagesMaxSelectionRounds 是单个请求内选号轮数的硬上限。
const fishImagesMaxSelectionRounds = 64

// runFishImagesFailover 逐个账号尝试出图，直到成功或没有可换的账号。
// stream=true 走假流式（与 Leonardo 同一套 leonardoFakeStream）。
func (h *GatewayHandler) runFishImagesFailover(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	req *fishImagesRequest,
) {
	parsed := req.parsed
	requestCtx := c.Request.Context()
	failedAccountIDs := make(map[int64]struct{})
	var lastFailover *service.UpstreamFailoverError
	busyAccountIDs := make(map[int64]struct{})
	forwardModel := req.routingModel
	if mapped := strings.TrimSpace(req.channelMapping.MappedModel); mapped != "" {
		forwardModel = mapped
	}

	// 保活：stream=true 假流式；非流式 JSON 空白保活（配置 0 关闭）。
	var fakeStream *leonardoFakeStream
	if parsed.Stream {
		fakeStream = startLeonardoFakeStream(c, h.leonardoImageStreamKeepaliveInterval())
		defer fakeStream.stop()
	} else {
		stopJSONKeepalive := service.StartOpenAIImagesJSONKeepalive(c, h.leonardoImageNonstreamKeepaliveInterval())
		defer stopJSONKeepalive()
	}

	requireRelay := service.FishRequiresRelay(parsed, req.call.FitLimits())
	deferredNatives := make(map[int64]struct{})
	req.call.SetCreditPolicy(h.gatewayService.FishCreditPolicy())

	attempts := 0
	for round := 0; attempts <= h.maxAccountSwitches && round < fishImagesMaxSelectionRounds; round++ {
		if failoverClientGone(c) {
			return
		}

		excluded := service.FishRelaySelectionExclusions(
			mergeLeonardoAccountIDSets(failedAccountIDs, busyAccountIDs), requireRelay, deferredNatives)
		if requireRelay {
			h.gatewayService.ExcludeFishNativeAccounts(requestCtx, apiKey.GroupID, excluded)
		}
		// 单价已知时选号排除付不起的账号、优先用零头（每轮重算：本请求的首次
		// 计价也会让后续轮次拿到单价）。
		account, err := h.gatewayService.SelectAccountForModelWithExclusions(
			service.WithFishCreditRequirement(requestCtx, req.call), apiKey.GroupID, "", req.routingModel, excluded)
		if err != nil || account == nil {
			if requireRelay && len(failedAccountIDs) == 0 {
				reqLog.Warn("fish_images.relay_required_unavailable",
					zap.String("model", parsed.Model),
					zap.String("size", parsed.Size),
					zap.Bool("mask", parsed.HasMask),
					zap.Int("n", parsed.N),
					zap.String("output_format", parsed.OutputFormat),
					zap.Error(err),
				)
				fishImagesError(c, http.StatusBadRequest, "invalid_request_error",
					"This request uses a size or parameter that fish native accounts do not support; configure an OpenAI-compatible relay account (base_url + api_key) in this group to serve it")
				return
			}
			if lastFailover == nil && len(busyAccountIDs) > 0 {
				fishImagesAccountsBusyError(c, reqLog, busyAccountIDs)
				return
			}
			h.finishFishImagesWithoutAccount(c, reqLog, apiKey, subject, subscription, req, failedAccountIDs, lastFailover, err)
			return
		}
		setOpsSelectedAccount(c, account.ID, account.Platform)

		if forwardModel != req.routingModel && !account.IsModelSupported(forwardModel) {
			reqLog.Warn("fish_images.channel_mapped_model_unsupported",
				zap.Int64("account_id", account.ID),
				zap.String("forward_model", forwardModel))
			failedAccountIDs[account.ID] = struct{}{}
			continue
		}

		if requireRelay && !service.IsFishRelayAccount(account) {
			// listing 漏网的原生号：推迟处理（requireRelay 下不会真的执行它们）。
			deferredNatives[account.ID] = struct{}{}
			continue
		}

		releaseAccount, acquired, err := h.concurrencyHelper.TryAcquireAccountSlot(requestCtx, account.ID, account.Concurrency)
		if err != nil {
			reqLog.Warn("fish_images.account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			status, errType, _, message := concurrencyErrorResponse(err, "account")
			fishImagesError(c, status, errType, message)
			return
		}
		if !acquired {
			busyAccountIDs[account.ID] = struct{}{}
			continue
		}
		releaseAccount = leonardoReleaseOnce(releaseAccount)
		if releaseAccount == nil {
			releaseAccount = func() {}
		}

		attempts++
		h.gatewayService.TouchFishAccountSelected(requestCtx, account.ID)
		if service.IsFishRelayAccount(account) {
			// 中转号对 stream=true 是真流式、自写响应头：转发期间暂停假流式保活。
			if fakeStream != nil {
				fakeStream.pause()
			}
			_, done := h.tryFishImagesRelay(
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

		token := account.GetCredential("api_key")
		if strings.TrimSpace(token) == "" {
			releaseAccount()
			reqLog.Warn("fish_images.api_key_unavailable", zap.Int64("account_id", account.ID))
			failedAccountIDs[account.ID] = struct{}{}
			continue
		}

		// 提交之后的阶段在 service 内脱离客户端连接：拿到产物就一定记账。
		result, err := h.fishImageService.GenerateCall(requestCtx, account, token, req.call)
		releaseAccount()
		h.gatewayService.RecordFishSpend(requestCtx, account, req.call.TakeAttemptSpend())
		if err == nil {
			h.finishFishImagesSuccess(c, reqLog, apiKey, subject, subscription, account, result, req)
			return
		}

		if errors.Is(err, context.Canceled) || failoverClientGone(c) {
			reqLog.Info("fish_images.aborted_client_disconnected", zap.Int64("account_id", account.ID))
			return
		}

		failover := h.gatewayService.FishFailover(requestCtx, account.ID, token, err)
		if failover == nil {
			reqLog.Error("fish_images.generate_failed",
				zap.Int64("account_id", account.ID), zap.Error(err))
			fishImagesError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
			return
		}
		if !failover.ShouldRetryNextAccount() {
			// 请求本身的问题（含提交结果不明），换号救不了——但已交付的图
			// 不丢弃：有进度时按部分成功收尾（张数=实际交付，按张计费）。
			if req.call.Delivered() > 0 {
				reqLog.Warn("fish_images.partial_success_after_terminal_error",
					zap.Int64("account_id", account.ID),
					zap.Int("delivered", req.call.Delivered()),
					zap.String("reason", string(failover.Reason)),
					zap.Error(err),
				)
				h.finishFishImagesPartialSuccess(c, reqLog, apiKey, subject, subscription, account, req)
				return
			}
			reqLog.Warn("fish_images.request_failed",
				zap.Int64("account_id", account.ID),
				zap.String("reason", string(failover.Reason)),
				zap.Int("upstream_status", failover.StatusCode),
				zap.Int("attempts", attempts),
				zap.Error(err),
			)
			fishImagesFailoverError(c, failover)
			return
		}

		// 可换号且有进度：不重置 call，下一账号只生成缺口（remaining），
		// 已交付图绝不再生成（防双扣）。
		if req.call.Delivered() > 0 {
			reqLog.Warn("fish_images.account_failover_with_progress",
				zap.Int64("account_id", account.ID),
				zap.Int("delivered", req.call.Delivered()),
				zap.Int("remaining", req.call.Remaining()),
				zap.String("reason", string(failover.Reason)),
				zap.Error(err),
			)
		}
		reqLog.Warn("fish_images.account_failover",
			zap.Int64("account_id", account.ID),
			zap.String("reason", string(failover.Reason)),
			zap.Int("upstream_status", failover.StatusCode),
			zap.Int("attempts", attempts),
			zap.Error(err),
		)
		failedAccountIDs[account.ID] = struct{}{}
		lastFailover = failover
	}

	// 换号预算用尽 / 无号可换：有交付按部分成功收尾。
	if req.call.Delivered() > 0 {
		reqLog.Warn("fish_images.partial_success_exhausted_failover",
			zap.Int("delivered", req.call.Delivered()),
			zap.Int("requested_n", req.parsed.N),
		)
		h.finishFishImagesPartialSuccess(c, reqLog, apiKey, subject, subscription, nil, req)
		return
	}
	if lastFailover != nil {
		fishImagesFailoverError(c, lastFailover)
		return
	}
	if len(busyAccountIDs) > 0 {
		fishImagesAccountsBusyError(c, reqLog, busyAccountIDs)
		return
	}
	fishImagesError(c, http.StatusServiceUnavailable, "api_error", "No available fish accounts")
}

// finishFishImagesPartialSuccess 用已交付图构建部分成功响应（张数=实际
// 交付、按张计费）。account 可为 nil（无号可换路径——记账跳过账号维度）。
func (h *GatewayHandler) finishFishImagesPartialSuccess(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	account *service.Account,
	req *fishImagesRequest,
) {
	result := h.fishImageService.BuildFishPartialResult(c.Request.Context(), req.call)
	if result == nil {
		reqLog.Error("fish_images.partial_build_failed")
		fishImagesError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
		return
	}
	if fakeStream := leonardoFakeStreamFromContext(c); fakeStream != nil {
		fakeStream.writeEvents(service.FishImagesStreamEvents(req.parsed, result))
	} else {
		c.Data(http.StatusOK, "application/json", result.Body)
	}
	if account != nil {
		h.recordFishImagesUsage(c, apiKey, subject, subscription, account, result.Forward, req)
	}
	reqLog.Info("fish_images.partial_success_completed",
		zap.Int("delivered", result.Forward.ImageCount),
	)
}

// finishFishImagesWithoutAccount 处理「选不出账号」。
func (h *GatewayHandler) finishFishImagesWithoutAccount(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	req *fishImagesRequest,
	failedAccountIDs map[int64]struct{},
	lastFailover *service.UpstreamFailoverError,
	selectErr error,
) {
	parsed := req.parsed
	if failoverClientGone(c) {
		return
	}
	reqLog.Warn("fish_images.account_select_failed",
		zap.Int("excluded_account_count", len(failedAccountIDs)),
		zap.Error(selectErr))

	if req.call.Delivered() > 0 {
		reqLog.Warn("fish_images.partial_success_no_account",
			zap.Int("delivered", req.call.Delivered()),
		)
		h.finishFishImagesPartialSuccess(c, reqLog, apiKey, subject, subscription, nil, req)
		return
	}
	if lastFailover != nil {
		fishImagesFailoverError(c, lastFailover)
		return
	}
	markOpsRoutingCapacityLimitedIfNoAvailable(c, selectErr)
	cls := classifyNoAccountErrorFromGin(c, h.openAIGatewayService, apiKey, parsed.Model, parsed.Model, service.PlatformFish)
	fishImagesError(c, cls.Status, cls.ErrType, cls.Message)
}

// finishFishImagesSuccess 写响应并记账。
func (h *GatewayHandler) finishFishImagesSuccess(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	account *service.Account,
	result *service.FishImageResult,
	req *fishImagesRequest,
) {
	if result == nil {
		reqLog.Error("fish_images.empty_result", zap.Int64("account_id", account.ID))
		fishImagesError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
		return
	}
	if fakeStream := leonardoFakeStreamFromContext(c); fakeStream != nil {
		fakeStream.writeEvents(service.FishImagesStreamEvents(req.parsed, result))
	} else {
		c.Data(http.StatusOK, "application/json", result.Body)
	}
	h.recordFishImagesUsage(c, apiKey, subject, subscription, account, result.Forward, req)
	reqLog.Debug("fish_images.request_completed",
		zap.Int64("account_id", account.ID),
		zap.String("group_id", result.GroupID),
	)
}

func (h *GatewayHandler) recordFishImagesUsage(
	c *gin.Context,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	account *service.Account,
	result *service.OpenAIForwardResult,
	req *fishImagesRequest,
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
				zap.String("component", "handler.gateway.fish_images"),
				zap.Int64("user_id", subject.UserID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Int64("account_id", account.ID),
			).Error("fish_images.record_usage_failed", zap.Error(err))
		}
	})
}

func fishImagesAccountsBusyError(c *gin.Context, reqLog *zap.Logger, busyAccountIDs map[int64]struct{}) {
	reqLog.Info("fish_images.accounts_busy", zap.Int("busy_account_count", len(busyAccountIDs)))
	markOpsRoutingCapacityLimited(c)
	fishImagesError(c, http.StatusTooManyRequests, "rate_limit_error",
		"Too many concurrent requests for available accounts, please retry later")
}

// fishImagesFailoverError 把 failover 错误渲染给客户端。
func fishImagesFailoverError(c *gin.Context, failover *service.UpstreamFailoverError) {
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
	fishImagesError(c, status, openAIErrorTypeForStatus(status), message)
}

func fishImagesError(c *gin.Context, status int, errType, message string) {
	if fakeStream := leonardoFakeStreamFromContext(c); fakeStream != nil && fakeStream.failIfCommitted(errType, "", message) {
		return
	}
	if service.IsResponseCommitted(c) {
		return
	}
	c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": message, "param": nil, "code": nil}})
}

// tryFishImagesRelay 把 OpenAI Images 形状的请求原样转发到中转号的 base_url
// （镜像 tryLeonardoImagesRelay）。done=true 表示已经给客户端写了成功或终态
// 错误；switched=true 表示应换下一个账号。
func (h *GatewayHandler) tryFishImagesRelay(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	account *service.Account,
	req *fishImagesRequest,
	failedAccountIDs map[int64]struct{},
	lastFailover **service.UpstreamFailoverError,
	attempts int,
) (switched bool, done bool) {
	if h.openAIGatewayService == nil {
		reqLog.Error("fish_images.relay_unavailable", zap.Int64("account_id", account.ID))
		fishImagesError(c, http.StatusBadGateway, "api_error", "Fish relay forwarding is not available")
		return false, true
	}

	result, err := h.openAIGatewayService.ForwardImages(
		c.Request.Context(), c, account, req.body, req.parsed, req.channelMapping.MappedModel)
	if err == nil {
		h.finishFishImagesRelaySuccess(c, reqLog, apiKey, subject, subscription, account, result, req)
		return false, true
	}

	// 中转流式中途断开时已经把部分图片交给了客户端：先按实际张数记账，再处理错误。
	partial := result != nil && result.ImageCount > 0
	if partial {
		reqLog.Warn("fish_images.relay_partial_error_with_image_result",
			zap.Int64("account_id", account.ID),
			zap.Int("image_count", result.ImageCount),
			zap.Error(err))
		h.recordFishImagesUsage(c, apiKey, subject, subscription, account, result, req)
	}

	if errors.Is(err, context.Canceled) || failoverClientGone(c) {
		reqLog.Info("fish_images.relay_aborted_client_disconnected", zap.Int64("account_id", account.ID))
		return false, true
	}

	if partial || service.IsResponseCommitted(c) {
		reqLog.Warn("fish_images.relay_failed_after_flush",
			zap.Int64("account_id", account.ID), zap.Error(err))
		return false, true
	}

	var failover *service.UpstreamFailoverError
	if errors.As(err, &failover) && failover.ShouldRetryNextAccount() {
		reqLog.Warn("fish_images.relay_failover",
			zap.Int64("account_id", account.ID),
			zap.Int("upstream_status", failover.StatusCode),
			zap.Int("attempts", attempts),
		)
		failedAccountIDs[account.ID] = struct{}{}
		*lastFailover = failover
		return true, false
	}

	reqLog.Error("fish_images.relay_failed",
		zap.Int64("account_id", account.ID), zap.Error(err))
	if failover != nil {
		fishImagesFailoverError(c, failover)
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
		fishImagesError(c, status, errType, message)
		return false, true
	}
	fishImagesError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
	return false, true
}

// finishFishImagesRelaySuccess 记账。响应体已由 ForwardImages 写入 gin，不能再 c.Data。
func (h *GatewayHandler) finishFishImagesRelaySuccess(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	account *service.Account,
	result *service.OpenAIForwardResult,
	req *fishImagesRequest,
) {
	h.recordFishImagesUsage(c, apiKey, subject, subscription, account, result, req)
	upstreamModel := ""
	if result != nil {
		upstreamModel = result.UpstreamModel
	}
	reqLog.Debug("fish_images.relay_completed",
		zap.Int64("account_id", account.ID),
		zap.String("upstream_model", upstreamModel),
	)
}
