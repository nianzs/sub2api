//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// Kiro API Key 账号现已直连 AWS(q.{region}.amazonaws.com),与 OAuth 账号同路径,
// 不再走 Anthropic 兼容反代(base_url + /v1/messages)。
func TestAccountTestService_KiroAPIKeyDirectAWSEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := newTestContext()

	account := &Account{
		ID:          19,
		Name:        "kiro-apikey-test",
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "kiro-api-key",
			"model_mapping": map[string]any{
				"claude-sonnet-4-6": "claude-sonnet-4-6",
			},
		},
	}
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	upstream := &queuedHTTPUpstream{
		responses: []*http.Response{
			newJSONResponse(http.StatusUnauthorized, `{"message":"invalid token"}`),
		},
	}
	svc := &AccountTestService{
		accountRepo:         repo,
		httpUpstream:        upstream,
		cfg:                 &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}

	err := svc.TestAccountConnection(ctx, account.ID, "claude-sonnet-4-6", "", AccountTestModeDefault)
	require.Error(t, err)
	require.Len(t, upstream.requests, 1)

	req := upstream.requests[0]
	// 直连 AWS Q endpoint,默认 us-east-1
	require.Equal(t, "q.us-east-1.amazonaws.com", req.URL.Host)
	require.Equal(t, "/generateAssistantResponse", req.URL.Path)
	// API Key 走 Bearer + 小写 tokentype: API_KEY(对齐 kiro.rs)
	require.Equal(t, "Bearer kiro-api-key", req.Header.Get("Authorization"))
	require.Equal(t, []string{"API_KEY"}, req.Header["tokentype"])
	require.Empty(t, req.Header.Get("x-api-key"))
}

// 缺少 base_url 不再报错:Kiro API Key 账号直连 AWS,base_url 与其无关。
func TestAccountTestService_KiroAPIKeyWithoutBaseURLDirectAWS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := newTestContext()

	account := &Account{
		ID:          20,
		Name:        "kiro-apikey-no-base-url",
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "kiro-api-key",
		},
	}
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	upstream := &queuedHTTPUpstream{
		responses: []*http.Response{
			newJSONResponse(http.StatusUnauthorized, `{"message":"invalid token"}`),
		},
	}
	svc := &AccountTestService{
		accountRepo:         repo,
		httpUpstream:        upstream,
		cfg:                 &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}

	err := svc.TestAccountConnection(ctx, account.ID, "claude-sonnet-4-6", "", AccountTestModeDefault)
	// 不再因缺少 base_url 报错;直连 AWS 后因 mock 返回 401 而报上游错误
	require.Error(t, err)
	require.NotContains(t, err.Error(), "Base URL")
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "q.us-east-1.amazonaws.com", upstream.requests[0].URL.Host)
}
