package apicompat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本 fork 对 Read 工具走「累积 → done 时 sanitize(pages 空串) → 一次性发送」路径，
// 不实时流式发 delta。上游 a5d40c98 改为实时流式，与本 fork 的 pages 净化互斥，
// 本文件按本 fork 契约断言。

func seedToolCall(t *testing.T, name string) *ResponsesEventToAnthropicState {
	t.Helper()
	state := NewResponsesEventToAnthropicState()
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:     "response.created",
		Response: &ResponsesResponse{ID: "resp_" + name, Model: "gpt-5.5"},
	}, state)
	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "function_call", CallID: "call_" + name, Name: name},
	}, state)
	require.Len(t, events, 1)
	assert.Equal(t, "content_block_start", events[0].Type)
	return state
}

func TestResToAnthFuncArgsDelta_ReadToolAccumulatesWithoutStreaming(t *testing.T) {
	state := seedToolCall(t, "Read")

	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 0,
		Delta:       `{"file_path":"/tmp/test.go"}`,
	}, state)

	// Read：delta 只累积，不发 content_block_delta
	require.Len(t, events, 0, "Read tool deltas must accumulate, not stream")
	assert.False(t, state.CurrentToolHadDelta, "Read accumulate path must not mark HadDelta")

	// done 时一次性发送完整参数
	events = ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 0,
		Arguments:   `{"file_path":"/tmp/test.go"}`,
	}, state)
	require.Len(t, events, 2, "done should emit input_json_delta + content_block_stop")
	assert.Equal(t, "content_block_delta", events[0].Type)
	assert.Equal(t, "input_json_delta", events[0].Delta.Type)
	assert.Equal(t, `{"file_path":"/tmp/test.go"}`, events[0].Delta.PartialJSON)
	assert.Equal(t, "content_block_stop", events[1].Type)
}

func TestResToAnthFuncArgsDelta_ReadToolDropsEmptyPagesOnDone(t *testing.T) {
	state := seedToolCall(t, "Read")

	// delta 带 pages:""，只累积
	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 0,
		Delta:       `{"file_path":"/tmp/demo.py","limit":2000,"offset":0,"pages":""}`,
	}, state)
	require.Len(t, events, 0)

	// done 时净化 pages 空串后一次性发送
	events = ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 0,
		Arguments:   `{"file_path":"/tmp/demo.py","limit":2000,"offset":0,"pages":""}`,
	}, state)
	require.Len(t, events, 2)
	assert.Equal(t, "content_block_delta", events[0].Type)
	assert.JSONEq(t, `{"file_path":"/tmp/demo.py","limit":2000,"offset":0}`, events[0].Delta.PartialJSON)
	assert.Equal(t, "content_block_stop", events[1].Type)
}

func TestResToAnthFuncArgsDelta_ReadToolWithoutDoneLosesArgs(t *testing.T) {
	// 本 fork 依赖 .done 才刷出 Read 参数；流直接 completed 时只关块，不补发参数。
	// 这是「累积+sanitize」设计相对「实时流式」的已知代价。
	state := seedToolCall(t, "Read")

	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 0,
		Delta:       `{"file_path":"/tmp/test.go"}`,
	}, state)
	require.Len(t, events, 0, "Read delta accumulates only")

	events = ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type: "response.completed",
		Response: &ResponsesResponse{
			Status: "completed",
		},
	}, state)

	hasStop := false
	hasDelta := false
	for _, e := range events {
		if e.Type == "content_block_stop" {
			hasStop = true
		}
		if e.Type == "content_block_delta" {
			hasDelta = true
		}
	}
	assert.True(t, hasStop, "block should still be closed on completed")
	assert.False(t, hasDelta, "without .done, accumulated Read args are not flushed")
}

func TestResToAnthFuncArgsDelta_NonReadToolUnchanged(t *testing.T) {
	// 非 Read 工具也需经 OpenBlocks 登记（本 fork 并行块防御）
	state := seedToolCall(t, "Write")

	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 0,
		Delta:       `{"file_path":"/tmp/out.txt","content":"hello"}`,
	}, state)

	require.Len(t, events, 1)
	assert.Equal(t, "content_block_delta", events[0].Type)
	assert.Equal(t, "input_json_delta", events[0].Delta.Type)
	assert.Equal(t, `{"file_path":"/tmp/out.txt","content":"hello"}`, events[0].Delta.PartialJSON)
	assert.True(t, state.CurrentToolHadDelta)
}
