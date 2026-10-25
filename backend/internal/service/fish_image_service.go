package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/fish"
	"github.com/tidwall/gjson"
)

// fish 账号的凭据键。api_key 必填；team_id / device_id 由账号连通性测试
// 自动派生回填（派生前请求照发——上游已验证这些头可省）。
const (
	fishCredentialToken    = "api_key"
	fishCredentialTeamID   = "team_id"
	fishCredentialDeviceID = "device_id"
	// fishCredentialMaxPerGen 是账号单次生图上限（entitlements
	// maxImagesPerGeneration：free=2 / plus=4 / pro=8），连通性测试回填。
	fishCredentialMaxPerGen = "max_images_per_generation"
	// fishCredentialPlan 是套餐档位（entitlements.plan：free/plus/pro），
	// 连通性测试与用量拉取回填；402 冷却路径据此分流（free 直挂 UTC 零点）。
	fishCredentialPlan = "plan_type"
)

const (
	fishSubmitBudget        = 60 * time.Second
	fishPollTimeout         = 10 * time.Minute
	fishMaxImagesPerRequest = 8 // 模型 maxCount；free 档配额由上游拒绝
	fishDownloadAttempts    = 3
)

// fishClientCache 按账号缓存带代理的客户端实例（连接复用；客户端无状态，
// 按代理指纹缓存即可）。代理变化时重建。镜像 leonardoClientCache。
type fishClientCache struct {
	mu      sync.Mutex
	clients map[int64]*fishClientEntry
	options fish.Options
}

type fishClientEntry struct {
	client   *fish.Client
	proxyURL string
}

