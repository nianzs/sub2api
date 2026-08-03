package zed

import (
	"encoding/json"
	"strings"
)

// defaultMaxTokensJSON is the fallback used when a request omits max_tokens,
// which Anthropic requires.
var defaultMaxTokensJSON = json.RawMessage("8192")

// anthropicPassthroughFields are copied verbatim from the client request into
// the provider_request. Passing them through unmodified keeps their official
// wire shape — notably tools/tool_choice, and output_config, which is how
// Anthropic's newer models carry adaptive reasoning effort and structured-output
// settings.
var anthropicPassthroughFields = []string{
	"temperature",
	"top_p",
	"top_k",
	"stop_sequences",
	"thinking",
	"output_config",
	"tools",
	"tool_choice",
	"metadata",
}

// BuildAnthropicProviderRequest converts a client Anthropic Messages request
// into the provider_request Zed expects for ProviderAnthropic.
//
// Two behaviours matter for cache correctness and are covered by tests:
//
//   - A top-level `system` array is preserved structurally, so cache_control
//     breakpoints set by clients such as Claude Code survive. Flattening it to a
//     string would silently discard them, raising cost and lowering the cache
//     hit rate.
//   - Some clients additionally place system/developer instructions inside
//     `messages`. Those are merged in as one extra system text block and removed
//     from `messages`, so the instruction is neither lost nor duplicated.
//
// Note the result intentionally carries no "stream" field: for the Anthropic
// provider the upstream streams based on the endpoint, and the real client does
// not send one.
func BuildAnthropicProviderRequest(body []byte, model string) (json.RawMessage, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}

	out := make(map[string]json.RawMessage, len(anthropicPassthroughFields)+4)

	modelJSON, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	out["model"] = modelJSON

	if raw, ok := req["max_tokens"]; ok && len(raw) > 0 && string(raw) != "null" {
		out["max_tokens"] = raw
	} else {
		out["max_tokens"] = defaultMaxTokensJSON
	}

	messages, systemFromMessages, err := splitSystemMessages(req["messages"])
	if err != nil {
		return nil, err
	}

	systemValue, err := buildSystemValue(req["system"], systemFromMessages)
	if err != nil {
		return nil, err
	}
	if systemValue != nil {
		out["system"] = systemValue
	}

	if messages == nil {
		messages = json.RawMessage("[]")
	}
	out["messages"] = messages

	for _, field := range anthropicPassthroughFields {
		if raw, ok := req[field]; ok && len(raw) > 0 && string(raw) != "null" {
			out[field] = raw
		}
	}

	return json.Marshal(out)
}

// isSystemInstructionRole reports whether a message role carries system-level
// instructions rather than conversation content.
func isSystemInstructionRole(role string) bool {
	return role == "system" || role == "developer"
}

// splitSystemMessages returns the conversation messages with system/developer
// entries removed, plus their concatenated text.
func splitSystemMessages(raw json.RawMessage) (json.RawMessage, string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, "", nil
	}

	var messages []json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		// Not an array: hand it back untouched rather than failing the request.
		return raw, "", nil
	}

	kept := make([]json.RawMessage, 0, len(messages))
	var systemParts []string

	for _, msg := range messages {
		var probe struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(msg, &probe); err != nil {
			kept = append(kept, msg)
			continue
		}
		if !isSystemInstructionRole(probe.Role) {
			kept = append(kept, msg)
			continue
		}
		if text := extractContentText(probe.Content); text != "" {
			systemParts = append(systemParts, text)
		}
	}

	out, err := json.Marshal(kept)
	if err != nil {
		return nil, "", err
	}
	return out, strings.Join(systemParts, "\n\n"), nil
}

// extractContentText pulls plain text out of an Anthropic content value, which
// may be a bare string or an array of typed blocks.
func extractContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}

	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}

	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// buildSystemValue combines the request's top-level system value with any text
// recovered from system-role messages.
//
// When both are present the result is an array so the original blocks keep
// their cache_control metadata and the message-level text is appended as one
// additional text block.
func buildSystemValue(topLevel json.RawMessage, fromMessages string) (json.RawMessage, error) {
	hasTopLevel := len(topLevel) > 0 && string(topLevel) != "null"

	switch {
	case !hasTopLevel && fromMessages == "":
		return nil, nil

	case !hasTopLevel:
		return json.Marshal(fromMessages)

	case fromMessages == "":
		// Verbatim: preserves array blocks and their cache_control.
		return topLevel, nil
	}

	entries, err := systemArrayEntries(topLevel)
	if err != nil {
		return nil, err
	}

	extra, err := json.Marshal(map[string]string{"type": "text", "text": fromMessages})
	if err != nil {
		return nil, err
	}
	entries = append(entries, extra)

	return json.Marshal(entries)
}

// systemArrayEntries normalizes a system value into system-block array entries.
func systemArrayEntries(raw json.RawMessage) ([]json.RawMessage, error) {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		block, err := json.Marshal(map[string]string{"type": "text", "text": asString})
		if err != nil {
			return nil, err
		}
		return []json.RawMessage{block}, nil
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}
