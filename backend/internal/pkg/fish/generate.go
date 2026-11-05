package fish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
)

// Variant 取值：文生图 / 参考生图。
const (
	VariantTextToImage = "t2i"
	VariantEdit        = "edit"
)

// RefAsset 是 generate 请求 params.reference_assets 的一项（参考图，
// 上传后以 objectKey 引用）。
type RefAsset struct {
	ObjectKey string `json:"objectKey"`
	Type      string `json:"type"`
}

// PriceParams 是 /tools/image-video/price 的参数（variant + params 子集）。
type PriceParams struct {
	Model     string
	Variant   string
	ImageSize string
	Quality   string // 可空：仅 gpt 家族参与计价
	Count     int
	// RefImageCount 是参考图数量（edit 计价用）。
	RefImageCount int
}

// PriceResult 是计价响应。
type PriceResult struct {
	Credits        int    `json:"credits"`
	PerItemCredits int    `json:"perItemCredits"`
	PricingVersion string `json:"pricingVersion"`
}

func (p PriceParams) toBody() map[string]any {
	params := map[string]any{
		"prompt":     "",
		"image_size": p.ImageSize,
	}
	if strings.TrimSpace(p.Quality) != "" {
		params["quality"] = p.Quality
	}
	return map[string]any{
		"modelId":       p.Model,
		"variantKey":    p.Variant,
		"mode":          "image",
		"count":         p.Count,
		"params":        params,
		"refImageCount": p.RefImageCount,
	}
}

// Price 查询一次生成的计价。价格表对参数组合做精确匹配：非法组合返回
// InvalidCombinationError（matches=0），因此它同时是组合合法性校验器。
func (c *Client) Price(ctx context.Context, token string, identity Identity, params PriceParams) (*PriceResult, error) {
	var result PriceResult
	if err := c.doJSON(ctx, http.MethodPost, "/tools/image-video/price", token, identity, params.toBody(), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GenerateRequest 是 SubmitGeneration 的入参。ExpectedCredits 与
// PricingVersion 必须来自紧邻的 Price 调用（上游校验一致性）。
type GenerateRequest struct {
	Model     string
	Variant   string
	Prompt    string
	Count     int
	ImageSize string
	Quality   string // 可空：仅 gpt 家族发送
	// ExtendedQuality 为 true（gpt-image-2.5 系列）时 quality 同时回显到
	// frontendParams；gpt-image-2 没有这样的抓包样本，保持原请求形状。
	ExtendedQuality bool
	Locale          string // 可空默认 en
	RefAssets       []RefAsset
	ExpectedCredits int
	PricingVersion  string
}

// TaskResult 是任务组里单个产物的状态。
type TaskResult struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	MediaItem     *MediaItem `json:"mediaItem"`
	FailureReason *string    `json:"failureReason"`
	ErrorMessage  *string    `json:"errorMessage"`
	FinishedAt    *string    `json:"finishedAt"`
	CreditCount   *int       `json:"creditCount"`
}

// TaskGroup 是 generate 返回/轮询的任务组。
type TaskGroup struct {
	ID      string       `json:"id"`
	Status  string       `json:"status"` // generating / done / failed
	Results []TaskResult `json:"results"`
}

// MediaItem 是产物元数据。
type MediaItem struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnailUrl"`
	ObjectKey    string `json:"objectKey"`
}

// SubmitGeneration 提交生成任务。clientRequestId 由本方法生成（uuid4），
// 上游按其幂等。⚠️ 网络层错误意味着提交结果不确定（可能已扣费），
// 调用方绝不能换账号重提，只能放弃或继续轮询。
func (c *Client) SubmitGeneration(ctx context.Context, token string, identity Identity, request GenerateRequest) (*TaskGroup, error) {
	locale := strings.TrimSpace(request.Locale)
	if locale == "" {
		locale = "en"
	}
	params := map[string]any{
		"prompt":     request.Prompt,
		"image_size": request.ImageSize,
	}
	if strings.TrimSpace(request.Quality) != "" {
		params["quality"] = request.Quality
	}
	if len(request.RefAssets) > 0 {
		params["reference_assets"] = request.RefAssets
	}
	body := map[string]any{
		"clientRequestId": newRequestID(),
		"mode":            "image",
		"prompt":          request.Prompt,
		"locale":          locale,
		"modelId":         request.Model,
		"variantKey":      request.Variant,
		"count":           request.Count,
		"promptEnhance":   false,
		"params":          params,
		"frontendParams":  fishFrontendParams(request),
		"expectedCredits": request.ExpectedCredits,
		"pricingVersion":  request.PricingVersion,
	}
	if len(request.RefAssets) > 0 {
		body["refImages"] = refImagesEcho(request.RefAssets)
	}
	var group TaskGroup
	if err := c.doJSON(ctx, http.MethodPost, "/tools/image-video/generate", token, identity, body, &group); err != nil {
		return nil, err
	}
	return &group, nil
}

