package zed

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestNewEnvelopeUsesDistinctUUIDs(t *testing.T) {
	// The real client sends two different v4 UUIDs. Reusing one value for both
	// is a request fingerprint that does not match a genuine client.
	for i := 0; i < 32; i++ {
		env := NewEnvelope(ProviderAnthropic, "claude-sonnet-5", json.RawMessage(`{}`))

		if env.ThreadID == env.PromptID {
			t.Fatal("thread_id and prompt_id must differ")
		}
		for name, value := range map[string]string{"thread_id": env.ThreadID, "prompt_id": env.PromptID} {
			parsed, err := uuid.Parse(value)
			if err != nil {
				t.Fatalf("%s is not a UUID: %v", name, err)
			}
			if parsed.Version() != 4 {
				t.Errorf("%s version = %d, want 4", name, parsed.Version())
			}
			if parsed.Variant() != uuid.RFC4122 {
				t.Errorf("%s variant = %v, want RFC4122", name, parsed.Variant())
			}
		}
	}
}

func TestNewEnvelopeShape(t *testing.T) {
	env := NewEnvelope(ProviderOpenAI, "gpt-5.6-sol", json.RawMessage(`{"model":"gpt-5.6-sol"}`))

	encoded, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var decoded struct {
		Intent          string          `json:"intent"`
		Provider        string          `json:"provider"`
		Model           string          `json:"model"`
		ProviderRequest json.RawMessage `json:"provider_request"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if decoded.Intent != IntentUserPrompt {
		t.Errorf("intent = %q, want %q", decoded.Intent, IntentUserPrompt)
	}
	if decoded.Provider != ProviderOpenAI {
		t.Errorf("provider = %q, want %q", decoded.Provider, ProviderOpenAI)
	}
	if decoded.Model != "gpt-5.6-sol" {
		t.Errorf("model = %q, want %q", decoded.Model, "gpt-5.6-sol")
	}
	if string(decoded.ProviderRequest) != `{"model":"gpt-5.6-sol"}` {
		t.Errorf("provider_request = %s, want it nested verbatim", decoded.ProviderRequest)
	}
}

func TestProviderForModel(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-6":   ProviderAnthropic,
		"claude-sonnet-5":   ProviderAnthropic,
		"gpt-5.6-sol":       ProviderOpenAI,
		"gpt-5.5":           ProviderOpenAI,
		"gemini-2.5-pro":    ProviderGoogle,
		"grok-4":            ProviderXAI,
		"something-unknown": ProviderAnthropic,
	}
	for model, want := range cases {
		if got := ProviderForModel(model); got != want {
			t.Errorf("ProviderForModel(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestIsSupportedProvider(t *testing.T) {
	if !IsSupportedProvider(ProviderAnthropic) || !IsSupportedProvider(ProviderOpenAI) {
		t.Error("anthropic and open_ai must be supported")
	}
	if IsSupportedProvider(ProviderGoogle) || IsSupportedProvider(ProviderXAI) {
		t.Error("google and x_ai are not translatable by this build")
	}
}

func TestNormalizeModel(t *testing.T) {
	// Bare gpt-5.6 resolves to the gpt-5 family upstream and 403s as "not
	// included in your plan", so it is routed to the real Sol variant. Every
	// other ID must pass through so group model mapping stays authoritative and
	// the Sol/Terra/Luna variants are never collapsed.
	cases := map[string]string{
		"gpt-5.6":           "gpt-5.6-sol",
		"gpt-5.6-sol":       "gpt-5.6-sol",
		"gpt-5.6-terra":     "gpt-5.6-terra",
		"gpt-5.6-luna":      "gpt-5.6-luna",
		"gpt-5.5":           "gpt-5.5",
		"claude-opus-4-6":   "claude-opus-4-6",
		"claude-sonnet-4-5": "claude-sonnet-4-5",
	}
	for input, want := range cases {
		if got := NormalizeModel(input); got != want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestUserAgentLooksLikeZed(t *testing.T) {
	// A non-Zed User-Agent is rejected by the upstream's abuse checks.
	ua := UserAgent("1.13.1")
	if want := "Zed/1.13.1 ("; len(ua) < len(want) || ua[:len(want)] != want {
		t.Errorf("UserAgent = %q, want it to start with %q", ua, want)
	}
}
