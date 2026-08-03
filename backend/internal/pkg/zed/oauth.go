package zed

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// rsaKeyBits matches the key size the Zed client generates for the native
// sign-in handshake.
const rsaKeyBits = 2048

// uuidPattern matches the canonical 8-4-4-4-12 hex form Zed's local database
// uses for system_id. Version and variant nibbles are deliberately unconstrained
// so a different UUID version does not read as malformed.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidateSystemID checks the per-account system_id.
//
// Zed ties plan and trial eligibility to the system_id an account was registered
// under. An absent value makes ApplyMintHeaders/ApplyCompletionHeaders omit the
// x-zed-system-id header entirely, which mints fine but makes /completions
// return trial_blocked (403) — a guaranteed inference failure that only shows up
// once real traffic arrives. Rejecting it up front is the only way it stays
// visible.
func ValidateSystemID(systemID string) error {
	if strings.TrimSpace(systemID) == "" {
		return errors.New("zed system_id is required: without it every /completions request returns trial_blocked (403)")
	}
	return nil
}

// ResolveSystemID returns the provided system_id if non-empty, otherwise falls
// back to DefaultSystemID. Use this at credential storage time so that every
// account always has a usable system_id without requiring manual configuration.
func ResolveSystemID(systemID string) string {
	systemID = strings.TrimSpace(systemID)
	if systemID == "" {
		return DefaultSystemID
	}
	return systemID
}

// IsUUIDLike reports whether a system_id has the shape Zed's local database
// uses.
//
// This is advisory only, and deliberately not enforced: rejecting a non-UUID
// would brick the whole platform the day upstream changes the format, while a
// warning still catches the obvious paste errors (a file path, a whole JSON
// document). Only emptiness is a hard error — see ValidateSystemID.
func IsUUIDLike(systemID string) bool {
	return uuidPattern.MatchString(strings.TrimSpace(systemID))
}

// DefaultCallbackPort is the port advertised to zed.dev as native_app_port.
//
// Nothing listens on it. The browser's request to 127.0.0.1 fails, but the full
// callback URL — including the encrypted token — stays in the address bar for
// the operator to copy back, which is what makes this flow work for a remote
// deployment. It is a plausible value from the range the real client uses rather
// than an obviously synthetic one.
const DefaultCallbackPort = 43117

// Credentials is the decrypted payload delivered by the sign-in callback.
type Credentials struct {
	UserID          string `json:"user_id"`
	AccessToken     string `json:"access_token"`
	GitHubUserID    int64  `json:"github_user_id,omitempty"`
	GitHubUserLogin string `json:"github_user_login,omitempty"`
}

// GenerateKeyPair creates the RSA keypair for one sign-in attempt. The private
// key must be retained server-side, keyed by OAuth session, until the operator
// pastes the callback URL back.
func GenerateKeyPair() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, rsaKeyBits)
}

// EncodePublicKey renders a public key the way the Zed client does: PKCS#1
// RSAPublicKey DER, base64url encoded without padding.
func EncodePublicKey(key *rsa.PublicKey) string {
	der := x509.MarshalPKCS1PublicKey(key)
	return base64.RawURLEncoding.EncodeToString(der)
}

