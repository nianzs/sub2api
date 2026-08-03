package zed

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestOAuthRoundTrip(t *testing.T) {
	key, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	// The auth URL must carry the PKCS#1 public key and the callback port.
	authURL := BuildAuthURL(&key.PublicKey, 0)
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	if got := parsed.Query().Get("native_app_port"); got != "43117" {
		t.Errorf("native_app_port = %q, want the default placeholder", got)
	}
	pub := parsed.Query().Get("native_app_public_key")
	if pub == "" {
		t.Fatal("native_app_public_key is missing")
	}
	if strings.Contains(pub, "=") {
		t.Error("public key must be base64url without padding")
	}

	// Simulate zed.dev encrypting the credential document to our public key.
	plaintext := `{"github_user_id":4242,"github_user_login":"octocat","access_token":"zed_secret"}`
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &key.PublicKey, []byte(plaintext), nil)
	if err != nil {
		t.Fatalf("EncryptOAEP: %v", err)
	}
	callback := "http://127.0.0.1:43117/?user_id=1234&access_token=" +
		url.QueryEscape(base64.RawURLEncoding.EncodeToString(ciphertext))

	userID, encrypted, err := ParseCallback(callback)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if userID != "1234" {
		t.Errorf("user_id = %q, want %q", userID, "1234")
	}

	// The private key must survive session storage as PEM.
	encodedKey, err := MarshalPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	restored, err := ParsePrivateKey(encodedKey)
	if err != nil {
		t.Fatalf("ParsePrivateKey: %v", err)
	}

	decrypted, err := DecryptAccessToken(restored, encrypted)
	if err != nil {
		t.Fatalf("DecryptAccessToken: %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}

	creds, err := BuildCredentials(userID, decrypted, "sys-1")
	if err != nil {
		t.Fatalf("BuildCredentials: %v", err)
	}
	if creds[CredentialAccessToken] != plaintext {
		t.Errorf("access_token = %v, want the complete decrypted payload", creds[CredentialAccessToken])
	}
	if creds[CredentialUserID] != "1234" {
		t.Errorf("user_id = %v, want it recorded", creds[CredentialUserID])
	}
	if creds[CredentialSystemID] != "sys-1" {
		t.Errorf("system_id = %v, want it recorded; a mismatch causes trial_blocked", creds[CredentialSystemID])
	}
	if _, ok := creds[CredentialGitHubLogin]; ok {
		t.Error("callback credential fields must not be parsed into separate stored values")
	}
	credentialJSON, err := CredentialJSON(creds)
	if err != nil {
		t.Fatalf("CredentialJSON: %v", err)
	}
	if credentialJSON != plaintext {
		t.Errorf("CredentialJSON = %q, want original plaintext %q", credentialJSON, plaintext)
	}
}

func TestParseCallbackAcceptsBareQuery(t *testing.T) {
	userID, token, err := ParseCallback("user_id=99&access_token=abc")
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if userID != "99" || token != "abc" {
		t.Errorf("got (%q, %q), want (99, abc)", userID, token)
	}
}

func TestParseCallbackRejectsIncomplete(t *testing.T) {
	for _, raw := range []string{
		"",
		"https://example.com/",
		"http://127.0.0.1:43117/?access_token=abc",
		"http://127.0.0.1:43117/?user_id=1",
	} {
		if _, _, err := ParseCallback(raw); err == nil {
			t.Errorf("ParseCallback(%q) should have failed", raw)
		}
	}
}

func TestCredentialJSONOmitsLocalOnlyKeys(t *testing.T) {
	creds := map[string]any{
		CredentialUserID:      "1234",
		CredentialAccessToken: "zed_secret",
		CredentialGitHubLogin: "octocat",
		CredentialSystemID:    "sys-1",
		CredentialLLMToken:    "cached.jwt.value",
	}

	encoded, err := CredentialJSON(creds)
	if err != nil {
		t.Fatalf("CredentialJSON: %v", err)
	}
	if !strings.Contains(encoded, "zed_secret") {
		t.Error("credential json must carry the access token")
	}
	for _, leaked := range []string{"sys-1", "cached.jwt.value", "1234"} {
		if strings.Contains(encoded, leaked) {
			t.Errorf("credential json must not include %q", leaked)
		}
	}
}

func TestCredentialJSONRequiresAccessToken(t *testing.T) {
	if _, err := CredentialJSON(map[string]any{CredentialUserID: "1"}); err == nil {
		t.Error("CredentialJSON should fail without an access token")
	}
}

func TestBuildCredentialsAcceptsBareToken(t *testing.T) {
	creds, err := BuildCredentials("7", "raw-token", "sys-7")
	if err != nil {
		t.Fatalf("BuildCredentials: %v", err)
	}
	if creds[CredentialAccessToken] != "raw-token" {
		t.Errorf("access_token = %v, want the bare payload", creds[CredentialAccessToken])
	}
	if creds[CredentialSystemID] != "sys-7" {
		t.Errorf("system_id = %v, want it stored on the bare-token path too", creds[CredentialSystemID])
	}
}

func TestBuildCredentialsPreservesOpaqueJSONObject(t *testing.T) {
	plaintext := `{"kind":"zed","credential":{"secret":"opaque"}}`
	creds, err := BuildCredentials("7", plaintext, "sys-7")
	if err != nil {
		t.Fatalf("BuildCredentials: %v", err)
	}
	if creds[CredentialAccessToken] != plaintext {
		t.Errorf("access_token = %v, want the complete decrypted payload", creds[CredentialAccessToken])
	}

	encoded, err := CredentialJSON(creds)
	if err != nil {
		t.Fatalf("CredentialJSON: %v", err)
	}
	if encoded != plaintext {
		t.Errorf("CredentialJSON = %q, want opaque payload %q", encoded, plaintext)
	}
}

// A stored account without system_id is guaranteed to fail every /completions
// request with trial_blocked (403), so the only assembly point on the exchange
// path must refuse to build one.
func TestBuildCredentialsRequiresSystemID(t *testing.T) {
	jsonPayload := `{"access_token":"tok","github_user_id":5,"github_user_login":"octocat"}`
	for _, tc := range []struct {
		name      string
		plaintext string
		systemID  string
	}{
		{"json payload, empty", jsonPayload, ""},
		{"json payload, whitespace", jsonPayload, "   "},
		{"bare token, empty", "raw-token", ""},
		{"bare token, whitespace", "raw-token", "\t "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildCredentials("7", tc.plaintext, tc.systemID); err == nil {
				t.Fatal("BuildCredentials should reject a missing system_id")
			}
		})
	}
}

