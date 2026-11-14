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
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	leonardo "github.com/Wei-Shaw/sub2api/internal/pkg/leonardo"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// LeonardoImageService 把一次 OpenAI 形状的出图请求翻译成 Leonardo 网页版
// GraphQL 调用，再把产物翻译回 OpenAI 形状的响应。
//
// 它不做账号选择与 failover——那是 handler 的职责；本服务只负责「给定账号和
// JWT 完成一次出图」，因此可以脱离 gin 与调度器单测。
type LeonardoImageService struct {
	clients *leonardoClientCache
	// resolveStorage 为 nil 或返回未启用时，响应回落 b64_json。
	resolveStorage ImageStorageResolver
	// inputClient 抓取参考图源图；见 leonardo_image_input.go 的 SSRF 说明。
	inputClient *http.Client
}

// NewLeonardoImageService 构造出图服务。
func NewLeonardoImageService(resolveStorage ImageStorageResolver) *LeonardoImageService {
	return &LeonardoImageService{
		clients:        &leonardoClientCache{},
		resolveStorage: resolveStorage,
		inputClient:    newInputImageClient(),
	}
}

// LeonardoImageResult 是一次出图的完整产出。
type LeonardoImageResult struct {
	// Body 是可直接写给客户端的 OpenAI 形状响应。
	Body []byte
	// Forward 交给 RecordUsage 记账；计费只认 ImageCount 与 ImageSize 两个字段。
	Forward *OpenAIForwardResult
	// APICreditCost 是上游确认的实耗积分（apiCreditCost），仅供日志/观测。
	APICreditCost int
	// GenerationID 是上游任务 id，用于问题排查。
	GenerationID string

	// 以下元数据供假流式的 completed 事件使用（OpenAI Images 流式协议字段）。
	// Size 是上游实际出图尺寸（如 "1024x1024"）。
	Size string
	// OutputFormat 按首张图（转码后）文件头判定：png 或 jpeg。
	OutputFormat string
	// Quality 是提交给上游的质量档（小写，如 "medium"）。
	Quality string
	// Background 是 transparent 或 opaque。
	Background string
	// Usage 是按扣除积分估算的 OpenAI usage 对象（JSON）；未开启估算或积分
	// 未知时为 nil。见 leonardoImageUsage。
	Usage json.RawMessage
}

// leonardoImageDetachedTimeout 是提交之后（轮询、下载、转存）脱离客户端连接
// 的总时限；参考 Adobe 的取值（生成通常分钟内完成）。
const leonardoImageDetachedTimeout = 10 * time.Minute

// 轮询参数（变量以便单测缩短）。
var (
	// leonardoPollInterval 是生成状态轮询间隔，也是下载重试的退避基数。
	leonardoPollInterval = 2 * time.Second
	// leonardoPollMaxBackoff 是轮询瞬时失败后的退避上限。
	leonardoPollMaxBackoff = 15 * time.Second
	// leonardoCompleteWithoutImagesGrace 是 COMPLETE 但产物尚未落表时的等待上限。
	leonardoCompleteWithoutImagesGrace = 30 * time.Second
)

// leonardoDownloadAttempts 是单张产物下载的最大尝试次数。
const leonardoDownloadAttempts = 3

// LeonardoImageCall 是一次客户端请求在多次换号尝试之间共享的状态。
//
// 参考图 URL 只抓一次：换号时复用已取回的字节，只重新上传（Leonardo 的
// initImageId 与账号绑定，不能跨号复用）。
type LeonardoImageCall struct {
	Request *OpenAIImagesRequest
	// ChannelMappedModel 非空时替代 Request.Model 参与账号模型映射与计费模型。
	ChannelMappedModel string
	// UsageTokensPerCredit 是估算 usage 的换算比例（每积分多少输出 token，
	// 配置 gateway.leonardo_usage_tokens_per_credit）；≤0 时响应不带 usage。
	UsageTokensPerCredit float64

	inputsOnce sync.Once
	inputs     []*inputImage
	inputsErr  error
}

