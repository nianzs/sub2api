package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newLeonardoStreamTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	return c, recorder
}

// 响应头提交前出错：照常返回带原状态码的 JSON，不出现 SSE 内容。
func TestLeonardoFakeStreamErrorBeforeCommitIsJSON(t *testing.T) {
	c, recorder := newLeonardoStreamTestContext()
	stream := startImagesFakeStream(c, time.Hour)
	defer stream.stop()

	leonardoImagesError(c, http.StatusTooManyRequests, "rate_limit_error", "busy")

	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
	require.NotContains(t, recorder.Body.String(), "event:")
	require.False(t, stream.committed())
}

// 保活触发后提交 SSE 响应头并写注释；出图完成写 completed 事件。
func TestLeonardoFakeStreamKeepaliveThenCompleted(t *testing.T) {
	c, recorder := newLeonardoStreamTestContext()
	stream := startImagesFakeStream(c, 5*time.Millisecond)
	defer stream.stop()

	require.Eventually(t, stream.committed, time.Second, 5*time.Millisecond)
	stream.writeEvents([]service.ImagesStreamEvent{{Name: "image_generation.completed", Payload: []byte(`{"type":"image_generation.completed","b64_json":"QUJD"}`)}})

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	body := recorder.Body.String()
	require.True(t, strings.HasPrefix(body, ":\n\n"), "保活注释应先于事件: %q", body)
	require.Contains(t, body, "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"QUJD\"}\n\n")
	require.True(t, service.IsResponseCommitted(c))
}

// 响应头提交后出错：写 SSE error 事件，状态码保持 200。
func TestLeonardoFakeStreamErrorAfterCommitIsSSE(t *testing.T) {
	c, recorder := newLeonardoStreamTestContext()
	stream := startImagesFakeStream(c, 5*time.Millisecond)
	defer stream.stop()

	require.Eventually(t, stream.committed, time.Second, 5*time.Millisecond)
	leonardoImagesFailoverError(c, &service.UpstreamFailoverError{
		ClientStatusCode: http.StatusBadRequest,
		ClientMessage:    "Content flagged by provider moderation",
		Reason:           service.GatewayFailureReason("leonardo_provider_moderation"),
	})

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.Contains(t, body, "event: error\n")
	require.Contains(t, body, `"type":"image_generation_user_error"`)
	require.Contains(t, body, `"code":"moderation_blocked"`)
	require.Contains(t, body, `"param":null`)
	require.Contains(t, body, `"message":"Content flagged by provider moderation"`)
}

// failover 错误按 OpenAI 错误对象渲染：type 由状态码决定（内部 Reason 不外露），
// param / code 两个键始终存在；内容安全拒绝为 image_generation_user_error + moderation_blocked。
func TestLeonardoImagesFailoverErrorUsesOpenAIShape(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		reason   string
		wantType string
		wantCode any
	}{
		{"request", http.StatusBadRequest, "leonardo_request", "invalid_request_error", nil},
		{"rate limited", http.StatusTooManyRequests, "leonardo_upstream", "rate_limit_error", nil},
		{"upstream", http.StatusBadGateway, "leonardo_upstream", "api_error", nil},
		{"indeterminate", http.StatusGatewayTimeout, "leonardo_indeterminate", "api_error", nil},
		{"content rejected", http.StatusBadRequest, "leonardo_content_rejected", "image_generation_user_error", "moderation_blocked"},
		{"provider moderation", http.StatusBadRequest, "leonardo_provider_moderation", "image_generation_user_error", "moderation_blocked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, recorder := newLeonardoStreamTestContext()
			leonardoImagesFailoverError(c, &service.UpstreamFailoverError{
				ClientStatusCode: tc.status,
				ClientMessage:    "boom",
				Reason:           service.GatewayFailureReason(tc.reason),
			})
			require.Equal(t, tc.status, recorder.Code)
			var body struct {
				Error map[string]any `json:"error"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			require.Equal(t, tc.wantType, body.Error["type"])
			require.Equal(t, "boom", body.Error["message"])
			require.Contains(t, body.Error, "param")
			require.Nil(t, body.Error["param"])
			require.Contains(t, body.Error, "code")
			require.Equal(t, tc.wantCode, body.Error["code"])
		})
	}
}

// 转交中转号期间暂停：不提交响应头、不写保活。
func TestLeonardoFakeStreamPauseSuppressesKeepalive(t *testing.T) {
	c, recorder := newLeonardoStreamTestContext()
	stream := startImagesFakeStream(c, 5*time.Millisecond)
	stream.pause()
	time.Sleep(40 * time.Millisecond)
	stream.stop()

	require.False(t, stream.committed())
	require.Empty(t, recorder.Body.String())
}

// 没有保活间隔时最终写出仍会提交 SSE。
func TestLeonardoFakeStreamWithoutKeepaliveStillStreams(t *testing.T) {
	c, recorder := newLeonardoStreamTestContext()
	stream := startImagesFakeStream(c, 0)
	stream.writeEvents([]service.ImagesStreamEvent{{Name: "image_edit.completed", Payload: []byte(`{}`)}})

	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Equal(t, "event: image_edit.completed\ndata: {}\n\n", recorder.Body.String())
}
