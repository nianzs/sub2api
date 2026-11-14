package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// FishModel 是 fish.audio 平台的一个对外模型。Name 同时是上游 modelId
// （fish 上游模型名即对外名）。gpt-image 家族走固定像素白名单
// （FishPixelSizes）。质量档 gpt-image-2 是 low/medium/high；两个 2.5
// 模型额外支持 xhigh / max，max 是最高档（2026-10-22 抓包）。
type FishModel struct {
	Name string
	// ExtendedQuality 为 true 时额外支持 xhigh / max。
	ExtendedQuality bool
}

// FishModels 是 fish.audio 平台暴露的生图模型（展示顺序）。
// nano-banana / gemini / seedream 系已按需求移除（2026-10）；
// fish 的 /models 端点是活的，账号连通性测试可顺带校验漂移。
var FishModels = []FishModel{
	{Name: "gpt-image-2"},
	{Name: "gpt-image-2.5-flare", ExtendedQuality: true},
	{Name: "gpt-image-2.5-sunburst", ExtendedQuality: true},
}

// DefaultFishModelMapping 是 fish 账号的默认模型映射：对外名恒等映射。
var DefaultFishModelMapping = func() map[string]string {
	mapping := make(map[string]string, len(FishModels))
	for _, model := range FishModels {
		mapping[model.Name] = model.Name
	}
	return mapping
}()

// FishPixelSizes 是 gpt-image 家族（gpt-image-2 / 2.5-flare / 2.5-sunburst）
// 的全部合法像素值（三家共享同一矩阵，2026-10-20 price Oracle 实测；
// 矩阵不对称：21:9 只有横版，4K 只有 1:1）。白名单外的尺寸优先由
// FishPlanSize 就近规划（cover 裁剪缩放，受 FishFitLimits 约束），
// 规划不出再由 handler 路由到 OpenAI 兼容中转账号。
var FishPixelSizes = []string{
	// 1K 档
	"1024x1024", "768x1024", "1024x768", "608x1088", "1088x608", "1344x576",
	// 2K 档
	"2048x2048", "1536x2048", "2048x1536", "1152x2048", "2048x1152",
	// 4K 档
	"2880x2880",
}

// gpt-image 的交付尺寸上限（OpenAI gpt-image-2 图片生成指南：长边 ≤ 3840，
// 总像素 ≤ 8,294,400）。上游没有查询接口，只能写死。超限的 size 不走原生号
// （也就不会本地放大到超大图），由 FishRequiresRelay 交给中转号，上游自行判定。
const (
	GPTImageMaxEdge   = 3840
	GPTImageMaxPixels = 8_294_400
)

// ── 动态尺寸规划（cover 就近档 + 居中裁剪缩放） ──

// FishFitLimits 是原生号动态尺寸规划的两个全局阈值（系统设置-网关）。
// MaxCropPixels 按交付分辨率计：0 = 不裁剪仅同比例缩放；负值 = 未配置按默认。
// MaxUpscaleFactor ≤0 = 未配置按默认；1 表示禁止放大。
type FishFitLimits struct {
	MaxCropPixels    int
	MaxUpscaleFactor float64
}

const (
	FishFitDefaultMaxCropPixels    = 4
	FishFitDefaultMaxUpscaleFactor = 2.0
	FishFitMaxCropPixelsHardLimit  = 64
	FishFitMaxUpscaleHardLimit     = 3.0
)

// DefaultFishFitLimits 是系统设置的缺省阈值（裁 4px / 放大 2 倍）。
func DefaultFishFitLimits() FishFitLimits {
	return FishFitLimits{
		MaxCropPixels:    FishFitDefaultMaxCropPixels,
		MaxUpscaleFactor: FishFitDefaultMaxUpscaleFactor,
	}
}

