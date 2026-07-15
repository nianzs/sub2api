package apicompat

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// ---------------------------------------------------------------------------
// Non-streaming: ResponsesResponse → AnthropicResponse
// ---------------------------------------------------------------------------

// ResponsesToAnthropic converts a Responses API response directly into an
// Anthropic Messages response. Reasoning output items are mapped to thinking
// blocks; function_call items become tool_use blocks.
func ResponsesToAnthropic(resp *ResponsesResponse, model string) *AnthropicResponse {
	out := &AnthropicResponse{
		ID:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: model,
	}

	var blocks []AnthropicContentBlock

	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			summaryText := ""
			for _, s := range item.Summary {
				if s.Type == "summary_text" && s.Text != "" {
					summaryText += s.Text
				}
			}
			if summaryText != "" {
				blocks = append(blocks, AnthropicContentBlock{
					Type:     "thinking",
					Thinking: summaryText,
				})
			}
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					blocks = append(blocks, AnthropicContentBlock{
						Type: "text",
						Text: part.Text,
					})
				}
			}
		case "function_call":
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(item.CallID),
				Name:  item.Name,
				Input: sanitizeAnthropicToolUseInput(item.Name, item.Arguments),
			})
		case "web_search_call":
			toolUseID := "srvtoolu_" + item.ID
			query := ""
			if item.Action != nil {
				query = item.Action.Query
			}
			inputJSON, _ := json.Marshal(map[string]string{"query": query})
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "server_tool_use",
				ID:    toolUseID,
				Name:  "web_search",
				Input: inputJSON,
			})
			emptyResults, _ := json.Marshal([]struct{}{})
			blocks = append(blocks, AnthropicContentBlock{
				Type:      "web_search_tool_result",
				ToolUseID: toolUseID,
				Content:   emptyResults,
			})
		}
	}

	if len(blocks) == 0 {
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: ""})
	}
	out.Content = blocks

	out.StopReason = responsesStatusToAnthropicStopReason(resp.Status, resp.IncompleteDetails, blocks)

	if resp.Usage != nil {
		out.Usage = anthropicUsageFromResponsesUsage(resp.Usage)
	}

	return out
}

func anthropicUsageFromResponsesUsage(usage *ResponsesUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}

	cachedTokens := 0
	if usage.InputTokensDetails != nil {
		cachedTokens = usage.InputTokensDetails.CachedTokens
	}

	inputTokens := usage.InputTokens - cachedTokens - usage.CacheCreationInputTokens
	if inputTokens < 0 {
		inputTokens = 0
	}

	return AnthropicUsage{
		InputTokens:              inputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheReadInputTokens:     cachedTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
	}
}

func responsesStatusToAnthropicStopReason(status string, details *ResponsesIncompleteDetails, blocks []AnthropicContentBlock) string {
	switch status {
	case "incomplete":
		if details != nil && details.Reason == "max_output_tokens" {
			return "max_tokens"
		}
		return "end_turn"
	case "completed":
		if containsAnthropicToolUseBlock(blocks) {
			return "tool_use"
		}
		return "end_turn"
	default:
		return "end_turn"
	}
}

func containsAnthropicToolUseBlock(blocks []AnthropicContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

func sanitizeAnthropicToolUseInput(name string, raw string) json.RawMessage {
	if name != "Read" || raw == "" {
		return json.RawMessage(raw)
	}

	var input map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return json.RawMessage(raw)
	}

	if pages, ok := input["pages"]; !ok || string(pages) != `""` {
		return json.RawMessage(raw)
	}

	delete(input, "pages")
	sanitized, err := json.Marshal(input)
	if err != nil {
		return json.RawMessage(raw)
	}
	return sanitized
}

// ---------------------------------------------------------------------------
// Streaming: ResponsesStreamEvent → []AnthropicStreamEvent (stateful converter)
// ---------------------------------------------------------------------------