// fishFrontendParams 是网页端回显用的参数。2.5 抓包里 quality 同时出现在
// params 和 frontendParams（xhigh / max 为小写）；其它模型不带。
func fishFrontendParams(request GenerateRequest) map[string]any {
	params := map[string]any{
		"modelId":           request.Model,
		"count":             request.Count,
		"promptEnhance":     false,
		"generationPurpose": "general",
	}
	if quality := strings.TrimSpace(request.Quality); quality != "" && request.ExtendedQuality {
		params["quality"] = quality
	}
	return params
}

func refImagesEcho(assets []RefAsset) []map[string]any {
	echo := make([]map[string]any, 0, len(assets))
	for _, asset := range assets {
		echo = append(echo, map[string]any{
			"id":        newRequestID(),
			"role":      "ref",
			"status":    "ready",
			"objectKey": asset.ObjectKey,
		})
	}
	return echo
}

// PollTaskGroup 轮询任务组状态。
func (c *Client) PollTaskGroup(ctx context.Context, token string, identity Identity, groupID string) (*TaskGroup, error) {
	var group TaskGroup
	if err := c.doJSON(ctx, http.MethodGet, "/task/group/"+pathEscape(groupID), token, identity, nil, &group); err != nil {
		return nil, err
	}
	return &group, nil
}

