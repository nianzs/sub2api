//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// forwardKiroCompatWithUpstreamFrames 让 Kiro 上游回一段事件流，经 Chat Completions /
// Responses（含 compact）入口走完整的转换链路。
func forwardKiroCompatWithUpstreamFrames(t *testing.T, endpoint string, stream bool, frames ...[]byte) (*ForwardResult, error, *httptest.ResponseRecorder, *stubKiroCooldownStore, *queuedHTTPUpstream) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	streamFlag := strconv.FormatBool(stream)
	var path, reqBody string
	switch endpoint {
	case "chat_completions":
		path = "/v1/chat/completions"
		reqBody = `{"model":"claude-sonnet-4-6","stream":` + streamFlag + `,"messages":[{"role":"user","content":"hi"}]}`
	case "responses":
		path = "/v1/responses"
		reqBody = `{"model":"claude-sonnet-4-6","stream":` + streamFlag + `,"input":[{"role":"user","content":"hi"}]}`
	case "responses_compact":
		path = "/v1/responses"
		reqBody = `{"model":"claude-sonnet-4-6","stream":` + streamFlag + `,"input":[{"role":"user","content":"hi"},{"type":"compaction_trigger"}]}`
	default:
		t.Fatalf("unknown endpoint %q", endpoint)
	}

	upstreamBody := bytes.NewBuffer(nil)
	for _, f := range frames {
		_, _ = upstreamBody.Write(f)
	}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
		Body:       io.NopCloser(upstreamBody),
	}}}
	cooldown := &stubKiroCooldownStore{}
	svc := &GatewayService{
		httpUpstream:        upstream,
		kiroCooldownStore:   cooldown,
		tlsFPProfileService: &TLSFingerprintProfileService{},
		cfg:                 &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		rateLimitService:    &RateLimitService{},
	}
	account := &Account{
		ID: 32, Name: "kiro-compat-exception", Platform: PlatformKiro, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "kiro-access-token",
			"profile_arn":  "arn:aws:codewhisperer:us-east-1:000000000000:profile/TEST",
		},
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(reqBody)))
	c.Request.Header.Set("Content-Type", "application/json")

	parsed := &ParsedRequest{Group: kiroCacheGroup(1)}
	var result *ForwardResult
	var err error
	if endpoint == "chat_completions" {
		result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(reqBody), parsed)
	} else {
		result, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(reqBody), parsed)
	}
	return result, err, rec, cooldown, upstream
}

// Chat Completions / Responses 与 /v1/messages 一致：输出前的 Kiro 流内异常按对应 HTTP 错误处理。
func TestKiroCompatEndpointsHandleInStreamExceptions(t *testing.T) {
	for _, endpoint := range []string{"chat_completions", "responses"} {
		errTypePath := "error.type"
		if endpoint == "responses" {
			errTypePath = "error.code"
		}
		for _, stream := range []bool{true, false} {
			name := fmt.Sprintf("%s/stream=%v", endpoint, stream)

			t.Run(name+"/input_too_long_returns_400", func(t *testing.T) {
				result, err, rec, _, upstream := forwardKiroCompatWithUpstreamFrames(t, endpoint, stream,
					buildKiroExceptionFrame(t, "ValidationException", `{"message":"Input is too long."}`))
				require.Error(t, err)
				require.Nil(t, result, "a failed request must not be billed")
				var failoverErr *UpstreamFailoverError
				require.NotErrorAs(t, err, &failoverErr, "a request-level error must not fail over")
				require.Equal(t, http.StatusBadRequest, rec.Code)
				require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
				require.Equal(t, kiroInputTooLongMessage, gjson.Get(rec.Body.String(), "error.message").String())
				require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), errTypePath).String())
				require.Len(t, upstream.requests, 1)
			})

			t.Run(name+"/throttling_fails_over_with_cooldown", func(t *testing.T) {
				result, err, rec, cooldown, _ := forwardKiroCompatWithUpstreamFrames(t, endpoint, stream,
					buildKiroExceptionFrame(t, "ThrottlingException", `{"message":"Rate exceeded"}`))
				require.Nil(t, result)
				var failoverErr *UpstreamFailoverError
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
				require.Len(t, cooldown.mark429Keys, 1, "in-stream throttling must cool the account down")
				require.Zero(t, rec.Body.Len(), "failover must not write a response")
			})
		}
	}
}

func TestKiroResponsesCompactionHandlesInStreamExceptions(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v/throttling_fails_over", stream), func(t *testing.T) {
			result, err, rec, cooldown, _ := forwardKiroCompatWithUpstreamFrames(t, "responses_compact", stream,
				buildKiroExceptionFrame(t, "ThrottlingException", `{"message":"Rate exceeded"}`))
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
			require.Len(t, cooldown.mark429Keys, 1)
			require.Zero(t, rec.Body.Len())
		})

		t.Run(fmt.Sprintf("stream=%v/input_too_long_without_tool_outputs_fails_as_context_length", stream), func(t *testing.T) {
			result, err, rec, _, upstream := forwardKiroCompatWithUpstreamFrames(t, "responses_compact", stream,
				buildKiroExceptionFrame(t, "ValidationException", `{"message":"Input is too long."}`))
			require.Error(t, err)
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.NotErrorAs(t, err, &failoverErr)
			require.Len(t, upstream.requests, 1, "no tool outputs to trim, so no retry")
			require.Contains(t, rec.Body.String(), `"context_length_exceeded"`)
			require.NotContains(t, rec.Body.String(), `"type":"compaction"`)
		})
	}
}

