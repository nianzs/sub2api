//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
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

// Responses / Chat Completions 的 Kiro 分支必须把可换号错误交回 handler，
// 而不是自己写 502：否则粘性会话绑定的号一进冷却，后续请求全部 8ms 失败。
func TestKiroCompatEndpointsReturnFailoverErrorInsteadOf502(t *testing.T) {
	gin.SetMode(gin.TestMode)

	endpoints := []struct {
		name    string
		path    string
		body    string
		forward func(*GatewayService, *gin.Context, *Account, []byte, *ParsedRequest) (*ForwardResult, error)
	}{
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"claude-sonnet-4-6","stream":true,"input":[{"role":"user","content":"hi"}]}`,
			forward: func(s *GatewayService, c *gin.Context, a *Account, b []byte, p *ParsedRequest) (*ForwardResult, error) {
				return s.ForwardAsResponses(context.Background(), c, a, b, p)
			},
		},
		{
			name: "chat_completions",
			path: "/v1/chat/completions",
			body: `{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			forward: func(s *GatewayService, c *gin.Context, a *Account, b []byte, p *ParsedRequest) (*ForwardResult, error) {
				return s.ForwardAsChatCompletions(context.Background(), c, a, b, p)
			},
		},
	}

	cases := []struct {
		name          string
		cooldown      error
		upstream      *http.Response
		wantStatus    int
		wantUpstreams int
	}{
		{
			name:          "cooldown_gate",
			cooldown:      kirocooldown.NewError(30*time.Second, kirocooldown.CooldownReason429),
			wantStatus:    http.StatusTooManyRequests,
			wantUpstreams: 0,
		},
		{
			name: "quota_exhausted_402",
			upstream: &http.Response{
				StatusCode: http.StatusPaymentRequired,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"message":"You have reached the limit.","reason":"MONTHLY_REQUEST_COUNT"}`)),
			},
			wantStatus:    http.StatusPaymentRequired,
			wantUpstreams: 1,
		},
	}

	for _, ep := range endpoints {
		for _, tc := range cases {
			t.Run(ep.name+"/"+tc.name, func(t *testing.T) {
				resetKiroCacheTracker()
				upstream := &httpUpstreamRecorder{}
				if tc.upstream != nil {
					upstream.responses = []*http.Response{tc.upstream}
				}
				svc := &GatewayService{
					httpUpstream:        upstream,
					kiroCooldownStore:   &stubKiroCooldownStore{checkErr: tc.cooldown},
					tlsFPProfileService: &TLSFingerprintProfileService{},
				}
				account := &Account{
					ID:          7897,
					Name:        "kiro-failover",
					Platform:    PlatformKiro,
					Type:        AccountTypeAPIKey,
					Concurrency: 1,
					Credentials: map[string]any{"api_key": "ksk_test"},
				}
				body := []byte(ep.body)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, ep.path, bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")

				result, err := ep.forward(svc, c, account, body, &ParsedRequest{Group: kiroCacheGroup(1)})

				require.Nil(t, result)
				var failoverErr *UpstreamFailoverError
				require.True(t, errors.As(err, &failoverErr), "want *UpstreamFailoverError, got %T: %v", err, err)
				require.Equal(t, tc.wantStatus, failoverErr.StatusCode)
				require.False(t, c.Writer.Written(), "must not write a client response before failover; got %d %s", rec.Code, rec.Body.String())
				require.Len(t, upstream.requests, tc.wantUpstreams)
			})
		}
	}
}