// openAnthropicBlock tracks one currently-open Anthropic content block.
// Grok/xAI Responses 可能并行发出多个 function_call；Claude Code 要求每个
// content_block_delta/stop 都能在 map 中找到对应 start，因此必须按 block 维度
// 跟踪开闭，而不是只保留“当前唯一块”。
type openAnthropicBlock struct {
	Type     string // "text" | "thinking" | "tool_use"
	ToolName string
	ToolArgs string
	HadDelta bool
}

// ResponsesEventToAnthropicState tracks state for converting a sequence of
// Responses SSE events directly into Anthropic SSE events.
type ResponsesEventToAnthropicState struct {
	MessageStartSent bool
	MessageStopSent  bool

	// ContentBlockIndex is the next Anthropic content block index to allocate.
	// 在 start 时分配并递增，避免并行 tool_use 共用同一 index。
	ContentBlockIndex int
	// ContentBlockOpen / Current* 描述“主活动块”（text/thinking 或最近打开的 tool），
	// 兼容既有单块路径；并行 tool 的权威状态在 OpenBlocks。
	ContentBlockOpen    bool
	CurrentBlockIndex   int
	CurrentBlockType    string // "text" | "thinking" | "tool_use"
	CurrentToolName     string
	CurrentToolArgs     string
	CurrentToolHadDelta bool
	HasToolCall         bool

	// OpenBlocks maps Anthropic content block index → open block metadata.
	OpenBlocks map[int]*openAnthropicBlock
	// OutputIndexToBlockIdx maps Responses output_index → Anthropic content block index.
	OutputIndexToBlockIdx map[int]int

	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int

	ResponseID string
	Model      string
	Created    int64
}

// NewResponsesEventToAnthropicState returns an initialised stream state.
func NewResponsesEventToAnthropicState() *ResponsesEventToAnthropicState {
	return &ResponsesEventToAnthropicState{
		OpenBlocks:            make(map[int]*openAnthropicBlock),
		OutputIndexToBlockIdx: make(map[int]int),
		Created:               time.Now().Unix(),
	}
}

// ResponsesEventToAnthropicEvents converts a single Responses SSE event into
// zero or more Anthropic SSE events, updating state as it goes.
func ResponsesEventToAnthropicEvents(
	evt *ResponsesStreamEvent,
	state *ResponsesEventToAnthropicState,
) []AnthropicStreamEvent {
	switch evt.Type {
	case "response.created":
		return resToAnthHandleCreated(evt, state)
	case "response.output_item.added":
		return resToAnthHandleOutputItemAdded(evt, state)
	case "response.output_text.delta":
		return resToAnthHandleTextDelta(evt, state)
	case "response.output_text.done":
		return resToAnthHandleBlockDone(state)
	case "response.function_call_arguments.delta",
		// custom/freeform 工具的输入增量与 function_call 参数增量同形。
		"response.custom_tool_call_input.delta":
		return resToAnthHandleFuncArgsDelta(evt, state)
	case "response.function_call_arguments.done",
		"response.custom_tool_call_input.done":
		return resToAnthHandleFuncArgsDone(evt, state)
	case "response.output_item.done":
		return resToAnthHandleOutputItemDone(evt, state)
	case "response.reasoning_summary_text.delta",
		// 原始推理文本增量，与 reasoning summary 一样映射为 thinking。
		"response.reasoning_text.delta":
		return resToAnthHandleReasoningDelta(evt, state)
	case "response.reasoning_summary_text.done":
		// reasoning_summary_text.done 只表示某一段推理文本结束；真正的 thinking 块关闭
		// 由 output_item.done(reasoning) 或流终态驱动，避免 Grok 多段 reasoning
		// 后对已关闭 block 再发 thinking_delta。
		return nil
	// response.done 是 Realtime/WS 与项目透传路径使用的终止别名；
	// 普通 Responses HTTP SSE 的公开终止事件仍以 response.completed 为主。
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return resToAnthHandleCompleted(evt, state)
	default:
		return nil
	}
}

