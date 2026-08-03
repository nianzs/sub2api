package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/zed"
)

// zedRefreshWindow is how long before a minted token's exp it is replaced.
// Zed's LLM tokens are short-lived, so the window is small relative to the
// OAuth-refresh platforms.
const zedRefreshWindow = 2 * time.Minute

// ZedTokenRefresher implements TokenRefresher for Zed accounts.
//
// Unlike the OAuth platforms here, there is no refresh token: the persistent
// access_token is a minting credential that does not rotate, and every "refresh"
// mints a brand-new short-lived LLM token from it.
type ZedTokenRefresher struct {
	zedOAuthService *ZedOAuthService
}

func NewZedTokenRefresher(zedOAuthService *ZedOAuthService) *ZedTokenRefresher {
	return &ZedTokenRefresher{zedOAuthService: zedOAuthService}
}

// CacheKey returns the distributed-lock key, matching the token provider's.
func (r *ZedTokenRefresher) CacheKey(account *Account) string {
	return ZedTokenCacheKey(account)
}

// CanRefresh reports whether this refresher handles the account.
func (r *ZedTokenRefresher) CanRefresh(account *Account) bool {
	if account == nil {
		return false
	}
	if account.Platform != PlatformZed || account.Type != AccountTypeOAuth {
		return false
	}
	// Without the minting credential there is nothing to mint from.
	return strings.TrimSpace(account.GetCredential(zed.CredentialAccessToken)) != ""
}

// NeedsRefresh reports whether the cached LLM token is missing or near expiry.
//
// A missing token counts as needing refresh — unlike the OAuth platforms, where
// an absent expires_at means "long-lived credential, leave alone", here it means
// no token has been minted yet.
func (r *ZedTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	if !r.CanRefresh(account) {
		return false
	}
	if strings.TrimSpace(account.GetCredential(zed.CredentialLLMToken)) == "" {
		return true
	}
	expiresAt := account.GetCredentialAsTime(zed.CredentialExpiresAt)
	if expiresAt == nil {
		return true
	}
	if refreshWindow < zedRefreshWindow {
		refreshWindow = zedRefreshWindow
	}
	return time.Until(*expiresAt) < refreshWindow
}

// Refresh mints a new LLM token, returning the full credential set with the
// persistent fields preserved.
func (r *ZedTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r.zedOAuthService == nil {
		return nil, errors.New("zed oauth service is nil")
	}

	tokenInfo, err := r.zedOAuthService.MintToken(ctx, account)
	if err != nil {
		return nil, err
	}

	return MergeCredentials(account.Credentials, r.zedOAuthService.BuildAccountCredentials(tokenInfo)), nil
}
