package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/zed"
)

const (
	zedTokenRefreshSkew = 2 * time.Minute
	zedTokenCacheSkew   = 3 * time.Minute
	zedRefreshLockTTL   = 30 * time.Second
	// zedRefreshLockPollInterval mirrors grokRefreshLockPollInterval.
	zedRefreshLockPollInterval = 25 * time.Millisecond
)

// zedRefreshLockWaitTimeout is how long a lock-losing worker waits for the
// holder's token before minting one itself. A var so tests can shrink it.
var zedRefreshLockWaitTimeout = 2 * time.Second

// ZedTokenCache reuses the shared provider token cache contract.
type ZedTokenCache = GeminiTokenCache

// zedAccountTokenMinter is the subset of ZedOAuthService this provider needs,
// kept narrow so tests can substitute a fake.
type zedAccountTokenMinter interface {
	MintToken(ctx context.Context, account *Account) (*ZedTokenInfo, error)
	BuildAccountCredentials(tokenInfo *ZedTokenInfo) map[string]any
}

// ZedTokenProvider supplies the bearer token for Zed inference requests.
//
// The token returned here is the short-lived minted JWT, not the account's
// persistent access_token — that one is only a minting credential and the
// upstream will not accept it on /completions.
type ZedTokenProvider struct {
	accountRepo     AccountRepository
	tokenCache      ZedTokenCache
	zedOAuthService zedAccountTokenMinter
	refreshAPI      *OAuthRefreshAPI
	executor        OAuthRefreshExecutor
	refreshPolicy   ProviderRefreshPolicy
}

func NewZedTokenProvider(
	accountRepo AccountRepository,
	tokenCache ZedTokenCache,
	zedOAuthService *ZedOAuthService,
) *ZedTokenProvider {
	return &ZedTokenProvider{
		accountRepo:     accountRepo,
		tokenCache:      tokenCache,
		zedOAuthService: zedOAuthService,
		refreshPolicy:   ZedProviderRefreshPolicy(),
	}
}

func (p *ZedTokenProvider) SetRefreshAPI(api *OAuthRefreshAPI, executor OAuthRefreshExecutor) {
	p.refreshAPI = api
	p.executor = executor
}

func (p *ZedTokenProvider) SetRefreshPolicy(policy ProviderRefreshPolicy) {
	p.refreshPolicy = policy
}

// GetAccessToken returns a usable LLM token, minting one when the cached value is
// absent or near expiry.
func (p *ZedTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformZed || account.Type != AccountTypeOAuth {
		return "", errors.New("not a zed oauth account")
	}

	cacheKey := ZedTokenCacheKey(account)
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}

	expiresAt := account.GetCredentialAsTime(zed.CredentialExpiresAt)
	hasToken := strings.TrimSpace(account.GetCredential(zed.CredentialLLMToken)) != ""
	needsRefresh := !hasToken || expiresAt == nil || time.Until(*expiresAt) <= zedTokenRefreshSkew

	if needsRefresh && p.refreshAPI != nil && p.executor != nil {
		result, err := p.refreshAPI.RefreshIfNeeded(ctx, account, p.executor, zedTokenRefreshSkew)
		if err != nil {
			if p.refreshPolicy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
		} else if result.LockHeld {
			if p.refreshPolicy.OnLockHeld == ProviderLockHeldWaitForCache && p.tokenCache != nil {
				if token, cacheErr := p.tokenCache.GetAccessToken(ctx, cacheKey); cacheErr == nil && strings.TrimSpace(token) != "" {
					return token, nil
				}
			}
		} else {
			if result.Account != nil {
				account = result.Account
			}
			if len(result.NewCredentials) > 0 {
				account.Credentials = shallowCopyMap(result.NewCredentials)
			}
			expiresAt = account.GetCredentialAsTime(zed.CredentialExpiresAt)
		}
	}

	llmToken := strings.TrimSpace(account.GetCredential(zed.CredentialLLMToken))
	if llmToken == "" {
		// No background refresher wired, or it produced nothing usable: mint
		// inline so the request can still proceed.
		return p.ForceRefreshAccessToken(ctx, account)
	}

	if p.tokenCache != nil {
		latestAccount, isStale := CheckTokenVersion(ctx, account, p.accountRepo)
		if isStale && latestAccount != nil {
			if latest := strings.TrimSpace(latestAccount.GetCredential(zed.CredentialLLMToken)); latest != "" {
				return latest, nil
			}
		} else {
			_ = p.tokenCache.SetAccessToken(ctx, cacheKey, llmToken, zedTokenTTL(expiresAt))
		}
	}

	return llmToken, nil
}