// FinalizeResponsesAnthropicStream emits synthetic termination events if the
// stream ended without a proper completion event.
func FinalizeResponsesAnthropicStream(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.MessageStartSent || state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	// 终态前关闭全部打开块（含并行 tool_use），避免 Claude Code 残留半开 block。
	events = append(events, closeAllOpenBlocks(state)...)

	stopReason := "end_turn"
	if state.HasToolCall {
		stopReason = "tool_use"
	}

	events = append(events,
		AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &AnthropicDelta{
				StopReason: stopReason,
			},
			Usage: &AnthropicUsage{
				InputTokens:              state.InputTokens,
				OutputTokens:             state.OutputTokens,
				CacheReadInputTokens:     state.CacheReadInputTokens,
				CacheCreationInputTokens: state.CacheCreationInputTokens,
			},
		},
		AnthropicStreamEvent{Type: "message_stop"},
	)
	state.MessageStopSent = true
	return events
}

// ResponsesAnthropicEventToSSE formats an AnthropicStreamEvent as an SSE line pair.
func ResponsesAnthropicEventToSSE(evt AnthropicStreamEvent) (string, error) {
	data, err := json.Marshal(evt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", evt.Type, data), nil
}

// --- internal handlers ---

func resToAnthHandleCreated(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Response != nil {
		state.ResponseID = evt.Response.ID
		// Only use upstream model if no override was set (e.g. originalModel)
		if state.Model == "" {
			state.Model = evt.Response.Model
		}
	}
	return ensureAnthropicMessageStart(state)
}

func ensureAnthropicMessageStart(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.MessageStartSent {
		return nil
	}
	state.MessageStartSent = true
	return []AnthropicStreamEvent{{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:      state.ResponseID,
			Type:    "message",
			Role:    "assistant",
			Content: []AnthropicContentBlock{},
			Model:   state.Model,
			Usage: AnthropicUsage{
				InputTokens:  0,
				OutputTokens: 0,
			},
		},
	}}
}

func resToAnthHandleOutputItemAdded(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	switch evt.Item.Type {
	// function_call 与 custom_tool_call（custom/freeform 工具，如新版 apply_patch）
	// 同样映射为 Anthropic 的 tool_use 块。
	// 并行 tool_use：不关闭其他已打开的 tool 块，只关闭 text/thinking 主活动块。
	case "function_call", "custom_tool_call":
		var events []AnthropicStreamEvent
		events = append(events, ensureAnthropicMessageStart(state)...)
		events = append(events, closeNonToolActiveBlock(state)...)

		idx := allocateAnthropicBlock(state, "tool_use", evt.Item.Name)
		state.OutputIndexToBlockIdx[evt.OutputIndex] = idx
		state.HasToolCall = true

		callID := evt.Item.CallID
		if callID == "" {
			callID = evt.Item.ID
		}
		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(callID),
				Name:  evt.Item.Name,
				Input: json.RawMessage("{}"),
			},
		})
		return events

	case "reasoning":
		var events []AnthropicStreamEvent
		events = append(events, ensureAnthropicMessageStart(state)...)
		events = append(events, closeAllOpenBlocks(state)...)

		idx := allocateAnthropicBlock(state, "thinking", "")
		state.OutputIndexToBlockIdx[evt.OutputIndex] = idx

		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type:     "thinking",
				Thinking: "",
			},
		})
		return events

	case "message":
		return nil
	}

	return nil
}

func resToAnthHandleTextDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	var events []AnthropicStreamEvent
	events = append(events, ensureAnthropicMessageStart(state)...)

	if !state.ContentBlockOpen || state.CurrentBlockType != "text" {
		// 文本块开始前关闭所有打开块（含并行 tool），保持 Anthropic 块生命周期串行。
		events = append(events, closeAllOpenBlocks(state)...)
		idx := allocateAnthropicBlock(state, "text", "")
		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type: "text",
				Text: "",
			},
		})
	}

	idx := state.CurrentBlockIndex
	if _, ok := state.OpenBlocks[idx]; !ok {
		// 防御：主活动块已关闭时不发 orphan delta。
		return events
	}
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: &idx,
		Delta: &AnthropicDelta{
			Type: "text_delta",
			Text: evt.Delta,
		},
	})
	return events
}

func resToAnthHandleFuncArgsDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	if state.CurrentBlockType == "tool_use" {
		state.CurrentToolHadDelta = true
	}

	blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
	if !ok {
		return nil
	}
	block, ok := state.OpenBlocks[blockIdx]
	if !ok || block.Type != "tool_use" {
		// 块已关闭或类型不对：丢弃，避免 Claude Code "Content block not found"。
		return nil
	}

	if block.ToolName == "Read" {
		// Read：累积参数，done 时 sanitize(pages 空串) 后一次性发送。
		block.ToolArgs += evt.Delta
		// 不标记 HadDelta，让 done 路径能刷出完整净化后的参数。
		syncCurrentToolFromBlock(state, blockIdx, block)
		return nil
	}
	block.HadDelta = true
	syncCurrentToolFromBlock(state, blockIdx, block)

	return []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:        "input_json_delta",
			PartialJSON: evt.Delta,
		},
	}}
}

func resToAnthHandleFuncArgsDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
	if !ok {
		// 兼容旧路径：无 output_index 映射时，仅当主活动块是 tool_use 才关闭。
		if state.CurrentBlockType == "tool_use" && state.ContentBlockOpen {
			return closeBlockAt(state, state.CurrentBlockIndex)
		}
		return nil
	}
	block, ok := state.OpenBlocks[blockIdx]
	if !ok || block.Type != "tool_use" {
		return nil
	}

	raw := evt.Arguments
	if raw == "" {
		raw = block.ToolArgs
	}
	var events []AnthropicStreamEvent
	if raw != "" && !block.HadDelta {
		if block.ToolName == "Read" {
			sanitized := sanitizeAnthropicToolUseInput(block.ToolName, raw)
			if len(sanitized) == 0 {
				return closeBlockAt(state, blockIdx)
			}
			raw = string(sanitized)
		}
		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_delta",
			Index: &blockIdx,
			Delta: &AnthropicDelta{
				Type:        "input_json_delta",
				PartialJSON: raw,
			},
		})
	}
	events = append(events, closeBlockAt(state, blockIdx)...)
	return events
}

func resToAnthHandleReasoningDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	var events []AnthropicStreamEvent
	events = append(events, ensureAnthropicMessageStart(state)...)

	blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
	if !ok || state.OpenBlocks[blockIdx] == nil || state.OpenBlocks[blockIdx].Type != "thinking" {
		// 缺少 output_item.added(reasoning) 或 thinking 已关闭时自恢复，
		// 避免直接对未知 index 发 thinking_delta。
		events = append(events, closeAllOpenBlocks(state)...)
		blockIdx = allocateAnthropicBlock(state, "thinking", "")
		state.OutputIndexToBlockIdx[evt.OutputIndex] = blockIdx
		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &blockIdx,
			ContentBlock: &AnthropicContentBlock{
				Type:     "thinking",
				Thinking: "",
			},
		})
	}

	if _, open := state.OpenBlocks[blockIdx]; !open {
		return events
	}
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:     "thinking_delta",
			Thinking: evt.Delta,
		},
	})
	return events
}

func resToAnthHandleBlockDone(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	// output_text.done 只关闭当前 text 主活动块，不动并行 tool。
	if state.CurrentBlockType == "text" {
		return closeBlockAt(state, state.CurrentBlockIndex)
	}
	return closeCurrentBlock(state)
}

func resToAnthHandleOutputItemDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	// Handle web_search_call → synthesize server_tool_use + web_search_tool_result blocks.
	if evt.Item.Type == "web_search_call" && evt.Item.Status == "completed" {
		return resToAnthHandleWebSearchDone(evt, state)
	}

	if blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]; ok {
		if _, open := state.OpenBlocks[blockIdx]; open {
			return closeBlockAt(state, blockIdx)
		}
		return nil
	}

	// 无 output_index 映射时退回主活动块关闭（兼容旧事件）。
	if state.ContentBlockOpen {
		return closeCurrentBlock(state)
	}
	return nil
}

// resToAnthHandleWebSearchDone converts an OpenAI web_search_call output item
// into Anthropic server_tool_use + web_search_tool_result content block pairs.
// This allows Claude Code to count the searches performed.
func resToAnthHandleWebSearchDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	var events []AnthropicStreamEvent
	events = append(events, ensureAnthropicMessageStart(state)...)
	events = append(events, closeAllOpenBlocks(state)...)

	toolUseID := "srvtoolu_" + evt.Item.ID
	query := ""
	if evt.Item.Action != nil {
		query = evt.Item.Action.Query
	}
	inputJSON, _ := json.Marshal(map[string]string{"query": query})

	// Emit server_tool_use block (start + stop). Index 在 start 时分配并立即占用，
	// stop 使用同一 index；不再依赖“open 标志 + 延迟递增”的旧模型。
	idx1 := state.ContentBlockIndex
	state.ContentBlockIndex++
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx1,
		ContentBlock: &AnthropicContentBlock{
			Type:  "server_tool_use",
			ID:    toolUseID,
			Name:  "web_search",
			Input: inputJSON,
		},
	})
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx1,
	})

	// Emit web_search_tool_result block (start + stop).
	// Content is empty because OpenAI does not expose individual search results;
	// the model consumes them internally and produces text output.
	emptyResults, _ := json.Marshal([]struct{}{})
	idx2 := state.ContentBlockIndex
	state.ContentBlockIndex++
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx2,
		ContentBlock: &AnthropicContentBlock{
			Type:      "web_search_tool_result",
			ToolUseID: toolUseID,
			Content:   emptyResults,
		},
	})
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx2,
	})

	return events
}

func resToAnthHandleCompleted(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	// 终态不主动补 message_start：若上游从未发 response.created，保持旧语义
	// 仅输出 message_delta/message_stop，避免改变纯终态事件的事件计数。
	events = append(events, closeAllOpenBlocks(state)...)

	stopReason := "end_turn"
	if evt.Usage != nil {
		usage := anthropicUsageFromResponsesUsage(evt.Usage)
		state.InputTokens = usage.InputTokens
		state.OutputTokens = usage.OutputTokens
		state.CacheReadInputTokens = usage.CacheReadInputTokens
		state.CacheCreationInputTokens = usage.CacheCreationInputTokens
	}
	if evt.Response != nil {
		if evt.Response.Usage != nil {
			usage := anthropicUsageFromResponsesUsage(evt.Response.Usage)
			state.InputTokens = usage.InputTokens
			state.OutputTokens = usage.OutputTokens
			state.CacheReadInputTokens = usage.CacheReadInputTokens
			state.CacheCreationInputTokens = usage.CacheCreationInputTokens
		}
		switch evt.Response.Status {
		case "incomplete":
			if evt.Response.IncompleteDetails != nil && evt.Response.IncompleteDetails.Reason == "max_output_tokens" {
				stopReason = "max_tokens"
			}
		case "completed":
			if state.HasToolCall {
				stopReason = "tool_use"
			}
		}
	}

	events = append(events,
		AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &AnthropicDelta{
				StopReason: stopReason,
			},
			Usage: &AnthropicUsage{
				InputTokens:              state.InputTokens,
				OutputTokens:             state.OutputTokens,
				CacheReadInputTokens:     state.CacheReadInputTokens,
				CacheCreationInputTokens: state.CacheCreationInputTokens,
			},
		},
		AnthropicStreamEvent{Type: "message_stop"},
	)
	state.MessageStopSent = true
	return events
}

