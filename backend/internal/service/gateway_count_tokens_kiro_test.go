//go:build unit

package service

import (
	"bytes"
	"context"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropictokenizer"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Kiro 直连没有 count_tokens 接口，本地估算又明显偏小，所以直接返回 404，
// 让客户端用自己的估算（与 Antigravity 一致）。
func TestGatewayServiceForwardCountTokensKiroDirectReturnsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{
		"model":"claude-sonnet-4-6",
		"system":[{"type":"text","text":"You are helpful."}],
		"messages":[{"role":"user","content":"hello"}]
	}`)
	accounts := []*Account{
		{ID: 101, Platform: PlatformKiro, Type: AccountTypeOAuth},
		{ID: 102, Platform: PlatformKiro, Type: AccountTypeAPIKey},
	}

	for _, account := range accounts {
		t.Run(string(account.Type), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)

			// 没有凭据和上游依赖：请求一旦离开本地分支，这里就会失败。
			err = (&GatewayService{}).ForwardCountTokens(context.Background(), c, account, parsed)

			require.NoError(t, err, "404 不应被 handler 当成错误记录")
			require.Equal(t, http.StatusNotFound, rec.Code)
			require.Contains(t, rec.Body.String(), "not_found_error")
		})
	}
}

// 图片按尺寸估算的逻辑仍用于请求的 input 预估，直接测估算函数。
func TestEstimateKiroInputTokensImageUsesVisualEstimate(t *testing.T) {
	dataURL := kiroPNGDataURL(t, 512, 512, color.RGBA{R: 25, G: 50, B: 75, A: 255})
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"` + dataURL + `"}}]}]}`)
	got := estimateKiroInputTokens(context.Background(), anthropictokenizer.AlgorithmOfficial, body)
	require.GreaterOrEqual(t, got, 350)
	require.Less(t, got, len(dataURL)/2, "base64 payload must not be counted as text")
}

func TestGatewayServiceForwardCountTokensKiroAPIKeyBaseURLUsesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"input_tokens":77}`)),
	}}
	svc := &GatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled:           false,
			AllowInsecureHTTP: true,
		}}},
		httpUpstream:        upstream,
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}
	account := &Account{
		ID:          104,
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "relay-key",
			"base_url": "http://kiro-relay.example",
		},
	}

	err = svc.ForwardCountTokens(context.Background(), c, account, parsed)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"input_tokens":77}`, rec.Body.String())
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "http://kiro-relay.example/v1/messages/count_tokens?beta=true", upstream.lastReq.URL.String())
	require.Equal(t, "relay-key", upstream.lastReq.Header.Get("x-api-key"))
}