// anthropicPartialSSE 是只有开头、没有结束事件的 Anthropic 流。
func anthropicPartialSSE(text string) string {
	block, _ := json.Marshal(text)
	return strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_cmp","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","usage":{"input_tokens":1000,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":` + string(block) + `}}`,
		``,
		``,
	}, "\n")
}

func kiroExceptionSSE(typ, payload string) string {
	exc := &kiropkg.KiroUpstreamException{Type: typ, Payload: []byte(payload)}
	if msg := gjson.Get(payload, "message").String(); msg != "" {
		exc.Message = msg
	}
	return kiroUpstreamExceptionSSE(exc)
}

func sseResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
}

// Kiro 流内「输入太长」和上下文已满一样触发一次截断重试。
func TestCompactionKiroContextLengthExceptionRetriesOnce(t *testing.T) {
	calls := 0
	retry := func() (*http.Response, error) {
		calls++
		return sseResponse(anthropicStopSSE("<summary>ok</summary>", "end_turn", 1200)), nil
	}
	rec, result, err := runCompactionHandler(t, kiroExceptionSSE("ValidationException", `{"message":"Input is too long."}`), true, retry)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, calls)
	require.Contains(t, rec.Body.String(), `"type":"compaction"`)
}

func TestCompactionKiroContextLengthExceptionRetryStillTooLongFails(t *testing.T) {
	retry := func() (*http.Response, error) {
		return sseResponse(kiroExceptionSSE("ValidationException", `{"message":"Input is too long."}`)), nil
	}
	rec, result, err := runCompactionHandler(t, kiroExceptionSSE("ValidationException", `{"message":"Input is too long."}`), false, retry)
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "context_length_exceeded", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, kiroCompactionOverflowMessage, gjson.Get(rec.Body.String(), "error.message").String())
}

// 客户端还没收到任何字节时，其它 Kiro 异常原样交回调用方（换号或回 4xx），不在这里写响应。
func TestCompactionKiroExceptionBeforeOutputIsReturnedUnwritten(t *testing.T) {
	for _, stream := range []bool{true, false} {
		rec, result, err := runCompactionHandler(t,
			anthropicPartialSSE("half a summ")+kiroExceptionSSE("InternalServerException", `{"message":"boom"}`), stream, nil)
		require.Nil(t, result)
		exc := kiroUpstreamExceptionFromError(err)
		require.NotNil(t, exc, "stream=%v: want the Kiro exception back, got %v", stream, err)
		require.Equal(t, "InternalServerException", exc.Type)
		require.Zero(t, rec.Body.Len())
	}
}

// 流没有正常结束时，半截摘要不能当成功交给 Codex。
func TestCompactionInterruptedStreamFailsInsteadOfPartialSummary(t *testing.T) {
	interruptedEvent := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"stream interrupted\"}}\n\n"
	for name, sse := range map[string]string{
		"error event":         anthropicPartialSSE("half a summ") + interruptedEvent,
		"missing final event": anthropicPartialSSE("half a summ"),
	} {
		t.Run(name, func(t *testing.T) {
			rec, result, err := runCompactionHandler(t, sse, false, nil)
			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, http.StatusBadGateway, rec.Code)
			require.NotContains(t, rec.Body.String(), `"type":"compaction"`)

			rec, result, err = runCompactionHandler(t, sse, true, nil)
			require.Error(t, err)
			require.Nil(t, result)
			require.Contains(t, rec.Body.String(), "response.failed")
			require.NotContains(t, rec.Body.String(), `"type":"compaction"`)
		})
	}
}

// compat 入口与 /v1/messages 一致：已有输出后的 Kiro 异常不转 403 failover，按流中断返回普通错误并记录用量。
func TestKiroCompatStreamExceptionAfterOutputRecordsPartialUsage(t *testing.T) {
	for _, endpoint := range []string{"chat_completions", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			result, err, rec, _, _ := forwardKiroCompatWithUpstreamFrames(t, endpoint, true,
				buildKiroEventStreamFrame(t, "assistantResponseEvent", map[string]any{
					"assistantResponseEvent": map[string]any{"content": "partial"},
				}),
				buildKiroEventStreamFrame(t, "meteringEvent", map[string]any{"meteringEvent": map[string]any{"usage": 0.2}}),
				buildKiroExceptionFrame(t, "InternalServerException", `{"message":"boom"}`),
			)
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.NotErrorAs(t, err, &failoverErr)
			require.NotNil(t, result)
			require.Positive(t, result.Usage.OutputTokens)
			require.InDelta(t, 0.2, result.Usage.KiroCredits, 1e-9)
			require.Contains(t, rec.Body.String(), "partial")
		})
	}
}
