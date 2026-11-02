//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newKiroErrorReportingTestService(upstream *httpUpstreamRecorder) *GatewayService {
	return &GatewayService{
		cfg:                 &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream:        upstream,
		kiroCooldownStore:   noopKiroChatCooldownStore{},
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}
}

func newKiroErrorReportingTestAccount() *Account {
	return &Account{
		ID:          803,
		Name:        "kiro-error-reporting",
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "ksk_test"},
	}
}

func newKiroErrorReportingTestContext(path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func forwardKiroMessagesForTest(t *testing.T, svc *GatewayService, c *gin.Context, account *Account, body []byte) error {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	_, err = svc.Forward(context.Background(), c, account, parsed)
	return err
}

// 本地不做体积预检、不裁历史，超大请求原样发给 Kiro；上游以 400 报「输入太长」时，
// 各入口返回 400（不是会被原样重试的 502）和固定文案，不回显上游原文（与 kiro.rs 一致）。
// 客户端是否压缩靠 usage 上报（contextUsageEvent 换算的 input_tokens），不靠这里的错误文案。
func TestKiroOversizeRequestPassesThroughToUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	big := strings.Repeat("X", 1200*1024)
	chatHistory := `{"role":"user","content":"` + big + `"},{"role":"assistant","content":"ok"},{"role":"user","content":"continue"}`
	upstreamBody := `{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`

	anthropicError := func(t *testing.T, body string) {
		require.Equal(t, "error", gjson.Get(body, "type").String())
		require.Equal(t, "invalid_request_error", gjson.Get(body, "error.type").String())
		require.Equal(t, kiroContextWindowFullMessage, gjson.Get(body, "error.message").String(), body)
	}
	cases := []struct {
		name    string
		path    string
		body    string
		forward func(t *testing.T, svc *GatewayService, c *gin.Context, account *Account, body []byte) error
		check   func(t *testing.T, body string)
	}{
		{
			name:    "messages stream",
			path:    "/v1/messages",
			body:    `{"model":"claude-sonnet-4-5","max_tokens":1024,"stream":true,"messages":[` + chatHistory + `]}`,
			forward: forwardKiroMessagesForTest,
			check:   anthropicError,
		},
		{
			name:    "messages non-stream",
			path:    "/v1/messages",
			body:    `{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[` + chatHistory + `]}`,
			forward: forwardKiroMessagesForTest,
			check:   anthropicError,
		},
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: `{"model":"claude-sonnet-4-5","messages":[` + chatHistory + `]}`,
			forward: func(_ *testing.T, svc *GatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, &ParsedRequest{})
				return err
			},
			check: func(t *testing.T, body string) {
				require.Equal(t, "invalid_request_error", gjson.Get(body, "error.type").String())
				require.Equal(t, kiroContextWindowFullMessage, gjson.Get(body, "error.message").String(), body)
			},
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"claude-sonnet-4-5","input":[` +
				`{"type":"message","role":"user","content":"` + big + `"},` +
				`{"type":"message","role":"assistant","content":"ok"},` +
				`{"type":"message","role":"user","content":"continue"}]}`,
			forward: func(_ *testing.T, svc *GatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.ForwardAsResponses(context.Background(), c, account, body, &ParsedRequest{})
				return err
			},
			check: func(t *testing.T, body string) {
				require.Equal(t, "invalid_request_error", gjson.Get(body, "error.code").String())
				require.Equal(t, kiroContextWindowFullMessage, gjson.Get(body, "error.message").String(), body)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(upstreamBody)),
			}}
			svc := newKiroErrorReportingTestService(upstream)
			body := []byte(tc.body)
			c, rec := newKiroErrorReportingTestContext(tc.path, body)

			err := tc.forward(t, svc, c, newKiroErrorReportingTestAccount(), body)

			require.Error(t, err)
			require.NotNil(t, upstream.lastReq, "oversize request must be sent to the upstream")
			require.Contains(t, string(upstream.lastBody), big, "request must reach the upstream untrimmed")
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			tc.check(t, rec.Body.String())
		})
	}
}

// 流转换中途失败时，真实原因要进日志；客户端只会看到 "stream interrupted"。
func TestKiroStreamInterruptionIsLogged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs, restore := captureStructuredLog(t)
	defer restore()

	first := kiroChatTestEventStreamFrame(t, "assistantResponseEvent", map[string]any{
		"assistantResponseEvent": map[string]any{"content": "partial"},
	})
	cut := kiroChatTestEventStreamFrame(t, "assistantResponseEvent", map[string]any{
		"assistantResponseEvent": map[string]any{"content": "never finished"},
	})
	upstreamBody := append(append([]byte(nil), first...), cut[:len(cut)/2]...)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
		Body:       io.NopCloser(bytes.NewReader(upstreamBody)),
	}}
	svc := newKiroErrorReportingTestService(upstream)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	c, _ := newKiroErrorReportingTestContext("/v1/messages", body)

	_ = forwardKiroMessagesForTest(t, svc, c, newKiroErrorReportingTestAccount(), body)

	require.NotNil(t, upstream.lastReq)
	require.True(t, logs.ContainsMessage("kiro.stream_interrupted"), "stream conversion error was not logged")
	require.True(t, logs.ContainsFieldValue("account_id", "803"))
	require.True(t, logs.ContainsFieldValue("error", "unexpected EOF"))
}

func kiroCompactionRequestBody(toolOutput string) []byte {
	output, _ := json.Marshal(toolOutput)
	return []byte(`{"model":"claude-sonnet-4-5","stream":true,"input":[` +
		`{"type":"message","role":"user","content":"start"},` +
		`{"type":"function_call","call_id":"call_1","name":"exec","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":` + string(output) + `},` +
		`{"type":"compaction_trigger"}]}`)
}

func kiroContextLengthUpstreamResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`)),
	}
}

