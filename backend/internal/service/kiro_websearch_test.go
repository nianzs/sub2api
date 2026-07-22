//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildKiroWebSearchMCPRequest_UsesUnderscoredMetaKeys(t *testing.T) {
	req := buildKiroWebSearchMCPRequest("golang concurrency")

	body, err := json.Marshal(req)
	require.NoError(t, err)

	require.Equal(t, "tools/call", gjson.GetBytes(body, "method").String())
	require.Equal(t, "web_search", gjson.GetBytes(body, "params.name").String())
	require.Equal(t, "golang concurrency", gjson.GetBytes(body, "params.arguments.query").String())
	require.True(t, gjson.GetBytes(body, "params.arguments._meta._isValid").Bool())
	require.Equal(t, "query", gjson.GetBytes(body, "params.arguments._meta._activePath.0").String())
	require.Equal(t, "query", gjson.GetBytes(body, "params.arguments._meta._completedPaths.0.0").String())
	require.False(t, gjson.GetBytes(body, "params.arguments._meta.isValid").Exists())
	require.False(t, gjson.GetBytes(body, "params.arguments._meta.activePath").Exists())
	require.False(t, gjson.GetBytes(body, "params.arguments._meta.completedPaths").Exists())
}

func TestWriteAnthropicMessageStart_UsesCacheEmulationUsage(t *testing.T) {
	var out bytes.Buffer
	err := writeAnthropicMessageStart(&out, "msg_test", "claude-sonnet-4-6", 100, &kiroCacheEmulationUsage{
		InputTokens:              25,
		CacheCreationInputTokens: 75,
		CacheReadInputTokens:     0,
	})
	require.NoError(t, err)
	body := out.String()
	require.Contains(t, body, `"input_tokens":25`)
	require.Contains(t, body, `"cache_creation_input_tokens":75`)
	require.Contains(t, body, `"cache_read_input_tokens":0`)
}

func TestOpenKiroAnthropicStreamResponsePropagatesWebSearchFailoverBeforeSynthetic200(t *testing.T) {
	body := kiroCacheRequestBody("stream failover", false)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	payload["tools"] = []any{map[string]any{
		"name":         "web_search",
		"description":  "Search the web",
		"input_schema": map[string]any{"type": "object"},
	}}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	endpoint := kiropkg.BuildMcpEndpoint("us-east-1")
	kiroWebSearchDescCache.Store(endpoint, "Search the web")
	t.Cleanup(func() { kiroWebSearchDescCache.Delete(endpoint) })

	upstream := &queuedHTTPUpstream{responses: []*http.Response{
		newJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"{\"results\":[]}"}]}}`),
		newJSONResponse(http.StatusPaymentRequired, `{"message":"payment required"}`),
	}}
	svc := &GatewayService{
		httpUpstream:        upstream,
		kiroCooldownStore:   &stubKiroCooldownStore{},
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}
	account := &Account{
		ID:          992,
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "ksk_test", "api_region": "us-east-1"},
	}

	resp, _, openErr := svc.openKiroAnthropicStreamResponse(context.Background(), account, nil, body, "claude-sonnet-4-6", "claude-sonnet-4-6", nil, kiroCacheGroup(1))
	if resp != nil {
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, openErr, &failoverErr)
	require.Equal(t, http.StatusPaymentRequired, failoverErr.StatusCode)
	require.Nil(t, resp, "failover must surface before a synthetic HTTP 200 is returned")
	require.Len(t, upstream.requests, 2)
}

func TestOpenKiroAnthropicStreamResponsePropagatesLaterWebSearchFailoverBeforeSynthetic200(t *testing.T) {
	body := kiroCacheRequestBody("later stream failover", false)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	payload["tools"] = []any{map[string]any{
		"name":         "web_search",
		"description":  "Search the web",
		"input_schema": map[string]any{"type": "object"},
	}}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	endpoint := kiropkg.BuildMcpEndpoint("us-east-1")
	kiroWebSearchDescCache.Store(endpoint, "Search the web")
	t.Cleanup(func() { kiroWebSearchDescCache.Delete(endpoint) })

	firstModelBody := bytes.NewBuffer(nil)
	_, _ = firstModelBody.Write(buildKiroEventStreamFrame(t, "toolUseEvent", map[string]any{
		"toolUseEvent": map[string]any{
			"toolUseId": "srvtoolu_next",
			"name":      "remote_web_search",
			"input":     `{"query":"refined query"}`,
			"stop":      true,
		},
	}))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{
		newJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"{\"results\":[]}"}]}}`),
		{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(firstModelBody)},
		newJSONResponse(http.StatusOK, `{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"{\"results\":[]}"}]}}`),
		newJSONResponse(http.StatusPaymentRequired, `{"message":"payment required"}`),
	}}
	svc := &GatewayService{
		httpUpstream:        upstream,
		kiroCooldownStore:   &stubKiroCooldownStore{},
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}
	account := &Account{
		ID:          993,
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "ksk_test", "api_region": "us-east-1"},
	}

	resp, _, openErr := svc.openKiroAnthropicStreamResponse(context.Background(), account, nil, body, "claude-sonnet-4-6", "claude-sonnet-4-6", nil, kiroCacheGroup(1))
	if resp != nil {
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, openErr, &failoverErr)
	require.Equal(t, http.StatusPaymentRequired, failoverErr.StatusCode)
	require.Nil(t, resp, "later-iteration failover must surface before a synthetic HTTP 200 is returned")
	require.Len(t, upstream.requests, 4)
}
