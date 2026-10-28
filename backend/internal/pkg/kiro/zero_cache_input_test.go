package kiro

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCachedZeroInputSurvivesNonStreamingEstimateFallback(t *testing.T) {
	stream := bytes.NewBuffer(buildEventStreamFrame(t, "assistantResponseEvent", map[string]any{
		"assistantResponseEvent": map[string]any{"content": "OK"},
	}))
	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-sonnet-4.5", KiroRequestContext{
		EstimatedInputTokens: 10000,
		CacheEmulationUsage:  &Usage{InputTokens: 0, CacheReadInputTokens: 10000},
	})
	require.NoError(t, err)
	require.Zero(t, result.Usage.InputTokens)
	require.Equal(t, int64(0), gjson.GetBytes(result.ResponseBody, "usage.input_tokens").Int())
	require.Equal(t, 10000, result.Usage.CacheReadInputTokens)
}
