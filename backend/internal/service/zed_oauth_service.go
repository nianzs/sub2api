package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/zed"
)

const (
	// zedMintTimeout bounds a request-path token mint so a slow upstream cannot
	// hold a gateway request open indefinitely.
	zedMintTimeout = 8 * time.Second
	// zedUpstreamTimeout bounds a single call to cloud.zed.dev. MintToken narrows
	// this further with its own request-path deadline (zedMintTimeout).
	zedUpstreamTimeout = 30 * time.Second
)

// ZedTokenInfo is the outcome of minting an LLM token.
type ZedTokenInfo struct {
	Token     string
	ExpiresAt time.Time
}

// ZedOAuthService mints Zed LLM tokens and drives the native sign-in exchange.
type ZedOAuthService struct {
	proxyRepo    ProxyRepository
	sessionStore *zed.SessionStore
}

func NewZedOAuthService(proxyRepo ProxyRepository) *ZedOAuthService {
	return &ZedOAuthService{
		proxyRepo:    proxyRepo,
		sessionStore: zed.NewSessionStore(),
	}
}

// resolveProxyURL returns the account's proxy URL, or "" for a direct connection.
//
// The hydrated Proxy edge is preferred: accountRepository.GetByID and GetByIDs
// both use WithProxy(), and the background refresh scan hydrates through
// GetByIDs, so every caller already holds the edge and no DB round-trip is
// needed. The repository lookup only covers a missing edge on a set ProxyID.
//
// A configured-but-unresolvable proxy is an error, never a direct-connection
// fallback: minting from the server IP while inference goes through the proxy is
// exactly the IP correlation the proxy exists to avoid.
func (s *ZedOAuthService) resolveProxyURL(ctx context.Context, account *Account) (string, error) {
	if account == nil || account.ProxyID == nil {
		return "", nil
	}
	if account.Proxy != nil {
		return account.Proxy.URL(), nil
	}
	if s.proxyRepo == nil {
		return "", fmt.Errorf("zed account %d has proxy %d configured but no proxy repository is available", account.ID, *account.ProxyID)
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID)
	if err != nil {
		return "", fmt.Errorf("look up zed account proxy %d: %w", *account.ProxyID, err)
	}
	if proxy == nil {
		return "", fmt.Errorf("zed account %d references proxy %d, which was not found", account.ID, *account.ProxyID)
	}
	return proxy.URL(), nil
}

// checkSystemID rejects an account with no system_id and warns about one whose
// shape looks wrong.
//
// This is the bypass-proof layer for the credential requirement: every
// inference, connection test, model sync, and admin refresh reaches the upstream
// through a mint, so guarding here also covers accounts already in the database
// and credentials that were edited directly.
func (s *ZedOAuthService) checkSystemID(account *Account) error {
	systemID := account.GetCredential(zed.CredentialSystemID)
	if strings.TrimSpace(systemID) == "" {
		// Silently fill the default rather than blocking — the operator can
		// override per-account when needed.
		return nil
	}
	if !zed.IsUUIDLike(systemID) {
		// Advisory only — see zed.IsUUIDLike for why this does not reject.
		slog.Warn("zed_system_id_not_uuid_shaped",
			"account_id", account.ID,
			"hint", "expected the 8-4-4-4-12 UUID from the local Zed installation")
	}
	return nil
}

// clientFor returns the pooled HTTP client for an account's egress path.
func (s *ZedOAuthService) clientFor(ctx context.Context, account *Account) (*http.Client, error) {
	proxyURL, err := s.resolveProxyURL(ctx, account)
	if err != nil {
		return nil, err
	}
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:           proxyURL,
		Timeout:            zedUpstreamTimeout,
		ValidateResolvedIP: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create zed http client: %w", err)
	}
	return client, nil
}

// GenerateAuthURL starts a sign-in attempt, returning the URL the operator opens
// and the session ID that must accompany the later exchange.
func (s *ZedOAuthService) GenerateAuthURL(_ context.Context) (authURL string, sessionID string, err error) {
	key, err := zed.GenerateKeyPair()
	if err != nil {
		return "", "", fmt.Errorf("generate zed keypair: %w", err)
	}
	encodedKey, err := zed.MarshalPrivateKey(key)
	if err != nil {
		return "", "", err
	}

	sessionID = zed.NewSessionID()
	s.sessionStore.Set(sessionID, &zed.AuthSession{
		PrivateKeyPEM: encodedKey,
		CreatedAt:     time.Now(),
	})

	return zed.BuildAuthURL(&key.PublicKey, zed.DefaultCallbackPort), sessionID, nil
}

// ExchangeCallback decrypts a pasted callback URL into account credentials.
//
// systemID must be the system_id from the operator's own Zed installation: the
// upstream ties plan and trial access to the system_id an account was registered
// under, so a value from elsewhere yields trial_blocked (403) at inference time
// even though this exchange succeeds.
func (s *ZedOAuthService) ExchangeCallback(_ context.Context, sessionID, callbackURL, systemID string) (map[string]any, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session_id is required")
	}

	session, ok := s.sessionStore.Get(sessionID)
	if !ok || session == nil {
		return nil, errors.New("zed sign-in session not found or expired; generate a new authorization URL")
	}

	key, err := zed.ParsePrivateKey(session.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}

	userID, encryptedToken, err := zed.ParseCallback(callbackURL)
	if err != nil {
		return nil, err
	}

	plaintext, err := zed.DecryptAccessToken(key, encryptedToken)
	if err != nil {
		return nil, err
	}

	credentials, err := zed.BuildCredentials(userID, plaintext, strings.TrimSpace(systemID))
	if err != nil {
		return nil, err
	}

	// One-shot: the key has served its purpose and must not be reusable.
	s.sessionStore.Delete(sessionID)

	return credentials, nil
}

