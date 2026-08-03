package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/zed"
)

// zedFakeTokenCache records the order of cache operations so the lock/delete
// ordering can be asserted directly.
type zedFakeTokenCache struct {
	mu sync.Mutex

	token    string
	lockFree bool
	ops      []string

	// onAcquireFail runs when a lock acquisition is refused, letting a test
	// simulate the holder committing a token mid-wait.
	onAcquireFail func()
}

func (c *zedFakeTokenCache) record(op string) {
	c.mu.Lock()
	c.ops = append(c.ops, op)
	c.mu.Unlock()
}

func (c *zedFakeTokenCache) operations() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ops...)
}

func (c *zedFakeTokenCache) sawOperation(op string) bool {
	for _, seen := range c.operations() {
		if seen == op {
			return true
		}
	}
	return false
}

func (c *zedFakeTokenCache) GetAccessToken(_ context.Context, _ string) (string, error) {
	c.record("get")
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token, nil
}

func (c *zedFakeTokenCache) SetAccessToken(_ context.Context, _ string, token string, _ time.Duration) error {
	c.record("set")
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	return nil
}

func (c *zedFakeTokenCache) DeleteAccessToken(_ context.Context, _ string) error {
	c.record("delete")
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
	return nil
}

func (c *zedFakeTokenCache) AcquireRefreshLock(_ context.Context, _ string, _ time.Duration) (bool, error) {
	c.record("acquire")
	if c.lockFree {
		return true, nil
	}
	if c.onAcquireFail != nil {
		c.onAcquireFail()
	}
	return false, nil
}

func (c *zedFakeTokenCache) ReleaseRefreshLock(_ context.Context, _ string) error {
	c.record("release")
	return nil
}

type zedFakeAccountRepo struct {
	AccountRepository

	mu      sync.Mutex
	account *Account
}

func (r *zedFakeAccountRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil {
		return nil, nil
	}
	clone := *r.account
	clone.Credentials = shallowCopyMap(r.account.Credentials)
	return &clone, nil
}

func (r *zedFakeAccountRepo) Update(_ context.Context, account *Account) error {
	r.mu.Lock()
	r.account = account
	r.mu.Unlock()
	return nil
}

func (r *zedFakeAccountRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

func (r *zedFakeAccountRepo) SetError(context.Context, int64, string) error { return nil }

func (r *zedFakeAccountRepo) setCredential(key string, value any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account.Credentials == nil {
		r.account.Credentials = map[string]any{}
	}
	r.account.Credentials[key] = value
}

func newZedProviderAccount() *Account {
	return &Account{
		ID:       701,
		Platform: PlatformZed,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			zed.CredentialUserID:      "42",
			zed.CredentialAccessToken: "long-lived",
			zed.CredentialSystemID:    "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			zed.CredentialLLMToken:    "known-bad-jwt",
		},
	}
}

// Regression test for a Zed-specific eviction race: the cache delete used to run
// before the lock was acquired, so a late-arriving 401 handler could drop a token
// another worker had just minted.
func TestZedForceRefreshAcquiresLockBeforeEvicting(t *testing.T) {
	account := newZedProviderAccount()
	cache := &zedFakeTokenCache{token: "known-bad-jwt", lockFree: true}
	repo := &zedFakeAccountRepo{account: account}
	minter := &zedMinterStub{token: "fresh-jwt"}

	provider := &ZedTokenProvider{accountRepo: repo, tokenCache: cache, zedOAuthService: minter}

	token, err := provider.ForceRefreshAccessToken(context.Background(), account)
	if err != nil {
		t.Fatalf("ForceRefreshAccessToken: %v", err)
	}
	if token != "fresh-jwt" {
		t.Errorf("token = %q, want the freshly minted one", token)
	}

	ops := cache.operations()
	acquireAt, deleteAt := -1, -1
	for i, op := range ops {
		if op == "acquire" && acquireAt < 0 {
			acquireAt = i
		}
		if op == "delete" && deleteAt < 0 {
			deleteAt = i
		}
	}
	if acquireAt < 0 || deleteAt < 0 {
		t.Fatalf("expected both acquire and delete, got %v", ops)
	}
	if acquireAt > deleteAt {
		t.Errorf("delete ran before acquire (ops=%v); only the lock holder may evict", ops)
	}
}