// NewLeonardoImageCall 构造请求级出图状态。
func NewLeonardoImageCall(req *OpenAIImagesRequest, channelMappedModel string) *LeonardoImageCall {
	return &LeonardoImageCall{Request: req, ChannelMappedModel: strings.TrimSpace(channelMappedModel)}
}

// inputImages 懒加载 JSON 体里的参考图 URL；首个调用的 ctx 决定抓取是否被取消。
func (call *LeonardoImageCall) inputImages(ctx context.Context, client *http.Client) ([]*inputImage, error) {
	call.inputsOnce.Do(func() {
		images := make([]*inputImage, 0, len(call.Request.InputImageURLs))
		for _, rawURL := range call.Request.InputImageURLs {
			image, err := fetchInputImage(ctx, client, rawURL)
			if err != nil {
				// 取图失败是请求本身的问题，换账号也救不了。
				call.inputsErr = leonardoInputImageRequestError(err)
				return
			}
			images = append(images, image)
		}
		call.inputs = images
	})
	return call.inputs, call.inputsErr
}

// validateLeonardoImageParams 校验原生路径不支持的参数组合。
// mask / n>8 由 handler 在选号前转中转或拒绝，这里是兜底防线。stream 由
// handler 以假流式承接，本服务只产出完整结果；partial_images 接受但忽略
// （假流式不发预览，OpenAI 允许实际预览张数少于请求）。moderation 与
// input_fidelity 同样接受但忽略：审核强度由 Leonardo 上游决定；参考图强度
// 原生固定 MID，没有 fidelity 档位，忽略只是少一点细节保真，不会出错（不少
// 客户端默认就带这两个字段；中转号照旧透传）。style（dall-e-3 专属）原生
// 协议没有对应语义，明确拒绝。
func validateLeonardoImageParams(req *OpenAIImagesRequest) error {
	if req == nil {
		return leonardo.NewRequestError("images request is required")
	}
	if req.HasMask {
		return leonardo.NewRequestError("mask edits are only supported through Leonardo relay accounts")
	}
	if req.N > leonardo.MaxImageQuantity {
		return leonardo.NewRequestError(fmt.Sprintf("n > %d is only supported through Leonardo relay accounts", leonardo.MaxImageQuantity))
	}
	if strings.TrimSpace(req.Style) != "" {
		return leonardo.NewRequestError(`parameter "style" is not supported on Leonardo native accounts`)
	}
	// output_format 在下载后本机转码（png / jpeg）；webp 只能由中转号服务。
	outputFormat, ok := normalizeImageOutputFormat(req.OutputFormat)
	switch {
	case isImageOutputFormatWebP(req.OutputFormat):
		return leonardo.NewRequestError("output_format=webp is only supported through Leonardo relay accounts")
	case !ok:
		return leonardo.NewRequestError(fmt.Sprintf("unsupported output_format %q (expected png or jpeg)", strings.TrimSpace(req.OutputFormat)))
	case outputFormat == imageOutputFormatJPEG && leonardoWantsTransparentBackground(req):
		return leonardo.NewRequestError("output_format=jpeg does not support background=transparent")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return leonardo.NewRequestError("prompt is required")
	}
	return nil
}

// Generate 用指定账号完成一次出图（单次调用，不跨换号复用输入图）。
func (s *LeonardoImageService) Generate(
	ctx context.Context, account *Account, token string, req *OpenAIImagesRequest,
) (*LeonardoImageResult, error) {
	return s.GenerateCall(ctx, account, token, NewLeonardoImageCall(req, ""))
}

