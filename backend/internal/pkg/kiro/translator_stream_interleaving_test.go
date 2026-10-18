package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Check the client-visible protocol, not just whether a tool name occurs in it.
func assertCompleteKiroToolStream(t *testing.T, output string, expected map[string]string) {
	t.Helper()
	openIndex, lastIndex := -1, -1
	toolIDs := map[int]string{}
	inputs := map[string]string{}
	starts, stops := 0, 0
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Block struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
		}
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
		switch event.Type {
		case "message_start":
			starts++
		case "content_block_start":
			require.Equal(t, -1, openIndex, "previous block must be closed")
			require.Equal(t, lastIndex+1, event.Index)
			openIndex, lastIndex = event.Index, event.Index
			if event.Block.Type == "tool_use" {
				require.NotEmpty(t, event.Block.ID)
				require.NotContains(t, inputs, event.Block.ID, "tool emitted twice")
				toolIDs[event.Index] = event.Block.ID
				inputs[event.Block.ID] = ""
			}
		case "content_block_delta":
			require.Equal(t, openIndex, event.Index)
			if event.Delta.Type == "input_json_delta" {
				id, ok := toolIDs[event.Index]
				require.True(t, ok)
				inputs[id] += event.Delta.PartialJSON
			}
		case "content_block_stop":
			require.Equal(t, openIndex, event.Index)
			openIndex = -1
		case "message_delta":
			require.Equal(t, "tool_use", event.Delta.StopReason)
		case "message_stop":
			require.Equal(t, -1, openIndex)
			stops++
		}
	}
	require.Equal(t, 1, starts)
	require.Equal(t, 1, stops)
	require.Equal(t, -1, openIndex)
	require.Len(t, inputs, len(expected))
	for id, want := range expected {
		require.Contains(t, inputs, id)
		require.JSONEq(t, want, inputs[id])
	}
}

func TestStreamEventStreamAsAnthropicKeepsInterleavedToolFragments(t *testing.T) {
	tool := func(id, name, input string, stop bool) map[string]any {
		return map[string]any{"toolUseEvent": map[string]any{
			"toolUseId": id, "name": name, "input": input, "stop": stop,
		}}
	}
	for _, boundary := range []string{"other_tool", "text", "thinking", "assistant_snapshot", "no_stop_at_eof"} {
		t.Run(boundary, func(t *testing.T) {
			stream := bytes.NewBuffer(nil)
			stream.Write(buildEventStreamFrame(t, "toolUseEvent", tool("write", "Write", `{"file_path":"/tmp/a","content":"hel`, false)))
			expected := map[string]string{"write": `{"file_path":"/tmp/a","content":"hello"}`}
			switch boundary {
			case "other_tool", "no_stop_at_eof":
				stream.Write(buildEventStreamFrame(t, "toolUseEvent", tool("bash", "Bash", `{"command":"pwd"}`, boundary != "no_stop_at_eof")))
				expected["bash"] = `{"command":"pwd"}`
			case "text":
				stream.Write(buildEventStreamFrame(t, "assistantResponseEvent", map[string]any{"content": "still working"}))
			case "thinking":
				stream.Write(buildEventStreamFrame(t, "reasoningContentEvent", map[string]any{"text": "still thinking"}))
			case "assistant_snapshot":
				stream.Write(buildEventStreamFrame(t, "assistantResponseEvent", map[string]any{"toolUses": []map[string]any{
					{"toolUseId": "bash", "name": "Bash", "input": map[string]any{"command": "pwd"}},
				}}))
				expected["bash"] = `{"command":"pwd"}`
			}
			stream.Write(buildEventStreamFrame(t, "toolUseEvent", tool("write", "", `lo"}`, boundary != "no_stop_at_eof")))
			var out bytes.Buffer
			result, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, &out, "claude-sonnet-4-5", 1, KiroRequestContext{ThinkingEnabled: boundary == "thinking"})
			require.NoError(t, err)
			require.Equal(t, "tool_use", result.StopReason)
			assertCompleteKiroToolStream(t, out.String(), expected)
		})
	}
}

func TestStreamEventStreamAsAnthropicRejectsUnresolvedToolFragments(t *testing.T) {
	for _, explicitStop := range []bool{false, true} {
		for _, otherTool := range []bool{false, true} {
			t.Run(fmt.Sprintf("stop=%t/other=%t", explicitStop, otherTool), func(t *testing.T) {
				stream := bytes.NewBuffer(buildEventStreamFrame(t, "toolUseEvent", map[string]any{"toolUseEvent": map[string]any{
					"toolUseId": "unfinished", "name": "Write", "input": `{"file_path":"/tmp/a","content":"sensitive-content`, "stop": explicitStop,
				}}))
				if otherTool {
					stream.Write(buildEventStreamFrame(t, "toolUseEvent", map[string]any{"toolUseEvent": map[string]any{
						"toolUseId": "complete", "name": "Bash", "input": `{"command":"pwd"}`, "stop": true,
					}}))
				}
				var out bytes.Buffer
				result, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, &out, "claude-sonnet-4-5", 1, KiroRequestContext{})
				require.Nil(t, result)
				require.ErrorContains(t, err, "incomplete or invalid input")
				require.NotContains(t, err.Error(), "sensitive-content")
				require.NotContains(t, out.String(), "event: message_stop")
				require.NotContains(t, out.String(), `"id":"unfinished"`)
			})
		}
	}
}

func TestStreamEventStreamAsAnthropicPreservesCompleteButUnstoppedToolUntilSnapshot(t *testing.T) {
	stream := bytes.NewBuffer(buildEventStreamFrame(t, "toolUseEvent", map[string]any{"toolUseEvent": map[string]any{
		"toolUseId": "first", "name": "custom_tool", "input": `{"value":"stale"}`,
	}}))
	stream.Write(buildEventStreamFrame(t, "toolUseEvent", map[string]any{"toolUseEvent": map[string]any{
		"toolUseId": "second", "name": "custom_tool", "input": `{"value":"second"}`, "stop": true,
	}}))
	stream.Write(buildEventStreamFrame(t, "assistantResponseEvent", map[string]any{"toolUses": []map[string]any{
		{"toolUseId": "first", "name": "custom_tool", "input": map[string]any{"value": "snapshot"}},
	}}))
	var out bytes.Buffer
	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, &out, "claude-sonnet-4-5", 1, KiroRequestContext{})
	require.NoError(t, err)
	assertCompleteKiroToolStream(t, out.String(), map[string]string{"first": `{"value":"snapshot"}`, "second": `{"value":"second"}`})
}
