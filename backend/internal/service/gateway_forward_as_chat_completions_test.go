//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kirocooldown"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestHandleCCBufferedFromAnthropic_ToolArgumentsAreValidJSON(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_tool","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		``,
	}, "\n")))}

	_, err := (&GatewayService{}).handleCCBufferedFromAnthropic(resp, c, "gpt-5", "claude-sonnet-4.5", nil, time.Now())
	require.NoError(t, err)

	var body struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Choices, 1)
	require.Len(t, body.Choices[0].Message.ToolCalls, 1)
	args := body.Choices[0].Message.ToolCalls[0].Function.Arguments
	require.JSONEq(t, `{"city":"Paris"}`, args)
}

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
		got := extractCCReasoningEffortFromBody([]byte(`{"model":"deepseek-v4-flash","reasoning_effort":"Max"}`))
		require.NotNil(t, got)
		require.Equal(t, "max", *got)
	})

	t.Run("mapped Kimi alias max", func(t *testing.T) {
		got := extractCCReasoningEffortFromBody(
			[]byte(`{"model":"public-alias","reasoning_effort":"max"}`),
			"kimi-k3",
			"public-alias",
		)
		require.NotNil(t, got)
		require.Equal(t, "max", *got)
	})

	t.Run("legacy model max", func(t *testing.T) {
		got := extractCCReasoningEffortFromBody([]byte(`{"model":"gpt-5.5","reasoning_effort":"max"}`))
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
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":12,"cache_read_input_tokens":9,"cache_creation_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}}}}`,
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
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, "gpt-5", "claude-sonnet-4.5", &reasoningEffort, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 9, result.Usage.CacheReadInputTokens)
	require.Equal(t, 5, result.Usage.CacheCreationInputTokens)
	require.Equal(t, 2, result.Usage.CacheCreation5mTokens)
	require.Equal(t, 3, result.Usage.CacheCreation1hTokens)
	require.InDelta(t, 0.17, result.Usage.KiroCredits, 0.000001)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
	require.NotContains(t, rec.Body.String(), "_sub2api_kiro_credits")
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
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, "k3", "k3", nil, time.Now())
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
	result, err := svc.handleCCStreamingFromAnthropic(resp, c, "k3", "k3", nil, time.Now())
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
	result, err := svc.handleCCStreamingFromAnthropic(resp, c, "gpt-5", "claude-sonnet-4.5", &reasoningEffort, time.Now())
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

func TestForwardAsChatCompletions_KiroCacheEmulation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetKiroCacheTracker()

	stable := strings.Repeat("stable chat history chunk ", 700)
	latestA := strings.Repeat("latest chat turn chunk A ", 180)
	latestB := strings.Repeat("latest chat turn chunk B ", 180)
	firstBody := kiroChatCompletionsConversationBody([]string{stable, latestA})
	secondBody := kiroChatCompletionsConversationBody([]string{stable, latestB})

	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
			Body:       io.NopCloser(bytes.NewReader(kiroChatTestEventStream(t, "first ok", 1200, 9))),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
			Body:       io.NopCloser(bytes.NewReader(kiroChatTestEventStream(t, "second ok", 1200, 9))),
		},
	}}
	service := &GatewayService{
		httpUpstream:        upstream,
		kiroCooldownStore:   noopKiroChatCooldownStore{},
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}
	account := &Account{
		ID:          801,
		Name:        "kiro-apikey",
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":       "ksk_test",
			"model_mapping": map[string]any{"gpt-5": "claude-sonnet-4-6"},
		},
	}
	parsed := &ParsedRequest{Group: kiroCacheGroup(1)}

	firstRecorder := httptest.NewRecorder()
	firstCtx, _ := gin.CreateTestContext(firstRecorder)
	firstCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(firstBody))
	firstCtx.Request.Header.Set("Content-Type", "application/json")
	firstResult, err := service.ForwardAsChatCompletions(context.Background(), firstCtx, account, firstBody, parsed)
	require.NoError(t, err)
	require.NotNil(t, firstResult)
	require.Equal(t, 0, firstResult.Usage.CacheReadInputTokens)
	require.Greater(t, firstResult.Usage.CacheCreationInputTokens, 0)

	secondRecorder := httptest.NewRecorder()
	secondCtx, _ := gin.CreateTestContext(secondRecorder)
	secondCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(secondBody))
	secondCtx.Request.Header.Set("Content-Type", "application/json")
	secondResult, err := service.ForwardAsChatCompletions(context.Background(), secondCtx, account, secondBody, parsed)
	require.NoError(t, err)
	require.NotNil(t, secondResult)
	require.Greater(t, secondResult.Usage.CacheReadInputTokens, 0)
	require.Greater(t, secondResult.Usage.CacheCreationInputTokens, 0)

	responseBody := secondRecorder.Body.String()
	require.Contains(t, responseBody, `"prompt_tokens_details"`)
	require.Contains(t, responseBody, `"cached_tokens"`)
	require.Contains(t, responseBody, `"cache_creation_tokens"`)
}

type noopKiroChatCooldownStore struct{}

func (noopKiroChatCooldownStore) CheckCooldown(context.Context, string) error {
	return nil
}

func (noopKiroChatCooldownStore) MarkSuccess(context.Context, string) error {
	return nil
}

func (noopKiroChatCooldownStore) Mark429(context.Context, string) (time.Duration, error) {
	return 0, nil
}

func (noopKiroChatCooldownStore) MarkSuspended(context.Context, string) (time.Duration, error) {
	return 0, nil
}

func (noopKiroChatCooldownStore) GetState(context.Context, string) (*kirocooldown.State, error) {
	return nil, nil
}

func (noopKiroChatCooldownStore) ClearEarliestTransientCooldown(context.Context, []string) (bool, error) {
	return false, nil
}

func kiroChatTestEventStream(t *testing.T, text string, inputTokens int, outputTokens int) []byte {
	t.Helper()
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(kiroChatTestEventStreamFrame(t, "assistantResponseEvent", map[string]any{
		"assistantResponseEvent": map[string]any{"content": text},
	}))
	_, _ = stream.Write(kiroChatTestEventStreamFrame(t, "messageMetadataEvent", map[string]any{
		"messageMetadataEvent": map[string]any{
			"tokenUsage": map[string]any{
				"uncachedInputTokens": inputTokens,
				"outputTokens":        outputTokens,
				"totalTokens":         inputTokens + outputTokens,
			},
		},
	}))
	return stream.Bytes()
}

func kiroChatTestEventStreamFrame(t *testing.T, eventType string, payload any) []byte {
	t.Helper()
	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)

	headers := bytes.NewBuffer(nil)
	_ = headers.WriteByte(byte(len(":event-type")))
	_, _ = headers.WriteString(":event-type")
	_ = headers.WriteByte(7)
	require.NoError(t, binary.Write(headers, binary.BigEndian, uint16(len(eventType))))
	_, _ = headers.WriteString(eventType)

	totalLength := uint32(12 + headers.Len() + len(payloadBytes) + 4)
	frame := bytes.NewBuffer(nil)
	require.NoError(t, binary.Write(frame, binary.BigEndian, totalLength))
	require.NoError(t, binary.Write(frame, binary.BigEndian, uint32(headers.Len())))
	require.NoError(t, binary.Write(frame, binary.BigEndian, uint32(0)))
	_, _ = frame.Write(headers.Bytes())
	_, _ = frame.Write(payloadBytes)
	require.NoError(t, binary.Write(frame, binary.BigEndian, uint32(0)))
	return frame.Bytes()
}
