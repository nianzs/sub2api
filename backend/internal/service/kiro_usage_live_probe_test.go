//go:build live

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type liveExportedAccounts struct {
	Accounts []liveExportedAccount `json:"accounts"`
}

type liveExportedAccount struct {
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
}

func TestLiveKiroUsageLimitsFromExportedAccount(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("KIRO_LIVE_ACCOUNT_JSON"))
	if path == "" {
		t.Fatal("KIRO_LIVE_ACCOUNT_JSON is required")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var exported liveExportedAccounts
	require.NoError(t, json.Unmarshal(raw, &exported))
	require.NotEmpty(t, exported.Accounts)

	src := exported.Accounts[0]
	require.Equal(t, "kiro", strings.ToLower(src.Platform))

	creds := src.Credentials
	require.NotNil(t, creds)

	clientID := stringCredential(creds, "client_id")
	clientSecret := stringCredential(creds, "client_secret")
	refreshToken := stringCredential(creds, "refresh_token")
	region := stringCredential(creds, "region")
	startURL := stringCredential(creds, "start_url")
	provider := stringCredential(creds, "provider")
	require.NotEmpty(t, clientID)
	require.NotEmpty(t, clientSecret)
	require.NotEmpty(t, refreshToken)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	token, err := kiropkg.RefreshIDCToken(ctx, "", clientID, clientSecret, refreshToken, region, startURL, provider)
	require.NoError(t, err)
	require.NotEmpty(t, token.AccessToken)
	t.Logf("refreshed token: provider=%s auth=%s profileArn=%q email=%v expiresAt=%s",
		token.Provider, token.AuthMethod, token.ProfileArn, token.Email != "", token.ExpiresAt)

	account := &Account{
		ID:       9001,
		Name:     src.Name,
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  token.AccessToken,
			"refresh_token": firstNonEmptyLive(token.RefreshToken, refreshToken),
			"client_id":     clientID,
			"client_secret": clientSecret,
			"auth_method":   "idc",
			"provider":      firstNonEmptyLive(token.Provider, provider),
			"region":        firstNonEmptyLive(token.Region, region),
			"start_url":     startURL,
		},
	}
	if arn := strings.TrimSpace(stringCredential(creds, "profile_arn")); arn != "" {
		account.Credentials["profile_arn"] = arn
	}
	if arn := strings.TrimSpace(token.ProfileArn); arn != "" {
		account.Credentials["profile_arn"] = arn
	}

	t.Logf("account: name=%s provider=%s start_url=%s primaryARN=%q regions=%v",
		account.Name,
		account.GetCredential("provider"),
		account.GetCredential("start_url"),
		kiroUsageQueryProfileArn(account),
		kiroUsageRegionCandidates(account),
	)

	svc := NewAccountUsageService(nil, nil, nil, nil, nil, nil, nil, nil, NewUsageCache(), nil, nil)
	accessToken := token.AccessToken

	type probe struct {
		name       string
		region     string
		profileArn string
	}
	runProbes := func(label string, probes []probe) {
		t.Helper()
		for _, p := range probes {
			endpoint := kiroRuntimeEndpoint(p.region)
			resp, probeErr := svc.doKiroUsageLimitsRequest(ctx, account, endpoint, accessToken, p.profileArn)
			if probeErr != nil {
				t.Logf("%s PROBE %-22s FAIL %v", label, p.name, probeErr)
				continue
			}
			t.Logf("%s PROBE %-22s OK %s", label, p.name, summarizeKiroUsage(resp))
		}
	}

	runProbes("as-exported", []probe{
		{name: "no_arn", region: "us-east-1", profileArn: ""},
		{name: "placeholder", region: "us-east-1", profileArn: kiroBuilderIDProfileARN},
		{name: "social_arn", region: "us-east-1", profileArn: kiroSocialProfileARN},
		{name: "no_arn_eu", region: "eu-central-1", profileArn: ""},
	})

	hybrid, hybridErr := svc.requestKiroUsageLimits(ctx, account, accessToken)
	if hybridErr != nil {
		t.Logf("HYBRID(as-exported) FAIL %v", hybridErr)
	} else {
		t.Logf("HYBRID(as-exported) OK %s", summarizeKiroUsage(hybrid))
	}

	if profiles, listErr := kiroListAvailableProfiles(ctx, account, accessToken); listErr != nil {
		t.Logf("ListAvailableProfiles: FAIL %v", listErr)
	} else if real := profiles.firstARN(); real != "" {
		t.Logf("ListAvailableProfiles: ok first=%q", real)
		runProbes("with-real-arn", []probe{
			{name: "real_arn", region: "us-east-1", profileArn: real},
		})
		account.Credentials["profile_arn"] = real
		hybrid2, hybrid2Err := svc.requestKiroUsageLimits(ctx, account, accessToken)
		if hybrid2Err != nil {
			t.Logf("HYBRID(with-real-arn) FAIL %v", hybrid2Err)
		} else {
			t.Logf("HYBRID(with-real-arn) OK %s", summarizeKiroUsage(hybrid2))
		}
	} else {
		t.Logf("ListAvailableProfiles: ok but empty")
	}

	require.NoError(t, hybridErr)
	require.NotNil(t, hybrid)
	require.NotEmpty(t, hybrid.UsageBreakdownList)

	modelSvc := &AccountTestService{}
	models, modelsErr := modelSvc.fetchKiroUpstreamModels(ctx, account)
	if modelsErr != nil {
		t.Fatalf("LIST MODELS FAIL %v", modelsErr)
	}
	t.Logf("LIST MODELS OK count=%d sample=%v", len(models), trimModelSample(models, 8))
	require.NotEmpty(t, models)

	testSvc := &AccountTestService{
		httpUpstream:        liveKiroHTTPUpstream{},
		tlsFPProfileService: &TLSFingerprintProfileService{},
	}
	payload, err := createTestPayload("claude-sonnet-4.6")
	require.NoError(t, err)
	payloadBytes, err := json.Marshal(payload)
	require.NoError(t, err)
	chatResp, chatErr := testSvc.executeKiroTestUpstream(ctx, account, payloadBytes, "claude-sonnet-4.6", accessToken)
	require.NoError(t, chatErr)
	defer func() { _ = chatResp.Body.Close() }()
	t.Logf("CHAT TEST HTTP %d", chatResp.StatusCode)
	require.Equal(t, http.StatusOK, chatResp.StatusCode)
}

