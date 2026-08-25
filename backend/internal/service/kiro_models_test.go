package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchKiroUpstreamModelsSendsProfileArnAndParsesIDs(t *testing.T) {
	account := &Account{
		ID:       801,
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "kiro-access-token",
			"auth_method":  "idc",
			"provider":     "BuilderId",
			"region":       "us-east-1",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/ListAvailableModels", r.URL.Path)
		require.Equal(t, "AI_EDITOR", r.URL.Query().Get("origin"))
		require.Equal(t, kiroBuilderIDProfileARN, r.URL.Query().Get("profileArn"))
		require.Contains(t, r.Header.Get("User-Agent"), "KiroIDE-0.12.155-")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"defaultModel": map[string]any{"modelId": "auto"},
			"models": []map[string]any{
				{"modelId": "claude-sonnet-4.6"},
				{"modelId": "claude-opus-4.6"},
				{"modelId": "auto"},
			},
		})
	}))
	defer server.Close()
	setKiroUsageTestEndpoint(t, server.URL)

	svc := &AccountTestService{}
	models, err := svc.fetchKiroUpstreamModels(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"auto", "claude-sonnet-4.6", "claude-opus-4.6"}, models)
}
