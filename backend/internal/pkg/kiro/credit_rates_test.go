package kiro

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var opus55Rates = kiroCreditRateTable["claude-opus-5.5"]

// kiroCreditsFor 按单价表生成一次请求的 credits：提示词 P（其中命中 C）+ 输出 O。
func kiroCreditsFor(r KiroCreditRates, prompt, cached, output int) float64 {
	return r.Cached*float64(cached) + r.Cold*float64(prompt-cached) + r.Output*float64(output)
}

func TestKiroCreditRatesForModel(t *testing.T) {
	for _, m := range []string{"claude-opus-5-5", "claude-opus-5.5", "claude-sonnet-5-5-thinking", "claude-opus-4-6", "claude-opus-5", "claude-opus-4-8", "claude-sonnet-5"} {
		_, ok := KiroCreditRatesForModel(m)
		require.True(t, ok, m)
	}
	for _, m := range []string{"claude-haiku-4.5", "claude-sonnet-4-6", "gpt-5.6-sol", ""} {
		_, ok := KiroCreditRatesForModel(m)
		require.False(t, ok, m)
	}
	t.Setenv(kiroCreditsOutputEnvKey, "off")
	_, ok := KiroCreditRatesForModel("claude-opus-5.5")
	require.False(t, ok, "开关关闭后不应返回单价")
}

func TestEstimateKiroOutputFromCredits(t *testing.T) {
	credits := kiroCreditsFor(opus55Rates, 97000, 90000, 3000)
	o, ok := estimateKiroOutputFromCredits(opus55Rates, credits, 100000, 90000)
	require.True(t, ok)
	require.InDelta(t, 3000, o, 1)

	_, ok = estimateKiroOutputFromCredits(opus55Rates, credits, 100000, 100001)
	require.False(t, ok, "命中量不能超过总量")
	_, ok = estimateKiroOutputFromCredits(opus55Rates, 0, 100000, 0)
	require.False(t, ok)
}

func TestResolveKiroCreditOutputUsesAllCachedWhenHintExceedsPrompt(t *testing.T) {
	// 上一轮提示词 97K 已缓存，本轮新增 3K，真实输出 15K。推断值 112K 偏大（上一轮按
	// "总量 - 可见输出"记录，含了隐藏思考），超过本轮提示词：应按整个提示词命中计算，
	// 误差只有约 4.3% × 新增的 3K，而不是退到 C=0 少算约 4.3% × 97K。
	credits := kiroCreditsFor(opus55Rates, 100000, 97000, 15000)
	o, est, ok := resolveKiroCreditOutput(opus55Rates, credits, 115000, KiroPromptCacheHint{Resolved: true, CachedTokens: 112000}, 500)
	require.True(t, ok)
	require.InDelta(t, 15000, o, 200)
	require.Equal(t, est.PromptTokens, est.CachedPromptTokens, "整个提示词都按命中计")
	require.Equal(t, KiroCacheAssumptionAllCached, est.CacheAssumption)
	require.InDelta(t, 100000, est.PromptTokens, 200)
}

func TestResolveKiroCreditOutputFallsBackToUncachedWhenOtherAssumptionsFail(t *testing.T) {
	// 实际全部未命中、输出接近上限：按推断值或整段命中反推都会超过输出上限，应退到 C=0。
	credits := kiroCreditsFor(opus55Rates, 300000, 0, 128000)
	o, est, ok := resolveKiroCreditOutput(opus55Rates, credits, 428000, KiroPromptCacheHint{Resolved: true, CachedTokens: 290000}, 500)
	require.True(t, ok)
	require.InDelta(t, 128000, o, 1)
	require.Equal(t, 0, est.CachedPromptTokens)
	require.Equal(t, KiroCacheAssumptionNone, est.CacheAssumption)
}

// 一轮没用上 credits（例如被 stop sequence 截断）后，按"总量 - 可见输出"记录的提示词
// 偏大。后续每轮都应在一轮内恢复准确，而不是一直少算。
func TestKiroCreditOutputRecoversAfterFallbackTurn(t *testing.T) {
	const (
		firstPrompt  = 100000
		perTurnAdded = 2500
		trueOutput   = 15000
		visibleLocal = 483 // 下发的思考摘要 + 正文，本地计数
		turns        = 8
	)
	prevPrompt, prompt := 0, firstPrompt
	recorded := 0
	for turn := 1; turn <= turns; turn++ {
		credits := kiroCreditsFor(opus55Rates, prompt, prevPrompt, trueOutput)
		total := prompt + trueOutput
		if turn == 1 {
			recorded = total - visibleLocal // 旧算法下的记录方式（见 recordKiroPromptCache）
		} else {
			o, est, ok := resolveKiroCreditOutput(opus55Rates, credits, total, KiroPromptCacheHint{Resolved: true, CachedTokens: recorded}, visibleLocal)
			require.True(t, ok, "turn %d", turn)
			if turn == 2 {
				require.InDelta(t, trueOutput, o, 200, "turn %d", turn)
			} else {
				require.InDelta(t, trueOutput, o, 10, "turn %d", turn)
			}
			recorded = est.PromptTokens
		}
		prevPrompt, prompt = prompt, prompt+perTurnAdded
	}
}

