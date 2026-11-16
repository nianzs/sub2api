package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 纯缓存命中的响应（input/output 均为 0，但 cache read/write 非 0，例如完整命中
// prompt cache 且无新增补全 token）不能被累积 delta 的守卫整体丢弃。
//
// 放在独立的无 build tag 文件里（而非 gateway_forward_as_responses_test.go，
// 那个文件是 //go:build unit），以便不受该文件既有、与本改动无关的编译错误影响。
func TestHandleResponsesBufferedStreamingResponse_PreservesCacheOnlyUsage(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_buffered_cache_only"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_cache_only","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":9,"cache_creation_input_tokens":0}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 0, result.Usage.InputTokens)
	require.Equal(t, 0, result.Usage.OutputTokens)
	require.Equal(t, 9, result.Usage.CacheReadInputTokens)
}

// Kiro 积分在 Responses 协议上只在调用方带 X-Sub2API-Upstream-Credits 时以
// usage.upstream_credits 输出（流式在终态事件的 response.usage 上），记账始终读内部字段。
func TestResponsesHandlersExposeKiroCreditsOnlyWhenRequested(t *testing.T) {
	const delta = `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8,"_sub2api_kiro_credits":0.37}}`
	upstream := func(withStop bool) string {
		lines := []string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_k","type":"message","role":"assistant","content":[],"model":"claude-haiku-5-5","stop_reason":"","usage":{"input_tokens":20}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}`,
			``,
			`event: message_delta`,
			delta,
			``,
		}
		if withStop {
			lines = append(lines, `event: message_stop`, `data: {"type":"message_stop"}`, ``)
		}
		return strings.Join(lines, "\n")
	}

	for _, tc := range []struct {
		name        string
		header      string
		stream      bool
		withStop    bool
		wantCredits bool
	}{
		{name: "buffered without header", withStop: true},
		{name: "buffered with header", header: "1", withStop: true, wantCredits: true},
		{name: "stream without header", stream: true, withStop: true},
		{name: "stream with header", header: "1", stream: true, withStop: true, wantCredits: true},
		{name: "stream finalized without message_stop", header: "1", stream: true, wantCredits: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tc.header != "" {
				c.Request.Header.Set(upstreamCreditsRequestHeader, tc.header)
			}
			resp := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(upstream(tc.withStop)))}

			svc := &GatewayService{}
			var result *ForwardResult
			var err error
			if tc.stream {
				result, err = svc.handleResponsesStreamingResponse(resp, c, "claude-haiku-5-5", "claude-haiku-5-5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			} else {
				result, err = svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-haiku-5-5", "claude-haiku-5-5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.InDelta(t, 0.37, result.Usage.KiroCredits, 1e-9)

			body := rec.Body.String()
			require.NotContains(t, body, "_sub2api_kiro_credits")
			if !tc.wantCredits {
				require.NotContains(t, body, "upstream_credits")
				return
			}
			if !tc.stream {
				require.InDelta(t, 0.37, gjson.Get(body, "usage.upstream_credits").Float(), 1e-9)
				return
			}
			var terminal string
			for _, line := range strings.Split(body, "\n") {
				if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").String() == "response.completed" {
					terminal = data
				}
			}
			require.NotEmpty(t, terminal, body)
			require.InDelta(t, 0.37, gjson.Get(terminal, "response.usage.upstream_credits").Float(), 1e-9)
			require.Equal(t, 1, strings.Count(body, "upstream_credits"))
		})
	}
}