// system_id inside the decrypted payload must not shadow the one the operator
// supplied for this account: only the latter matches the local Zed install.
func TestBuildCredentialsPrefersSuppliedSystemID(t *testing.T) {
	creds, err := BuildCredentials("7", `{"access_token":"tok","system_id":"from-payload"}`, "from-operator")
	if err != nil {
		t.Fatalf("BuildCredentials: %v", err)
	}
	if creds[CredentialSystemID] != "from-operator" {
		t.Errorf("system_id = %v, want the operator-supplied value to win", creds[CredentialSystemID])
	}
}

func TestValidateSystemID(t *testing.T) {
	for _, bad := range []string{"", "   ", "\t\n"} {
		if err := ValidateSystemID(bad); err == nil {
			t.Errorf("ValidateSystemID(%q) should fail", bad)
		}
	}
	for _, ok := range []string{"3f2504e0-4f89-11d3-9a0c-0305e82c3301", "not-a-uuid-but-present"} {
		if err := ValidateSystemID(ok); err != nil {
			t.Errorf("ValidateSystemID(%q) = %v, want nil", ok, err)
		}
	}
}

// IsUUIDLike drives a warning, never a rejection — see its doc comment.
func TestIsUUIDLike(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"3f2504e0-4f89-11d3-9a0c-0305e82c3301", true},
		{"3F2504E0-4F89-11D3-9A0C-0305E82C3301", true},
		{"  3f2504e0-4f89-11d3-9a0c-0305e82c3301  ", true},
		{"3f2504e04f8911d39a0c0305e82c3301", false},
		{"/Users/me/Library/Application Support/Zed", false},
		{`{"system_id":"3f2504e0-4f89-11d3-9a0c-0305e82c3301"}`, false},
		{"", false},
	} {
		if got := IsUUIDLike(tc.in); got != tc.want {
			t.Errorf("IsUUIDLike(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
