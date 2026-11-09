package kiro

import (
	"math"
	"os"
	"strings"
)

// KiroCreditRates 是 Kiro 按 token 扣 credits 的单价（credits/token）。
//
// Kiro 只下发每个请求的 credits（meteringEvent）和本轮结束后的上下文总量
// （contextUsageEvent，= 提示词 + 完整输出，含未下发的完整思考），不下发
// input/output 明细。credits 对三类 token 线性计费：
//
//	credits = Cold·(提示词中未命中缓存的部分) + Cached·(命中缓存的部分) + Output·(完整输出)
//
// 因此已知命中量时可以从 credits 精确反推完整输出，见 estimateKiroOutputFromCredits。
type KiroCreditRates struct {
	Cold   float64 // 未命中 Kiro 提示词缓存的提示词 token
	Cached float64 // 命中 Kiro 提示词缓存的提示词 token
	Output float64 // 输出 token（含未下发的完整思考）
}

// kiroCreditRateTable 是实测单价（2026-11-09）。测法：唯一前缀的冷请求得 Cold，
// 60 秒内原样重发得 Cached，追加一轮长回答得 Output；max_tokens 要足够大，避免报出的
// output 被截断导致 usage 总和小于 Kiro 总量。
// 命中/输出单价按 Kiro 官方倍率成比例，但 5.5 代的未命中单价比倍率推算高约 1.8–1.9 倍，
// 所以只能按模型实测，不能用倍率推导。Kiro 调价后需要重测（未命中/输出被否决的
// warn 日志变多就是信号）。未列出的模型不用 credits 反推，沿用旧算法。
var kiroCreditRateTable = map[string]KiroCreditRates{
	"claude-opus-5.5":   {Cold: 12.28e-6, Cached: 3.325e-6, Output: 2.2107e-4},
	"claude-opus-5":     {Cold: 6.965e-6, Cached: 3.684e-6, Output: 2.45e-4},
	"claude-opus-4.8":   {Cold: 6.965e-6, Cached: 3.685e-6, Output: 2.47e-4},
	"claude-opus-4.6":   {Cold: 6.98e-6, Cached: 3.70e-6, Output: 2.48e-4},
	"claude-sonnet-5.5": {Cold: 7.97e-6, Cached: 2.15e-6, Output: 1.45e-4},
	"claude-sonnet-5":   {Cold: 4.116e-6, Cached: 2.177e-6, Output: 1.46e-4},
}

// kiroCreditsOutputEnvKey 设为 off/false/0 时整体关闭 credits 反推，回到旧算法。
const kiroCreditsOutputEnvKey = "SUB2API_KIRO_CREDITS_OUTPUT"

// kiroMaxOutputTokens 是反推结果的合理性上限。Kiro 不按客户端 max_tokens 截断思考
// （实测 max_tokens=32000 的请求输出了 47,895），所以用模型最大输出而非 max_tokens。
const kiroMaxOutputTokens = 131072

// KiroOutputMethodCredits 表示本次 output 由 credits 反推得出。
const KiroOutputMethodCredits = "credits"

// credits 反推采用的命中量假设（KiroOutputEstimate.CacheAssumption）。
const (
	KiroCacheAssumptionHint      = "hint"       // 采用调用方推断的命中量
	KiroCacheAssumptionAllCached = "all_cached" // 推断值不成立，按整个提示词都命中
	KiroCacheAssumptionNone      = "none"       // 按全部未命中
)

// KiroCreditRatesForModel 返回模型的 credits 单价；未实测的模型或已关闭时返回 false。
func KiroCreditRatesForModel(model string) (KiroCreditRates, bool) {
	if !kiroCreditsOutputEnabled() {
		return KiroCreditRates{}, false
	}
	rates, ok := kiroCreditRateTable[normalizeKiroModelKey(model)]
	return rates, ok
}

func kiroCreditsOutputEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(kiroCreditsOutputEnvKey))) {
	case "off", "false", "0", "disabled":
		return false
	}
	return true
}

