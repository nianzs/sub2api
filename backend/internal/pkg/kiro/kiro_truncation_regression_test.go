package kiro

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

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

func TestKiroTruncationKeepsToolPairingRegression(t *testing.T) {
	raw := makeLongToolConversation()

	result, err := BuildKiroPayloadWithContext(
		raw,
		"claude-sonnet-4.5",
		"",
		"AI_EDITOR",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	orphans := countOrphanedToolResults(result.Payload)

	t.Logf(
		"Payload bytes: %d, orphaned tool_results: %d",
		len(result.Payload), orphans,
	)

	if orphans > 0 {
		t.Fatalf(
			"BUG REPRODUCED: %d orphaned tool_result(s)",
			orphans,
		)
	}
}

// 测试多轮并行工具调用在历史截断后是否仍然配对。
func TestKiroTruncationParallelToolCalls(t *testing.T) {
	messages := []any{
		map[string]any{
			"role":    "user",
			"content": "Start coding.",
		},
	}

	for i := 0; i < 12; i++ {
		uses := make([]any, 0, 2)
		results := make([]any, 0, 2)

		for j := 0; j < 2; j++ {
			id := fmt.Sprintf("toolu_%02d_%d", i, j)

			uses = append(uses, map[string]any{
				"type": "tool_use",
				"id":   id,
				"name": "read_file",
				"input": map[string]any{
					"path": fmt.Sprintf("file-%d-%d.txt", i, j),
				},
			})

			results = append(results, map[string]any{
				"type":        "tool_result",
				"tool_use_id": id,
				"content":     strings.Repeat("R", 55*1024),
			})
		}

		messages = append(messages,
			map[string]any{
				"role":    "assistant",
				"content": uses,
			},
			map[string]any{
				"role":    "user",
				"content": results,
			},
		)
	}

	body, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 1024,
		"messages":   messages,
	})
	if err != nil {
		t.Fatal(err)
	}

	built, err := BuildKiroPayloadWithContext(
		body, "claude-sonnet-4.5", "", "AI_EDITOR", nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	assertCandidatePairsAndRecentHistory(t, built.Payload)
	orphans := countOrphanedToolResults(built.Payload)
	currentResults := gjson.GetBytes(
		built.Payload,
		"conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults",
	).Array()

	t.Logf(
		"Payload=%d bytes, orphans=%d, current_results=%d",
		len(built.Payload), orphans, len(currentResults),
	)

	if orphans != 0 {
		t.Fatalf("Found %d orphaned tool_results", orphans)
	}

	ids := map[string]bool{}
	for _, result := range currentResults {
		ids[result.Get("toolUseId").String()] = true
	}
	if len(ids) != 2 || !ids["toolu_11_0"] || !ids["toolu_11_1"] {
		t.Fatal("latest parallel result IDs changed or disappeared")
	}
	if len(currentResults) != 2 {
		t.Fatalf("Expected 2 current tool results, got %d", len(currentResults))
	}
	if len(built.Payload) > kiroMaxPayloadBytes {
		t.Fatalf("Payload exceeds Kiro size limit")
	}
}

