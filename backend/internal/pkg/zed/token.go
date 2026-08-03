package zed

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// TokenRefreshSkew is how long before a minted JWT's exp it is treated as due
// for replacement, matching the upstream client's own margin.
const TokenRefreshSkew = 60 * time.Second

// LLMTokenResponse is the body of POST /client/llm_tokens.
type LLMTokenResponse struct {
	Token string `json:"token"`
}

// ParseJWTExpiry reads the exp claim from a minted LLM token. The signature is
// not verified — the token is opaque to us and only its lifetime is needed.
func ParseJWTExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("parse jwt: expected 3 segments, got %d", len(parts))
	}

	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse jwt payload: %w", err)
	}

	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("parse jwt claims: %w", err)
	}
	if claims.Exp <= 0 {
		return time.Time{}, fmt.Errorf("parse jwt: missing exp claim")
	}
	return time.Unix(claims.Exp, 0), nil
}

// MintAuthorization builds the Authorization header value for token minting and
// for the account-info endpoint.
//
// The upstream expects the user_id, a single space, then the raw credential JSON
// document — not a standard bearer scheme.
func MintAuthorization(userID, credentialJSON string) string {
	return userID + " " + credentialJSON
}

// ApplyMintHeaders sets the headers required by POST /client/llm_tokens and
// GET /client/users/me.
//
// Callers must have validated systemID via ValidateSystemID. An empty value is
// omitted rather than sent blank, because a blank header is strictly worse than
// an absent one upstream — it is not a license to skip validation.
func ApplyMintHeaders(h http.Header, userID, credentialJSON, systemID, version string) {
	if version == "" {
		version = DefaultZedVersion
	}
	if systemID == "" {
		systemID = DefaultSystemID
	}
	h.Set("Authorization", MintAuthorization(userID, credentialJSON))
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("User-Agent", UserAgent(version))
	h.Set(HeaderZedSystemID, systemID)
}

// ApplyCompletionHeaders sets the headers required by POST /completions.
//
// Every header set here is one the genuine client sends, and the upstream
// validates requests against them: omitting any of them makes an otherwise
// working account fail with trial_blocked (403). That includes the User-Agent,
// which must look like Zed rather than an HTTP library default.
//
// Callers must have validated systemID via ValidateSystemID; an unvalidated
// empty value is omitted rather than sent blank.
func ApplyCompletionHeaders(h http.Header, token, systemID, version string) {
	if version == "" {
		version = DefaultZedVersion
	}
	if systemID == "" {
		systemID = DefaultSystemID
	}
	h.Set("Authorization", "Bearer "+token)
	h.Set("Content-Type", "application/json")
	h.Set(HeaderZedVersion, version)
	h.Set(HeaderSupportsStatusMessages, "true")
	h.Set(HeaderSupportsStreamEnded, "true")
	h.Set("User-Agent", UserAgent(version))
	h.Set(HeaderZedSystemID, systemID)
}
