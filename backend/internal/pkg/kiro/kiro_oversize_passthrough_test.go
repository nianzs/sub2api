package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 本地不再做整包体积预检或裁历史：超大请求原样发给 Kiro，由上游判定是否太长
// （上游 400 会在 service 层转成 "prompt is too long"）。与 kiro.rs 一致。

// 模拟 Claude Code 的长会话：
// assistant tool_use -> user tool_result
func makeLongToolConversation() []byte {
	messages := []any{
		map[string]any{
			"role":    "user",
			"content": "Start the coding task.",
		},
	}

	output := strings.Repeat("R", 120*1024)

	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("toolu_%04d", i)

		messages = append(messages,
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    id,
						"name":  "read_file",
						"input": map[string]any{"path": "test.txt"},
					},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": id,
						"content":     output,
					},
				},
			},
		)
	}

	raw, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 1024,
		"messages":   messages,
	})
	return raw
}

// 检查 Kiro Payload 中是否存在孤立的 tool_result。
func countOrphanedToolResults(payload []byte) int {
	previousTools := map[string]bool{}
	orphans := 0

	history := gjson.GetBytes(
		payload, "conversationState.history",
	).Array()

	for _, entry := range history {
		if entry.Get("assistantResponseMessage").Exists() {
			previousTools = map[string]bool{}

			tools := entry.Get(
				"assistantResponseMessage.toolUses",
			).Array()

			for _, tool := range tools {
				previousTools[tool.Get("toolUseId").String()] = true
			}
		}

		if entry.Get("userInputMessage").Exists() {
			results := entry.Get(
				"userInputMessage.userInputMessageContext.toolResults",
			).Array()

			for _, result := range results {
				id := result.Get("toolUseId").String()
				if !previousTools[id] {
					orphans++
				}
			}

			previousTools = map[string]bool{}
		}
	}

	currentResults := gjson.GetBytes(
		payload,
		"conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults",
	).Array()

	for _, result := range currentResults {
		if !previousTools[result.Get("toolUseId").String()] {
			orphans++
		}
	}

	return orphans
}

// 12 轮各 120KiB 的工具结果（约 1.4MB，远超旧的 900KiB 阈值）：所有轮次、
// 工具配对和内容都原样保留，不插入任何截断说明。
func TestBuildKiroPayloadPassesOversizedConversationThrough(t *testing.T) {
	built, err := BuildKiroPayloadWithContext(makeLongToolConversation(), "claude-sonnet-4.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)

	payload := string(built.Payload)
	require.Greater(t, len(built.Payload), 12*120*1024)
	for i := 0; i < 12; i++ {
		require.Contains(t, payload, fmt.Sprintf("toolu_%04d", i))
	}
	require.Equal(t, 0, countOrphanedToolResults(built.Payload))
	require.NotContains(t, payload, "truncated")
	require.NotContains(t, payload, "omitted")

	for _, result := range gjson.Get(payload, "conversationState.history.#.userInputMessage.userInputMessageContext.toolResults|@flatten").Array() {
		require.Len(t, result.Get("content.0.text").String(), 120*1024)
	}
	current := gjson.Get(payload, "conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0")
	require.Equal(t, "toolu_0011", current.Get("toolUseId").String())
	require.Len(t, current.Get("content.0.text").String(), 120*1024)
}

// 当前消息本身就很大时也原样发送，不再静默缩短正文。
func TestBuildKiroPayloadPassesOversizedCurrentMessageThrough(t *testing.T) {
	huge := strings.Repeat("X", 1200*1024)
	body, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 1024,
		"messages": []any{
			map[string]any{"role": "user", "content": "start"},
			map[string]any{"role": "assistant", "content": "ok"},
			map[string]any{"role": "user", "content": huge},
		},
	})
	require.NoError(t, err)

	built, err := BuildKiroPayloadWithContext(body, "claude-sonnet-4.5", "", "AI_EDITOR", nil)
	require.NoError(t, err)
	require.Equal(t, huge, gjson.GetBytes(built.Payload, "conversationState.currentMessage.userInputMessage.content").String())
	require.True(t, gjson.GetBytes(built.Payload, `conversationState.history.#(userInputMessage.content=="start")`).Exists())
}
