// Package zed implements the wire protocol for Zed's LLM gateway
// (cloud.zed.dev), which proxies both Anthropic and OpenAI models behind a
// single request envelope.
package zed

// Upstream endpoints.
const (
	BaseURL            = "https://cloud.zed.dev"
	CompletionsPath    = "/completions"
	LLMTokensPath      = "/client/llm_tokens"
	ModelsPath         = "/models"
	UsersMePath        = "/client/users/me"
	SignInURL          = "https://zed.dev/native_app_signin"
	SignInSucceededURL = "https://zed.dev/native_app_signin_succeeded"
)

// DefaultZedVersion is the Zed client version reported to the LLM endpoint.
//
// This must be the bare semantic version as produced by Zed's
// `app_version.to_string()` (e.g. "1.13.1") — NOT the full bundle string
// ("1.13.1+stable.<build>.<sha>"). The backend applies trial and model
// restrictions differently to unexpected client versions, which surfaces as
// spurious `trial_blocked` (403) errors.
//
// Operators can override this per deployment; keep it in step with a real Zed
// release. See ZedVersionForAccount.
const DefaultZedVersion = "1.13.1"

// Provider identifiers used in the envelope's "provider" field.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "open_ai"
	ProviderGoogle    = "google"
	ProviderXAI       = "x_ai"
)

// Intent identifies the request's purpose to the upstream. Zed's agent panel
// sends "user_prompt" for ordinary completions.
const IntentUserPrompt = "user_prompt"

// Credential keys stored on a Zed account.
const (
	CredentialUserID      = "user_id"
	CredentialAccessToken = "access_token"
	// CredentialSystemID is the system_id the Zed client registered this
	// account under. Zed's trial-abuse detection ties plan access to the
	// system_id embedded in the minted LLM token, so a value that does not
	// match the originating client makes /completions return trial_blocked
	// (403) even when login and token minting both succeed. It is therefore a
	// per-account credential, never a shared constant.
	CredentialSystemID    = "system_id"
	CredentialGitHubLogin = "github_user_login"
	CredentialGitHubID    = "github_user_id"
	// CredentialZedVersion optionally pins the client version for one account.
	CredentialZedVersion = "zed_version"
	// CredentialLLMToken caches the minted short-lived JWT.
	CredentialLLMToken  = "llm_token"
	CredentialExpiresAt = "expires_at"
)

// Header names required by the upstream. The genuine Zed client attaches all of
// these to every /completions request and trial accounts are validated against
// them; omitting any one makes the backend reject the request as
// trial_blocked even though the same account works inside the editor.
const (
	HeaderZedVersion             = "x-zed-version"
	HeaderZedSystemID            = "x-zed-system-id"
	HeaderSupportsStatusMessages = "x-zed-client-supports-status-messages"
	HeaderSupportsStreamEnded    = "x-zed-client-supports-stream-ended-request-completion-status"
)
