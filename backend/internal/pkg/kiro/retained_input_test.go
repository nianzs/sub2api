package kiro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRetainedInputStopsGrowingWithDiscardedHistory(t *testing.T) {
	var previous []byte
	for _, n := range []int{160, 320} {
		messages := []any{}
		for i := 0; i < n; i++ {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			messages = append(messages, map[string]any{"role": role, "content": strings.Repeat("audit synthetic context line 12345. ", 600)})
		}
		messages = append(messages, map[string]any{"role": "user", "content": "next step"})
		raw, err := json.Marshal(map[string]any{"model": "claude-opus-5-5-thinking", "messages": messages})
		require.NoError(t, err)
		built, err := BuildKiroPayloadWithContext(raw, "claude-opus-5.5", "", "AI_EDITOR", nil)
		require.NoError(t, err)
		require.NotEmpty(t, built.Context.RetainedInputBody)
		require.LessOrEqual(t, len(built.Payload), kiroMaxPayloadBytes)
		require.Less(t, len(built.Context.RetainedInputBody), len(raw)/2)
		if previous != nil {
			require.JSONEq(t, string(previous), string(built.Context.RetainedInputBody))
		}
		previous = built.Context.RetainedInputBody
	}
}
func TestRetainedInputPreservesToolsImagesAndTTL(t *testing.T) {
	user := KiroUserInputMessage{Content: "keep", Images: []KiroImage{{Format: "png", Source: KiroImageSource{Bytes: "image-data"}}}, UserInputMessageContext: &KiroUserInputMessageContext{ToolResults: []KiroToolResult{{ToolUseID: "call1", Content: []KiroTextContent{{Text: "retained result"}}, Status: "success"}}, Tools: []KiroToolWrapper{{ToolSpecification: KiroToolSpecification{Name: "read", Description: "read file", InputSchema: KiroInputSchema{JSON: map[string]any{"type": "object"}}}}}}}
	payload := KiroPayload{ConversationState: KiroConversationState{History: []KiroHistoryMessage{{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: "calling", ToolUses: []KiroToolUse{{ToolUseID: "call1", Name: "read", Input: map[string]any{"path": "retained"}}}}}}, CurrentMessage: KiroCurrentMessage{UserInputMessage: user}}}
	body, err := retainedKiroInputBody(payload, []byte(`{"model":"claude-opus-5-5-thinking","system":[{"type":"text","text":"DROPPED_PRIVATE_CONTENT","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`))
	require.NoError(t, err)
	require.NotContains(t, string(body), "DROPPED_PRIVATE_CONTENT")
	for _, want := range []string{"retained result", "call1", "read file", "image/png", "image-data", "1h"} {
		require.Contains(t, string(body), want)
	}
}
func TestShortInputKeepsExistingMetering(t *testing.T) {
	built, err := BuildKiroPayloadWithContext([]byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hello"}]}`), "claude-sonnet-4.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	require.Empty(t, built.Context.RetainedInputBody)
}

func TestRetainedInputUsesShrunkCurrentToolResult(t *testing.T) {
	output := strings.Repeat("z", kiroMaxPayloadBytes+64*1024)
	raw := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"read"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"a.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":%q}]}]}`, output))
	built, err := BuildKiroPayloadWithContext(raw, "claude-sonnet-4.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	sent := gjson.GetBytes(built.Payload, "conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0.content.0.text").String()
	var body map[string]any
	require.NoError(t, json.Unmarshal(built.Context.RetainedInputBody, &body))
	messages := body["messages"].([]any)
	blocks := messages[len(messages)-1].(map[string]any)["content"].([]any)
	var metered string
	for _, block := range blocks {
		m := block.(map[string]any)
		if m["type"] == "tool_result" {
			metered = m["content"].([]any)[0].(map[string]any)["text"].(string)
		}
	}
	require.NotEmpty(t, metered)
	require.Equal(t, sent, metered)
	require.Less(t, len(metered), len(output))
}

func TestRetainedInputDoesNotUpgradeMixedCacheTTL(t *testing.T) {
	payload := KiroPayload{ConversationState: KiroConversationState{CurrentMessage: KiroCurrentMessage{UserInputMessage: KiroUserInputMessage{Content: "retained"}}}}
	body, err := retainedKiroInputBody(payload, []byte(`{"model":"claude-sonnet-4.5","system":[{"type":"text","text":"old","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"recent","cache_control":{"type":"ephemeral","ttl":"5m"}}]}`))
	require.NoError(t, err)
	require.Equal(t, "5m", gjson.GetBytes(body, "messages.0.content.0.cache_control.ttl").String())
}

func TestCachedZeroInputSurvivesNonStreamingEstimateFallback(t *testing.T) {
	stream := bytes.NewBuffer(buildEventStreamFrame(t, "assistantResponseEvent", map[string]any{"assistantResponseEvent": map[string]any{"content": "OK"}}))
	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-sonnet-4.5", KiroRequestContext{EstimatedInputTokens: 10000, CacheEmulationUsage: &Usage{InputTokens: 0, CacheReadInputTokens: 10000}})
	require.NoError(t, err)
	require.Zero(t, result.Usage.InputTokens)
	require.Equal(t, int64(0), gjson.GetBytes(result.ResponseBody, "usage.input_tokens").Int())
	require.Equal(t, 10000, result.Usage.CacheReadInputTokens)
}

func TestRetainedInputIgnoresNestedCacheControl(t *testing.T) {
	payload := KiroPayload{ConversationState: KiroConversationState{CurrentMessage: KiroCurrentMessage{UserInputMessage: KiroUserInputMessage{Content: "retained"}}}}
	for _, tc := range []struct {
		name, original, expectedTTL string
		marked                      bool
	}{
		{"schema-only", `{"tools":[{"name":"read","input_schema":{"cache_control":{"type":"ephemeral","ttl":"1h"}}}]}`, "", false},
		{"argument-only", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","input":{"cache_control":{"type":"ephemeral","ttl":"1h"}}}]}]}`, "", false},
		{"valid-tool-with-schema", `{"tools":[{"name":"read","cache_control":{"type":"ephemeral","ttl":"1h"},"input_schema":{"cache_control":{"type":"ephemeral","ttl":"5m"}}}]}`, "1h", true},
		{"invalid-control-type", `{"system":[{"type":"text","text":"old","cache_control":{"type":"other","ttl":"1h"}}]}`, "", false},
		{"normalized-control", `{"messages":[{"role":"user","content":[{"type":"text","text":"old","cache_control":{"type":" EPHEMERAL ","ttl":" 1H "}}]}]}`, "1h", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := retainedKiroInputBody(payload, []byte(tc.original))
			require.NoError(t, err)
			require.Equal(t, tc.marked, gjson.GetBytes(body, "messages.0.content.0.cache_control").Exists())
			require.Equal(t, tc.expectedTTL, gjson.GetBytes(body, "messages.0.content.0.cache_control.ttl").String())
		})
	}
}