func TestResolveKiroCreditOutputUsesCacheHint(t *testing.T) {
	credits := kiroCreditsFor(opus55Rates, 300000, 280000, 1200)
	o, est, ok := resolveKiroCreditOutput(opus55Rates, credits, 301200, KiroPromptCacheHint{Resolved: true, CachedTokens: 280000}, 400)
	require.True(t, ok)
	require.InDelta(t, 1200, o, 1)
	require.Equal(t, 280000, est.CachedPromptTokens)
	require.Equal(t, KiroCacheAssumptionHint, est.CacheAssumption)
	require.Equal(t, KiroOutputMethodCredits, est.Method)
}

func TestResolveKiroCreditOutputRejectsBelowFloor(t *testing.T) {
	// credits 只够付提示词，却下发了 2000 个本地 token 的可见内容：不可信，应放弃。
	credits := kiroCreditsFor(opus55Rates, 50000, 0, 0)
	_, est, ok := resolveKiroCreditOutput(opus55Rates, credits, 52000, KiroPromptCacheHint{Resolved: true}, 2000)
	require.False(t, ok)
	require.Equal(t, "out_of_bounds", est.Reason)
}

func TestResolveKiroCreditOutputClampsSmallShortfall(t *testing.T) {
	// 单价略有偏差导致反推出一点负数：在容差内截到下限 0，而不是放弃。
	credits := kiroCreditsFor(opus55Rates, 80000, 0, 0) * 0.997
	o, est, ok := resolveKiroCreditOutput(opus55Rates, credits, 80000, KiroPromptCacheHint{Resolved: true}, 0)
	require.True(t, ok)
	require.Equal(t, 0, o)
	require.Equal(t, KiroCacheAssumptionNone, est.CacheAssumption, "没有推断值时按全部未命中")
}

func TestApplyKiroCreditOutputReasons(t *testing.T) {
	good := kiroCreditOutputInput{
		Credits:      kiroCreditsFor(opus55Rates, 97000, 90000, 3000),
		ContextTotal: 100000, ContextPercent: 10, VisibleLocal: 500,
	}
	ctx := KiroRequestContext{UpstreamModelID: "claude-opus-5.5", PromptCache: KiroPromptCacheHint{Resolved: true, CachedTokens: 90000}}

	o, p, est, ok := applyKiroCreditOutput(ctx, "", good)
	require.True(t, ok)
	require.InDelta(t, 3000, o, 1)
	require.InDelta(t, 97000, p, 1)
	require.Equal(t, KiroOutputMethodCredits, est.Method)

	capped := ctx
	capped.MaxOutputTokens = 2000
	o, p, _, ok = applyKiroCreditOutput(capped, "", good)
	require.True(t, ok)
	require.Equal(t, 2000, o, "报出的 output 不超过 max_tokens")
	require.InDelta(t, 97000, p, 1, "input 仍以真实提示词为基数")

	cases := []struct {
		name   string
		ctx    KiroRequestContext
		in     kiroCreditOutputInput
		reason string
	}{
		{"unresolved", KiroRequestContext{UpstreamModelID: "claude-opus-5.5"}, good, "cache_unresolved"},
		{"no rates", KiroRequestContext{UpstreamModelID: "gpt-5.6-sol", PromptCache: ctx.PromptCache}, good, "no_rates"},
		{"provider output", ctx, func() kiroCreditOutputInput { c := good; c.ProviderOutput = true; return c }(), "provider_output"},
		{"context full", ctx, func() kiroCreditOutputInput { c := good; c.ContextPercent = 100; return c }(), "context_full"},
		{"no credits", ctx, func() kiroCreditOutputInput { c := good; c.Credits = 0; return c }(), "no_credits"},
		{"truncated", ctx, func() kiroCreditOutputInput { c := good; c.ClientTruncated = true; return c }(), "client_truncated"},
	}
	for _, tc := range cases {
		_, _, est, ok := applyKiroCreditOutput(tc.ctx, "", tc.in)
		require.False(t, ok, tc.name)
		require.Equal(t, tc.reason, est.Reason, tc.name)
	}
}

func buildCreditFrames(t *testing.T, text string, credits, pct float64) *bytes.Buffer {
	t.Helper()
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(buildEventStreamFrame(t, "assistantResponseEvent", map[string]any{
		"assistantResponseEvent": map[string]any{"content": text},
	}))
	_, _ = stream.Write(buildEventStreamFrame(t, "meteringEvent", map[string]any{
		"meteringEvent": map[string]any{"unit": "credit", "usage": credits},
	}))
	_, _ = stream.Write(buildEventStreamFrame(t, "contextUsageEvent", map[string]any{
		"contextUsageEvent": map[string]any{"contextUsagePercentage": pct},
	}))
	return stream
}

