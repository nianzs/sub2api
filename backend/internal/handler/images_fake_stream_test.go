package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newImagesFakeStreamTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	return c, recorder
}

// 响应头提交前出错：照常返回带原状态码的 JSON，不出现 SSE 内容。
func TestImagesFakeStreamErrorBeforeCommitIsJSON(t *testing.T) {
	c, recorder := newImagesFakeStreamTestContext()
	stream := startImagesFakeStream(c, time.Hour)
	defer stream.stop()

	fishImagesError(c, http.StatusTooManyRequests, "rate_limit_error", "busy")

	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
	require.NotContains(t, recorder.Body.String(), "event:")
	require.False(t, stream.committed())
}

// 保活触发后提交 SSE 响应头并写注释；出图完成写 completed 事件。
func TestImagesFakeStreamKeepaliveThenCompleted(t *testing.T) {
	c, recorder := newImagesFakeStreamTestContext()
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
func TestImagesFakeStreamErrorAfterCommitIsSSE(t *testing.T) {
	c, recorder := newImagesFakeStreamTestContext()
	stream := startImagesFakeStream(c, 5*time.Millisecond)
	defer stream.stop()

	require.Eventually(t, stream.committed, time.Second, 5*time.Millisecond)
	fishImagesError(c, http.StatusBadGateway, "api_error", "Upstream request failed")

	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.Contains(t, body, "event: error\n")
	require.Contains(t, body, `"type":"api_error"`)
	require.Contains(t, body, `"param":null`)
	require.Contains(t, body, `"code":null`)
	require.Contains(t, body, `"message":"Upstream request failed"`)
}

// 转交中转号期间暂停：不提交响应头、不写保活。
func TestImagesFakeStreamPauseSuppressesKeepalive(t *testing.T) {
	c, recorder := newImagesFakeStreamTestContext()
	stream := startImagesFakeStream(c, 5*time.Millisecond)
	stream.pause()
	time.Sleep(40 * time.Millisecond)
	stream.stop()

	require.False(t, stream.committed())
	require.Empty(t, recorder.Body.String())
}

// 没有保活间隔时最终写出仍会提交 SSE。
func TestImagesFakeStreamWithoutKeepaliveStillStreams(t *testing.T) {
	c, recorder := newImagesFakeStreamTestContext()
	stream := startImagesFakeStream(c, 0)
	stream.writeEvents([]service.ImagesStreamEvent{{Name: "image_edit.completed", Payload: []byte(`{}`)}})

	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Equal(t, "event: image_edit.completed\ndata: {}\n\n", recorder.Body.String())
}