// ForceRefreshAccessToken mints a new token unconditionally, discarding any
// cached value. Used when the upstream rejects the current token with 401.
func (p *ZedTokenProvider) ForceRefreshAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformZed || account.Type != AccountTypeOAuth {
		return "", errors.New("not a zed oauth account")
	}
	if p.zedOAuthService == nil {
		return "", errors.New("zed oauth service is nil")
	}

	cacheKey := ZedTokenCacheKey(account)

	// Capture the token this call is about before touching anything. On the 401
	// path the caller's token is known-bad, so a wait must be able to tell a
	// genuinely new token from the one it was called about. Read before the
	// delete, or the discriminator is destroyed.
	staleToken := strings.TrimSpace(account.GetCredential(zed.CredentialLLMToken))
	staleVersion := account.GetCredentialAsInt64("_token_version")
	if p.tokenCache != nil {
		if cached, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil {
			if cached = strings.TrimSpace(cached); cached != "" {
				staleToken = cached
			}
		}
	}

	lockHeld := false
	if p.tokenCache != nil {
		locked, lockErr := p.tokenCache.AcquireRefreshLock(ctx, cacheKey, zedRefreshLockTTL)
		if lockErr == nil && locked {
			lockHeld = true
			defer func() { _ = p.tokenCache.ReleaseRefreshLock(ctx, cacheKey) }()
			// Only the lock holder may evict. Deleting before acquiring let a late
			// 401 handler drop a token another worker had just minted.
			_ = p.tokenCache.DeleteAccessToken(ctx, cacheKey)
		} else {
			// Another worker owns the refresh. Wait for its result instead of
			// minting a duplicate: N concurrent 401s otherwise mean N mints and N
			// credential writes, which is exactly what the lock exists to prevent.
			if token, waitErr := p.waitForMintedToken(ctx, account, cacheKey, staleToken, staleVersion); waitErr == nil && token != "" {
				return token, nil
			}
		}
	}

	if p.accountRepo != nil {
		if latestAccount, err := p.accountRepo.GetByID(ctx, account.ID); err == nil && latestAccount != nil {
			account = latestAccount
		}
	}

	tokenInfo, err := p.zedOAuthService.MintToken(ctx, account)
	if err != nil {
		// Another worker may have minted while we were blocked; prefer its result
		// over failing the request.
		if !lockHeld {
			if latestAccount, stale := CheckTokenVersion(ctx, account, p.accountRepo); stale && latestAccount != nil {
				if token := strings.TrimSpace(latestAccount.GetCredential(zed.CredentialLLMToken)); token != "" {
					_ = p.cacheAccessToken(ctx, latestAccount, token)
					return token, nil
				}
			}
		}
		if p.accountRepo != nil && isNonRetryableRefreshError(err) {
			_ = p.accountRepo.SetError(ctx, account.ID, "Zed token mint failed (non-retryable): "+err.Error())
		}
		return "", err
	}

	newCredentials := MergeCredentials(account.Credentials, p.zedOAuthService.BuildAccountCredentials(tokenInfo))
	newCredentials["_token_version"] = time.Now().UnixMilli()
	if err := persistAccountCredentials(ctx, p.accountRepo, account, newCredentials); err != nil {
		return "", err
	}

	token := strings.TrimSpace(tokenInfo.Token)
	if token == "" {
		return "", errors.New("llm_token not found after zed mint")
	}
	if err := p.cacheAccessToken(ctx, account, token); err != nil {
		return "", err
	}
	return token, nil
}

