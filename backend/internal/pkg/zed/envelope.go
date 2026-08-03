package zed

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/google/uuid"
)

// Envelope is the request wrapper cloud.zed.dev/completions expects. The inner
// ProviderRequest is the native request body for the routed provider: an
// Anthropic Messages request for ProviderAnthropic, an OpenAI Responses request
// for ProviderOpenAI.
type Envelope struct {
	ThreadID        string          `json:"thread_id"`
	PromptID        string          `json:"prompt_id"`
	Intent          string          `json:"intent"`
	Provider        string          `json:"provider"`
	Model           string          `json:"model"`
	ProviderRequest json.RawMessage `json:"provider_request"`
}

// NewEnvelope wraps a provider-native request body.
//
// thread_id and prompt_id must be distinct v4 UUIDs: the real client sends two
// different values and reusing one for both is a known-bad shape.
func NewEnvelope(provider, model string, providerRequest json.RawMessage) *Envelope {
	return &Envelope{
		ThreadID:        uuid.NewString(),
		PromptID:        uuid.NewString(),
		Intent:          IntentUserPrompt,
		Provider:        provider,
		Model:           model,
		ProviderRequest: providerRequest,
	}
}

// ProviderForModel routes a model ID to its Zed provider by prefix.
func ProviderForModel(model string) string {
	switch {
	case strings.HasPrefix(model, "claude"):
		return ProviderAnthropic
	case strings.HasPrefix(model, "gpt-"):
		return ProviderOpenAI
	case strings.HasPrefix(model, "gemini"):
		return ProviderGoogle
	case strings.HasPrefix(model, "grok"):
		return ProviderXAI
	default:
		return ProviderAnthropic
	}
}

// IsSupportedProvider reports whether this build can translate the provider's
// request and response formats. Google and xAI are routable upstream but not
// yet implemented here.
func IsSupportedProvider(provider string) bool {
	return provider == ProviderAnthropic || provider == ProviderOpenAI
}

// NormalizeModel maps a client-supplied model ID onto one the upstream serves.
//
// The only rewrite applied is for bare "gpt-5.6", which the upstream resolves to
// the gpt-5 family and rejects with 403 "not included in your plan"; it is
// routed to the real gpt-5.6-sol variant instead. Every other ID is passed
// through untouched so that group-level model mapping stays authoritative and
// the Sol/Terra/Luna variants are never silently collapsed into one another.
func NormalizeModel(model string) string {
	if model == "gpt-5.6" {
		return "gpt-5.6-sol"
	}
	return model
}

// UserAgent builds the User-Agent the real Zed client sets globally:
//
//	format!("Zed/{} ({}; {})", AppVersion, std::env::consts::OS, std::env::consts::ARCH)
//
// The trial-abuse check rejects non-Zed User-Agents, so this must not be left
// to the HTTP client's default.
func UserAgent(version string) string {
	return fmt.Sprintf("Zed/%s (%s; %s)", version, goosToRust(runtime.GOOS), goarchToRust(runtime.GOARCH))
}

// goosToRust maps Go's GOOS onto Rust's std::env::consts::OS spelling.
func goosToRust(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	default:
		return goos
	}
}

// goarchToRust maps Go's GOARCH onto Rust's std::env::consts::ARCH spelling.
func goarchToRust(goarch string) string {
	switch goarch {
	case "arm64":
		return "aarch64"
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	default:
		return goarch
	}
}
