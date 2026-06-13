package kiro

import (
	"encoding/json"
	"strings"
)

type BufferedStreamResult struct {
	StopReason            string
	WebSearchQuery        string
	WebSearchToolUseID    string
	HasWebSearchToolUse   bool
	WebSearchToolUseIndex int
}

func GenerateSearchIndicatorEvents(query, toolUseID string, results *WebSearchResults, startIndex int) [][]byte {
	searchContent := make([]map[string]any, 0)
	if results != nil {
		for _, result := range results.Results {
			snippet := ""
			if result.Snippet != nil {
				snippet = strings.TrimSpace(*result.Snippet)
			}
			searchContent = append(searchContent, map[string]any{
				"type":              "web_search_result",
				"title":             result.Title,
				"url":               result.URL,
				"encrypted_content": snippet,
				"page_age":          nil,
			})
		}
	}

	inputJSON, _ := json.Marshal(map[string]string{"query": query})

	events := []map[string]any{
		{
			"type":  "content_block_start",
			"index": startIndex,
			"content_block": map[string]any{
				"type":  "server_tool_use",
				"id":    toolUseID,
				"name":  "web_search",
				"input": map[string]any{},
			},
		},
		{
			"type":  "content_block_delta",
			"index": startIndex,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": string(inputJSON),
			},
		},
		{
			"type":  "content_block_stop",
			"index": startIndex,
		},
		{
			"type":  "content_block_start",
			"index": startIndex + 1,
			"content_block": map[string]any{
				"type":    "web_search_tool_result",
				"content": searchContent,
			},
		},
		{
			"type":  "content_block_stop",
			"index": startIndex + 1,
		},
	}

	result := make([][]byte, 0, len(events))
	for _, event := range events {
		eventType, _ := event["type"].(string)
		payload, _ := json.Marshal(event)
		result = append(result, []byte("event: "+eventType+"\ndata: "+string(payload)+"\n\n"))
	}
	return result
}

func AnalyzeBufferedStream(chunks [][]byte) BufferedStreamResult {
	result := BufferedStreamResult{WebSearchToolUseIndex: -1}
	var currentToolName string
	currentToolIndex := -1
	var toolInputBuilder strings.Builder

	for _, chunk := range chunks {
		lines := strings.Split(string(chunk), "\n")
		for _, line := range lines {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if payload == "" || payload == "[DONE]" {
				continue
			}

			var event map[string]any
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				continue
			}

			switch eventType, _ := event["type"].(string); eventType {
			case "message_delta":
				if delta, ok := event["delta"].(map[string]any); ok {
					if stopReason, ok := delta["stop_reason"].(string); ok && strings.TrimSpace(stopReason) != "" {
						result.StopReason = stopReason
					}
				}
			case "content_block_start":
				contentBlock, ok := event["content_block"].(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := contentBlock["type"].(string)
				if blockType != "tool_use" {
					continue
				}
				currentToolName, _ = contentBlock["name"].(string)
				currentToolName = strings.ToLower(strings.TrimSpace(currentToolName))
				if idx, ok := event["index"].(float64); ok {
					currentToolIndex = int(idx)
				}
				if toolUseID, ok := contentBlock["id"].(string); ok && isWebSearchToolName(currentToolName, "") {
					result.WebSearchToolUseID = strings.TrimSpace(toolUseID)
				}
				toolInputBuilder.Reset()
			case "content_block_delta":
				if currentToolName == "" {
					continue
				}
				delta, ok := event["delta"].(map[string]any)
				if !ok {
					continue
				}
				deltaType, _ := delta["type"].(string)
				if deltaType != "input_json_delta" {
					continue
				}
				if partialJSON, ok := delta["partial_json"].(string); ok {
					_, _ = toolInputBuilder.WriteString(partialJSON)
				}
			case "content_block_stop":
				if !isWebSearchToolName(currentToolName, "") {
					currentToolName = ""
					currentToolIndex = -1
					toolInputBuilder.Reset()
					continue
				}
				result.HasWebSearchToolUse = true
				result.WebSearchToolUseIndex = currentToolIndex
				var input map[string]string
				if err := json.Unmarshal([]byte(toolInputBuilder.String()), &input); err == nil {
					result.WebSearchQuery = strings.TrimSpace(input["query"])
				}
				currentToolName = ""
				currentToolIndex = -1
				toolInputBuilder.Reset()
			}
		}
	}

	return result
}

