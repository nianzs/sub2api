//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestKiroUpstreamExceptionStatus(t *testing.T) {
	tests := []struct {
		name string
		exc  kiropkg.KiroUpstreamException
		want int
	}{
		{"throttling", kiropkg.KiroUpstreamException{Type: "ThrottlingException", Message: "Rate exceeded"}, http.StatusTooManyRequests},
		{"input too long", kiropkg.KiroUpstreamException{Type: "ValidationException", Message: "Input is too long."}, http.StatusBadRequest},
		{"monthly quota", kiropkg.KiroUpstreamException{Type: "ServiceQuotaExceededException", Payload: []byte(`{"reason":"MONTHLY_REQUEST_COUNT"}`)}, http.StatusPaymentRequired},
		{"access denied stays account-safe", kiropkg.KiroUpstreamException{Type: "AccessDeniedException", Message: "denied"}, http.StatusInternalServerError},
		{"internal", kiropkg.KiroUpstreamException{Type: "InternalServerException"}, http.StatusInternalServerError},
		{"unknown", kiropkg.KiroUpstreamException{Type: "UnknownUpstreamError"}, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exc := tt.exc
			require.Equal(t, tt.want, kiroUpstreamExceptionStatus(&exc))
		})
	}
}

func TestKiroUpstreamExceptionResponseKeepsPayload(t *testing.T) {
	exc := &kiropkg.KiroUpstreamException{
		Type:    "ValidationException",
		Message: "Input is too long.",
		Payload: []byte(`{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`),
	}
	resp := kiroUpstreamExceptionResponse(exc, http.Header{"X-Request-Id": []string{"req-1"}})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "req-1", resp.Header.Get("X-Request-Id"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "ValidationException", gjson.GetBytes(body, "__type").String())
	require.Equal(t, "CONTENT_LENGTH_EXCEEDS_THRESHOLD", gjson.GetBytes(body, "reason").String())
	require.Equal(t, kiroErrorBadRequestContextLength, classifyKiroHTTPError(resp.StatusCode, string(body)).Category)
}

func TestKiroUpstreamExceptionSSERoundTrip(t *testing.T) {
	exc := &kiropkg.KiroUpstreamException{Type: "ThrottlingException", Message: "slow down", Payload: []byte(`{"message":"slow down"}`)}
	sse := kiroUpstreamExceptionSSE(exc)
	require.Contains(t, sse, "event: error\n")
	data := sse[len("event: error\ndata: ") : len(sse)-2]
	require.Equal(t, "rate_limit_error", gjson.Get(data, "error.type").String())

	back := kiroUpstreamExceptionFromSSE(data)
	require.NotNil(t, back)
	require.Equal(t, exc.Type, back.Type)
	require.Equal(t, exc.Message, back.Message)
	require.Equal(t, string(exc.Payload), string(back.Payload))

	require.Nil(t, kiroUpstreamExceptionFromSSE(`{"type":"error","error":{"type":"overloaded_error","message":"x"}}`))
}

func newKiroExceptionTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	// handleStreamingResponse 先设好 SSE 头再读流；模拟这一状态。
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	return c, rec
}

func TestHandleKiroUpstreamExceptionInputTooLongReturns400(t *testing.T) {
	c, rec := newKiroExceptionTestContext()
	account := &Account{ID: 7, Platform: PlatformKiro, Type: AccountTypeOAuth, Name: "kiro-7"}
	svc := &GatewayService{}
	exc := &kiropkg.KiroUpstreamException{Type: "ValidationException", Message: "Input is too long.", Payload: []byte(`{"message":"Input is too long."}`)}

	err := svc.handleKiroUpstreamException(context.Background(), c, account, "claude-opus-4.7", []byte(`{"model":"claude-opus-4-7"}`), exc, http.Header{})
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, kiroInputTooLongMessage, gjson.Get(rec.Body.String(), "error.message").String())
}

func TestHandleKiroUpstreamExceptionRetryableFailsOver(t *testing.T) {
	for _, tc := range []struct {
		exc    kiropkg.KiroUpstreamException
		status int
	}{
		{kiropkg.KiroUpstreamException{Type: "ThrottlingException", Message: "Rate exceeded"}, http.StatusTooManyRequests},
		{kiropkg.KiroUpstreamException{Type: "InternalServerException", Message: "boom"}, http.StatusInternalServerError},
	} {
		t.Run(tc.exc.Type, func(t *testing.T) {
			c, rec := newKiroExceptionTestContext()
			account := &Account{ID: 8, Platform: PlatformKiro, Type: AccountTypeOAuth, Name: "kiro-8"}
			svc := &GatewayService{}
			exc := tc.exc

			err := svc.handleKiroUpstreamException(context.Background(), c, account, "claude-opus-4.7", []byte(`{}`), &exc, http.Header{})
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, tc.status, failoverErr.StatusCode)
			require.Contains(t, string(failoverErr.ResponseBody), exc.Type)
			require.Zero(t, rec.Body.Len(), "failover must not write a response")
		})
	}
}

