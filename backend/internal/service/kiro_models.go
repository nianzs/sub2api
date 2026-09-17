package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/google/uuid"
)

type kiroAvailableModel struct {
	ModelID   string `json:"modelId"`
	ModelName string `json:"modelName"`
}

type kiroAvailableModelsResponse struct {
	DefaultModel *kiroAvailableModel  `json:"defaultModel"`
	Models       []kiroAvailableModel `json:"models"`
	NextToken    string               `json:"nextToken"`
}

func (s *AccountTestService) fetchKiroUpstreamModels(ctx context.Context, account *Account) ([]string, error) {
	if account == nil {
		return nil, newUpstreamModelSyncConfigError("Account is required", nil)
	}
	token, err := s.kiroModelsAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	ensureKiroEnterpriseRealProfileArn(ctx, s.accountRepo, account, token)
	profileArn := kiroOAuthRequestProfileArn(account)

	candidates := kiroUsageRegionCandidates(account)
	var lastErr error
	seen := make(map[string]struct{}, len(candidates))
	for i, region := range candidates {
		endpoint := strings.TrimRight(resolveKiroRuntimeEndpoint(region), "/")
		if endpoint == "" {
			continue
		}
		if _, dup := seen[endpoint]; dup {
			continue
		}
		seen[endpoint] = struct{}{}

		models, err := s.doKiroListAvailableModels(ctx, account, endpoint, token, profileArn)
		if err == nil {
			if len(models) == 0 {
				return nil, newUpstreamModelSyncUpstreamError("Upstream returned no supported models", nil)
			}
			return models, nil
		}
		lastErr = err
		var httpErr *kiroUsageHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden && i+1 < len(candidates) {
			continue
		}
		break
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("kiro list models request failed: no endpoint")
	}
	return nil, newUpstreamModelSyncUpstreamError("Failed to request upstream model list", lastErr)
}

func (s *AccountTestService) kiroModelsAccessToken(ctx context.Context, account *Account) (string, error) {
	if account.Type == AccountTypeAPIKey {
		token := firstKiroCredential(account, "kiro_api_key", "kiroApiKey", "api_key")
		if token == "" {
			return "", newUpstreamModelSyncConfigError("No API key available", nil)
		}
		return token, nil
	}
	if s == nil || s.kiroTokenProvider == nil {
		token := strings.TrimSpace(account.GetCredential("access_token"))
		if token == "" {
			return "", newUpstreamModelSyncConfigError("Kiro token provider not configured", nil)
		}
		return token, nil
	}
	token, err := s.kiroTokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return "", newUpstreamModelSyncUpstreamError("Failed to get Kiro access token", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", newUpstreamModelSyncConfigError("No access token available", nil)
	}
	return token, nil
}

func (s *AccountTestService) doKiroListAvailableModels(ctx context.Context, account *Account, endpoint, token, profileArn string) ([]string, error) {
	reqURL, err := url.Parse(endpoint + "/ListAvailableModels")
	if err != nil {
		return nil, fmt.Errorf("build kiro models url failed: %w", err)
	}
	q := reqURL.Query()
	q.Set("origin", kiroUsageOrigin)
	q.Set("maxResults", "50")
	if profileArn = strings.TrimSpace(profileArn); profileArn != "" {
		q.Set("profileArn", profileArn)
	}
	reqURL.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create kiro models request failed: %w", err)
	}
	accountKey := buildKiroAccountKey(account)
	machineID := buildKiroMachineID(account)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("User-Agent", kiropkg.BuildUsageRuntimeUserAgent(accountKey, machineID))
	req.Header.Set("X-Amz-User-Agent", kiropkg.BuildUsageRuntimeAmzUserAgent(accountKey, machineID))
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("Amz-Sdk-Request", "attempt=1; max=1")
	req.Header.Set("Amz-Sdk-Invocation-Id", uuid.NewString())
	applyKiroConditionalHeaders(req, account)

	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:           kiroProxyURL(account),
		Timeout:            30 * time.Second,
		ValidateResolvedIP: true,
		AllowPrivateHosts:  isLoopbackEndpoint(endpoint),
	})
	if err != nil {
		return nil, fmt.Errorf("create kiro models client failed: %w", err)
	}

	models := make([]string, 0, 16)
	seen := make(map[string]struct{}, 16)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}

	for page := 0; page < 8; page++ {
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("kiro list models request failed: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read kiro models response failed: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, &kiroUsageHTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
		}
		var parsed kiroAvailableModelsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode kiro models response failed: %w", err)
		}
		if parsed.DefaultModel != nil {
			add(parsed.DefaultModel.ModelID)
		}
		for _, model := range parsed.Models {
			add(model.ModelID)
		}
		next := strings.TrimSpace(parsed.NextToken)
		if next == "" {
			break
		}
		q := req.URL.Query()
		q.Set("nextToken", next)
		req.URL.RawQuery = q.Encode()
		nextReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL.String(), nil)
		if err != nil {
			return nil, err
		}
		nextReq.Header = req.Header.Clone()
		req = nextReq
	}
	return models, nil
}