func TestKiroTruncationBoundarySweep(t *testing.T) {
	sizes := []int{
		24, 48, 64, 72, 80, 88, 96,
		104, 112, 120, 128, 144, 160, 192,
	}

	for _, sizeKiB := range sizes {
		t.Run(fmt.Sprintf("%dKiB", sizeKiB), func(t *testing.T) {
			messages := []any{
				map[string]any{
					"role":    "user",
					"content": "Start coding.",
				},
			}

			for i := 0; i < 12; i++ {
				id := fmt.Sprintf("toolu_%04d", i)

				messages = append(messages,
					map[string]any{
						"role": "assistant",
						"content": []any{
							map[string]any{
								"type": "tool_use",
								"id":   id,
								"name": "read_file",
								"input": map[string]any{
									"path": "test.txt",
								},
							},
						},
					},
					map[string]any{
						"role": "user",
						"content": []any{
							map[string]any{
								"type":        "tool_result",
								"tool_use_id": id,
								"content": strings.Repeat(
									"R", sizeKiB*1024,
								),
							},
						},
					},
				)
			}

			body, err := json.Marshal(map[string]any{
				"model":      "claude-sonnet-4-5",
				"max_tokens": 1024,
				"messages":   messages,
			})
			if err != nil {
				t.Fatal(err)
			}

			built, err := BuildKiroPayloadWithContext(
				body, "claude-sonnet-4.5",
				"", "AI_EDITOR", nil,
			)
			if err != nil {
				t.Fatal(err)
			}

			assertCandidatePairsAndRecentHistory(t, built.Payload)
			orphans := countOrphanedToolResults(built.Payload)

			currentResults := gjson.GetBytes(
				built.Payload,
				"conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults",
			).Array()

			t.Logf(
				"size=%dKiB payload=%d orphans=%d current=%d",
				sizeKiB, len(built.Payload),
				orphans, len(currentResults),
			)

			if orphans != 0 {
				t.Fatalf(
					"Found %d orphaned tool_results",
					orphans,
				)
			}

			if len(currentResults) != 1 {
				t.Fatalf(
					"Expected 1 current tool result, got %d",
					len(currentResults),
				)
			}

			if len(built.Payload) > kiroMaxPayloadBytes {
				t.Fatalf(
					"Payload exceeds limit: %d bytes",
					len(built.Payload),
				)
			}
		})
	}
}

func TestKiroOversizedToolInputLimit(t *testing.T) {
	id := "toolu_large_input"

	p := &KiroPayload{
		ConversationState: KiroConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  "test",
			CurrentMessage: KiroCurrentMessage{
				UserInputMessage: KiroUserInputMessage{
					Content: "continue",
					ModelID: "claude-sonnet-4.5",
					Origin:  "AI_EDITOR",
				},
			},
			History: []KiroHistoryMessage{
				{
					UserInputMessage: &KiroUserInputMessage{
						Content: "start",
					},
				},
				{
					AssistantResponseMessage: &KiroAssistantResponseMessage{
						ToolUses: []KiroToolUse{{
							ToolUseID: id,
							Name:      "write_file",
							Input: map[string]any{
								"content": strings.Repeat(
									"X", kiroMaxPayloadBytes+1024,
								),
							},
						}},
					},
				},
				{
					UserInputMessage: &KiroUserInputMessage{
						Content: "result",
						UserInputMessageContext: &KiroUserInputMessageContext{
							ToolResults: []KiroToolResult{{
								ToolUseID: id,
								Status:    "success",
								Content:   []KiroTextContent{{Text: "done"}},
							}},
						},
					},
				},
			},
		},
	}

	originalHistoryCount := len(p.ConversationState.History)

	err := truncateKiroPayloadToLimit(p, false)

	if err == nil {
		t.Fatalf("Expected an explicit error for oversized tool input")
	}

	if !strings.Contains(err.Error(), "kiro payload exceeds") {
		t.Fatalf("Unexpected error: %v", err)
	}

	var tooLarge *PayloadTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.LimitBytes != kiroMaxPayloadBytes {
		t.Fatalf("expected *PayloadTooLargeError with limit %d, got %#v", kiroMaxPayloadBytes, err)
	}

	if len(p.ConversationState.History) != originalHistoryCount {
		t.Fatalf("Recent history was unexpectedly discarded")
	}

	t.Logf("Oversized request safely rejected: %v", err)

}

// 最近几轮里的用户正文无法裁剪也无法缩短时，构建应返回带类型的超限错误，
// 供服务层映射成 400 上下文超限，而不是 502。
func TestBuildKiroPayloadReturnsTypedErrorWhenRecentHistoryTooLarge(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[` +
		`{"role":"user","content":"` + strings.Repeat("X", kiroMaxPayloadBytes+1024) + `"},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":"continue"}]}`)

	_, err := BuildKiroPayloadWithContext(body, "claude-sonnet-4.5", "", "AI_EDITOR", nil)

	var tooLarge *PayloadTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.LimitBytes != kiroMaxPayloadBytes {
		t.Fatalf("expected *PayloadTooLargeError with limit %d, got %v", kiroMaxPayloadBytes, err)
	}
}