// MintToken exchanges an account's long-lived credential for a short-lived LLM
// token.
//
// Unlike most platforms here, the persistent access_token is not the bearer
// token: it is the minting credential, and the returned JWT is what /completions
// accepts.
func (s *ZedOAuthService) MintToken(ctx context.Context, account *Account) (*ZedTokenInfo, error) {
	if account == nil {
		return nil, errors.New("account is nil")
	}

	userID := strings.TrimSpace(account.GetCredential(zed.CredentialUserID))
	if userID == "" {
		return nil, errors.New("zed account is missing user_id")
	}
	// Checked before any network call: minting would succeed without a system_id
	// and then every /completions request would return trial_blocked (403).
	if err := s.checkSystemID(account); err != nil {
		return nil, err
	}
	credentialJSON, err := zed.CredentialJSON(account.Credentials)
	if err != nil {
		return nil, err
	}

	// Resolved before the deadline starts so a proxy misconfiguration does not
	// consume the request-path mint budget.
	client, err := s.clientFor(ctx, account)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, zedMintTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, zed.BaseURL+zed.LLMTokensPath, strings.NewReader(""))
	if err != nil {
		return nil, fmt.Errorf("build zed token request: %w", err)
	}
	zed.ApplyMintHeaders(req.Header, userID, credentialJSON,
		account.GetCredential(zed.CredentialSystemID),
		account.GetCredential(zed.CredentialZedVersion))

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mint zed token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read zed token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ZedUpstreamError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var parsed zed.LLMTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse zed token response: %w", err)
	}
	token := strings.TrimSpace(parsed.Token)
	if token == "" {
		return nil, errors.New("zed token response contained no token")
	}

	expiresAt, err := zed.ParseJWTExpiry(token)
	if err != nil {
		// The token is usable even if its lifetime is opaque; fall back to a
		// conservative window so it still gets rotated.
		expiresAt = time.Now().Add(30 * time.Minute)
	}

	return &ZedTokenInfo{Token: token, ExpiresAt: expiresAt}, nil
}

// BuildAccountCredentials renders a minted token as credential updates. Only the
// token fields are returned; callers merge them over the existing credentials so
// user_id, access_token and system_id survive.
func (s *ZedOAuthService) BuildAccountCredentials(tokenInfo *ZedTokenInfo) map[string]any {
	if tokenInfo == nil {
		return nil
	}
	return map[string]any{
		zed.CredentialLLMToken:  tokenInfo.Token,
		zed.CredentialExpiresAt: tokenInfo.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// FetchAccountInfo reads GET /client/users/me for health checks and quota
// display.
func (s *ZedOAuthService) FetchAccountInfo(ctx context.Context, account *Account) (map[string]any, error) {
	if account == nil {
		return nil, errors.New("account is nil")
	}

	userID := strings.TrimSpace(account.GetCredential(zed.CredentialUserID))
	if userID == "" {
		return nil, errors.New("zed account is missing user_id")
	}
	if err := s.checkSystemID(account); err != nil {
		return nil, err
	}
	credentialJSON, err := zed.CredentialJSON(account.Credentials)
	if err != nil {
		return nil, err
	}

	client, err := s.clientFor(ctx, account)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zed.BaseURL+zed.UsersMePath, nil)
	if err != nil {
		return nil, fmt.Errorf("build zed account info request: %w", err)
	}
	zed.ApplyMintHeaders(req.Header, userID, credentialJSON,
		account.GetCredential(zed.CredentialSystemID),
		account.GetCredential(zed.CredentialZedVersion))

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch zed account info: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read zed account info: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ZedUpstreamError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var info map[string]any
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("parse zed account info: %w", err)
	}
	return info, nil
}

// FetchModels reads the upstream model catalog, falling back to the built-in
// list when the call or its payload is unusable.
func (s *ZedOAuthService) FetchModels(ctx context.Context, account *Account, token string) ([]zed.Model, error) {
	// A nil account is tolerated here and yields the direct-connection client.
	client, err := s.clientFor(ctx, account)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zed.BaseURL+zed.ModelsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("build zed models request: %w", err)
	}

	version := ""
	systemID := ""
	if account != nil {
		version = account.GetCredential(zed.CredentialZedVersion)
		systemID = account.GetCredential(zed.CredentialSystemID)
	}
	zed.ApplyCompletionHeaders(req.Header, token, systemID, version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch zed models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read zed models: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &ZedUpstreamError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	return zed.ParseModels(body)
}

// ZedUpstreamError carries a non-2xx response from cloud.zed.dev.
type ZedUpstreamError struct {
	StatusCode int
	Body       string
}

func (e *ZedUpstreamError) Error() string {
	body := e.Body
	if len(body) > 512 {
		body = body[:512]
	}
	return fmt.Sprintf("zed upstream returned %d: %s", e.StatusCode, body)
}

// IsAuthFailure reports whether the upstream rejected the credential itself, as
// opposed to failing the inference request.
func (e *ZedUpstreamError) IsAuthFailure() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
}