// A worker that loses the lock must not evict: the token in the cache may belong
// to the holder that is mid-refresh.
func TestZedForceRefreshDoesNotEvictWhenLockLost(t *testing.T) {
	account := newZedProviderAccount()
	cache := &zedFakeTokenCache{token: "known-bad-jwt"}
	repo := &zedFakeAccountRepo{account: account}
	minter := &zedMinterStub{token: "self-minted-jwt"}

	provider := &ZedTokenProvider{accountRepo: repo, tokenCache: cache, zedOAuthService: minter}

	restore := zedRefreshLockWaitTimeout
	zedRefreshLockWaitTimeout = 60 * time.Millisecond
	defer func() { zedRefreshLockWaitTimeout = restore }()

	if _, err := provider.ForceRefreshAccessToken(context.Background(), account); err != nil {
		t.Fatalf("ForceRefreshAccessToken: %v", err)
	}
	if cache.sawOperation("delete") {
		t.Errorf("a lock-losing worker must never delete the cached token (ops=%v)", cache.operations())
	}
}

// The point of the lock: when the holder publishes a token, the loser uses it
// instead of minting a duplicate and writing credentials again.
func TestZedForceRefreshWaitsForLockHoldersToken(t *testing.T) {
	account := newZedProviderAccount()
	repo := &zedFakeAccountRepo{account: newZedProviderAccount()}
	minter := &zedMinterStub{token: "should-not-be-minted"}

	cache := &zedFakeTokenCache{token: "known-bad-jwt"}
	cache.onAcquireFail = func() {
		// The holder finishes right after we lose the race.
		cache.mu.Lock()
		cache.token = "holder-minted-jwt"
		cache.mu.Unlock()
	}

	provider := &ZedTokenProvider{accountRepo: repo, tokenCache: cache, zedOAuthService: minter}

	token, err := provider.ForceRefreshAccessToken(context.Background(), account)
	if err != nil {
		t.Fatalf("ForceRefreshAccessToken: %v", err)
	}
	if token != "holder-minted-jwt" {
		t.Errorf("token = %q, want the lock holder's token", token)
	}
	if minter.calls != 0 {
		t.Errorf("MintToken calls = %d, want 0 when the holder already published one", minter.calls)
	}
}

// The known-bad token must never satisfy the wait — this is reached from the 401
// path, so returning it would just fail again.
func TestZedForceRefreshRejectsStaleTokenAndMintsOnce(t *testing.T) {
	account := newZedProviderAccount()
	repo := &zedFakeAccountRepo{account: newZedProviderAccount()}
	minter := &zedMinterStub{token: "self-minted-jwt"}

	// Cache and DB both keep serving the token we were called about.
	cache := &zedFakeTokenCache{token: "known-bad-jwt"}

	provider := &ZedTokenProvider{accountRepo: repo, tokenCache: cache, zedOAuthService: minter}

	restore := zedRefreshLockWaitTimeout
	zedRefreshLockWaitTimeout = 60 * time.Millisecond
	defer func() { zedRefreshLockWaitTimeout = restore }()

	token, err := provider.ForceRefreshAccessToken(context.Background(), account)
	if err != nil {
		t.Fatalf("ForceRefreshAccessToken: %v", err)
	}
	if token == "known-bad-jwt" {
		t.Fatal("returned the known-bad token the call was made about")
	}
	if token != "self-minted-jwt" {
		t.Errorf("token = %q, want the self-minted one after the wait times out", token)
	}
	if minter.calls != 1 {
		t.Errorf("MintToken calls = %d, want exactly 1", minter.calls)
	}
}

// A bumped _token_version means "someone minted" even when the token string is
// unchanged.
func TestZedWaitForMintedTokenAcceptsVersionBump(t *testing.T) {
	stored := newZedProviderAccount()
	repo := &zedFakeAccountRepo{account: stored}
	cache := &zedFakeTokenCache{}

	provider := &ZedTokenProvider{accountRepo: repo, tokenCache: cache}
	repo.setCredential("_token_version", int64(2))

	restore := zedRefreshLockWaitTimeout
	zedRefreshLockWaitTimeout = 200 * time.Millisecond
	defer func() { zedRefreshLockWaitTimeout = restore }()

	token, err := provider.waitForMintedToken(context.Background(), stored, "k", "known-bad-jwt", 1)
	if err != nil {
		t.Fatalf("waitForMintedToken: %v", err)
	}
	if token != "known-bad-jwt" {
		t.Errorf("token = %q, want the DB token accepted on the strength of the version bump", token)
	}
}

