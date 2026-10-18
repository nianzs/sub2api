//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKiroStreamTracksRawActivityThroughGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"tool_fragments", "partial_frame", "stalled"} {
		t.Run(scenario, func(t *testing.T) {
			svc := newMinimalGatewayService()
			// Exercise the production timeout mechanism on a shorter timescale.
			svc.cfg.Gateway.StreamDataIntervalTimeout = 1
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			rawR, rawW := io.Pipe()
			body := newKiroAnthropicStreamBody(context.Background(), rawR, "model", 1, kiro.KiroRequestContext{})
			defer body.Close()
			var chunks [][]byte
			if scenario == "partial_frame" {
				frame := buildKiroEventStreamFrame(t, "toolUseEvent", map[string]any{
					"toolUseId": "write", "name": "Write", "input": map[string]any{"file_path": "/tmp/a", "content": "hello"}, "stop": true,
				})
				for i := 0; i < 21; i++ {
					chunks = append(chunks, frame[i*len(frame)/21:(i+1)*len(frame)/21])
				}
			} else {
				for i := 0; i < 21; i++ {
					fragment := "x"
					if i == 0 {
						fragment = `{"file_path":"/tmp/a","content":"`
					}
					if i == 20 {
						fragment = `"}`
					}
					chunks = append(chunks, buildKiroEventStreamFrame(t, "toolUseEvent", map[string]any{
						"toolUseId": "write", "name": "Write", "input": fragment, "stop": i == 20,
					}))
				}
			}
			var received atomic.Int64
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer rawW.Close()
				for i, chunk := range chunks {
					if _, err := rawW.Write(chunk); err != nil {
						return
					}
					received.Add(1)
					if scenario == "stalled" && i == 0 {
						<-stop
						return
					}
					if i == len(chunks)-1 {
						return
					}
					select {
					case <-stop:
						return
					case <-time.After(100 * time.Millisecond):
					}
				}
			}()
			defer func() {
				close(stop)
				_ = body.Close()
				<-done
			}()
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}
			result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
			t.Logf("scenario=%s raw_reads=%d error=%v", scenario, received.Load(), err)
			if scenario == "stalled" {
				require.ErrorContains(t, err, "stream data interval timeout")
				require.NotContains(t, rec.Body.String(), "event: message_stop")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, int64(21), received.Load())
			require.Contains(t, rec.Body.String(), `"id":"write"`)
			require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_stop"))
		})
	}
}

func TestKiroStreamCloseUnblocksUpstreamRead(t *testing.T) {
	rawR, rawW := io.Pipe()
	defer rawW.Close()
	body := newKiroAnthropicStreamBody(context.Background(), rawR, "model", 1, kiro.KiroRequestContext{})
	require.NoError(t, body.Close())
	done := make(chan error, 1)
	go func() { _, err := rawW.Write([]byte("x")); done <- err }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("closing converted stream did not close stalled upstream")
	}
}