// 总量 10% × 1M = 100,000；提示词 97,000 其中命中 90,000；完整输出 3,000（下发的只有一句话）。
func creditScenario(t *testing.T) (credits, pct float64, ctx KiroRequestContext) {
	t.Helper()
	return kiroCreditsFor(opus55Rates, 97000, 90000, 3000), 10.0, KiroRequestContext{
		UpstreamModelID: "claude-opus-5.5",
		PromptCache:     KiroPromptCacheHint{Resolved: true, CachedTokens: 90000},
	}
}

func TestStreamOutputFromCredits(t *testing.T) {
	credits, pct, ctx := creditScenario(t)
	out := bytes.NewBuffer(nil)
	res, err := StreamEventStreamAsAnthropicWithContext(context.Background(), buildCreditFrames(t, "short visible answer", credits, pct), out, "claude-opus-5-5", 50, ctx)
	require.NoError(t, err)
	require.InDelta(t, 3000, res.Usage.OutputTokens, 1)
	require.InDelta(t, 97000, res.Usage.InputTokens, 1)
	require.Equal(t, KiroOutputMethodCredits, res.OutputEstimate.Method)
	require.InDelta(t, 97000, res.OutputEstimate.PromptTokens, 1)

	var delta gjson.Result
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "data: ") && gjson.Get(line[6:], "type").String() == "message_delta" {
			delta = gjson.Get(line[6:], "usage")
		}
	}
	require.InDelta(t, 3000, delta.Get("output_tokens").Int(), 1)
	require.InDelta(t, 97000, delta.Get("input_tokens").Int(), 1)
}

func TestNonStreamOutputFromCredits(t *testing.T) {
	credits, pct, ctx := creditScenario(t)
	res, err := ParseNonStreamingEventStreamWithContext(buildCreditFrames(t, "short visible answer", credits, pct), "claude-opus-5-5", ctx)
	require.NoError(t, err)
	require.InDelta(t, 3000, res.Usage.OutputTokens, 1)
	require.InDelta(t, 97000, res.Usage.InputTokens, 1)
	require.Equal(t, KiroOutputMethodCredits, res.OutputEstimate.Method)
	require.InDelta(t, 3000, gjson.GetBytes(res.ResponseBody, "usage.output_tokens").Int(), 1)
}

func TestOutputFromCreditsSplitsCacheEmulationAgainstPrompt(t *testing.T) {
	credits, pct, ctx := creditScenario(t)
	// 缓存模拟按本地估算 1000 拆成 900 读 + 100 输入，应按提示词 97,000 等比缩放。
	ctx.CacheEmulationUsage = &Usage{InputTokens: 100, CacheReadInputTokens: 900}
	ctx.EstimatedInputTokens = 1000
	res, err := ParseNonStreamingEventStreamWithContext(buildCreditFrames(t, "short visible answer", credits, pct), "claude-opus-5-5", ctx)
	require.NoError(t, err)
	require.InDelta(t, 3000, res.Usage.OutputTokens, 1)
	require.InDelta(t, 97000, res.Usage.InputTokens+res.Usage.CacheReadInputTokens+res.Usage.CacheCreationInputTokens, 2)
	require.InDelta(t, 87300, res.Usage.CacheReadInputTokens, 2)
}

func TestOutputFromCreditsFallsBackToLocalCount(t *testing.T) {
	credits, pct, ctx := creditScenario(t)

	// 命中量未解析（如 web search、账号测试路径）：沿用旧算法。
	unresolved := ctx
	unresolved.PromptCache = KiroPromptCacheHint{}
	res, err := ParseNonStreamingEventStreamWithContext(buildCreditFrames(t, "short visible answer", credits, pct), "claude-opus-5-5", unresolved)
	require.NoError(t, err)
	require.Less(t, res.Usage.OutputTokens, 100)
	require.Equal(t, "cache_unresolved", res.OutputEstimate.Reason)

	// 上下文打满：总量被截断，不可信。
	res, err = ParseNonStreamingEventStreamWithContext(buildCreditFrames(t, "short visible answer", credits, 100), "claude-opus-5-5", ctx)
	require.NoError(t, err)
	require.Less(t, res.Usage.OutputTokens, 100)
	require.Equal(t, "context_full", res.OutputEstimate.Reason)

	// 未实测单价的模型。
	gpt := ctx
	gpt.UpstreamModelID = "claude-haiku-4.5"
	res, err = ParseNonStreamingEventStreamWithContext(buildCreditFrames(t, "short visible answer", credits, pct), "claude-haiku-4-5", gpt)
	require.NoError(t, err)
	require.Less(t, res.Usage.OutputTokens, 100)
	require.Equal(t, "no_rates", res.OutputEstimate.Reason)
}