// Normalized 把越界值钳回合法区间。零值结构体 = {0, 2.0}（不裁剪、允许
// 2 倍放大）——生产路径的值恒来自设置解析层（缺省 4 / 2.0）。
func (l FishFitLimits) Normalized() FishFitLimits {
	crop := l.MaxCropPixels
	if crop < 0 {
		crop = FishFitDefaultMaxCropPixels
	}
	if crop > FishFitMaxCropPixelsHardLimit {
		crop = FishFitMaxCropPixelsHardLimit
	}
	factor := l.MaxUpscaleFactor
	if factor <= 0 || factor != factor { // 含 NaN
		factor = FishFitDefaultMaxUpscaleFactor
	}
	if factor < 1 {
		factor = 1
	}
	if factor > FishFitMaxUpscaleHardLimit {
		factor = FishFitMaxUpscaleHardLimit
	}
	return FishFitLimits{MaxCropPixels: crop, MaxUpscaleFactor: factor}
}

// 裁剪轴。
const (
	FishCropNone   = 0
	FishCropWidth  = 1
	FishCropHeight = 2
)

// FishSizePlan 是一次请求的尺寸决策：在原生白名单里挑一个 cover 档，
// 出图后本地裁剪缩放回客户请求的尺寸。
type FishSizePlan struct {
	// TargetWidth/Height 是客户请求的交付尺寸；TargetSize 是回显串（归一化）。
	TargetWidth  int
	TargetHeight int
	TargetSize   string
	// Native 是发往上游的白名单 image_size，NativeWidth/Height 为其像素。
	Native       string
	NativeWidth  int
	NativeHeight int
	// ScaleFactor 是 cover 倍数：≤1 缩小（先裁后缩）、>1 放大（先缩后裁）。
	ScaleFactor float64
	// CropAxis 是溢出轴（FishCropWidth/Height/None）；CropPixels 是交付分辨率
	// 下的总裁量（居中对称均分，执行时奇数像素两侧差 1）。
	CropAxis   int
	CropPixels int
}

// NeedsTransform 报告出图后是否需要本地裁剪/缩放（原生档即目标时零变换）。
func (p *FishSizePlan) NeedsTransform() bool {
	return p != nil && (p.TargetWidth != p.NativeWidth || p.TargetHeight != p.NativeHeight)
}

// FishPlanSize 为请求 size 在原生白名单里挑一个 cover 方案：
//
// 对每个白名单档算 cover 倍数 f = max(w/nw, h/nh) 与溢出（仅一轴溢出），
// 过滤「交付裁量 ≤ MaxCropPixels 且 f ≤ MaxUpscaleFactor」，择优顺序为
// 裁剪最小 → 缩小优于放大 → f 最接近 1。空 size 用默认 1024x1024。
// 不可解析、超出 gpt-image 尺寸上限或无候选返回错误——调用方
// （FishRequiresRelay）据此路由中转。
func FishPlanSize(size string, limits FishFitLimits) (*FishSizePlan, error) {
	limits = limits.Normalized()
	w, h, err := parseFishPixelSize(size)
	if err != nil {
		return nil, err
	}
	if max(w, h) > GPTImageMaxEdge || w*h > GPTImageMaxPixels {
		return nil, fmt.Errorf(
			"image size %dx%d exceeds gpt-image limits (long edge <= %d, total pixels <= %d)",
			w, h, GPTImageMaxEdge, GPTImageMaxPixels,
		)
	}
	var best *FishSizePlan
	bestOverflow, bestFactor := 0.0, 0.0
	for _, native := range FishPixelSizes {
		nw, nh, parseErr := parseFishPixelSize(native)
		if parseErr != nil {
			continue // 白名单恒合法，防御性跳过
		}
		f := math.Max(float64(w)/float64(nw), float64(h)/float64(nh))
		if f > limits.MaxUpscaleFactor+FishFitEpsilon {
			continue
		}
		axis, overflow := fishCoverOverflow(w, h, nw, nh, f)
		if float64(limits.MaxCropPixels) < overflow-FishFitEpsilon {
			continue
		}
		if !betterFishFactor(overflow, f, bestOverflow, bestFactor) {
			continue
		}
		best = &FishSizePlan{
			TargetWidth: w, TargetHeight: h, TargetSize: fmt.Sprintf("%dx%d", w, h),
			Native: native, NativeWidth: nw, NativeHeight: nh,
			ScaleFactor: f, CropAxis: axis, CropPixels: int(math.Round(overflow)),
		}
		bestOverflow, bestFactor = overflow, f
	}
	if best == nil {
		return nil, fmt.Errorf(
			"no fish native size can cover %q within fit limits (max crop %dpx, max upscale %.2f); allowed native: %s",
			strings.TrimSpace(size), limits.MaxCropPixels, limits.MaxUpscaleFactor, strings.Join(FishPixelSizes, ", "),
		)
	}
	return best, nil
}