// KiroPromptCacheHint 是调用方推断的 Kiro 提示词缓存命中量。
// 零值（Resolved=false）表示未知，此时不用 credits 反推——手工构造
// KiroRequestContext 的路径（web search、账号测试）因此自动沿用旧算法。
type KiroPromptCacheHint struct {
	Resolved     bool
	CachedTokens int
}

// KiroOutputEstimate 记录本次 output 的来源，供调用方写缓存状态与打日志。
type KiroOutputEstimate struct {
	// Method 为 KiroOutputMethodCredits 时 output 由 credits 反推；为空表示沿用旧算法。
	Method string
	// Reason 是未用 credits 反推的原因（Method 为空时有值）。
	Reason              string
	Credits             float64
	ContextTokens       int // Kiro 上下文总量 T
	PromptTokens        int // 提示词大小 P = T - O（仅 credits 反推时有值）
	CachedPromptTokens  int // 采用的命中量 C
	VisibleOutputTokens int // 本地 tokenizer 数的下发内容（正文 + 工具参数 + 思考摘要）
	OutputTokens        int // 最终 output

	// CacheAssumption 是 credits 反推采用的命中量假设（KiroCacheAssumption*）。
	CacheAssumption string
}

// kiroCreditOutputInput 是收尾时判断能否用 credits 反推 output 所需的信号。
type kiroCreditOutputInput struct {
	Credits         float64
	ContextTotal    int     // contextUsageEvent 换算出的总量 T
	ContextPercent  float64 // contextUsageEvent 原始百分比
	ProviderOutput  bool    // 上游自己给了 output token 数
	ClientTruncated bool    // sub2api 按 stop sequence 截断了下发内容
	VisibleLocal    int     // 本地 tokenizer 数的下发内容
}

// applyKiroCreditOutput 在条件都满足时用 credits 反推 output。返回值：
// output 是要报给客户端的 output（不超过请求的 max_tokens，与官方语义一致），
// prompt 是提示词大小 P = T - 完整输出（input 以它为基数，Kiro 多生成的部分不计入 input）。
// ok=false 时调用方沿用旧算法；est 里记录原因，供调用方打日志。
func applyKiroCreditOutput(requestCtx KiroRequestContext, fallbackModel string, in kiroCreditOutputInput) (output, prompt int, est KiroOutputEstimate, ok bool) {
	est = KiroOutputEstimate{Credits: in.Credits, ContextTokens: in.ContextTotal, VisibleOutputTokens: in.VisibleLocal}
	if !requestCtx.PromptCache.Resolved {
		est.Reason = "cache_unresolved"
		return 0, 0, est, false
	}
	model := strings.TrimSpace(requestCtx.UpstreamModelID)
	if model == "" {
		model = fallbackModel
	}
	rates, hasRates := KiroCreditRatesForModel(model)
	switch {
	case !hasRates:
		est.Reason = "no_rates"
	case in.ProviderOutput:
		est.Reason = "provider_output"
	case in.ContextPercent >= 100.0:
		est.Reason = "context_full"
	case in.Credits <= 0:
		est.Reason = "no_credits"
	case in.ClientTruncated:
		est.Reason = "client_truncated"
	}
	if est.Reason != "" {
		return 0, 0, est, false
	}
	full, resolved, good := resolveKiroCreditOutput(rates, in.Credits, in.ContextTotal, requestCtx.PromptCache, in.VisibleLocal)
	if !good {
		return 0, 0, resolved, false
	}
	output = full
	if requestCtx.MaxOutputTokens > 0 && output > requestCtx.MaxOutputTokens {
		output = requestCtx.MaxOutputTokens
	}
	resolved.OutputTokens = output
	return output, resolved.PromptTokens, resolved, true
}

// estimateKiroOutputFromCredits 由 credits、上下文总量 T 和命中量 C 反推完整输出：
//
//	O = (credits - Cached·C - Cold·(T - C)) / (Output - Cold)
func estimateKiroOutputFromCredits(r KiroCreditRates, credits float64, total, cached int) (int, bool) {
	denom := r.Output - r.Cold
	if denom <= 0 || credits <= 0 || total <= 0 || cached < 0 || cached > total {
		return 0, false
	}
	o := (credits - r.Cached*float64(cached) - r.Cold*float64(total-cached)) / denom
	if math.IsNaN(o) || math.IsInf(o, 0) {
		return 0, false
	}
	return int(math.Round(o)), true
}