// GenerateCall 用指定账号完成一次出图。
//
// 错误一律是 leonardo 包的类型化错误（交由 classifyLeonardoError 翻译成网关
// 语义）。ctx 取消只影响提交之前的阶段（取图、上传、余额门槛）：一旦提交，
// 上游已扣积分，之后的轮询与下载脱离客户端连接，保证产物能交回 handler 记账。
//
// 提交阶段（Generate mutation）的网络错误是「结果不明」：无法判断上游是否
// 已受理扣费（无幂等键），返回错误时 handler 不得换号重提。
func (s *LeonardoImageService) GenerateCall(
	ctx context.Context, account *Account, token string, call *LeonardoImageCall,
) (*LeonardoImageResult, error) {
	var req *OpenAIImagesRequest
	if call != nil {
		req = call.Request
	}
	if account == nil {
		return nil, leonardo.NewRequestError("leonardo account is required")
	}
	if req == nil {
		return nil, leonardo.NewRequestError("images request is required")
	}
	if strings.TrimSpace(token) == "" {
		return nil, leonardo.NewAuthError("leonardo access token is empty", http.StatusUnauthorized)
	}
	if err := validateLeonardoImageParams(req); err != nil {
		return nil, err
	}
	startTime := time.Now()

	requestedModel := strings.TrimSpace(req.Model)
	if call.ChannelMappedModel != "" {
		requestedModel = call.ChannelMappedModel
	}
	mappedModel := account.GetMappedModel(requestedModel)
	// 兜底防线（调度层已按 IsModelSupported 过滤）：映射结果必须是平台已实现
	// 的模型，换成上游 slug（gpt-image-2.5 系列带 openai/ 前缀）后再提交。
	upstreamModel, ok := domain.LeonardoNativeSlug(mappedModel)
	if !ok {
		reqErr := leonardo.NewRequestError(fmt.Sprintf(
			"leonardo account cannot serve model %q (mapped %q)", requestedModel, mappedModel))
		reqErr.UserMessage = fmt.Sprintf("model %s is not supported on Leonardo", requestedModel)
		return nil, reqErr
	}

	request, plan, err := s.buildUpstreamRequest(upstreamModel, req, account.LeonardoImageLimits())
	if err != nil {
		return nil, err
	}

	// 不做提交前的积分预估：直接提交，积分不足由上游拒绝（不扣费），
	// 归类为 InsufficientTokensError 后冷却换号。
	client := s.clients.clientForAccount(account)

	referenceIDs, err := s.uploadReferenceImages(ctx, client, token, call)
	if err != nil {
		return nil, err
	}
	request.Parameters.Guidances = leonardoGuidancesFor(referenceIDs)

	if err := ctx.Err(); err != nil {
		// 客户端在提交前离开：还没扣积分，直接放弃。
		return nil, err
	}

	// 提交是唯一可能「结果不明」的阶段：给它独立的小预算，失败时区分
	// 上游明确拒绝（类型化错误）与网络不明（RequestError.IsNetwork）。
	submitCtx, cancelSubmit := context.WithTimeout(ctx, 60*time.Second)
	generated, err := client.SubmitGeneration(submitCtx, token, *request)
	cancelSubmit()
	if err != nil {
		var networkErr *leonardo.RequestError
		if errors.As(err, &networkErr) && networkErr.IsNetwork() {
			// 提交结果不明：不重试、不换号（上游可能已受理并扣费）。
			return nil, &leonardoSubmitIndeterminateError{err: err}
		}
		return nil, err
	}

	// 提交成功：脱离客户端连接完成轮询与下载。
	upstreamCtx, cancelUpstream := context.WithTimeout(context.WithoutCancel(ctx), leonardoImageDetachedTimeout)
	defer cancelUpstream()

	// 提交成功之后，只有上游明确判定 FAILED（积分退回）才允许换号重提；轮询、
	// 取详情、下载阶段的其它失败一律包成「结果不可取」，否则换号会让同一张图
	// 在另一个账号上再扣一次积分。
	detail, err := s.pollUntilDone(upstreamCtx, client, token, generated.GenerationID)
	if err != nil {
		var failedErr *leonardoGenerationFailedError
		if errors.As(err, &failedErr) {
			return nil, failedErr.err
		}
		return nil, &leonardoResultUnavailableError{generationID: generated.GenerationID, err: err}
	}
	images, err := s.downloadImages(upstreamCtx, client, detail)
	if err != nil {
		return nil, &leonardoResultUnavailableError{generationID: generated.GenerationID, err: err}
	}
	// 上游普通图是 JPEG、透明背景是 PNG；客户端指定 output_format 时本机转码，
	// 未指定时保持上游格式（validateLeonardoImageParams 已拦掉不支持的取值）。
	// 账号出图上限让出图尺寸小于客户尺寸时，本地等比超分回客户尺寸（超分时
	// 一并按 output_format 编码）。
	outputFormat, _ := normalizeImageOutputFormat(req.OutputFormat)
	if plan.Upscaled() {
		images = upscaleImages(images, plan.Target.Width, plan.Target.Height, outputFormat, req.OutputCompression)
	} else {
		images = applyImageOutputFormat(images, outputFormat, req.OutputCompression)
	}
	delivered := leonardoDeliveredSize(images, detail, plan)
	if plan.Upscaled() || plan.Quality != plan.RequestedQuality {
		slog.Info("leonardo_image_limits_applied",
			"account_id", account.ID,
			"requested_size", fmt.Sprintf("%dx%d", plan.Target.Width, plan.Target.Height),
			"source_size", fmt.Sprintf("%dx%d", plan.Source.Width, plan.Source.Height),
			"delivered_size", fmt.Sprintf("%dx%d", delivered.Width, delivered.Height),
			"requested_quality", plan.RequestedQuality,
			"quality", plan.Quality,
			"credits", generated.APICreditCost)
	}

	// 回显字段与 usage 先算好：非流式写进响应体最外层，假流式写进 completed 事件。
	// size 是交付尺寸，quality 回显客户请求的档位（账号上限只影响实际出图）。
	result := &LeonardoImageResult{
		APICreditCost: generated.APICreditCost,
		GenerationID:  generated.GenerationID,
		Size:          fmt.Sprintf("%dx%d", delivered.Width, delivered.Height),
		OutputFormat:  leonardoImageOutputFormat(images),
		Quality:       strings.ToLower(plan.RequestedQuality),
		Background:    leonardoResultBackground(request.Parameters.Transparency),
		Usage: leonardoImageUsage(requestedModel, req.Prompt, generated.APICreditCost, call.UsageTokensPerCredit,
			leonardoUsageScale(plan, delivered)),
	}

	requestID := "leonardo-image:" + uuid.NewString()
	body, err := s.buildResponseBody(upstreamCtx, requestID, images, result)
	if err != nil {
		return nil, &leonardoResultUnavailableError{generationID: generated.GenerationID, err: err}
	}
	result.Body = body
	result.Forward = &OpenAIForwardResult{
		RequestID:        requestID,
		Model:            requestedModel,
		ImageCount:       len(images),
		ImageSize:        leonardoBillingSize(delivered.Width, delivered.Height, &plan.Target),
		UpstreamModel:    upstreamModel,
		UpstreamEndpoint: leonardo.GraphQLURL,
		Duration:         time.Since(startTime),
	}
	// 估算的 usage 同时写进用量记录（/usage 展示；渠道按 token 计价时按它计费）。
	// 与 OpenAI 出图同一套映射：OutputTokens 含图片 token，ImageOutputTokens 单列，
	// 计费时文本输出按 OutputTokens - ImageOutputTokens 计，不会重复。
	if len(result.Usage) > 0 {
		if usage, ok := openAIUsageFromGJSON(gjson.ParseBytes(result.Usage)); ok {
			result.Forward.Usage = usage
		}
	}
	// 本次生成实扣的上游积分（整次生成、含 n 张图）写进用量记录的上游积分列
	// （usage_logs.kiro_credits，与 Kiro 共用），供今日统计按账号积分单价估算成本。
	if generated.APICreditCost > 0 {
		result.Forward.Usage.KiroCredits = float64(generated.APICreditCost)
	}
	return result, nil
}

