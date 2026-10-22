package kiro

import (
	"encoding/json"
	"strings"
)

// retainedKiroInputBody projects the final wire payload into the input format
// consumed by the local token estimator and cache tracker. Never reuse dropped
// source messages, account identifiers or base64 as ordinary text.
func retainedKiroInputBody(payload KiroPayload, original []byte) ([]byte, error) {
	var source map[string]any
	if err := json.Unmarshal(original, &source); err != nil {
		return nil, err
	}
	messages := []any{}
	tools := []any{}
	addUser := func(user *KiroUserInputMessage) {
		blocks := []any{map[string]any{"type": "text", "text": user.Content}}
		for _, image := range user.Images {
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/" + image.Format, "data": image.Source.Bytes}})
		}
		if ctx := user.UserInputMessageContext; ctx != nil {
			for _, result := range ctx.ToolResults {
				content := []any{}
				for _, part := range result.Content {
					content = append(content, map[string]any{"type": "text", "text": part.Text})
				}
				blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": result.ToolUseID, "content": content, "is_error": result.Status == "error"})
			}
			for _, tool := range ctx.Tools {
				spec := tool.ToolSpecification
				tools = append(tools, map[string]any{"name": spec.Name, "description": spec.Description, "input_schema": spec.InputSchema.JSON})
			}
		}
		messages = append(messages, map[string]any{"role": "user", "content": blocks})
	}
	for _, entry := range payload.ConversationState.History {
		if entry.UserInputMessage != nil {
			addUser(entry.UserInputMessage)
		}
		if assistant := entry.AssistantResponseMessage; assistant != nil {
			blocks := []any{map[string]any{"type": "text", "text": assistant.Content}}
			for _, tool := range assistant.ToolUses {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": tool.ToolUseID, "name": tool.Name, "input": tool.Input})
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
		}
	}
	addUser(&payload.ConversationState.CurrentMessage.UserInputMessage)
	// Preserve the requested cache TTL, but fingerprint only retained content.
	// Original cache locations cannot survive history cuts. Activate retained
	// message boundaries only; choose the shorter TTL for mixed TTL requests
	// so truncation never upgrades a 5m creation charge to a 1h charge.
	var cacheControl map[string]any
	consider := func(value any) {
		block, _ := value.(map[string]any)
		cc, _ := block["cache_control"].(map[string]any)
		typeName, _ := cc["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(typeName), "ephemeral") {
			return
		}
		ttl, _ := cc["ttl"].(string)
		isOneHour := strings.EqualFold(strings.TrimSpace(ttl), "1h")
		if cacheControl == nil || !isOneHour {
			cacheControl = map[string]any{"type": "ephemeral", "ttl": "5m"}
			if isOneHour {
				cacheControl["ttl"] = "1h"
			}
		}
	}
	// Match the cache tracker's supported block locations. A schema property or
	// tool argument named cache_control must not change the billing TTL.
	for _, field := range []string{"tools", "system"} {
		blocks, _ := source[field].([]any)
		for _, block := range blocks {
			consider(block)
		}
	}
	sourceMessages, _ := source["messages"].([]any)
	for _, value := range sourceMessages {
		message, _ := value.(map[string]any)
		blocks, _ := message["content"].([]any)
		for _, block := range blocks {
			consider(block)
		}
	}
	if cacheControl != nil {
		first := messages[0].(map[string]any)["content"].([]any)
		first[0].(map[string]any)["cache_control"] = cacheControl
	}
	return json.Marshal(map[string]any{"model": source["model"], "messages": messages, "tools": tools, "tool_choice": source["tool_choice"]})
}