func FilterChunksForClient(chunks [][]byte, webSearchToolUseIndex, indexOffset int) [][]byte {
	filtered := make([][]byte, 0, len(chunks))
	for _, chunk := range chunks {
		adjusted, shouldForward := filterSSEChunk(chunk, webSearchToolUseIndex, indexOffset)
		if shouldForward {
			filtered = append(filtered, adjusted)
		}
	}
	return filtered
}

func AdjustSSEChunk(chunk []byte, offset int) ([]byte, bool) {
	return filterSSEChunk(chunk, -1, offset)
}

// RewriteFinalKiroCredits 把缓冲 chunks 里 message_delta 事件的
// usage._sub2api_kiro_credits 覆写为 totalCredits。多轮 web search 时每轮都会
// 产生一个 message_delta（只有最后一轮会转发给客户端/计费层），但每个 message_delta
// 只带本轮 credits；本函数在最后一轮转发前把它替换成所有轮次的累计总额，
// 确保 gateway 捕获到的 kiro_credits 是全部轮次之和而非仅最后一轮。
// totalCredits<=0 时原样返回（无 Kiro credits 场景，如非 PRO 账号）。
func RewriteFinalKiroCredits(chunks [][]byte, totalCredits float64) [][]byte {
	if totalCredits <= 0 {
		return chunks
	}
	for ci, chunk := range chunks {
		lines := strings.Split(string(chunk), "\n")
		changed := false
		for li, line := range lines {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				continue
			}
			if eventType, _ := event["type"].(string); eventType != "message_delta" {
				continue
			}
			usage, ok := event["usage"].(map[string]any)
			if !ok {
				continue
			}
			// 只覆写已经携带内部 credits 字段的 message_delta（即 Kiro PRO 反向缩放路径）。
			if _, has := usage["_sub2api_kiro_credits"]; !has {
				continue
			}
			usage["_sub2api_kiro_credits"] = totalCredits
			rewritten, err := json.Marshal(event)
			if err != nil {
				continue
			}
			lines[li] = "data: " + string(rewritten)
			changed = true
		}
		if changed {
			chunks[ci] = []byte(strings.Join(lines, "\n"))
		}
	}
	return chunks
}

func MaxContentBlockIndex(chunks [][]byte) int {
	maxIndex := -1
	for _, chunk := range chunks {
		lines := strings.Split(string(chunk), "\n")
		for _, line := range lines {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				continue
			}
			switch eventType, _ := event["type"].(string); eventType {
			case "content_block_start", "content_block_delta", "content_block_stop":
				if idx, ok := event["index"].(float64); ok && int(idx) > maxIndex {
					maxIndex = int(idx)
				}
			}
		}
	}
	return maxIndex
}

func filterSSEChunk(chunk []byte, webSearchToolUseIndex, indexOffset int) ([]byte, bool) {
	lines := strings.Split(string(chunk), "\n")
	var builder strings.Builder
	hasContent := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "event: ") {
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "data: ") {
				payload := strings.TrimSpace(strings.TrimPrefix(lines[i+1], "data: "))
				if shouldSuppressEventPayload(payload, webSearchToolUseIndex) {
					i++
					continue
				}
			}
			_, _ = builder.WriteString(line + "\n")
			hasContent = true
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if payload == "[DONE]" {
				continue
			}
			if shouldSuppressEventPayload(payload, webSearchToolUseIndex) {
				continue
			}
			adjusted := adjustEventPayload(payload, indexOffset)
			if adjusted == "" {
				continue
			}
			_, _ = builder.WriteString("data: " + adjusted + "\n")
			hasContent = true
			continue
		}

		_, _ = builder.WriteString(line + "\n")
		if strings.TrimSpace(line) != "" {
			hasContent = true
		}
	}

	if !hasContent {
		return nil, false
	}
	return []byte(builder.String()), true
}

func shouldSuppressEventPayload(payload string, webSearchToolUseIndex int) bool {
	if payload == "" {
		return false
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return false
	}
	eventType, _ := event["type"].(string)
	if eventType == "message_start" || eventType == "message_delta" || eventType == "message_stop" {
		return true
	}
	if webSearchToolUseIndex < 0 {
		return false
	}
	if idx, ok := event["index"].(float64); ok && int(idx) == webSearchToolUseIndex {
		return true
	}
	return false
}

func adjustEventPayload(payload string, indexOffset int) string {
	if payload == "" || indexOffset == 0 {
		return payload
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return payload
	}
	switch eventType, _ := event["type"].(string); eventType {
	case "content_block_start", "content_block_delta", "content_block_stop":
		if idx, ok := event["index"].(float64); ok {
			event["index"] = int(idx) + indexOffset
			if adjusted, err := json.Marshal(event); err == nil {
				return string(adjusted)
			}
		}
	}
	return payload
}