func (c *fishClientCache) clientForAccount(account *Account) *fish.Client {
	proxyURL := accountProxyURL(account)
	if account == nil || account.ID <= 0 {
		return c.newClient(proxyURL)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clients == nil {
		c.clients = make(map[int64]*fishClientEntry)
	}
	if entry, ok := c.clients[account.ID]; ok && entry.proxyURL == proxyURL {
		return entry.client
	}
	client := c.newClient(proxyURL)
	c.clients[account.ID] = &fishClientEntry{client: client, proxyURL: proxyURL}
	return client
}

func (c *fishClientCache) newClient(proxyURL string) *fish.Client {
	opts := c.options
	opts.ProxyURL = proxyURL
	return fish.NewClient(opts)
}

// 轮询间隔用变量便于测试注入。
var (
	fishPollInitialInterval = 2 * time.Second
	fishPollMaxInterval     = 15 * time.Second
)

// FishImageService 实现 fish.audio 生图（OpenAI Images 兼容入口）。
type FishImageService struct {
	clients        *fishClientCache
	resolveStorage ImageStorageResolver
	// inputClient 抓取参考图源图（复用 leonardo_image_input.go 的
	// SSRF 三层防护客户端）。
	inputClient *http.Client
}

// NewFishImageService 构造服务。
func NewFishImageService(resolveStorage ImageStorageResolver) *FishImageService {
	return &FishImageService{
		clients:        &fishClientCache{},
		resolveStorage: resolveStorage,
		inputClient:    newLeonardoInputImageClient(),
	}
}

// FishImageResult 是一次 fish 生图的结果。
type FishImageResult struct {
	// Body 是 OpenAI Images 形状的完整 JSON 响应（data[].b64_json 或经
	// 对象存储转存后的 data[].url）。
	Body []byte
	// Forward 供计费记账（ImageCount / ImageSize 档位）。
	Forward *OpenAIForwardResult
	// GroupID 是上游任务组 id（排障用）。
	GroupID string
	// Size / Quality 是回显给客户端的请求参数。
	Size    string
	Quality string
	// Usage 是估算的 OpenAI usage（fish 不报 token 用量，见 fishImageUsage）；
	// 非流式写进响应体最外层，假流式挂在最后一个 completed 事件上。
	Usage json.RawMessage
}

// fishInputImage 是一张待上传的参考图（复用 leonardoInputImage 形状）。
type fishInputImage = leonardoInputImage

// FishImageCall 跨账号切换保持的请求级状态：参考图只取一次；已交付的图
// 与剩余张数在换号续跑间累计——重跑只生成缺口，绝不重生成已交付图（防双扣）。
type FishImageCall struct {
	Request            *OpenAIImagesRequest
	ChannelMappedModel string
	// fitLimits 是动态尺寸规划阈值（系统设置-网关，handler 每请求传入）；
	// plan 是解析出的尺寸决策，partial 路径复用，避免换号后重猜。
	fitLimits  domain.FishFitLimits
	plan       *domain.FishSizePlan
	inputsOnce sync.Once
	inputs     []*fishInputImage
	inputsErr  error
	// delivered 是已下载入内存的成品图（跨账号累计）。
	delivered [][]byte
	// remaining 是还需生成的张数（NewFishImageCall 时 = n）。
	remaining int
	// startedAt 是请求起点：usage 记录的总耗时按整个请求计（含换号续跑）。
	startedAt time.Time
}

// NewFishImageCall 构造一次调用的共享状态。fitLimits 零值 = 不裁剪 +
// 允许 2 倍放大（生产路径恒传设置解析层的值，缺省 4px / 2.0）。
func NewFishImageCall(req *OpenAIImagesRequest, channelMappedModel string, fitLimits domain.FishFitLimits) *FishImageCall {
	n := 1
	if req != nil && req.N > 1 {
		n = req.N
	}
	return &FishImageCall{Request: req, ChannelMappedModel: channelMappedModel, fitLimits: fitLimits, remaining: n, startedAt: time.Now()}
}

// Remaining 返回还需生成的张数（handler 日志用）。
func (c *FishImageCall) Remaining() int {
	if c == nil {
		return 0
	}
	return c.remaining
}

// Delivered 返回已交付张数（handler 判断部分成功用）。
func (c *FishImageCall) Delivered() int {
	if c == nil {
		return 0
	}
	return len(c.delivered)
}

// FitLimits 返回本次调用的尺寸规划阈值（handler 的中转判定与规划共用）。
func (c *FishImageCall) FitLimits() domain.FishFitLimits {
	if c == nil {
		return domain.DefaultFishFitLimits()
	}
	return c.fitLimits
}

// modelName 返回实际出图的模型名：渠道映射优先，未映射回落请求名。
func (c *FishImageCall) modelName() string {
	if mapped := strings.TrimSpace(c.ChannelMappedModel); mapped != "" {
		return mapped
	}
	if c.Request == nil {
		return ""
	}
	return strings.TrimSpace(c.Request.Model)
}

// Generate 是 GenerateCall 的薄封装（单账号直调 / 测试用），规划阈值取
// 系统设置缺省（裁 4px / 放大 2 倍）。
func (s *FishImageService) Generate(ctx context.Context, account *Account, token string, req *OpenAIImagesRequest) (*FishImageResult, error) {
	return s.GenerateCall(ctx, account, token, NewFishImageCall(req, req.Model, domain.DefaultFishFitLimits()))
}

// fishIdentityFromAccount 从账号凭据构造指纹身份。
func fishIdentityFromAccount(account *Account) fish.Identity {
	return fish.Identity{
		DeviceID: account.GetCredential(fishCredentialDeviceID),
		TeamID:   account.GetCredential(fishCredentialTeamID),
	}
}

// accountFishImageCap 返回账号单次生图上限：凭据回填的
// max_images_per_generation（连通性测试写入），非法/缺失回落模型层 8。
func accountFishImageCap(account *Account) int {
	if account == nil {
		return fishMaxImagesPerRequest
	}
	if cap, err := strconv.Atoi(strings.TrimSpace(account.GetCredential(fishCredentialMaxPerGen))); err == nil &&
		cap >= 1 && cap <= fishMaxImagesPerRequest {
		return cap
	}
	return fishMaxImagesPerRequest
}

// fishPendingBatch 是提交段产出：一个已受理的任务组及其张数。
type fishPendingBatch struct {
	groupID string
	count   int
}

// GenerateCall 在指定账号上尽量完成 call.remaining 张图（三段式）：
//
//  1. 提交段（串行）：n > 账号单次上限时拆多个子生成（计价按张线性，
//     拆分不改变总成本）。提交错分类处理：网络错=结果不明（停止提交，
//     已提交的照走完）；配额/429/5xx 等可换号错（停止提交，走完已提交
//     的，错误随进度返回——handler 换号续跑只生成缺口）。
//  2. 轮询段：逐组 poll 到终态；failed 的组不交付（remaining 不减）。
//  3. 下载段：done 的产物下载进 call.delivered。
//
// remaining 清零 → 用全部 delivered（含前序账号进度）构建响应；
// remaining > 0 → 返回「最有分量的错误」（提交错优先于组内 failed）。
func (s *FishImageService) GenerateCall(ctx context.Context, account *Account, token string, call *FishImageCall) (*FishImageResult, error) {
	if call == nil || call.Request == nil {
		return nil, fmt.Errorf("fish: nil call")
	}
	parsed := call.Request
	mappedModel := call.modelName()
	model, ok := domain.LookupFishModel(mappedModel)
	if !ok {
		return nil, fish.NewRequestError(fmt.Sprintf("fish: model %q is not a fish image model", mappedModel))
	}
	// 动态尺寸规划：白名单外但能就近 cover 的尺寸也走原生（受阈值约束）。
	plan, err := domain.FishPlanSize(parsed.Size, call.fitLimits)
	if err != nil {
		return nil, fish.NewRequestError(err.Error())
	}
	call.plan = plan
	imageSize := plan.Native
	requestedQuality, err := fishQualityForRequest(parsed.Quality, model.ExtendedQuality)
	if err != nil {
		return nil, err
	}
	// 账号上限只压低实际上游档；响应回显请求档，计费按交付尺寸。
	quality := fishCapQuality(requestedQuality, account.FishMaxQuality())
	if call.remaining < 1 {
		call.remaining = 1
	}
	if call.remaining > fishMaxImagesPerRequest {
		return nil, fish.NewRequestError(fmt.Sprintf("fish: n=%d exceeds max %d per generation", call.remaining, fishMaxImagesPerRequest))
	}
	if strings.TrimSpace(parsed.Prompt) == "" {
		return nil, fish.NewRequestError("fish: prompt is required")
	}
	// 空 output_format 保持官网 PNG。png / jpeg 在下载后本机转码。
	// webp 由选号阶段交给中转；落到原生号时在扣费前拒绝。
	_, formatOK := normalizeImageOutputFormat(parsed.OutputFormat)
	switch {
	case isImageOutputFormatWebP(parsed.OutputFormat):
		return nil, fish.NewRequestError("output_format=webp is only supported through Fish relay accounts")
	case !formatOK:
		return nil, fish.NewRequestError(fmt.Sprintf("unsupported output_format %q (expected png or jpeg)", strings.TrimSpace(parsed.OutputFormat)))
	}

	client := s.clients.clientForAccount(account)
	identity := fishIdentityFromAccount(account)
	cap := accountFishImageCap(account)

	// 参考图（/v1/images/edits）：跨账号只取一次
	var refAssets []fish.RefAsset
	if wantsFishImageEdits(parsed) {
		inputs, err := s.loadInputs(ctx, call)
		if err != nil {
			return nil, err
		}
		for _, input := range inputs {
			asset, err := client.UploadRefImage(ctx, token, identity, "image.png", input.ContentType, input.Data)
			if err != nil {
				return nil, err
			}
			refAssets = append(refAssets, *asset)
		}
	}
	variant := fish.VariantTextToImage
	if len(refAssets) > 0 {
		variant = fish.VariantEdit
	}

	// ── 提交段（串行）──
	var pending []fishPendingBatch
	var submitErr error     // 可换号的提交错误（配额/429/5xx…）
	var indeterminate error // 提交结果不明（网络错）
	remaining := call.remaining
	for remaining > 0 {
		batch := minInt(cap, remaining)
		price, err := client.Price(ctx, token, identity, fish.PriceParams{
			Model: model.Name, Variant: variant, ImageSize: imageSize,
			Quality: quality, Count: batch, RefImageCount: len(refAssets),
		})
		if err != nil {
			submitErr = err
			break
		}
		submitCtx, cancel := context.WithTimeout(ctx, fishSubmitBudget)
		group, err := client.SubmitGeneration(submitCtx, token, identity, fish.GenerateRequest{
			Model: model.Name, Variant: variant, Prompt: parsed.Prompt,
			Count: batch, ImageSize: imageSize, Quality: quality, ExtendedQuality: model.ExtendedQuality,
			RefAssets: refAssets, ExpectedCredits: price.Credits, PricingVersion: price.PricingVersion,
		})
		cancel()
		if err != nil {
			if isFishNetworkErr(err) {
				indeterminate = &fishSubmitIndeterminateError{err: err}
			} else {
				submitErr = err
			}
			break
		}
		pending = append(pending, fishPendingBatch{groupID: group.ID, count: batch})
		remaining -= batch
	}
	// 提交段一个都没成且无不明 → 参数类错误直接终态（没有进度可言）
	if len(pending) == 0 && indeterminate == nil && submitErr != nil && !isFishFailoverClass(submitErr) {
		return nil, submitErr
	}

	// ── 轮询段 + 下载段（脱离请求生命周期；客户端断开不打断）──
	pollCtx, pollCancel := context.WithTimeout(context.WithoutCancel(ctx), fishPollTimeout)
	defer pollCancel()
	var lastGroupErr error
	for _, batch := range pending {
		finalGroup, err := s.pollUntilDone(pollCtx, client, token, identity, batch.groupID)
		if err != nil {
			var generationFailed *fishGenerationFailedError
			if errors.As(err, &generationFailed) {
				// 组内失败（审核等）：这批不交付，继续下一组
				lastGroupErr = err
				continue
			}
			// 轮询/结果不可得：保留进度，错误上抛（不换号重提语义）
			call.remaining = clampRemaining(call, pending, batch)
			return nil, err
		}
		mediaItems := collectFishMedia(finalGroup)
		if len(mediaItems) == 0 {
			lastGroupErr = &fishResultUnavailableError{groupID: batch.groupID, err: fmt.Errorf("task finished without media")}
			continue
		}
		for _, item := range mediaItems {
			data, err := s.downloadWithRetry(pollCtx, client, item.URL)
			if err != nil {
				call.remaining = clampRemaining(call, pending, batch)
				return nil, &fishResultUnavailableError{groupID: batch.groupID, err: err}
			}
			call.delivered = append(call.delivered, data)
		}
	}
	// remaining 重算：n - 已交付
	call.remaining = callInitialN(call) - len(call.delivered)
	if call.remaining <= 0 {
		delivered, echoedFormat := fishResponseImages(call, plan)
		// 回显与计费都按交付尺寸（正常即客户请求尺寸；裁剪缩放失败回退原图时如实变化）。
		deliveredSize := fishDeliveredSize(delivered, plan)
		usage := fishImageUsage(parsed.Model, parsed.Prompt, requestedQuality, deliveredSize, len(delivered))
		body, err := s.buildResponseBody(ctx, delivered, deliveredSize, requestedQuality, echoedFormat, usage)
		if err != nil {
			return nil, err
		}
		if plan.NeedsTransform() {
			slog.Info("fish_image_fit_applied",
				"requested_size", plan.TargetSize,
				"native_size", plan.Native,
				"crop_px", plan.CropPixels,
				"scale", strconv.FormatFloat(plan.ScaleFactor, 'f', 3, 64),
				"account_id", accountIDOrZero(account),
			)
		}
		forward := fishForwardResult(parsed.Model, len(call.delivered), deliveredSize, usage)
		forward.Duration = time.Since(call.startedAt)
		return &FishImageResult{
			Body:    body,
			Forward: forward,
			GroupID: fishFirstGroupID(pending),
			Size:    deliveredSize,
			Quality: requestedQuality,
			Usage:   usage,
		}, nil
	}

	// 还有缺口：按分量返回错误（不明 > 可换号提交错 > 组内失败）
	if indeterminate != nil {
		return nil, indeterminate
	}
	if submitErr != nil {
		return nil, submitErr
	}
	if lastGroupErr != nil {
		return nil, lastGroupErr
	}
	return nil, &fishResultUnavailableError{groupID: fishFirstGroupID(pending), err: fmt.Errorf("generation incomplete: %d of %d delivered", len(call.delivered), callInitialN(call))}
}

// callInitialN 返回本次请求的初始张数。
func callInitialN(call *FishImageCall) int {
	n := 1
	if call != nil && call.Request != nil && call.Request.N > 1 {
		n = call.Request.N
	}
	return n
}

// clampRemaining 在轮询/下载中途失败时重算缺口 = 初始 n - 已交付。
func clampRemaining(call *FishImageCall, pending []fishPendingBatch, failed fishPendingBatch) int {
	return callInitialN(call) - len(call.delivered)
}

func fishFirstGroupID(pending []fishPendingBatch) string {
	if len(pending) > 0 {
		return pending[0].groupID
	}
	return ""
}

// isFishFailoverClass 报告错误是否属于可换号类（配额/限流/暂态/显式失败）。
func isFishFailoverClass(err error) bool {
	failure := classifyFishError(err)
	return failure.Failover != nil && failure.Failover.ShouldRetryNextAccount()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// BuildFishPartialResult 为「有进度但终止」的请求构建部分成功响应：
// 张数=实际交付数，计费按实际张数。调用方（handler）负责日志标注 partial。
func (s *FishImageService) BuildFishPartialResult(ctx context.Context, call *FishImageCall) *FishImageResult {
	if call == nil || call.Request == nil || len(call.delivered) == 0 {
		return nil
	}
	parsed := call.Request
	// 正常路径恒有 call.plan；注入交付图的测试路径宽松回退。
	plan := call.plan
	if plan == nil {
		plan = domainFishResolveLoose(parsed, call.fitLimits)
	}
	if plan == nil {
		return nil
	}
	quality := fishLooseQuality(call)
	images, echoedFormat := fishResponseImages(call, plan)
	deliveredSize := fishDeliveredSize(images, plan)
	usage := fishImageUsage(parsed.Model, parsed.Prompt, quality, deliveredSize, len(images))
	body, err := s.buildResponseBody(ctx, images, deliveredSize, quality, echoedFormat, usage)
	if err != nil {
		return nil
	}
	forward := fishForwardResult(parsed.Model, len(call.delivered), deliveredSize, usage)
	forward.Duration = time.Since(call.startedAt)
	return &FishImageResult{
		Body:    body,
		Forward: forward,
		Size:    deliveredSize,
		Quality: quality,
		Usage:   usage,
	}
}

// fishForwardResult 构造计费记账用的转发结果：张数 + 交付尺寸档，估算的 usage
// 同时写进用量记录（/usage 展示；渠道按 token 计价时按它计费）。与 OpenAI 出图
// 同一套映射：OutputTokens 含图片 token，ImageOutputTokens 单列，不会重复计费。
func fishForwardResult(model string, imageCount int, deliveredSize string, usage json.RawMessage) *OpenAIForwardResult {
	forward := &OpenAIForwardResult{
		Model:      model,
		ImageCount: imageCount,
		ImageSize:  fishBillingSize(deliveredSize),
	}
	if len(usage) > 0 {
		if parsed, ok := openAIUsageFromGJSON(gjson.ParseBytes(usage)); ok {
			forward.Usage = parsed
		}
	}
	return forward
}

// fishImageUsage 估算 fish 原生出图的 OpenAI usage：fish 不报 token 用量，输出
// token 按 gpt-image-1 官方表 × 交付像素 × 张数估算（见
// estimateGPTImageOutputTokens），质量按客户请求档；提示词按 o200k 计数。
// 尺寸无法解析或档位未知时返回 nil。
func fishImageUsage(model, prompt, quality, deliveredSize string, imageCount int) json.RawMessage {
	if imageCount <= 0 {
		return nil
	}
	width, height, ok := parseImageBillingDimensions(deliveredSize)
	if !ok {
		return nil
	}
	return openAIImageUsageJSON(model, prompt, estimateGPTImageOutputTokens(quality, width, height)*imageCount)
}

// domainFishResolveLoose 为 partial 路径宽松解析尺寸：规划失败回落默认
// 1024x1024（回显与计费兜底；正常路径恒有 call.plan）。
func domainFishResolveLoose(parsed *OpenAIImagesRequest, limits domain.FishFitLimits) *domain.FishSizePlan {
	if plan, err := domain.FishPlanSize(parsed.Size, limits); err == nil {
		return plan
	}
	plan, _ := domain.FishPlanSize("1024x1024", limits)
	return plan
}

// fishLooseQuality 宽松归一质量档（与 GenerateCall 同样按渠道映射后的模型
// 判定 xhigh / max），失败回落 medium。
func fishLooseQuality(call *FishImageCall) string {
	model, ok := domain.LookupFishModel(call.modelName())
	if !ok && len(domain.FishModels) > 0 {
		model = domain.FishModels[0]
	}
	quality, err := fishQualityForRequest(call.Request.Quality, model.ExtendedQuality)
	if err != nil {
		return "medium"
	}
	return quality
}

// fishDeliveredSize 返回交付给客户的实际尺寸（镜像 leonardoDeliveredSize）：
// 优先读首张最终图的像素（裁剪缩放失败退回原图时如实变化），读不到时取
// plan 的目标尺寸。
func fishDeliveredSize(images [][]byte, plan *domain.FishSizePlan) string {
	if len(images) > 0 {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(images[0])); err == nil && cfg.Width > 0 && cfg.Height > 0 {
			return fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
		}
	}
	return plan.TargetSize
}

// accountIDOrZero 供日志安全取账号 id。
func accountIDOrZero(account *Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}

// pollUntilDone 轮询任务组到终态。429/5xx/网络是暂态（退避重试）；
// 上游显式 failed 才是可重试的失败（积分已退）。
func (s *FishImageService) pollUntilDone(ctx context.Context, client *fish.Client, token string, identity fish.Identity, groupID string) (*fish.TaskGroup, error) {
	interval := fishPollInitialInterval
	for {
		group, err := client.PollTaskGroup(ctx, token, identity, groupID)
		if err != nil {
			if isFishTransientErr(err) {
				select {
				case <-time.After(interval):
					interval = minDuration(interval*2, fishPollMaxInterval)
					continue
				case <-ctx.Done():
					return nil, &fishResultUnavailableError{groupID: groupID, err: ctx.Err()}
				}
			}
			return nil, err
		}
		switch group.Status {
		case "done":
			return group, nil
		case "failed":
			return nil, &fishGenerationFailedError{
				groupID: groupID,
				reason:  fishFailureSummary(group),
			}
		}
		select {
		case <-time.After(interval):
			interval = minDuration(interval*2, fishPollMaxInterval)
		case <-ctx.Done():
			return nil, &fishResultUnavailableError{groupID: groupID, err: ctx.Err()}
		}
	}
}

func (s *FishImageService) downloadWithRetry(ctx context.Context, client *fish.Client, imageURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < fishDownloadAttempts; attempt++ {
		data, err := client.DownloadImage(ctx, imageURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
		select {
		case <-time.After(time.Duration(attempt+1) * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// fishResponseImages 在写响应前按 plan 做裁剪缩放并按 output_format 转码。
// 有变换时 fit 内已完成格式编码（含 jpeg 质量）；无变换时仅转码。
// 空格式保持官网字节。
func fishResponseImages(call *FishImageCall, plan *domain.FishSizePlan) ([][]byte, string) {
	format := ""
	var compression *int
	if call != nil && call.Request != nil {
		format, _ = normalizeImageOutputFormat(call.Request.OutputFormat)
		compression = call.Request.OutputCompression
	}
	images := call.delivered
	if plan.NeedsTransform() {
		images = fitCoverImages(images, plan, format, compression)
	} else {
		images = applyImageOutputFormat(images, format, compression)
	}
	return images, fishDeliveredOutputFormat(images)
}

// fishDeliveredOutputFormat 按首张图文件头回显 png / jpeg。认不出时按官网
// 产物回显 png（未转码的默认路径）。
func fishDeliveredOutputFormat(images [][]byte) string {
	if len(images) > 0 {
		if format := sniffImageOutputFormat(images[0]); format != "" {
			return format
		}
	}
	return imageOutputFormatPNG
}

// buildResponseBody 构造 OpenAI Images 形状响应；对象存储启用时把
// b64 换存 URL（失败回退 b64）。images 已是转码后的字节。
func (s *FishImageService) buildResponseBody(ctx context.Context, images [][]byte, size, quality, outputFormat string, usage json.RawMessage) ([]byte, error) {
	data := make([]map[string]any, 0, len(images))
	for _, image := range images {
		data = append(data, map[string]any{
			"b64_json": base64.StdEncoding.EncodeToString(image),
		})
	}
	bodyMap := map[string]any{
		"created": time.Now().Unix(),
		"data":    data,
	}
	if strings.TrimSpace(size) != "" {
		bodyMap["size"] = size
	}
	if strings.TrimSpace(quality) != "" {
		bodyMap["quality"] = quality
	}
	if strings.TrimSpace(outputFormat) != "" {
		bodyMap["output_format"] = outputFormat
	}
	if len(usage) > 0 {
		bodyMap["usage"] = usage
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, fmt.Errorf("fish: encode response body: %w", err)
	}
	if s.resolveStorage != nil {
		if uploader, enabled := s.resolveStorage(); enabled && uploader != nil {
			if rewritten, rerr := uploader.Rewrite(ctx, fmt.Sprintf("fish-%d", time.Now().UnixNano()), body); rerr == nil {
				return rewritten, nil
			}
			// 转存失败回退 b64 响应
		}
	}
	return body, nil
}

// loadInputs 取 edits 的参考图（multipart 直传数据 + URL 抓取），跨账号
// 只执行一次。
func (s *FishImageService) loadInputs(ctx context.Context, call *FishImageCall) ([]*fishInputImage, error) {
	call.inputsOnce.Do(func() {
		inputs := make([]*fishInputImage, 0, len(call.Request.Uploads)+len(call.Request.InputImageURLs))
		for i := range call.Request.Uploads {
			upload := call.Request.Uploads[i]
			if len(upload.Data) == 0 {
				call.inputsErr = fmt.Errorf("fish: uploaded input image is empty")
				return
			}
			inputs = append(inputs, &fishInputImage{Data: upload.Data, ContentType: upload.ContentType})
		}
		for _, rawURL := range call.Request.InputImageURLs {
			input, err := fetchLeonardoInputImage(ctx, s.inputClient, rawURL)
			if err != nil {
				call.inputsErr = err
				return
			}
			inputs = append(inputs, input)
		}
		if len(inputs) == 0 {
			call.inputsErr = fmt.Errorf("fish: image edits require an input image")
			return
		}
		call.inputs = inputs
	})
	return call.inputs, call.inputsErr
}

// ── 内部工具 ──

// fishQualityForRequest 归一 OpenAI 质量到 fish 档位。
// gpt-image-2 只有 low/medium/high。extended 为 true（两个 2.5 模型）时
// 额外接受 xhigh 和 max；max 是最高档。不支持的高档返回请求错误。
func fishQualityForRequest(requested string, extended bool) (string, error) {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "low":
		return "low", nil
	case "high", "hd":
		return "high", nil
	case "xhigh", "x-high", "extra-high", "extra_high", "extrahigh":
		if !extended {
			return "", fish.NewRequestError("quality xhigh is only supported by gpt-image-2.5 models")
		}
		return "xhigh", nil
	case "max", "maximum":
		if !extended {
			return "", fish.NewRequestError("quality max is only supported by gpt-image-2.5 models")
		}
		return "max", nil
	default: // "", "auto", "medium", "standard"
		return "medium", nil
	}
}

// fishBillingSize 把 fish 的 image_size 值映射为计费档位。
func fishBillingSize(imageSize string) string {
	if tier, ok := ClassifyImageBillingTier(imageSize); ok {
		return tier
	}
	return NormalizeImageBillingTierOrDefault("")
}

func wantsFishImageEdits(parsed *OpenAIImagesRequest) bool {
	if parsed == nil {
		return false
	}
	return len(parsed.InputImageURLs) > 0 || len(parsed.Uploads) > 0
}

func collectFishMedia(group *fish.TaskGroup) []*fish.MediaItem {
	if group == nil {
		return nil
	}
	items := make([]*fish.MediaItem, 0, len(group.Results))
	for i := range group.Results {
		if item := group.Results[i].MediaItem; item != nil && strings.TrimSpace(item.URL) != "" {
			items = append(items, item)
		}
	}
	return items
}

func fishFailureSummary(group *fish.TaskGroup) string {
	for i := range group.Results {
		result := group.Results[i]
		if result.FailureReason != nil && *result.FailureReason != "" {
			return *result.FailureReason
		}
		if result.ErrorMessage != nil && *result.ErrorMessage != "" {
			return *result.ErrorMessage
		}
	}
	return "unknown failure"
}

func isFishNetworkErr(err error) bool {
	var requestErr *fish.RequestError
	return errors.As(err, &requestErr) && requestErr.IsNetwork()
}

func isFishTransientErr(err error) bool {
	var rateLimited *fish.RateLimitedError
	if errors.As(err, &rateLimited) {
		return true
	}
	var temporary *fish.UpstreamTemporaryError
	if errors.As(err, &temporary) {
		return true
	}
	return isFishNetworkErr(err)
}

// ── 包内错误类型（分类器消费） ──

// fishSubmitIndeterminateError 标记「提交结果不明」：上游可能已受理并
// 扣积分，调用方不得重试或换号。
type fishSubmitIndeterminateError struct{ err error }

func (e *fishSubmitIndeterminateError) Error() string {
	return fmt.Sprintf("fish submit indeterminate: %v", e.err)
}

func (e *fishSubmitIndeterminateError) Unwrap() error { return e.err }

// fishGenerationFailedError 是上游显式失败（积分已退，可换账号重试）。
type fishGenerationFailedError struct {
	groupID string
	reason  string
}

func (e *fishGenerationFailedError) Error() string {
	return fmt.Sprintf("fish generation %s failed: %s", e.groupID, e.reason)
}

// fishResultUnavailableError 是任务已受理但结果取不回（轮询超时/下载
// 失败）。不重提（可能双扣），客户端按上游故障处理。
type fishResultUnavailableError struct {
	groupID string
	err     error
}

func (e *fishResultUnavailableError) Error() string {
	return fmt.Sprintf("fish generation %s result unavailable: %v", e.groupID, e.err)
}

func (e *fishResultUnavailableError) Unwrap() error { return e.err }