// parseFishPixelSize 解析 "WxH"（trim/lower；空串用默认 1024x1024）。
// "auto" 等非像素值返回错误——保持既有中转路由。
func parseFishPixelSize(size string) (int, int, error) {
	normalized := strings.ToLower(strings.TrimSpace(size))
	if normalized == "" {
		return 1024, 1024, nil
	}
	widthRaw, heightRaw, ok := strings.Cut(normalized, "x")
	if !ok {
		return 0, 0, fmt.Errorf("invalid image size %q", size)
	}
	width, err := strconv.Atoi(widthRaw)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid image size %q", size)
	}
	height, err := strconv.Atoi(heightRaw)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid image size %q", size)
	}
	if width < 1 || height < 1 {
		return 0, 0, fmt.Errorf("invalid image size %q", size)
	}
	return width, height, nil
}

// FishFitEpsilon 吸收 f*nw 与 w 的浮点尾差（如 880/1536*1536 < 880）。
// service 层执行裁剪缩放时共用。
const FishFitEpsilon = 1e-6

// fishCoverOverflow 返回 cover 缩放的溢出轴与溢出量（目标分辨率）。
// 比例精确匹配时两轴溢出均为 0。
func fishCoverOverflow(w, h, nw, nh int, f float64) (axis int, overflow float64) {
	dw := f*float64(nw) - float64(w)
	dh := f*float64(nh) - float64(h)
	if dw > FishFitEpsilon {
		return FishCropWidth, dw
	}
	if dh > FishFitEpsilon {
		return FishCropHeight, dh
	}
	return FishCropNone, 0
}

// betterFishFactor 按「裁剪最小 → 缩小优于放大 → f 最接近 1」判定候选
// (overflow, f) 是否优于当前最优 (bestOverflow, bestFactor)。
func betterFishFactor(overflow, f, bestOverflow, bestFactor float64) bool {
	if bestFactor == 0 {
		return true // 尚无候选
	}
	if overflow < bestOverflow-FishFitEpsilon {
		return true
	}
	if overflow > bestOverflow+FishFitEpsilon {
		return false
	}
	candidateDownscale, bestDownscale := f <= 1, bestFactor <= 1
	if candidateDownscale != bestDownscale {
		return candidateDownscale
	}
	return math.Abs(f-1) < math.Abs(bestFactor-1)-FishFitEpsilon
}

// LookupFishModel 按模型名查表（大小写不敏感）。
func LookupFishModel(model string) (FishModel, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return FishModel{}, false
	}
	for _, candidate := range FishModels {
		if strings.EqualFold(candidate.Name, model) {
			return candidate, true
		}
	}
	return FishModel{}, false
}

// IsFishPublicModel 报告 model 是否为 fish 平台的对外模型名。
func IsFishPublicModel(model string) bool {
	_, ok := LookupFishModel(model)
	return ok
}

// FishModelNames 返回对外名列表（展示顺序）。
func FishModelNames() []string {
	names := make([]string, 0, len(FishModels))
	for _, model := range FishModels {
		names = append(names, model.Name)
	}
	return names
}
