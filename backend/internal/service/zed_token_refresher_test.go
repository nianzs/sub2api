package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/zed"
)

func newZedRefresherAccount(mutate func(creds map[string]any)) *Account {
	creds := map[string]any{
		zed.CredentialUserID:      "42",
		zed.CredentialAccessToken: "long-lived",
		zed.CredentialSystemID:    "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
		zed.CredentialLLMToken:    "minted-jwt",
		zed.CredentialExpiresAt:   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	if mutate != nil {
		mutate(creds)
	}
	return &Account{ID: 701, Platform: PlatformZed, Type: AccountTypeOAuth, Credentials: creds}
}

func TestZedTokenRefresherCanRefresh(t *testing.T) {
	refresher := NewZedTokenRefresher(nil)

	t.Run("healthy account", func(t *testing.T) {
		if !refresher.CanRefresh(newZedRefresherAccount(nil)) {
			t.Error("CanRefresh = false, want true for a fully configured account")
		}
	})

	// Minting without a system_id produces a token that fails every inference
	// request, so there is nothing to gain from doing it on a schedule.
	t.Run("missing system_id", func(t *testing.T) {
		for _, systemID := range []string{"", "   "} {
			account := newZedRefresherAccount(func(creds map[string]any) {
				creds[zed.CredentialSystemID] = systemID
			})
			if refresher.CanRefresh(account) {
				t.Errorf("CanRefresh(system_id=%q) = true, want false", systemID)
			}
			if refresher.NeedsRefresh(account, time.Hour) {
				t.Errorf("NeedsRefresh(system_id=%q) = true, want false", systemID)
			}
		}
	})

	t.Run("missing minting credential", func(t *testing.T) {
		account := newZedRefresherAccount(func(creds map[string]any) {
			delete(creds, zed.CredentialAccessToken)
		})
		if refresher.CanRefresh(account) {
			t.Error("CanRefresh = true, want false without an access_token to mint from")
		}
	})

	t.Run("wrong platform or type", func(t *testing.T) {
		apikey := newZedRefresherAccount(nil)
		apikey.Type = AccountTypeAPIKey
		if refresher.CanRefresh(apikey) {
			t.Error("CanRefresh = true for a non-oauth account, want false")
		}
		other := newZedRefresherAccount(nil)
		other.Platform = PlatformAnthropic
		if refresher.CanRefresh(other) {
			t.Error("CanRefresh = true for a non-zed account, want false")
		}
		if refresher.CanRefresh(nil) {
			t.Error("CanRefresh(nil) = true, want false")
		}
	})
}

// An absent llm_token means "nothing minted yet", not "long-lived credential".
func TestZedTokenRefresherNeedsRefresh(t *testing.T) {
	refresher := NewZedTokenRefresher(nil)

	if !refresher.NeedsRefresh(newZedRefresherAccount(func(creds map[string]any) {
		delete(creds, zed.CredentialLLMToken)
	}), time.Minute) {
		t.Error("NeedsRefresh = false with no minted token, want true")
	}

	if !refresher.NeedsRefresh(newZedRefresherAccount(func(creds map[string]any) {
		delete(creds, zed.CredentialExpiresAt)
	}), time.Minute) {
		t.Error("NeedsRefresh = false with no expires_at, want true")
	}

	if refresher.NeedsRefresh(newZedRefresherAccount(nil), time.Minute) {
		t.Error("NeedsRefresh = true for a token valid for another hour, want false")
	}

	// The floor keeps a caller's tiny window from outliving the short-lived JWT.
	if !refresher.NeedsRefresh(newZedRefresherAccount(func(creds map[string]any) {
		creds[zed.CredentialExpiresAt] = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	}), time.Nanosecond) {
		t.Error("NeedsRefresh = false inside the zedRefreshWindow floor, want true")
	}
}
