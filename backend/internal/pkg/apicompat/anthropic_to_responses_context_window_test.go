package apicompat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// model_context_window_exceeded 表示上下文已满，转成 Responses 时要和
// max_tokens 一样报 incomplete/max_output_tokens（Chat 侧随之得到 length），
// 否则 Codex 等客户端看到 completed，不会触发上下文压缩。
func TestAnthropicToResponsesResponse_ContextWindowExceededIsIncomplete(t *testing.T) {
	resp := &AnthropicResponse{
		ID:         "msg_1",
		Model:      "claude-sonnet-4-5",
		Content:    []AnthropicContentBlock{{Type: "text", Text: "partial"}},
		StopReason: AnthropicStopReasonPtr("model_context_window_exceeded"),
	}

	out := AnthropicToResponsesResponse(resp)
	require.Equal(t, "incomplete", out.Status)
	require.NotNil(t, out.IncompleteDetails)
	require.Equal(t, "max_output_tokens", out.IncompleteDetails.Reason)
}

func TestAnthropicEventToResponses_ContextWindowExceededIsIncomplete(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.Model = "claude-sonnet-4-5"

	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(evt, state)...)
	}

	idx := 0
	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1", Model: "claude-sonnet-4-5"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "partial"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	feed(&AnthropicStreamEvent{Type: "message_delta", Delta: &AnthropicDelta{StopReason: "model_context_window_exceeded"}})
	feed(&AnthropicStreamEvent{Type: "message_stop"})
	events = append(events, FinalizeAnthropicResponsesStream(state)...)

	var terminal *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" || events[i].Type == "response.incomplete" {
			terminal = &events[i]
		}
	}
	require.NotNil(t, terminal, "no terminal response event emitted")
	require.Equal(t, "response.incomplete", terminal.Type)
	require.NotNil(t, terminal.Response)
	require.Equal(t, "incomplete", terminal.Response.Status)
	require.NotNil(t, terminal.Response.IncompleteDetails)
	require.Equal(t, "max_output_tokens", terminal.Response.IncompleteDetails.Reason)
}