// Codex 压缩请求被 Kiro 以超限拒绝时，截断工具输出重试一次（与 kiro.rs 一致），成功则返回 compaction 项。
func TestKiroCompactionRetriesWithBoundedToolOutputs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	summary := kiroChatTestEventStreamFrame(t, "assistantResponseEvent", map[string]any{
		"assistantResponseEvent": map[string]any{"content": "<summary>ok</summary>"},
	})
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		kiroContextLengthUpstreamResponse(),
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
			Body:       io.NopCloser(bytes.NewReader(summary)),
		},
	}}
	svc := newKiroErrorReportingTestService(upstream)
	body := kiroCompactionRequestBody(strings.Repeat("R", 60*1024))
	c, rec := newKiroErrorReportingTestContext("/v1/responses", body)

	_, err := svc.ForwardAsResponses(context.Background(), c, newKiroErrorReportingTestAccount(), body, &ParsedRequest{})

	require.NoError(t, err)
	require.Len(t, upstream.bodies, 2)
	require.NotContains(t, string(upstream.bodies[0]), "tool output truncated for compaction retry")
	require.Contains(t, string(upstream.bodies[1]), "tool output truncated for compaction retry")
	require.Contains(t, rec.Body.String(), `"type":"compaction"`)
	require.Contains(t, rec.Body.String(), "response.completed")
}

// 重试后仍超限：以 response.failed + context_length_exceeded 结束，不再发第三次。
func TestKiroCompactionFailsWhenRetryStillOverflows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		kiroContextLengthUpstreamResponse(),
		kiroContextLengthUpstreamResponse(),
	}}
	svc := newKiroErrorReportingTestService(upstream)
	body := kiroCompactionRequestBody(strings.Repeat("R", 60*1024))
	c, rec := newKiroErrorReportingTestContext("/v1/responses", body)

	_, err := svc.ForwardAsResponses(context.Background(), c, newKiroErrorReportingTestAccount(), body, &ParsedRequest{})

	require.Error(t, err)
	require.Len(t, upstream.bodies, 2)
	out := rec.Body.String()
	require.Contains(t, out, "response.failed")
	require.Contains(t, out, `"code":"context_length_exceeded"`)
	require.NotContains(t, out, "Input is too long")
}
