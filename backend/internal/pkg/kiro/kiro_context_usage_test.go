package kiro

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestKiroContextWindowTokens(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"claude-sonnet-4.5", 200_000},
		{"claude-sonnet-4-5-20250929", 200_000},
		{"claude-sonnet-4.6", 1_000_000},
		{"claude-sonnet-4-6-thinking", 1_000_000},
		{"claude-sonnet-4.8", 1_000_000},
		{"claude-sonnet-5", 1_000_000},
		{"claude-sonnet-5-5-thinking", 1_000_000},
		{"claude-opus-4.5", 200_000},
		{"claude-opus-4.6", 1_000_000},
		{"claude-opus-4.8-thinking", 1_000_000},
		{"claude-opus-5.5", 1_000_000},
		{"claude-haiku-4.5", 200_000},
		{"claude-haiku-5.5", 1_000_000},
		{"claude-fable-5", 1_000_000},
		{"claude-fable-5-thinking", 1_000_000},
		{"gpt-5.6-sol", 1_000_000},
		{"gpt-5.6-terra", 1_000_000},
		{"gpt-5.4", 272_000},
		{"kimi-k3", 200_000},
		{"", 200_000},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, KiroContextWindowTokens(tc.model), "model=%q", tc.model)
	}
}

func TestKiroInputTokensExcludingOutput(t *testing.T) {
	// kiro.rs 实测样本：contextUsage 换算 8404，本轮输出 1514。
	require.Equal(t, 6890, kiroInputTokensExcludingOutput(8404, true, 25, 1514))
	// 无 contextUsage：prompt 估算本就不含输出，原样返回（仅负值保护）。
	require.Equal(t, 25, kiroInputTokensExcludingOutput(0, false, 25, 1514))
	require.Equal(t, 0, kiroInputTokensExcludingOutput(0, false, -3, 10))
	// 输出估算超过换算总量：宁可少报，不凭空造 token。
	require.Equal(t, 0, kiroInputTokensExcludingOutput(10, true, 25, 999))
	// 负值不得反向放大。
	require.Equal(t, 10, kiroInputTokensExcludingOutput(10, true, 25, -5))
	require.Equal(t, 0, kiroInputTokensExcludingOutput(-7, true, 25, 0))
}

func buildContextUsageFrames(t *testing.T, text string, tokenUsage map[string]any, pct *float64) *bytes.Buffer {
	t.Helper()
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(buildEventStreamFrame(t, "assistantResponseEvent", map[string]any{
		"assistantResponseEvent": map[string]any{"content": text},
	}))
	if tokenUsage != nil {
		_, _ = stream.Write(buildEventStreamFrame(t, "messageMetadataEvent", map[string]any{
			"messageMetadataEvent": map[string]any{"tokenUsage": tokenUsage},
		}))
	}
	if pct != nil {
		_, _ = stream.Write(buildEventStreamFrame(t, "contextUsageEvent", map[string]any{
			"contextUsageEvent": map[string]any{"contextUsagePercentage": *pct},
		}))
	}
	return stream
}

func TestStreamContextUsageOverridesEstimatedInput(t *testing.T) {
	pct := 0.5 // 0.5% × 200k = 1000，扣输出 100 → 900
	stream := buildContextUsageFrames(t, "hi", map[string]any{"outputTokens": 100}, &pct)
	out := bytes.NewBuffer(nil)

	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, out, "claude-sonnet-4-5", 4242, KiroRequestContext{})
	require.NoError(t, err)

	// message_start 仍是本地估算占位，message_delta 带换算终值。
	startUsage := gjson.ParseBytes(extractSSEEventData(t, out.String(), "message_start")).Get("message.usage")
	require.Equal(t, 4242, int(startUsage.Get("input_tokens").Int()))
	deltaUsage := gjson.ParseBytes(extractSSEEventData(t, out.String(), "message_delta")).Get("usage")
	require.Equal(t, 900, int(deltaUsage.Get("input_tokens").Int()))
	require.Equal(t, 100, int(deltaUsage.Get("output_tokens").Int()))
}

func TestStreamProviderTokenUsageWinsOverContextUsage(t *testing.T) {
	pct := 0.5
	stream := buildContextUsageFrames(t, "hi", map[string]any{
		"uncachedInputTokens": 123,
		"outputTokens":        7,
	}, &pct)
	out := bytes.NewBuffer(nil)

	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, out, "claude-sonnet-4-5", 4242, KiroRequestContext{})
	require.NoError(t, err)

	deltaUsage := gjson.ParseBytes(extractSSEEventData(t, out.String(), "message_delta")).Get("usage")
	require.Equal(t, 123, int(deltaUsage.Get("input_tokens").Int()))
}