// waitForMintedToken waits for the worker holding the refresh lock to publish a
// token, returning only one that is demonstrably not the token this call was
// invoked about.
//
// The discriminator matters: this is reached from the 401 path where the current
// token is known-bad, so returning the same value would just fail again. A
// different token string or a higher _token_version both mean "someone minted".
// When there was no token to begin with (first-ever mint via GetAccessToken),
// staleToken is empty and any non-empty token qualifies immediately — the wait
// costs one poll interval, not the full window.
//
// Best-effort by design: on timeout the caller mints itself, so a crashed lock
// holder cannot wedge requests. The stampede becomes bounded by the wait window
// rather than unbounded.
func (p *ZedTokenProvider) waitForMintedToken(
	ctx context.Context,
	account *Account,
	cacheKey string,
	staleToken string,
	staleVersion int64,
) (string, error) {
	if p.tokenCache == nil {
		return "", errors.New("no token cache")
	}
	waitCtx, cancel := context.WithTimeout(ctx, zedRefreshLockWaitTimeout)
	defer cancel()

	ticker := time.NewTicker(zedRefreshLockPollInterval)
	defer ticker.Stop()

	for {
		// Cache first: the lock holder writes it last, so a hit here is fresh.
		if cached, err := p.tokenCache.GetAccessToken(waitCtx, cacheKey); err == nil {
			if cached = strings.TrimSpace(cached); cached != "" && cached != staleToken {
				return cached, nil
			}
		}

		// The versioned DB row is authoritative and also repairs a stale cache.
		if p.accountRepo != nil {
			if latest, err := p.accountRepo.GetByID(waitCtx, account.ID); err == nil && latest != nil {
				token := strings.TrimSpace(latest.GetCredential(zed.CredentialLLMToken))
				version := latest.GetCredentialAsInt64("_token_version")
				changed := token != staleToken || (version > 0 && version > staleVersion)
				expiresAt := latest.GetCredentialAsTime(zed.CredentialExpiresAt)
				// A nil expires_at is accepted: MintToken always writes one, so nil
				// only occurs for hand-written credentials, and rejecting those would
				// make the wait a guaranteed timeout.
				valid := expiresAt == nil || time.Until(*expiresAt) > zedTokenRefreshSkew
				if token != "" && changed && valid {
					_ = p.cacheAccessToken(waitCtx, latest, token)
					return token, nil
				}
			}
		}

		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// Holder still owns the refresh. The caller mints itself rather than
			// failing the request.
			return "", errors.New("zed refresh lock still held after wait")
		case <-ticker.C:
		}
	}
}

func (p *ZedTokenProvider) cacheAccessToken(ctx context.Context, account *Account, token string) error {
	if p.tokenCache == nil || account == nil || strings.TrimSpace(token) == "" {
		return nil
	}
	return p.tokenCache.SetAccessToken(ctx, ZedTokenCacheKey(account), token,
		zedTokenTTL(account.GetCredentialAsTime(zed.CredentialExpiresAt)))
}

// InvalidateToken drops the cached token for an account.
func (p *ZedTokenProvider) InvalidateToken(ctx context.Context, account *Account) {
	if p.tokenCache == nil || account == nil {
		return
	}
	_ = p.tokenCache.DeleteAccessToken(ctx, ZedTokenCacheKey(account))
}

// ZedTokenCacheKey returns the account-scoped cache key. Keyed by account ID so
// several Zed accounts on one deployment cannot share a token or refresh lock.
func ZedTokenCacheKey(account *Account) string {
	if account == nil {
		return "zed:account:0"
	}
	return "zed:account:" + strconv.FormatInt(account.ID, 10)
}

// zedTokenTTL derives a cache TTL that always expires before the token itself.
func zedTokenTTL(expiresAt *time.Time) time.Duration {
	if expiresAt == nil {
		return 5 * time.Minute
	}
	until := time.Until(*expiresAt)
	switch {
	case until > zedTokenCacheSkew:
		return until - zedTokenCacheSkew
	case until > 0:
		return until
	default:
		return time.Minute
	}
}
