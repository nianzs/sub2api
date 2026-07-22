//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExtractCCReasoningEffortFromBody(t *testing.T) {
	t.Parallel()

	t.Run("nested reasoning.effort", func(t *testing.T) {
		got := extractCCReasoningEffortFromBody([]byte(`{"reasoning":{"effort":"HIGH"}}`))
		require.NotNil(t, got)
		require.Equal(t, "high", *got)
	})

	t.Run("flat reasoning_effort", func(t *testing.T) {
		got := extractCCReasoningEffortFromBody([]byte(`{"reasoning_effort":"x-high"}`))
		require.NotNil(t, got)
		require.Equal(t, "xhigh", *got)
	})

	t.Run("DeepSeek max", func(t *testing.T) {
		got := extractCCReasoningEffortFromBody([]byte(`{"reasoning_effort":"Max"}`))
		require.NotNil(t, got)
		require.Equal(t, "xhigh", *got)
	})

	t.Run("missing effort", func(t *testing.T) {
		require.Nil(t, extractCCReasoningEffortFromBody([]byte(`{"model":"gpt-5"}`)))
	})
}

func TestHandleCCBufferedFromAnthropic_PreservesMessageStartCacheUsageAndReasoning(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	reasoningEffort := "high"
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_buffered"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":12,"cache_read_input_tokens":9,"cache_creation_input_tokens":3}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7,"_sub2api_kiro_credits":0.17}}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, "gpt-5", "claude-sonnet-4.5", &reasoningEffort, time.Now(), 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 9, result.Usage.CacheReadInputTokens)
	require.Equal(t, 3, result.Usage.CacheCreationInputTokens)
	require.InDelta(t, 0.17, result.Usage.KiroCredits, 0.000001)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
	require.NotContains(t, rec.Body.String(), "_sub2api_kiro_credits")
}

func TestHandleCCBufferedFromAnthropic_PreservesCacheOnlyUsage(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_cache_only"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_cache","type":"message","role":"assistant","content":[],"model":"gpt-5.6-sol","stop_reason":"","usage":{"input_tokens":0,"output_tokens":0}}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":120,"cache_creation_input_tokens":0}}`,
			``,
		}, "\n"))),
	}

	result, err := (&GatewayService{}).handleCCBufferedFromAnthropic(resp, c, "gpt-5.6-sol", "gpt-5.6-sol", nil, time.Now(), 0)
	require.NoError(t, err)
	require.Zero(t, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
	require.Equal(t, 120, result.Usage.CacheReadInputTokens)
	require.Contains(t, rec.Body.String(), `"cached_tokens":120`)
}

// Kimi 等 Anthropic 兼容上游返回 SSE 紧凑格式（冒号后无空格），CC 桥此前按
// "event: " / "data: " 严格匹配会丢弃全部事件，最终报 "Upstream stream ended
// without a response"（#4653 同根因；#4657 只修了 /v1/responses 桥）。
func TestHandleCCBufferedFromAnthropic_CompactSSEFormat(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_buffered_compact"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_c1","type":"message","role":"assistant","content":[],"model":"k3","stop_reason":"","usage":{"input_tokens":15,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, "k3", "k3", nil, time.Now(), 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 15, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)
	require.Equal(t, 5, result.Usage.CacheReadInputTokens)
	require.Equal(t, 2, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `"OK"`, "紧凑格式事件必须被解析并产出响应内容")
}

func TestHandleCCStreamingFromAnthropic_CompactSSEFormat(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_stream_compact"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_c2","type":"message","role":"assistant","content":[],"model":"k3","stop_reason":"","usage":{"input_tokens":21,"cache_read_input_tokens":6,"cache_creation_input_tokens":1}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			``,
			`event:message_stop`,
			`data:{"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleCCStreamingFromAnthropic(resp, c, "k3", "k3", nil, time.Now(), true, 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 21, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.OutputTokens)
	require.Equal(t, 6, result.Usage.CacheReadInputTokens)
	require.Equal(t, 1, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `[DONE]`)
}

func TestHandleCCStreamingFromAnthropic_PreservesMessageStartCacheUsageAndReasoning(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	reasoningEffort := "medium"
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_cc_stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":20,"cache_read_input_tokens":11,"cache_creation_input_tokens":4}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8,"_sub2api_kiro_credits":0.23}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleCCStreamingFromAnthropic(resp, c, "gpt-5", "claude-sonnet-4.5", &reasoningEffort, time.Now(), true, 0)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 20, result.Usage.InputTokens)
	require.Equal(t, 8, result.Usage.OutputTokens)
	require.Equal(t, 11, result.Usage.CacheReadInputTokens)
	require.Equal(t, 4, result.Usage.CacheCreationInputTokens)
	require.InDelta(t, 0.23, result.Usage.KiroCredits, 0.000001)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "medium", *result.ReasoningEffort)
	require.Contains(t, rec.Body.String(), `[DONE]`)
	require.NotContains(t, rec.Body.String(), "_sub2api_kiro_credits")
}

func TestHandleCCStreamingFromAnthropic_DrainsTerminalUsageAfterClientDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Writer = &openAICompatFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_kiro","type":"message","role":"assistant","content":[],"model":"gpt-5.6-sol","usage":{"input_tokens":0,"output_tokens":0}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":90,"output_tokens":7,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

	result, err := (&GatewayService{}).handleCCStreamingFromAnthropic(resp, c, "customer-sol", "gpt-5.6-sol", nil, time.Now(), true, 120)
	require.NoError(t, err)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 90, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
}

func TestHandleCCStreamingFromAnthropic_UsesFallbackWhenTerminalUsageIsMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_kiro","type":"message","role":"assistant","content":[],"model":"gpt-5.6-sol","usage":{"input_tokens":0,"output_tokens":0}}}`,
		``,
	}, "\n")
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}

	result, err := (&GatewayService{}).handleCCStreamingFromAnthropic(resp, c, "customer-sol", "gpt-5.6-sol", nil, time.Now(), true, 120)
	require.NoError(t, err)
	require.Equal(t, 120, result.Usage.InputTokens)
}

func TestHandleCCStreamingFromAnthropic_BoundsDisconnectDrain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Writer = &openAICompatFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_kiro","type":"message","role":"assistant","content":[],"model":"gpt-5.6-sol","usage":{"input_tokens":0,"output_tokens":0}}}`,
		``,
	}, "\n")
	body := newOpenAICompatBlockingReadCloser([]byte(stream))
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}
	svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}}

	start := time.Now()
	result, err := svc.handleCCStreamingFromAnthropic(resp, c, "customer-sol", "gpt-5.6-sol", nil, time.Now(), true, 120)
	require.NoError(t, err)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, 120, result.Usage.InputTokens)
	require.Less(t, time.Since(start), 3*time.Second)
}