func TestStreamContextUsageRescalesCacheEmulation(t *testing.T) {
	// 0.45% × 200k = 900，扣输出 100 → base 800；缓存模拟原基数 1000
	// （read 300 / creation 100 / input 600），factor=0.8 → 240/80/480。
	pct := 0.45
	stream := buildContextUsageFrames(t, "hi", map[string]any{"outputTokens": 100}, &pct)
	out := bytes.NewBuffer(nil)
	requestCtx := KiroRequestContext{CacheEmulationUsage: &Usage{
		InputTokens:              600,
		CacheReadInputTokens:     300,
		CacheCreationInputTokens: 100,
	}}

	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, out, "claude-sonnet-4-5", 1000, requestCtx)
	require.NoError(t, err)

	deltaUsage := gjson.ParseBytes(extractSSEEventData(t, out.String(), "message_delta")).Get("usage")
	require.Equal(t, 480, int(deltaUsage.Get("input_tokens").Int()))
	require.Equal(t, 240, int(deltaUsage.Get("cache_read_input_tokens").Int()))
	require.Equal(t, 80, int(deltaUsage.Get("cache_creation_input_tokens").Int()))
}

func TestStreamContextUsageFullSetsStopReason(t *testing.T) {
	full := 100.0
	stream := buildContextUsageFrames(t, "hi", nil, &full)
	out := bytes.NewBuffer(nil)

	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, out, "claude-sonnet-4-5", 4242, KiroRequestContext{})
	require.NoError(t, err)
	require.Equal(t, "model_context_window_exceeded",
		gjson.ParseBytes(extractSSEEventData(t, out.String(), "message_delta")).Get("delta.stop_reason").String())

	near := 99.9
	stream = buildContextUsageFrames(t, "hi", nil, &near)
	out = bytes.NewBuffer(nil)
	_, err = StreamEventStreamAsAnthropicWithContext(context.Background(), stream, out, "claude-sonnet-4-5", 4242, KiroRequestContext{})
	require.NoError(t, err)
	require.Equal(t, "end_turn",
		gjson.ParseBytes(extractSSEEventData(t, out.String(), "message_delta")).Get("delta.stop_reason").String())
}

func TestStreamWithoutContextUsageKeepsEstimate(t *testing.T) {
	stream := buildContextUsageFrames(t, "hi", map[string]any{"outputTokens": 100}, nil)
	out := bytes.NewBuffer(nil)

	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, out, "claude-sonnet-4-5", 4242, KiroRequestContext{})
	require.NoError(t, err)

	deltaUsage := gjson.ParseBytes(extractSSEEventData(t, out.String(), "message_delta")).Get("usage")
	require.Equal(t, 4242, int(deltaUsage.Get("input_tokens").Int()))
	require.Equal(t, "end_turn",
		gjson.ParseBytes(extractSSEEventData(t, out.String(), "message_delta")).Get("delta.stop_reason").String())
}

func TestParseNonStreamingContextUsageOverridesEstimatedInput(t *testing.T) {
	pct := 0.5 // × 200k = 1000，扣输出 100 → 900
	stream := buildContextUsageFrames(t, "hi", map[string]any{"outputTokens": 100}, &pct)

	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-sonnet-4-5",
		KiroRequestContext{EstimatedInputTokens: 555})
	require.NoError(t, err)
	require.Equal(t, 900, result.Usage.InputTokens)
	require.Equal(t, 900, int(gjson.GetBytes(result.ResponseBody, "usage.input_tokens").Int()))
}

func TestParseNonStreamingContextUsageFullSetsStopReason(t *testing.T) {
	full := 100.0
	stream := buildContextUsageFrames(t, "hi", nil, &full)

	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-sonnet-4-5",
		KiroRequestContext{EstimatedInputTokens: 555})
	require.NoError(t, err)
	require.Equal(t, "model_context_window_exceeded", result.StopReason)
	require.Equal(t, "model_context_window_exceeded", gjson.GetBytes(result.ResponseBody, "stop_reason").String())
}

func TestParseNonStreamingProviderTokenUsageWinsOverContextUsage(t *testing.T) {
	pct := 0.5
	stream := buildContextUsageFrames(t, "hi", map[string]any{
		"uncachedInputTokens": 123,
		"outputTokens":        7,
	}, &pct)

	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-sonnet-4-5",
		KiroRequestContext{EstimatedInputTokens: 555})
	require.NoError(t, err)
	require.Equal(t, 123, result.Usage.InputTokens)
}

