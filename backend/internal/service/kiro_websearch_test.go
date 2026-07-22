//go:build unit

package service

import (
	"bytes"
	"encoding/json"
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
