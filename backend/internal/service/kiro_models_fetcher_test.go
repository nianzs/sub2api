//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func newKiroModelSyncService(upstream *queuedHTTPUpstream, repo AccountRepository) *AccountTestService {
	return &AccountTestService{
		accountRepo:         repo,
		cfg:                 upstreamModelSyncTestConfig(),
		kiroTokenProvider:   NewKiroTokenProvider(nil, nil, nil),
		httpUpstream:        upstream,
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}
}

func TestKiroUpstreamModelSync_DirectOAuthAccountListsAvailableModels(t *testing.T) {
	account := &Account{
		ID:          41,
		Name:        "kiro-oauth",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "t",
			"api_region":   "us-east-1",
			"profile_arn":  "arn:aws:codewhisperer:us-east-1:1:profile/P",
		},
	}
	upstream := &queuedHTTPUpstream{
		responses: []*http.Response{
			newJSONResponse(http.StatusOK, `{"models":[{"id":"claude-opus-4.8","name":"Claude Opus 4.8"},{"id":"gpt-5.6-sol"}],"defaultModel":{"id":"claude-sonnet-4.6"}}`),
		},
	}
	repo := &upstreamModelMetadataRepoStub{}
	svc := newKiroModelSyncService(upstream, repo)

	catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-opus-4.8", "claude-sonnet-4.6", "gpt-5.6-sol"}, catalog.Models)
	require.Empty(t, catalog.Warnings)
	require.Empty(t, catalog.Metadata)
	// Kiro 走 Anthropic 协议，不消费能力快照，不应写入 upstream_model_metadata。
	require.Nil(t, repo.updates)

	require.Len(t, upstream.requests, 1)
	req := upstream.requests[0]
	require.Equal(t, "q.us-east-1.amazonaws.com", req.URL.Host)
	require.Equal(t, http.MethodGet, req.Method)
	require.Equal(t, "AI_EDITOR", req.URL.Query().Get("origin"))
	require.Equal(t, "arn:aws:codewhisperer:us-east-1:1:profile/P", req.URL.Query().Get("profileArn"))
	require.Equal(t, "Bearer t", req.Header.Get("Authorization"))
}

func TestKiroUpstreamModelSync_ProbesPathCasing(t *testing.T) {
	account := &Account{
		ID:          42,
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "t"},
	}
	upstream := &queuedHTTPUpstream{
		responses: []*http.Response{
			newJSONResponse(http.StatusNotFound, `{"message":"not found"}`),
			newJSONResponse(http.StatusOK, `{"models":[{"id":"claude-opus-4.8"}]}`),
		},
	}
	svc := newKiroModelSyncService(upstream, &upstreamModelMetadataRepoStub{})

	catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-opus-4.8"}, catalog.Models)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "/listAvailableModels", upstream.requests[0].URL.Path)
	require.Equal(t, "/ListAvailableModels", upstream.requests[1].URL.Path)
}

func TestKiroUpstreamModelSync_FollowsNextToken(t *testing.T) {
	account := &Account{
		ID:          43,
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "t"},
	}
	upstream := &queuedHTTPUpstream{
		responses: []*http.Response{
			newJSONResponse(http.StatusOK, `{"models":[{"id":"a"}],"nextToken":"tk"}`),
			newJSONResponse(http.StatusOK, `{"models":[{"id":"b"}]}`),
		},
	}
	svc := newKiroModelSyncService(upstream, &upstreamModelMetadataRepoStub{})

	catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, catalog.Models)
	require.Len(t, upstream.requests, 2)
	require.Empty(t, upstream.requests[0].URL.Query().Get("nextToken"))
	require.Equal(t, "tk", upstream.requests[1].URL.Query().Get("nextToken"))
}

func TestKiroUpstreamModelSync_DirectAPIKeyAccountUsesCredentialBearer(t *testing.T) {
	account := &Account{
		ID:          44,
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "ksk_x"},
	}
	upstream := &queuedHTTPUpstream{
		responses: []*http.Response{
			newJSONResponse(http.StatusOK, `{"models":[{"id":"claude-opus-4.8"}]}`),
		},
	}
	svc := newKiroModelSyncService(upstream, &upstreamModelMetadataRepoStub{})

	catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-opus-4.8"}, catalog.Models)

	require.Len(t, upstream.requests, 1)
	req := upstream.requests[0]
	require.Equal(t, "Bearer ksk_x", req.Header.Get("Authorization"))
	require.Equal(t, []string{"API_KEY"}, headerValuesEqualFold(req.Header, "TokenType"))
	require.Empty(t, req.URL.Query().Get("profileArn"))
}

func TestKiroUpstreamModelSync_RelayAccountUsesAnthropicModelsEndpoint(t *testing.T) {
	account := &Account{
		ID:          45,
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "ksk_x",
			"base_url": "https://relay.example.com",
		},
	}
	upstream := &queuedHTTPUpstream{
		responses: []*http.Response{
			newJSONResponse(http.StatusOK, `{"data":[{"id":"claude-sonnet-4-5"}]}`),
		},
	}
	svc := newKiroModelSyncService(upstream, &upstreamModelMetadataRepoStub{})

	catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-sonnet-4-5"}, catalog.Models)

	require.Len(t, upstream.requests, 1)
	req := upstream.requests[0]
	require.Equal(t, "https://relay.example.com/v1/models", req.URL.String())
	require.Equal(t, "ksk_x", req.Header.Get("x-api-key"))
	require.Empty(t, req.Header.Get("Authorization"))
}