// 确认 rescale 的非法比例退回整组覆盖（原基数 0 时）。
func TestRescaleKiroCacheUsageFallsBackWhenBaseInvalid(t *testing.T) {
	base := Usage{OutputTokens: 50}
	simulated := &Usage{InputTokens: 600, CacheReadInputTokens: 300, CacheCreationInputTokens: 100}
	out := rescaleKiroCacheUsageAgainstTotal(base, simulated, 800, 0)
	require.Equal(t, 600, out.InputTokens)
	require.Equal(t, 300, out.CacheReadInputTokens)
}

// 窗口按实际上游模型查：客户端名 claude-sonnet-4-5（200k）经账号映射发往
// claude-opus-4.8（1M），0.5% 应换算成 5000 而不是 1000。
func TestStreamContextUsageUsesUpstreamModelWindow(t *testing.T) {
	pct := 0.5 // × 1M = 5000，扣输出 100 → 4900
	stream := buildContextUsageFrames(t, "hi", map[string]any{"outputTokens": 100}, &pct)
	out := bytes.NewBuffer(nil)

	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, out, "claude-sonnet-4-5", 4242,
		KiroRequestContext{UpstreamModelID: "claude-opus-4.8"})
	require.NoError(t, err)

	deltaUsage := gjson.ParseBytes(extractSSEEventData(t, out.String(), "message_delta")).Get("usage")
	require.Equal(t, 4900, int(deltaUsage.Get("input_tokens").Int()))
}

func TestParseNonStreamingContextUsageUsesUpstreamModelWindow(t *testing.T) {
	pct := 0.5
	stream := buildContextUsageFrames(t, "hi", map[string]any{"outputTokens": 100}, &pct)

	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-sonnet-4-5",
		KiroRequestContext{EstimatedInputTokens: 555, UpstreamModelID: "claude-opus-4.8"})
	require.NoError(t, err)
	require.Equal(t, 4900, result.Usage.InputTokens)
}

func TestBuildKiroPayloadRecordsUpstreamModelID(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	built, err := BuildKiroPayloadWithContext(body, "claude-opus-4.8", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4.8", built.Context.UpstreamModelID)
}

// contextUsage 覆盖 input 后 TotalTokens 要跟着重算，不能停留在
// parseEventStream 按旧 input 算出的值。
func TestParseNonStreamingContextUsageRecomputesTotalTokens(t *testing.T) {
	pct := 0.5 // × 200k = 1000，扣输出 100 → 900
	stream := buildContextUsageFrames(t, "hi", map[string]any{"outputTokens": 100}, &pct)

	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-sonnet-4-5",
		KiroRequestContext{EstimatedInputTokens: 555})
	require.NoError(t, err)
	require.Equal(t, 900, result.Usage.InputTokens)
	require.Equal(t, 1000, result.Usage.TotalTokens)
}

func TestParseNonStreamingEstimateFallbackRecomputesTotalTokens(t *testing.T) {
	stream := buildContextUsageFrames(t, "hi", map[string]any{"outputTokens": 100}, nil)

	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-sonnet-4-5",
		KiroRequestContext{EstimatedInputTokens: 555})
	require.NoError(t, err)
	require.Equal(t, 555, result.Usage.InputTokens)
	require.Equal(t, 655, result.Usage.TotalTokens)
}

// 上游给了精确 input 时不做 contextUsage 换算；但缓存模拟仍整组覆盖
// input 与 cache 字段（Kiro 缓存数据只来自本地模拟），保持拆分自洽。
func TestStreamProviderInputWithCacheEmulationUsesEmulation(t *testing.T) {
	pct := 0.5
	stream := buildContextUsageFrames(t, "hi", map[string]any{
		"uncachedInputTokens": 123,
		"outputTokens":        7,
	}, &pct)
	out := bytes.NewBuffer(nil)
	requestCtx := KiroRequestContext{CacheEmulationUsage: &Usage{
		InputTokens:              600,
		CacheReadInputTokens:     300,
		CacheCreationInputTokens: 100,
	}}

	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, out, "claude-sonnet-4-5", 1000, requestCtx)
	require.NoError(t, err)

	deltaUsage := gjson.ParseBytes(extractSSEEventData(t, out.String(), "message_delta")).Get("usage")
	require.Equal(t, 600, int(deltaUsage.Get("input_tokens").Int()))
	require.Equal(t, 300, int(deltaUsage.Get("cache_read_input_tokens").Int()))
	require.Equal(t, 100, int(deltaUsage.Get("cache_creation_input_tokens").Int()))
}