// leonardoImageUsage 估算 OpenAI 形状的 usage：Leonardo 不报 token 用量，输出
// token 按本次扣除的积分 × tokensPerCredit × scale 估算（积分与 OpenAI 出图 token
// 成正比：gpt-image-1 官方表 LOW 1024x1024 = 272 token、1536x1024 = 408 token，对应
// Leonardo 8 / 12 积分，均为 34 token/积分；scale 把账号上限降下来的尺寸与质量
// 折回客户请求的口径，见 leonardoUsageScale）；提示词按 o200k 分词计数，参考图
// 输入 token 没有公开公式，记 0。tokensPerCredit ≤ 0 或积分未知时返回 nil。
func leonardoImageUsage(model, prompt string, credits int, tokensPerCredit, scale float64) json.RawMessage {
	if tokensPerCredit <= 0 || credits <= 0 {
		return nil
	}
	if scale <= 0 {
		scale = 1
	}
	outputTokens := int(math.Round(float64(credits) * tokensPerCredit * scale))
	return openAIImageUsageJSON(model, prompt, outputTokens)
}

// leonardoUsageScale 把实扣积分折回客户请求口径的倍数：超分时乘交付像素 ÷ 出图
// 像素，质量被账号上限降档时再乘请求档 ÷ 实际档的 token 倍数（gpt-image-1 官方
// 表，见 gptImageQualityTokens）。
func leonardoUsageScale(plan *leonardo.ImagePlan, delivered leonardo.ImageSlot) float64 {
	scale := 1.0
	if plan == nil {
		return scale
	}
	if plan.Upscaled() && plan.Source.Width > 0 && plan.Source.Height > 0 && delivered.Width > 0 && delivered.Height > 0 {
		scale *= float64(delivered.Width*delivered.Height) / float64(plan.Source.Width*plan.Source.Height)
	}
	requested, actual := gptImageQualityTokens(plan.RequestedQuality), gptImageQualityTokens(plan.Quality)
	if requested > 0 && actual > 0 {
		scale *= requested / actual
	}
	return scale
}