// MarshalPrivateKey serializes a private key to PKCS#8 PEM for session storage.
func MarshalPrivateKey(key *rsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("marshal private key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// ParsePrivateKey restores a private key produced by MarshalPrivateKey.
func ParsePrivateKey(encoded string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(encoded))
	if block == nil {
		return nil, fmt.Errorf("decode private key: no PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("parse private key: unexpected type %T", parsed)
	}
	return key, nil
}

// BuildAuthURL constructs the sign-in URL the operator opens in a browser.
func BuildAuthURL(publicKey *rsa.PublicKey, port int) string {
	if port <= 0 {
		port = DefaultCallbackPort
	}
	query := url.Values{}
	query.Set("native_app_port", strconv.Itoa(port))
	query.Set("native_app_public_key", EncodePublicKey(publicKey))
	return SignInURL + "?" + query.Encode()
}

// ParseCallback extracts user_id and the encrypted access token from a pasted
// callback URL. A bare query string ("user_id=...&access_token=...") is also
// accepted, since operators sometimes copy only that part.
func ParseCallback(raw string) (userID string, encryptedToken string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("callback url is empty")
	}

	query := raw
	if idx := strings.Index(raw, "?"); idx >= 0 {
		query = raw[idx+1:]
	} else if strings.Contains(raw, "://") {
		return "", "", fmt.Errorf("callback url has no query string")
	}
	if idx := strings.Index(query, "#"); idx >= 0 {
		query = query[:idx]
	}

	values, err := url.ParseQuery(query)
	if err != nil {
		return "", "", fmt.Errorf("parse callback url: %w", err)
	}

	userID = strings.TrimSpace(values.Get("user_id"))
	encryptedToken = strings.TrimSpace(values.Get("access_token"))

	if userID == "" {
		return "", "", fmt.Errorf("callback url is missing user_id")
	}
	if encryptedToken == "" {
		return "", "", fmt.Errorf("callback url is missing access_token")
	}
	return userID, encryptedToken, nil
}

// DecryptAccessToken decrypts the callback's access_token.
//
// The ciphertext is base64url encoded and encrypted with RSA-OAEP/SHA-256.
// Padding is tolerated in either form because the value is copied by hand.
func DecryptAccessToken(key *rsa.PrivateKey, encrypted string) (string, error) {
	ciphertext, err := decodeBase64URLLenient(encrypted)
	if err != nil {
		return "", fmt.Errorf("decode access token: %w", err)
	}

	plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt access token: %w", err)
	}
	return string(plaintext), nil
}

// BuildCredentials assembles the account credentials from a decrypted callback.
//
// The plaintext is the credential JSON Zed itself stores. systemID must come
// from the operator's own Zed installation: the upstream ties plan and trial
// access to the system_id the account was registered under, so a mismatched
// value causes trial_blocked (403) at inference time even though sign-in and
// token minting succeed.
func BuildCredentials(userID, plaintext, systemID string) (map[string]any, error) {
	systemID = ResolveSystemID(systemID)
	if strings.TrimSpace(plaintext) == "" {
		return nil, fmt.Errorf("decrypted callback payload is empty")
	}

	// Match the real Zed client and zed2api: the decrypted value is opaque. It
	// may currently look like JSON, but parsing and rebuilding it couples us to
	// an upstream credential schema that can change independently.
	return map[string]any{
		CredentialUserID:      userID,
		CredentialAccessToken: plaintext,
		CredentialSystemID:    systemID,
	}, nil
}

// CredentialJSON returns the credential sent in the Authorization header when
// minting an LLM token. New exchanges preserve Zed's opaque document verbatim;
// credentials stored by older Sub2API builds are reconstructed for compatibility.
func CredentialJSON(creds map[string]any) (string, error) {
	accessToken, ok := creds[CredentialAccessToken].(string)
	if !ok || strings.TrimSpace(accessToken) == "" {
		return "", fmt.Errorf("credentials are missing access_token")
	}

	// Callback exchanges store the complete decrypted credential document in
	// access_token. Match the real Zed client and zed2api by sending that document
	// unchanged. Accounts created by older Sub2API builds store only the inner
	// token and fall through to the legacy reconstruction below.
	var opaqueCredential map[string]any
	if json.Unmarshal([]byte(accessToken), &opaqueCredential) == nil {
		return accessToken, nil
	}

	payload := make(map[string]any, 3)
	for _, key := range []string{CredentialAccessToken, CredentialGitHubID, CredentialGitHubLogin} {
		if value, ok := creds[key]; ok && value != nil {
			payload[key] = value
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal credential json: %w", err)
	}
	return string(encoded), nil
}

// decodeBase64URLLenient decodes base64url with or without padding.
func decodeBase64URLLenient(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "=")); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}
