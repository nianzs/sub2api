package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/zed"
)

type zedProxyRepoStub struct {
	ProxyRepository
	proxy *Proxy
	err   error
	calls int
}

func (r *zedProxyRepoStub) GetByID(context.Context, int64) (*Proxy, error) {
	r.calls++
	return r.proxy, r.err
}

func newZedOAuthAccount(credentials map[string]any) *Account {
	if credentials == nil {
		credentials = map[string]any{
			zed.CredentialUserID:      "42",
			zed.CredentialAccessToken: "long-lived",
			zed.CredentialSystemID:    "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		}
	}
	return &Account{
		ID:          701,
		Platform:    PlatformZed,
		Type:        AccountTypeOAuth,
		Credentials: credentials,
	}
}

func zedProxy(id int64) *Proxy {
	return &Proxy{ID: id, Protocol: "http", Host: "proxy.internal", Port: 8080}
}

// A missing system_id must be rejected before any upstream call: minting would
// succeed and then every /completions request would return trial_blocked (403).
func TestZedOAuthServiceRejectsMissingSystemIDBeforeAnyRequest(t *testing.T) {
	for _, systemID := range []string{"", "   "} {
		account := newZedOAuthAccount(map[string]any{
			zed.CredentialUserID:      "42",
			zed.CredentialAccessToken: "long-lived",
			zed.CredentialSystemID:    systemID,
		})

		// A nil proxyRepo would fail loudly if a request were attempted with a
		// proxy configured, so reaching the guard first is what keeps this hermetic.
		svc := NewZedOAuthService(nil)

		if _, err := svc.MintToken(context.Background(), account); err == nil {
			t.Errorf("MintToken(system_id=%q) should fail", systemID)
		}
		if _, err := svc.FetchAccountInfo(context.Background(), account); err == nil {
			t.Errorf("FetchAccountInfo(system_id=%q) should fail", systemID)
		}
	}
}

func TestZedOAuthServiceResolveProxyURL(t *testing.T) {
	proxyID := int64(9)

	t.Run("no proxy configured is a direct connection", func(t *testing.T) {
		repo := &zedProxyRepoStub{}
		svc := NewZedOAuthService(repo)
		got, err := svc.resolveProxyURL(context.Background(), newZedOAuthAccount(nil))
		if err != nil || got != "" {
			t.Fatalf("resolveProxyURL = (%q, %v), want (\"\", nil)", got, err)
		}
	})

	// GetByID/GetByIDs both hydrate the edge via WithProxy(), so the request-path
	// mint must not spend a DB round-trip re-reading it.
	t.Run("hydrated edge is used without a repository call", func(t *testing.T) {
		repo := &zedProxyRepoStub{proxy: zedProxy(proxyID)}
		svc := NewZedOAuthService(repo)
		account := newZedOAuthAccount(nil)
		account.ProxyID = &proxyID
		account.Proxy = zedProxy(proxyID)

		got, err := svc.resolveProxyURL(context.Background(), account)
		if err != nil {
			t.Fatalf("resolveProxyURL: %v", err)
		}
		if got != "http://proxy.internal:8080" {
			t.Errorf("resolveProxyURL = %q, want the hydrated edge URL", got)
		}
		if repo.calls != 0 {
			t.Errorf("repository calls = %d, want 0 when the edge is hydrated", repo.calls)
		}
	})

	t.Run("missing edge falls back to the repository", func(t *testing.T) {
		repo := &zedProxyRepoStub{proxy: zedProxy(proxyID)}
		svc := NewZedOAuthService(repo)
		account := newZedOAuthAccount(nil)
		account.ProxyID = &proxyID

		got, err := svc.resolveProxyURL(context.Background(), account)
		if err != nil {
			t.Fatalf("resolveProxyURL: %v", err)
		}
		if got != "http://proxy.internal:8080" || repo.calls != 1 {
			t.Errorf("resolveProxyURL = (%q, calls=%d), want the looked-up URL after one call", got, repo.calls)
		}
	})

	// Every failure below must be an error, never "" — falling back to a direct
	// connection would mint from the server IP while inference goes through the
	// proxy, which is the IP correlation the proxy exists to avoid.
	t.Run("unresolvable proxy never degrades to direct", func(t *testing.T) {
		for name, repo := range map[string]ProxyRepository{
			"repository unavailable": nil,
			"lookup failed":          &zedProxyRepoStub{err: errors.New("db down")},
			"proxy not found":        &zedProxyRepoStub{err: ErrProxyNotFound},
			"nil proxy, nil error":   &zedProxyRepoStub{},
		} {
			t.Run(name, func(t *testing.T) {
				svc := NewZedOAuthService(repo)
				account := newZedOAuthAccount(nil)
				account.ProxyID = &proxyID

				got, err := svc.resolveProxyURL(context.Background(), account)
				if err == nil {
					t.Fatalf("resolveProxyURL = (%q, nil), want an error", got)
				}
				if got != "" {
					t.Errorf("resolveProxyURL returned %q alongside its error; must not offer a usable direct URL", got)
				}
			})
		}
	})
}

func TestZedOAuthServiceClientForRejectsUnusableProxy(t *testing.T) {
	proxyID := int64(9)
	repo := &zedProxyRepoStub{proxy: &Proxy{ID: proxyID, Protocol: "gopher", Host: "proxy.internal", Port: 8080}}
	svc := NewZedOAuthService(repo)
	account := newZedOAuthAccount(nil)
	account.ProxyID = &proxyID

	if _, err := svc.clientFor(context.Background(), account); err == nil {
		t.Fatal("clientFor should fail on an unsupported proxy scheme rather than return a direct client")
	}
}

// FetchModels tolerates a nil account, which must yield the direct client rather
// than a nil-pointer dereference.
func TestZedOAuthServiceClientForNilAccount(t *testing.T) {
	svc := NewZedOAuthService(nil)
	client, err := svc.clientFor(context.Background(), nil)
	if err != nil {
		t.Fatalf("clientFor(nil): %v", err)
	}
	if client == nil {
		t.Fatal("clientFor(nil) returned no client")
	}
}

func TestZedUpstreamErrorIsAuthFailure(t *testing.T) {
	for status, want := range map[int]bool{401: true, 403: true, 429: false, 500: false, 503: false} {
		err := &ZedUpstreamError{StatusCode: status}
		if got := err.IsAuthFailure(); got != want {
			t.Errorf("IsAuthFailure(%d) = %v, want %v", status, got, want)
		}
	}
}