// leonardoDeliveredSize 返回交付给客户的实际尺寸：优先读首张最终图（超分失败
// 退回原图时如实变小）；读不到时未超分取上游报告的尺寸，再退回计划的交付尺寸。
func leonardoDeliveredSize(images [][]byte, detail *leonardo.GenerationDetail, plan *leonardo.ImagePlan) leonardo.ImageSlot {
	if len(images) > 0 {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(images[0])); err == nil && cfg.Width > 0 && cfg.Height > 0 {
			return leonardo.ImageSlot{Width: cfg.Width, Height: cfg.Height}
		}
	}
	if !plan.Upscaled() && detail != nil && detail.ImageWidth > 0 && detail.ImageHeight > 0 {
		return leonardo.ImageSlot{Width: detail.ImageWidth, Height: detail.ImageHeight}
	}
	if plan != nil {
		return plan.Target
	}
	return leonardo.ImageSlot{}
}

// leonardoImageOutputFormat 按首张图文件头判定输出格式（Leonardo 普通图是 JPEG，
// 透明背景是 PNG）。
func leonardoImageOutputFormat(images [][]byte) string {
	if len(images) == 0 {
		return ""
	}
	switch http.DetectContentType(images[0]) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpeg"
	case "image/webp":
		return "webp"
	default:
		return ""
	}
}

func leonardoResultBackground(transparent bool) string {
	if transparent {
		return "transparent"
	}
	return "opaque"
}

// buildUpstreamRequest 解析 size/quality 并构造 Generate 请求；limits 是账号出图
// 上限，返回的 plan 记录交付尺寸、出图尺寸与请求/实际质量档。
func (s *LeonardoImageService) buildUpstreamRequest(
	upstreamModel string, req *OpenAIImagesRequest, limits leonardo.ImageLimits,
) (*leonardo.GenerateRequest, *leonardo.ImagePlan, error) {
	return leonardo.BuildImagePlanRequest(leonardo.ImageOptions{
		UpstreamModel: upstreamModel,
		Prompt:        req.Prompt,
		Quality:       req.Quality,
		Size:          req.Size,
		Quantity:      req.N,
		// gpt-image-2.5 系列额外支持 XHIGH / MAX 质量档。
		ExtendedQuality: domain.LeonardoSupportsExtendedQuality(upstreamModel),
		// 只在客户端明确要透明背景时才开：默认开会把所有图都变成透明 PNG。
		Transparent: leonardoWantsTransparentBackground(req),
		Limits:      limits,
	})
}