// buildKiroExceptionFrame 构造一帧 :message-type=exception 的上游失败。
func buildKiroExceptionFrame(t *testing.T, exceptionType, payload string) []byte {
	t.Helper()
	headers := bytes.NewBuffer(nil)
	for _, kv := range [][2]string{{":message-type", "exception"}, {":exception-type", exceptionType}} {
		_ = headers.WriteByte(byte(len(kv[0])))
		_, _ = headers.WriteString(kv[0])
		_ = headers.WriteByte(7)
		require.NoError(t, binary.Write(headers, binary.BigEndian, uint16(len(kv[1]))))
		_, _ = headers.WriteString(kv[1])
	}
	totalLen := 12 + headers.Len() + len(payload) + 4
	frame := bytes.NewBuffer(nil)
	require.NoError(t, binary.Write(frame, binary.BigEndian, uint32(totalLen)))
	require.NoError(t, binary.Write(frame, binary.BigEndian, uint32(headers.Len())))
	require.NoError(t, binary.Write(frame, binary.BigEndian, uint32(0)))
	_, _ = frame.Write(headers.Bytes())
	_, _ = frame.WriteString(payload)
	require.NoError(t, binary.Write(frame, binary.BigEndian, uint32(0)))
	return frame.Bytes()
}

func forwardKiroWithUpstreamFrames(t *testing.T, stream bool, frames ...[]byte) (*ForwardResult, error, *httptest.ResponseRecorder, *queuedHTTPUpstream) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	account := &Account{
		ID: 31, Name: "kiro-exception", Platform: PlatformKiro, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "kiro-access-token",
			"profile_arn":  "arn:aws:codewhisperer:us-east-1:123456789012:profile/EXCEPTION",
		},
	}
	body := bytes.NewBuffer(nil)
	for _, f := range frames {
		_, _ = body.Write(f)
	}
	upstream := &queuedHTTPUpstream{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
		Body:       io.NopCloser(body),
	}}}
	svc := &GatewayService{
		httpUpstream:        upstream,
		kiroCooldownStore:   &stubKiroCooldownStore{},
		tlsFPProfileService: &TLSFingerprintProfileService{},
		cfg:                 &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		rateLimitService:    &RateLimitService{},
	}
	streamFlag := "false"
	if stream {
		streamFlag = "true"
	}
	requestBody := []byte(`{"model":"claude-sonnet-4-6","stream":` + streamFlag + `,"messages":[{"role":"user","content":"hi"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(requestBody), domain.PlatformAnthropic)
	require.NoError(t, err)
	result, err := svc.Forward(context.Background(), c, account, parsed)
	return result, err, rec, upstream
}

func TestForwardKiroStreamExceptionBeforeOutputReturns400(t *testing.T) {
	result, err, rec, _ := forwardKiroWithUpstreamFrames(t, true,
		buildKiroEventStreamFrame(t, "contextUsageEvent", map[string]any{"contextUsagePercentage": 1.8}),
		buildKiroExceptionFrame(t, "ValidationException", `{"message":"Input is too long."}`),
	)
	require.Error(t, err)
	require.Nil(t, result, "a failed request must not be billed")
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.Equal(t, kiroInputTooLongMessage, gjson.Get(rec.Body.String(), "error.message").String())
}

func TestForwardKiroStreamThrottlingBeforeOutputFailsOver(t *testing.T) {
	result, err, rec, _ := forwardKiroWithUpstreamFrames(t, true,
		buildKiroExceptionFrame(t, "ThrottlingException", `{"message":"Rate exceeded"}`),
	)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Zero(t, rec.Body.Len())
}

func TestForwardKiroStreamExceptionAfterOutputEndsStream(t *testing.T) {
	result, err, rec, _ := forwardKiroWithUpstreamFrames(t, true,
		buildKiroEventStreamFrame(t, "assistantResponseEvent", map[string]any{
			"assistantResponseEvent": map[string]any{"content": "partial"},
		}),
		buildKiroExceptionFrame(t, "InternalServerException", `{"message":"boom"}`),
	)
	require.Nil(t, result, "a failed request must not be billed")
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Contains(t, rec.Body.String(), "partial")
}

func TestForwardKiroNonStreamExceptionReturns400(t *testing.T) {
	result, err, rec, _ := forwardKiroWithUpstreamFrames(t, false,
		buildKiroExceptionFrame(t, "ValidationException", `{"message":"Input is too long."}`),
	)
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, kiroInputTooLongMessage, gjson.Get(rec.Body.String(), "error.message").String())
}
