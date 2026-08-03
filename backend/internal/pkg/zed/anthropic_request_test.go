package zed

import (
	"encoding/json"
	"testing"
)

// decodeProviderRequest builds a provider request and unmarshals it for
// inspection.
func decodeProviderRequest(t *testing.T, body string, model string) map[string]json.RawMessage {
	t.Helper()
	raw, err := BuildAnthropicProviderRequest([]byte(body), model)
	if err != nil {
		t.Fatalf("BuildAnthropicProviderRequest: %v", err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal provider request: %v", err)
	}
	return out
}

func TestBuildAnthropicProviderRequestPreservesSystemCacheControl(t *testing.T) {
	body := `{"model":"claude-sonnet-5","max_tokens":1024,"output_config":{"effort":"xhigh"},` +
		`"system":[{"type":"text","text":"system prompt","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":"ping"}]}`

	req := decodeProviderRequest(t, body, "claude-sonnet-5")

	var system []struct {
		Type         string `json:"type"`
		Text         string `json:"text"`
		CacheControl *struct {
			Type string `json:"type"`
		} `json:"cache_control"`
	}
	if err := json.Unmarshal(req["system"], &system); err != nil {
		t.Fatalf("system is not an array: %v", err)
	}
	if len(system) != 1 {
		t.Fatalf("system length = %d, want 1", len(system))
	}
	if system[0].Text != "system prompt" {
		t.Errorf("system[0].text = %q, want %q", system[0].Text, "system prompt")
	}
	if system[0].CacheControl == nil {
		t.Fatal("cache_control was dropped; cache breakpoints must survive")
	}
	if system[0].CacheControl.Type != "ephemeral" {
		t.Errorf("cache_control.type = %q, want %q", system[0].CacheControl.Type, "ephemeral")
	}

	var outputConfig struct {
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(req["output_config"], &outputConfig); err != nil {
		t.Fatalf("output_config missing: %v", err)
	}
	if outputConfig.Effort != "xhigh" {
		t.Errorf("output_config.effort = %q, want %q", outputConfig.Effort, "xhigh")
	}
}

func TestBuildAnthropicProviderRequestMergesTrailingSystemMessage(t *testing.T) {
	body := `{"model":"claude-sonnet-5","max_tokens":1024,` +
		`"system":[{"type":"text","text":"base system","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"ping"}]},` +
		`{"role":"system","content":"workspace instructions"}]}`

	req := decodeProviderRequest(t, body, "claude-sonnet-5")

	var system []struct {
		Text         string `json:"text"`
		CacheControl *struct {
			Type string `json:"type"`
		} `json:"cache_control"`
	}
	if err := json.Unmarshal(req["system"], &system); err != nil {
		t.Fatalf("system is not an array: %v", err)
	}
	if len(system) != 2 {
		t.Fatalf("system length = %d, want 2", len(system))
	}
	if system[0].Text != "base system" {
		t.Errorf("system[0].text = %q, want %q", system[0].Text, "base system")
	}
	if system[0].CacheControl == nil || system[0].CacheControl.Type != "ephemeral" {
		t.Error("cache_control on the original block must survive the merge")
	}
	if system[1].Text != "workspace instructions" {
		t.Errorf("system[1].text = %q, want %q", system[1].Text, "workspace instructions")
	}

	var messages []struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(req["messages"], &messages); err != nil {
		t.Fatalf("messages is not an array: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1 (system message must be removed)", len(messages))
	}
	if messages[0].Role != "user" {
		t.Errorf("messages[0].role = %q, want %q", messages[0].Role, "user")
	}
}

func TestBuildAnthropicProviderRequestPromotesSystemAndDeveloperMessages(t *testing.T) {
	body := `{"model":"claude-sonnet-5","messages":[` +
		`{"role":"system","content":"system"},` +
		`{"role":"developer","content":"developer"},` +
		`{"role":"user","content":"ping"}]}`

	req := decodeProviderRequest(t, body, "claude-sonnet-5")

	var system string
	if err := json.Unmarshal(req["system"], &system); err != nil {
		t.Fatalf("system should be a string when no top-level system exists: %v", err)
	}
	if system != "system\n\ndeveloper" {
		t.Errorf("system = %q, want %q", system, "system\n\ndeveloper")
	}

	var messages []struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(req["messages"], &messages); err != nil {
		t.Fatalf("messages is not an array: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("messages = %s, want only the user message", req["messages"])
	}
}

func TestBuildAnthropicProviderRequestDefaultsMaxTokens(t *testing.T) {
	req := decodeProviderRequest(t, `{"model":"claude-sonnet-5","messages":[]}`, "claude-sonnet-5")
	if got := string(req["max_tokens"]); got != "8192" {
		t.Errorf("max_tokens = %s, want 8192", got)
	}
}

func TestBuildAnthropicProviderRequestOmitsStream(t *testing.T) {
	// The real client sends no "stream" field for the Anthropic provider; the
	// upstream streams based on the endpoint.
	body := `{"model":"claude-sonnet-5","stream":true,"messages":[{"role":"user","content":"ping"}]}`
	req := decodeProviderRequest(t, body, "claude-sonnet-5")
	if _, ok := req["stream"]; ok {
		t.Error("provider_request must not carry a stream field")
	}
}

func TestBuildAnthropicProviderRequestPassesToolsThrough(t *testing.T) {
	body := `{"model":"claude-sonnet-5","messages":[],"tools":[{"name":"read","input_schema":{"type":"object"}}],` +
		`"tool_choice":{"type":"auto"},"thinking":{"type":"enabled","budget_tokens":1024}}`

	req := decodeProviderRequest(t, body, "claude-sonnet-5")

	var tools []struct {
		Name        string          `json:"name"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	if err := json.Unmarshal(req["tools"], &tools); err != nil {
		t.Fatalf("tools missing: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "read" {
		t.Fatalf("tools = %s, want the read tool preserved verbatim", req["tools"])
	}
	if _, ok := req["tool_choice"]; !ok {
		t.Error("tool_choice must be preserved")
	}
	if _, ok := req["thinking"]; !ok {
		t.Error("thinking must be preserved")
	}
}

func TestBuildAnthropicProviderRequestUsesNormalizedModel(t *testing.T) {
	req := decodeProviderRequest(t, `{"model":"claude-sonnet-5","messages":[]}`, "claude-opus-4-6")
	var model string
	if err := json.Unmarshal(req["model"], &model); err != nil {
		t.Fatalf("model missing: %v", err)
	}
	if model != "claude-opus-4-6" {
		t.Errorf("model = %q, want the caller-supplied model to win", model)
	}
}
