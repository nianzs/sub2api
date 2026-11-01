//go:build unit

package service

import (
	"bytes"
	"context"
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

// 最近几轮里的用户正文裁不掉也缩不短时，Kiro 各入口都应返回 400 上下文超限，
// 不发上游请求，也不再返回会被客户端原样重试的 502。
func TestKiroOversizeRequestReturnsContextLengthError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	big := strings.Repeat("X", 900*1024+1024)
	chatHistory := `{"role":"user","content":"` + big + `"},{"role":"assistant","content":"ok"},{"role":"user","content":"continue"}`

	anthropicError := func(t *testing.T, body string) {
		require.Equal(t, "error", gjson.Get(body, "type").String())
		require.Equal(t, "invalid_request_error", gjson.Get(body, "error.type").String())
		require.True(t, strings.HasPrefix(gjson.Get(body, "error.message").String(), "prompt is too long"), body)
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
				require.True(t, strings.HasPrefix(gjson.Get(body, "error.message").String(), "prompt is too long"), body)
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
				require.Equal(t, "context_length_exceeded", gjson.Get(body, "error.code").String())
				require.True(t, strings.HasPrefix(gjson.Get(body, "error.message").String(), "prompt is too long"), body)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc := newKiroErrorReportingTestService(upstream)
			body := []byte(tc.body)
			c, rec := newKiroErrorReportingTestContext(tc.path, body)

			err := tc.forward(t, svc, c, newKiroErrorReportingTestAccount(), body)

			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			tc.check(t, rec.Body.String())
			require.Nil(t, upstream.lastReq, "oversize request must not reach the upstream")
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
