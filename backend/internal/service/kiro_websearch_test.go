//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestWriteAnthropicMessageCompletion_EmitsRealUsageAndTerminalEvent(t *testing.T) {
	var out bytes.Buffer
	err := writeAnthropicMessageCompletion(&out, &kiropkg.StreamResult{
		StopReason:   "stop_sequence",
		StopSequence: "<STOP>",
		Usage: kiropkg.Usage{
			InputTokens:                0,
			InputTokensReported:        true,
			OutputTokens:               7,
			CacheReadInputTokens:       120,
			CacheCreationInputTokens:   2,
			CacheCreation5mInputTokens: 2,
			KiroCredits:                0.17,
		},
	})

	require.NoError(t, err)
	body := out.String()
	require.Contains(t, body, `event: message_delta`)
	require.Contains(t, body, `"input_tokens":0`)
	require.Contains(t, body, `"cache_read_input_tokens":120`)
	require.Contains(t, body, `"cache_creation_input_tokens":2`)
	require.Contains(t, body, `"_sub2api_kiro_credits":0.17`)
	require.Contains(t, body, `"stop_sequence":"<STOP>"`)
	require.Contains(t, body, `event: message_stop`)
}

func TestAddKiroStreamUsageAggregatesWebSearchIterations(t *testing.T) {
	total := kiropkg.Usage{}
	addKiroStreamUsage(&total, kiropkg.Usage{
		InputTokens:              10,
		InputTokensReported:      true,
		OutputTokens:             2,
		TotalTokens:              20,
		CacheReadInputTokens:     8,
		CacheCreationInputTokens: 2,
		KiroCredits:              0.17,
	})
	addKiroStreamUsage(&total, kiropkg.Usage{
		InputTokens:                4,
		OutputTokens:               3,
		TotalTokens:                12,
		CacheReadInputTokens:       5,
		CacheCreationInputTokens:   1,
		CacheCreation5mInputTokens: 1,
		KiroCredits:                0.23,
	})

	require.Equal(t, 14, total.InputTokens)
	require.True(t, total.InputTokensReported)
	require.Equal(t, 5, total.OutputTokens)
	require.Equal(t, 32, total.TotalTokens)
	require.Equal(t, 13, total.CacheReadInputTokens)
	require.Equal(t, 3, total.CacheCreationInputTokens)
	require.Equal(t, 1, total.CacheCreation5mInputTokens)
	require.InDelta(t, 0.4, total.KiroCredits, 0.000001)
}

func TestAddKiroStreamUsageResolvesFallbackPerIteration(t *testing.T) {
	total := kiropkg.Usage{}
	reported := resolveKiroInputUsage(kiropkg.Usage{InputTokens: 100, InputTokensReported: true}, 100)
	omitted := resolveKiroInputUsage(kiropkg.Usage{}, 150)

	addKiroStreamUsage(&total, reported)
	addKiroStreamUsage(&total, omitted)

	require.Equal(t, 250, total.InputTokens)
	require.True(t, total.InputTokensReported)
}

func TestKiroWebSearchFailedUpstreamDoesNotPopulateCacheTracker(t *testing.T) {
	body := kiroCacheRequestBody("failed web search", false)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	payload["tools"] = []any{map[string]any{
		"name":         "web_search",
		"description":  "Search the web",
		"input_schema": map[string]any{"type": "object"},
	}}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		run  func(*GatewayService, *Account, *Group) error
	}{
		{
			name: "streaming",
			run: func(svc *GatewayService, account *Account, group *Group) error {
				var out bytes.Buffer
				return svc.streamKiroWebSearchAsAnthropic(context.Background(), account, group, body, "claude-sonnet-4-6", "claude-sonnet-4-6", "ksk_test", nil, &out)
			},
		},
		{
			name: "non-streaming",
			run: func(svc *GatewayService, account *Account, group *Group) error {
				_, err := svc.executeKiroWebSearch(context.Background(), account, group, body, "claude-sonnet-4-6", "claude-sonnet-4-6", "ksk_test", nil)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetKiroCacheTracker()
			t.Cleanup(resetKiroCacheTracker)
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
				ID:          991,
				Platform:    PlatformKiro,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "ksk_test", "api_region": "us-east-1"},
			}

			err := tc.run(svc, account, kiroCacheGroup(1))
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusPaymentRequired, failoverErr.StatusCode)
			require.Len(t, upstream.requests, 2)

			globalKiroCacheTracker.mu.Lock()
			defer globalKiroCacheTracker.mu.Unlock()
			require.Empty(t, globalKiroCacheTracker.entries)
		})
	}
}