// allocateAnthropicBlock reserves the next content block index and marks it open.
func allocateAnthropicBlock(state *ResponsesEventToAnthropicState, blockType, toolName string) int {
	if state.OpenBlocks == nil {
		state.OpenBlocks = make(map[int]*openAnthropicBlock)
	}
	idx := state.ContentBlockIndex
	state.ContentBlockIndex++
	state.OpenBlocks[idx] = &openAnthropicBlock{
		Type:     blockType,
		ToolName: toolName,
	}
	state.ContentBlockOpen = true
	state.CurrentBlockIndex = idx
	state.CurrentBlockType = blockType
	state.CurrentToolName = toolName
	state.CurrentToolArgs = ""
	state.CurrentToolHadDelta = false
	return idx
}

func syncCurrentToolFromBlock(state *ResponsesEventToAnthropicState, blockIdx int, block *openAnthropicBlock) {
	if state.CurrentBlockIndex != blockIdx {
		return
	}
	state.CurrentToolName = block.ToolName
	state.CurrentToolArgs = block.ToolArgs
	state.CurrentToolHadDelta = block.HadDelta
}

func closeBlockAt(state *ResponsesEventToAnthropicState, idx int) []AnthropicStreamEvent {
	if state.OpenBlocks == nil {
		return nil
	}
	if _, ok := state.OpenBlocks[idx]; !ok {
		return nil
	}
	delete(state.OpenBlocks, idx)
	if state.CurrentBlockIndex == idx {
		state.ContentBlockOpen = len(state.OpenBlocks) > 0
		if !state.ContentBlockOpen {
			state.CurrentBlockType = ""
			state.CurrentToolName = ""
			state.CurrentToolArgs = ""
			state.CurrentToolHadDelta = false
		} else {
			// 主活动块关闭后，若仍有打开块，选一个作为 Current*（任意即可）。
			for otherIdx, other := range state.OpenBlocks {
				state.CurrentBlockIndex = otherIdx
				state.CurrentBlockType = other.Type
				state.CurrentToolName = other.ToolName
				state.CurrentToolArgs = other.ToolArgs
				state.CurrentToolHadDelta = other.HadDelta
				break
			}
		}
	} else if len(state.OpenBlocks) == 0 {
		state.ContentBlockOpen = false
		state.CurrentBlockType = ""
		state.CurrentToolName = ""
		state.CurrentToolArgs = ""
		state.CurrentToolHadDelta = false
	}
	return []AnthropicStreamEvent{{
		Type:  "content_block_stop",
		Index: &idx,
	}}
}

// closeCurrentBlock closes the primary active block only.
func closeCurrentBlock(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	return closeBlockAt(state, state.CurrentBlockIndex)
}

// closeNonToolActiveBlock closes text/thinking primary block before opening a
// tool_use block, while leaving other open tool blocks intact (parallel tools).
func closeNonToolActiveBlock(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	if state.CurrentBlockType == "tool_use" {
		return nil
	}
	return closeBlockAt(state, state.CurrentBlockIndex)
}

// closeAllOpenBlocks closes every open content block in ascending index order.
func closeAllOpenBlocks(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if len(state.OpenBlocks) == 0 {
		state.ContentBlockOpen = false
		state.CurrentBlockType = ""
		state.CurrentToolName = ""
		state.CurrentToolArgs = ""
		state.CurrentToolHadDelta = false
		return nil
	}
	idxs := make([]int, 0, len(state.OpenBlocks))
	for idx := range state.OpenBlocks {
		idxs = append(idxs, idx)
	}
	// 稳定按 index 升序关闭，满足 Claude Code 对 content_block 生命周期的期望。
	sort.Ints(idxs)
	var events []AnthropicStreamEvent
	for _, idx := range idxs {
		events = append(events, closeBlockAt(state, idx)...)
	}
	return events
}