// An expired token from the holder is not usable, so the wait must not accept it.
func TestZedWaitForMintedTokenRejectsExpiredToken(t *testing.T) {
	stored := newZedProviderAccount()
	stored.Credentials[zed.CredentialLLMToken] = "holder-jwt"
	stored.Credentials[zed.CredentialExpiresAt] = time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339)
	repo := &zedFakeAccountRepo{account: stored}
	cache := &zedFakeTokenCache{}

	provider := &ZedTokenProvider{accountRepo: repo, tokenCache: cache}

	restore := zedRefreshLockWaitTimeout
	zedRefreshLockWaitTimeout = 60 * time.Millisecond
	defer func() { zedRefreshLockWaitTimeout = restore }()

	if _, err := provider.waitForMintedToken(context.Background(), stored, "k", "known-bad-jwt", 0); err == nil {
		t.Fatal("waitForMintedToken should not accept a token inside the refresh skew")
	}
}

// First-ever mint has no stale token to discriminate against, so any published
// token qualifies immediately rather than burning the whole window.
func TestZedWaitForMintedTokenReturnsQuicklyOnFirstMint(t *testing.T) {
	stored := newZedProviderAccount()
	delete(stored.Credentials, zed.CredentialLLMToken)
	repo := &zedFakeAccountRepo{account: stored}
	cache := &zedFakeTokenCache{token: "holder-minted-jwt"}

	provider := &ZedTokenProvider{accountRepo: repo, tokenCache: cache}

	restore := zedRefreshLockWaitTimeout
	zedRefreshLockWaitTimeout = 2 * time.Second
	defer func() { zedRefreshLockWaitTimeout = restore }()

	start := time.Now()
	token, err := provider.waitForMintedToken(context.Background(), stored, "k", "", 0)
	if err != nil {
		t.Fatalf("waitForMintedToken: %v", err)
	}
	if token != "holder-minted-jwt" {
		t.Errorf("token = %q, want the published token", token)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %v, want an immediate return when there was no stale token", elapsed)
	}
}

func TestZedWaitForMintedTokenHonorsContextCancellation(t *testing.T) {
	stored := newZedProviderAccount()
	repo := &zedFakeAccountRepo{account: stored}
	cache := &zedFakeTokenCache{token: "known-bad-jwt"}
	provider := &ZedTokenProvider{accountRepo: repo, tokenCache: cache}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := provider.waitForMintedToken(ctx, stored, "k", "known-bad-jwt", 0); err == nil {
		t.Fatal("waitForMintedToken should surface a cancelled context")
	}
}

// The production wire entry must not re-override NewZedTokenProvider's policy
// with Antigravity's UseExistingToken — that reuses a stale JWT under lock contention.
func TestProvideZedTokenProviderUsesZedRefreshPolicy(t *testing.T) {
	p := ProvideZedTokenProvider(nil, nil, nil, nil)
	if p.refreshPolicy.OnLockHeld != ProviderLockHeldWaitForCache {
		t.Fatalf("OnLockHeld = %v, want WaitForCache (production must not reuse a stale JWT)", p.refreshPolicy.OnLockHeld)
	}
	if p.refreshPolicy.OnRefreshError != ProviderRefreshErrorReturn {
		t.Fatalf("OnRefreshError = %v, want Return", p.refreshPolicy.OnRefreshError)
	}
}

// The gateway's 401 retry tests construct a provider with no cache at all; the
// lock and wait block must be skipped rather than panic.
func TestZedForceRefreshWithoutCacheStillMints(t *testing.T) {
	account := newZedProviderAccount()
	repo := &zedFakeAccountRepo{account: account}
	minter := &zedMinterStub{token: "fresh-jwt"}

	provider := &ZedTokenProvider{accountRepo: repo, zedOAuthService: minter}

	token, err := provider.ForceRefreshAccessToken(context.Background(), account)
	if err != nil {
		t.Fatalf("ForceRefreshAccessToken: %v", err)
	}
	if token != "fresh-jwt" || minter.calls != 1 {
		t.Errorf("token = %q, mint calls = %d; want one mint with no cache wired", token, minter.calls)
	}
}