// uploadReferenceImages 把参考图（multipart 字节 + JSON URL）上传并过审，
// 返回可直接放入 guidances 的 initImageId。文生图时返回 nil。
// OpenAI mask 不上传：原生协议无 inpaint 语义，真 mask 由 handler 转中转。
func (s *LeonardoImageService) uploadReferenceImages(
	ctx context.Context, client *leonardo.Client, token string, call *LeonardoImageCall,
) ([]string, error) {
	req := call.Request
	if len(req.Uploads) == 0 && len(req.InputImageURLs) == 0 {
		return nil, nil
	}
	inputs := make([]*inputImage, 0, len(req.Uploads)+len(req.InputImageURLs))
	for _, upload := range req.Uploads {
		if len(upload.Data) == 0 {
			continue
		}
		inputs = append(inputs, &inputImage{Data: upload.Data, ContentType: upload.ContentType})
	}
	// JSON 体里的 URL 需要先取回字节；同一请求换号时复用已取回的字节。
	urlImages, err := call.inputImages(ctx, s.inputClient)
	if err != nil {
		return nil, err
	}
	inputs = append(inputs, urlImages...)

	ids := make([]string, 0, len(inputs))
	for _, input := range inputs {
		id, err := s.uploadOneImage(ctx, client, token, input)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) > 6 {
		return nil, leonardo.NewRequestError("leonardo image models accept at most 6 image references")
	}
	return ids, nil
}

// uploadOneImage 执行三步上传：UploadImage(INIT) → S3 presigned POST →
// 审核轮询至 Accepted 得 initImageId。
func (s *LeonardoImageService) uploadOneImage(ctx context.Context, client *leonardo.Client, token string, input *inputImage) (string, error) {
	extension := leonardo.UploadExtensionForContentType(input.ContentType)
	init, err := client.UploadInitImage(ctx, token, extension)
	if err != nil {
		return "", err
	}
	if err := client.PostToPresignedURL(ctx, init.URL, init.Fields, "upload."+extension, input.ContentType, input.Data); err != nil {
		return "", err
	}
	return client.WaitForInitImage(ctx, token, init.UploadID, 120*time.Second)
}

// leonardoWantsTransparentBackground 报告客户端是否明确要求透明背景
// （OpenAI Images 的 background=transparent；opaque / auto / 空都不开）。
func leonardoWantsTransparentBackground(req *OpenAIImagesRequest) bool {
	return req != nil && strings.EqualFold(strings.TrimSpace(req.Background), "transparent")
}

// leonardoGuidancesFor 把 initImageId 列表构造成参考图 guidances。
func leonardoGuidancesFor(ids []string) *leonardo.GenerateGuidances {
	if len(ids) == 0 {
		return nil
	}
	guidances := &leonardo.GenerateGuidances{}
	for _, id := range ids {
		guidance := leonardo.ImageReferenceGuidance{Strength: leonardo.GuidanceStrengthMID}
		guidance.Image.ID = id
		guidance.Image.Type = leonardo.GuidanceUploadedType
		guidances.ImageReference = append(guidances.ImageReference, guidance)
	}
	return guidances
}