// FetchMedia 取单个产物元数据（含下载 URL）。
func (c *Client) FetchMedia(ctx context.Context, token string, identity Identity, mediaID string) (*MediaItem, error) {
	var item MediaItem
	if err := c.doJSON(ctx, http.MethodGet, "/tools/image-video/media/"+pathEscape(mediaID), token, identity, nil, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// UserSelf 是 /user/self 响应的可用子集。
type UserSelf struct {
	ID       string `json:"_id"`
	Email    string `json:"email"`
	Nickname string `json:"nickname"`
	Banned   bool   `json:"banned"`
}

// UserSelf 拉取当前账号档案（用于连通性测试与 team/device 回填）。
func (c *Client) UserSelf(ctx context.Context, token string, identity Identity) (*UserSelf, error) {
	var self UserSelf
	if err := c.doJSON(ctx, http.MethodGet, "/user/self", token, identity, nil, &self); err != nil {
		return nil, err
	}
	return &self, nil
}

// Wallet 是 /wallet/<team_id>/package 响应的可用子集。
type Wallet struct {
	Total   int    `json:"total"`
	Balance int    `json:"balance"`
	Type    string `json:"type"`
}

// QueryWallet 查询钱包余额。teamID 为空时先经 UserSelf 解析。
func (c *Client) QueryWallet(ctx context.Context, token string, identity Identity, teamID string) (*Wallet, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		self, err := c.UserSelf(ctx, token, identity)
		if err != nil {
			return nil, err
		}
		teamID = self.ID
	}
	var wallet Wallet
	if err := c.doJSON(ctx, http.MethodGet, "/wallet/"+pathEscape(teamID)+"/package", token, identity, nil, &wallet); err != nil {
		return nil, err
	}
	return &wallet, nil
}

// uploadURLRequest / uploadURLResponse 是参考图上传的第一步。
type uploadURLRequest struct {
	Purpose     string `json:"purpose"`
	FileName    string `json:"file_name"`
	FileSize    int64  `json:"file_size"`
	ContentType string `json:"content_type"`
}

type uploadURLResponse struct {
	ObjectKey string `json:"object_key"`
	UploadURL string `json:"upload_url"`
}

type inspectResponse struct {
	ObjectKey string `json:"objectKey"`
	URL       string `json:"url"`
	Kind      string `json:"kind"`
	SizeBytes int64  `json:"sizeBytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// UploadRefImage 完成参考图上传三步：取 R2 预签名 URL → PUT 二进制 →
// inspect 校验。返回 generate(edit) 用的 RefAsset。
func (c *Client) UploadRefImage(ctx context.Context, token string, identity Identity, filename, contentType string, data []byte) (*RefAsset, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = "image.png"
	}
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		contentType = mime.TypeByExtension(strings.ToLower(path.Ext(filename)))
		if contentType == "" {
			contentType = "image/png"
		}
	}
	var grant uploadURLResponse
	if err := c.doJSON(ctx, http.MethodPost, "/task/upload-url", token, identity, uploadURLRequest{
		Purpose:     "media_ref_image",
		FileName:    filename,
		FileSize:    int64(len(data)),
		ContentType: contentType,
	}, &grant); err != nil {
		return nil, err
	}
	if grant.UploadURL == "" || grant.ObjectKey == "" {
		return nil, NewRequestError("fish upload-url response missing fields")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, grant.UploadURL, bytes.NewReader(data))
	if err != nil {
		return nil, &RequestError{Message: fmt.Sprintf("create upload request: %v", err), ErrorType: ErrorTypeNetwork}
	}
	req.ContentLength = int64(len(data))
	req.Close = true
	req.Header.Set("Content-Type", contentType)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, wrapNetworkError("upload fish ref image", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &RequestError{Message: fmt.Sprintf("upload fish ref image: status %d", resp.StatusCode), ErrorType: ErrorTypeStatus, StatusCode: resp.StatusCode}
	}

	var inspected inspectResponse
	if err := c.doJSON(ctx, http.MethodPost, "/tools/image-video/media/inspect", token, identity, map[string]any{
		"objectKey": grant.ObjectKey,
	}, &inspected); err != nil {
		return nil, err
	}
	if inspected.Width <= 0 || inspected.Height <= 0 {
		return nil, NewRequestError("fish ref image inspect returned no dimensions")
	}
	return &RefAsset{ObjectKey: grant.ObjectKey, Type: "image"}, nil
}

// DailyImageQuota 是每日出图窗口。limit/used 是窗口进度，resetAt 是下一次重置
// （免费档为 UTC 零点）。钱包余额是另一套长期积分，不在这个窗口里。
type DailyImageQuota struct {
	Limit     int    `json:"limit"`
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
	ResetAt   string `json:"resetAt"`
}

// Entitlements 是配额信息（套餐、单次张数、每日出图窗口）。
type Entitlements struct {
	Plan                   string           `json:"plan"`
	MaxImagesPerGeneration int              `json:"maxImagesPerGeneration"`
	VideoAllowed           bool             `json:"videoAllowed"`
	DailyImage             *DailyImageQuota `json:"dailyImage"`
}

// QueryEntitlements 拉取账号生图配额。
func (c *Client) QueryEntitlements(ctx context.Context, token string, identity Identity) (*Entitlements, error) {
	raw, err := c.queryRaw(ctx, token, identity, "/tools/image-video/entitlements")
	if err != nil {
		return nil, err
	}
	var entitlements Entitlements
	if err := json.Unmarshal(raw, &entitlements); err != nil {
		return nil, &RequestError{Message: fmt.Sprintf("decode entitlements: %v", err), ErrorType: ErrorTypeStatus}
	}
	return &entitlements, nil
}

// queryRaw 是不需要类型化响应的裸 GET。
func (c *Client) queryRaw(ctx context.Context, token string, identity Identity, apiPath string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+apiPath, nil)
	if err != nil {
		return nil, &RequestError{Message: fmt.Sprintf("create request: %v", err), ErrorType: ErrorTypeNetwork}
	}
	req.Close = true
	c.applyBrowserHeaders(req, token, identity)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, wrapNetworkError("fish request failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, wrapNetworkError("read fish response", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope apiErrorEnvelope
		_ = json.Unmarshal(raw, &envelope)
		return nil, withErrorCode(classifyAPIError(resp.StatusCode, envelope.Message), strings.TrimSpace(envelope.Code))
	}
	return raw, nil
}
