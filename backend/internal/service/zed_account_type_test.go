package service

import (
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func TestValidateZedAccountType(t *testing.T) {
	t.Run("non-zed platforms are unrestricted", func(t *testing.T) {
		for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGrok, PlatformKiro} {
			for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey, AccountTypeUpstream, "setup-token"} {
				if err := validateZedAccountType(platform, accountType); err != nil {
					t.Errorf("validateZedAccountType(%q, %q) = %v, want nil", platform, accountType, err)
				}
			}
		}
	})

	t.Run("zed oauth is allowed", func(t *testing.T) {
		if err := validateZedAccountType(PlatformZed, AccountTypeOAuth); err != nil {
			t.Fatalf("validateZedAccountType(zed, oauth) = %v, want nil", err)
		}
	})

	// Non-OAuth Zed accounts fall through IsZedOAuth into the Anthropic path and
	// can never mint a JWT, so create/update must refuse them.
	t.Run("zed rejects every non-oauth type", func(t *testing.T) {
		for _, accountType := range []string{AccountTypeAPIKey, AccountTypeUpstream, "setup-token", "bedrock", "service_account", ""} {
			err := validateZedAccountType(PlatformZed, accountType)
			if err == nil {
				t.Fatalf("validateZedAccountType(zed, %q) = nil, want ZED_OAUTH_ONLY", accountType)
			}
			if infraerrors.Reason(err) != "ZED_OAUTH_ONLY" {
				t.Fatalf("error = %v (reason=%q), want ZED_OAUTH_ONLY", err, infraerrors.Reason(err))
			}
		}
	})
}

func TestBuildAccountForCreateRejectsNonOAuthZed(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, "setup-token", AccountTypeUpstream} {
		_, err := buildAccountForCreate(&CreateAccountInput{
			Name:     "bad-zed",
			Platform: PlatformZed,
			Type:     accountType,
		}, nil)
		if err == nil {
			t.Fatalf("buildAccountForCreate(zed, %q) succeeded, want ZED_OAUTH_ONLY", accountType)
		}
		if infraerrors.Reason(err) != "ZED_OAUTH_ONLY" {
			t.Fatalf("error = %v (reason=%q), want ZED_OAUTH_ONLY", err, infraerrors.Reason(err))
		}
	}

	account, err := buildAccountForCreate(&CreateAccountInput{
		Name:     "good-zed",
		Platform: PlatformZed,
		Type:     AccountTypeOAuth,
	}, nil)
	if err != nil {
		t.Fatalf("buildAccountForCreate(zed, oauth) = %v, want nil", err)
	}
	if account.Type != AccountTypeOAuth || account.Platform != PlatformZed {
		t.Fatalf("account = %+v, want zed/oauth", account)
	}
}

// Other platforms must still accept apikey accounts through the same entry point.
func TestBuildAccountForCreateAllowsNonZedAPIKey(t *testing.T) {
	account, err := buildAccountForCreate(&CreateAccountInput{
		Name:     "anthropic-key",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
	}, nil)
	if err != nil {
		t.Fatalf("buildAccountForCreate(anthropic, apikey) = %v, want nil", err)
	}
	if account.Type != AccountTypeAPIKey {
		t.Fatalf("type = %q, want apikey", account.Type)
	}
}