func trimModelSample(models []string, n int) []string {
	if len(models) <= n {
		return models
	}
	return models[:n]
}

type liveKiroHTTPUpstream struct{}

func (liveKiroHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return liveKiroHTTPUpstream{}.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (liveKiroHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:           proxyURL,
		Timeout:            60 * time.Second,
		ValidateResolvedIP: true,
	})
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func stringCredential(creds map[string]any, key string) string {
	if creds == nil {
		return ""
	}
	v, ok := creds[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func firstNonEmptyLive(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func summarizeKiroUsage(resp *kiroUsageLimitsResponse) string {
	if resp == nil {
		return "nil"
	}
	credit := selectKiroCreditBreakdown(resp.UsageBreakdownList)
	current, limit := 0.0, 0.0
	if credit != nil {
		current = selectKiroFloat(credit.CurrentUsageWithPrecision, credit.CurrentUsage)
		limit = selectKiroFloat(credit.UsageLimitWithPrecision, credit.UsageLimit)
	}
	return fmt.Sprintf("sub=%q type=%q credit=%.4f/%.4f reset=%v",
		strings.TrimSpace(resp.SubscriptionInfo.SubscriptionTitle),
		strings.TrimSpace(resp.SubscriptionInfo.Type),
		current, limit, resp.NextDateReset)
}