// pollUntilDone 轮询生成状态直到 COMPLETE / FAILED。
//
// 积分在提交时已扣，这里的失败不能触发换号重提：瞬时错误（5xx / 429 / 网络）
// 原地退避重试直到 ctx 截止（与 leo2api / leo_proxy 的轮询容错一致）；COMPLETE
// 但产物尚未落表时短暂等待。只有上游明确 FAILED 才返回
// leonardoGenerationFailedError（失败生成积分退回，调用方可换号）。
func (s *LeonardoImageService) pollUntilDone(
	ctx context.Context, client *leonardo.Client, token, generationID string,
) (*leonardo.GenerationDetail, error) {
	delay := leonardoPollInterval
	var completedAt time.Time
	var lastErr error
	for {
		detail, done, err := s.pollOnce(ctx, client, token, generationID, &completedAt)
		if done {
			return detail, err
		}
		if err != nil {
			if !isLeonardoTransientError(err) {
				return nil, err
			}
			lastErr = err
			delay = min(delay*2, leonardoPollMaxBackoff)
		} else {
			lastErr = nil
			delay = leonardoPollInterval
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return nil, fmt.Errorf("%w (last poll error: %v)", ctx.Err(), lastErr)
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// pollOnce 执行一轮状态查询。done=true 时 detail/err 即最终结果；done=false
// 时 err 非空表示本轮查询失败（由调用方判断能否重试），为空表示仍在生成。
func (s *LeonardoImageService) pollOnce(
	ctx context.Context, client *leonardo.Client, token, generationID string, completedAt *time.Time,
) (*leonardo.GenerationDetail, bool, error) {
	status, err := client.PollGenerationStatus(ctx, token, generationID)
	if err != nil {
		return nil, false, err
	}
	switch {
	case leonardo.IsFailedStatus(status.Status):
		return nil, true, &leonardoGenerationFailedError{err: s.generationFailure(ctx, client, token, generationID)}
	case leonardo.IsCompleteStatus(status.Status):
		detail, err := client.GetGenerationDetail(ctx, token, generationID)
		if err != nil {
			return nil, false, err
		}
		if leonardoDetailHasImages(detail) {
			return detail, true, nil
		}
		if completedAt.IsZero() {
			*completedAt = time.Now()
		}
		if time.Since(*completedAt) >= leonardoCompleteWithoutImagesGrace {
			return nil, true, leonardo.NewUpstreamTemporaryError("leonardo generation completed without images", 0)
		}
	}
	return nil, false, nil
}

// isLeonardoTransientError 报告错误是否值得用同一 JWT 原地重试：上游 5xx、
// 429 与网络层错误。鉴权、封号、GraphQL 校验类错误重试无益。
func isLeonardoTransientError(err error) bool {
	var temporaryErr *leonardo.UpstreamTemporaryError
	if errors.As(err, &temporaryErr) {
		return true
	}
	var rateLimitedErr *leonardo.RateLimitedError
	if errors.As(err, &rateLimitedErr) {
		return true
	}
	var requestErr *leonardo.RequestError
	return errors.As(err, &requestErr) && requestErr.IsNetwork()
}

func leonardoDetailHasImages(detail *leonardo.GenerationDetail) bool {
	if detail == nil {
		return false
	}
	for _, image := range detail.Images {
		if strings.TrimSpace(image.URL) != "" {
			return true
		}
	}
	return false
}

// generationFailure 把 FAILED 归因为类型化错误（优先上游 errorCode）。
func (s *LeonardoImageService) generationFailure(
	ctx context.Context, client *leonardo.Client, token, generationID string,
) error {
	if failure, err := client.FailureReason(ctx, token, generationID); err == nil && failure != nil {
		return leonardo.ClassifyFailureErrorCode(failure)
	}
	return leonardo.NewUpstreamTemporaryError("leonardo generation failed without a reason", 0)
}

// downloadImages 下载产物字节。Leonardo CDN URL 有过期时间，必须立即下载。
func (s *LeonardoImageService) downloadImages(
	ctx context.Context, client *leonardo.Client, detail *leonardo.GenerationDetail,
) ([][]byte, error) {
	if !leonardoDetailHasImages(detail) {
		return nil, leonardo.NewUpstreamTemporaryError("leonardo generation completed without images", 0)
	}
	images := make([][]byte, 0, len(detail.Images))
	for _, item := range detail.Images {
		imageURL := strings.TrimSpace(item.URL)
		if imageURL == "" {
			continue
		}
		data, err := downloadLeonardoImageWithRetry(ctx, client, imageURL)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return nil, leonardo.NewUpstreamTemporaryError("leonardo returned an empty image", 0)
		}
		images = append(images, data)
	}
	return images, nil
}

// downloadLeonardoImageWithRetry 对瞬时失败原地重试下载（积分已扣，不能靠换号兜底）。
func downloadLeonardoImageWithRetry(ctx context.Context, client *leonardo.Client, imageURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < leonardoDownloadAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * leonardoPollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("%w (last download error: %v)", ctx.Err(), lastErr)
			case <-timer.C:
			}
		}
		data, err := client.DownloadImage(ctx, imageURL)
		if err == nil {
			return data, nil
		}
		if !isLeonardoTransientError(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// buildResponseBody 构造 OpenAI 形状的响应：先 b64_json 组装，对象存储可用
// 时整体过 ImageResultUploader.Rewrite 换成 url（转存失败回落 b64，不丢产物）。
func (s *LeonardoImageService) buildResponseBody(ctx context.Context, requestID string, images [][]byte, meta *LeonardoImageResult) ([]byte, error) {
	if len(images) == 0 {
		return nil, leonardo.NewRequestError("leonardo returned an empty image")
	}
	data := make([]any, 0, len(images))
	for _, image := range images {
		if len(image) == 0 {
			return nil, leonardo.NewRequestError("leonardo returned an empty image")
		}
		data = append(data, map[string]any{"b64_json": base64.StdEncoding.EncodeToString(image)})
	}
	top := map[string]any{
		"created": time.Now().Unix(),
		"data":    data,
	}
	// 与 OpenAI gpt-image 响应一致，最外层回显实际出图参数与 usage。
	if meta != nil {
		for key, value := range map[string]string{
			"background":    meta.Background,
			"output_format": meta.OutputFormat,
			"quality":       meta.Quality,
			"size":          meta.Size,
		} {
			if value != "" {
				top[key] = value
			}
		}
		if len(meta.Usage) > 0 {
			top["usage"] = meta.Usage
		}
	}
	body, err := json.Marshal(top)
	if err != nil {
		return nil, leonardo.NewRequestError(fmt.Sprintf("encode images response: %v", err))
	}
	if s.resolveStorage == nil {
		return body, nil
	}
	uploader, enabled := s.resolveStorage()
	if !enabled || uploader == nil {
		return body, nil
	}
	rewritten, err := uploader.Rewrite(ctx, requestID, body)
	if err != nil {
		// 转存失败不该让已经生成好（且已扣上游积分）的图丢掉，回落 b64_json。
		return body, nil //nolint:nilerr // 有意吞掉：产物已生成，降级返回优于整体失败
	}
	return rewritten, nil
}

// leonardoBillingSize 决定计费档位：按交付给客户的尺寸（超分后的尺寸），缺失时
// 退回计划的交付尺寸。
func leonardoBillingSize(width, height int, slot *leonardo.ImageSlot) string {
	if width > 0 && height > 0 {
		if tier, ok := ClassifyImageBillingTier(fmt.Sprintf("%dx%d", width, height)); ok {
			return tier
		}
	}
	if slot != nil {
		if tier, ok := ClassifyImageBillingTier(fmt.Sprintf("%dx%d", slot.Width, slot.Height)); ok {
			return tier
		}
	}
	return NormalizeImageBillingTierOrDefault("")
}

// leonardoSubmitIndeterminateError 标记「提交结果不明」：上游可能已受理并
// 扣积分，调用方不得重试或换号。
type leonardoSubmitIndeterminateError struct{ err error }

func (e *leonardoSubmitIndeterminateError) Error() string {
	return "leonardo generation submit result is unknown: " + e.err.Error()
}

func (e *leonardoSubmitIndeterminateError) Unwrap() error { return e.err }

// leonardoGenerationFailedError 标记上游明确判定 FAILED 的生成（积分退回），
// 只在 pollUntilDone 内部传递；GenerateCall 解包后按内层错误分类。
type leonardoGenerationFailedError struct{ err error }

func (e *leonardoGenerationFailedError) Error() string { return e.err.Error() }

func (e *leonardoGenerationFailedError) Unwrap() error { return e.err }

// leonardoResultUnavailableError 标记「已提交成功但取不回结果」：轮询、取详情
// 或下载失败。上游可能仍在出图并已扣积分，调用方不得换号重提。
type leonardoResultUnavailableError struct {
	generationID string
	err          error
}

func (e *leonardoResultUnavailableError) Error() string {
	return fmt.Sprintf("leonardo generation %s was submitted but its result could not be retrieved: %v", e.generationID, e.err)
}

func (e *leonardoResultUnavailableError) Unwrap() error { return e.err }