// estimateKiroOutputAllCached 假设整个提示词都命中缓存（C = P = T - O）反推完整输出：
//
//	O = (credits - Cached·T) / (Output - Cached)
func estimateKiroOutputAllCached(r KiroCreditRates, credits float64, total int) (int, bool) {
	denom := r.Output - r.Cached
	if denom <= 0 || credits <= 0 || total <= 0 {
		return 0, false
	}
	o := (credits - r.Cached*float64(total)) / denom
	if math.IsNaN(o) || math.IsInf(o, 0) {
		return 0, false
	}
	return int(math.Round(o)), true
}

// kiroCacheCandidate 是反推时尝试的一种命中量假设。
type kiroCacheCandidate struct {
	cached     int    // 命中量
	allCached  bool   // 假设整个提示词都命中（命中量 = P，由反推结果决定）
	assumption string // KiroCacheAssumption*
}

// resolveKiroCreditOutput 依次尝试命中量假设，取第一个通过合理性检查的反推结果：
//
//	有推断值时：[推断值, 整个提示词都命中, 0]；没有推断值时：[0]
//
// 推断值偏大时（例如上一轮没用上 credits，按"总量 - 可见输出"记录的提示词含了隐藏
// 思考），真实命中量接近整个提示词，先试"整个提示词都命中"的误差只有约
// 4.3% × 本轮新增内容；直接退到 0 会少算 output 约 4.3% × 缓存量，而且按少算的
// output 记下的提示词又偏大，会让同一会话后续每一轮都继续偏差。
//
// 合理性检查：
//   - O 不低于本地可见输出的 0.7 倍（本地 tokenizer 对旧 tokenizer 模型会高估，0.7 留余量），
//     单价误差导致的小幅不足按下限截齐；
//   - O 不超过 kiroMaxOutputTokens（容差内截齐）；
//   - 提示词 P = T - O 非负，且命中量不超过 P（否则说明这个命中量假设不对）。
//
// 都不通过时返回 ok=false，调用方沿用旧算法。
func resolveKiroCreditOutput(r KiroCreditRates, credits float64, total int, hint KiroPromptCacheHint, visibleLocal int) (int, KiroOutputEstimate, bool) {
	est := KiroOutputEstimate{Credits: credits, ContextTokens: total, VisibleOutputTokens: visibleLocal}
	candidates := []kiroCacheCandidate{{cached: 0, assumption: KiroCacheAssumptionNone}}
	if hint.CachedTokens > 0 {
		candidates = []kiroCacheCandidate{
			{cached: hint.CachedTokens, assumption: KiroCacheAssumptionHint},
			{allCached: true, assumption: KiroCacheAssumptionAllCached},
			{cached: 0, assumption: KiroCacheAssumptionNone},
		}
	}
	floor := int(math.Floor(0.7 * float64(visibleLocal)))
	// 单价 1% 的误差折算到输出上的偏差，用作下限容差。
	lowSlack := int(0.01*r.Cold*float64(total)/(r.Output-r.Cold)) + 32
	highSlack := kiroMaxOutputTokens/50 + 64
	for _, cand := range candidates {
		var o int
		var ok bool
		if cand.allCached {
			o, ok = estimateKiroOutputAllCached(r, credits, total)
		} else {
			o, ok = estimateKiroOutputFromCredits(r, credits, total, cand.cached)
		}
		if !ok {
			continue
		}
		if o > kiroMaxOutputTokens {
			if o > kiroMaxOutputTokens+highSlack {
				continue
			}
			o = kiroMaxOutputTokens
		}
		if o < floor {
			if o < floor-lowSlack {
				continue
			}
			o = floor
		}
		p := total - o
		c := cand.cached
		if cand.allCached {
			c = p
		}
		if p < 0 || c > p {
			continue
		}
		est.Method = KiroOutputMethodCredits
		est.PromptTokens = p
		est.CachedPromptTokens = c
		est.CacheAssumption = cand.assumption
		est.OutputTokens = o
		return o, est, true
	}
	est.Reason = "out_of_bounds"
	return 0, est, false
}
