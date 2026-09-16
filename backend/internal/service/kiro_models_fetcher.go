package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// kiroListAvailableModelsOrigin 与 generateAssistantResponse / getUsageLimits 一致，
// 标识 Kiro IDE 客户端来源。
const kiroListAvailableModelsOrigin = "AI_EDITOR"

// kiroListAvailableModelsPaths 是 listAvailableModels 的候选路径。
// Kiro IDE（0.8.206 反编译）记录为大写 "/ListAvailableModels"，而本仓已用的兄弟
// 端点是 lowerCamel。上游对错误大小写返回 404/405，因此按序探测：首个非
// 404/405 的响应即为准。
var kiroListAvailableModelsPaths = []string{"/listAvailableModels", "/ListAvailableModels"}

// kiroListAvailableModelsMaxPages 给 nextToken 分页兜底上限（上游目录仅 ~20 条，
// 正常 1 页即完；上限仅防上游 token 不收敛导致死循环）。
const kiroListAvailableModelsMaxPages = 10

type kiroAvailableModelEntry struct {
	ID          string `json:"id"`
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	ModelName   string `json:"modelName"`
	Description string `json:"description"`
}

type kiroListAvailableModelsResponse struct {
	Models       []kiroAvailableModelEntry `json:"models"`
	DefaultModel *kiroAvailableModelEntry  `json:"defaultModel"`
	NextToken    string                    `json:"nextToken"`
}

// kiroAvailableModelID 取条目的上游模型 id。上游按 { id, name, description }
// 使用；modelId/modelName 作为 AWS SDK 风格命名的兼容兜底。name 放最后——它是
// 展示名，不能优先于协议 id。
func kiroAvailableModelID(entry kiroAvailableModelEntry) string {
	for _, candidate := range []string{entry.ID, entry.ModelID, entry.ModelName, entry.Name} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// fetchKiroDirectUpstreamModels 拉取直连 AWS 模式 Kiro 账号的上游模型列表。
//
// 端点：GET {kiroRuntimeEndpoint(region)}/listAvailableModels?origin=AI_EDITOR[&profileArn=...][&nextToken=...]
// 鉴权：API Key 账号用 kiro_api_key 凭据；其余经 kiroTokenProvider 取 access_token，
// 401/403 时 ForceRefreshAccessToken 重试一次（与 executeKiroTestUpstream 一致）。
func (s *AccountTestService) fetchKiroDirectUpstreamModels(ctx context.Context, account *Account) ([]string, error) {
	if s == nil || s.httpUpstream == nil {
		return nil, newUpstreamModelSyncConfigError("Upstream HTTP client is not configured", nil)
	}
	if account == nil {
		return nil, newUpstreamModelSyncConfigError("Account is required", nil)
	}

	token, err := s.resolveKiroModelSyncToken(ctx, account)
	if err != nil {
		return nil, err
	}

	endpoint := resolveKiroRuntimeEndpoint(kiroAPIRegion(account))
	profileArn := kiroResolveRequestProfileArn(account)
	proxyURL := upstreamModelsProxyURL(account)
	bodyLimit := resolveModelsListReadLimit(s.cfg)

	// 路径大小写探测：只在首个请求上做，命中后固定。
	path := ""
	models := make([]string, 0, 32)
	nextToken := ""
	refreshed := false

	for range kiroListAvailableModelsMaxPages {
		candidates := kiroListAvailableModelsPaths
		if path != "" {
			candidates = []string{path}
		}

		var (
			parsed      *kiroListAvailableModelsResponse
			lastErr     error
			matchedPath string
		)
		for _, candidate := range candidates {
			resp, reqErr := s.doKiroModelsRequest(ctx, account, endpoint, candidate, profileArn, nextToken, token, proxyURL)
			if reqErr != nil {
				return nil, reqErr
			}

			// OAuth token 过期：强制刷新后重试同一路径一次。
			if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) &&
				!refreshed && account.Type != AccountTypeAPIKey && s.kiroTokenProvider != nil {
				_ = resp.Body.Close()
				refreshedToken, refreshErr := s.kiroTokenProvider.ForceRefreshAccessToken(ctx, account)
				refreshed = true
				if refreshErr != nil || strings.TrimSpace(refreshedToken) == "" {
					return nil, newUpstreamModelSyncUpstreamError("Failed to refresh Kiro access token", refreshErr)
				}
				token = refreshedToken
				profileArn = kiroResolveRequestProfileArn(account)
				resp, reqErr = s.doKiroModelsRequest(ctx, account, endpoint, candidate, profileArn, nextToken, token, proxyURL)
				if reqErr != nil {
					return nil, reqErr
				}
			}

			pageResult, readErr := readKiroModelsResponse(resp, bodyLimit)
			if readErr != nil {
				lastErr = readErr
				if upstreamModelListEndpointUnsupported(readErr) && path == "" {
					continue // 换下一个路径大小写
				}
				return nil, readErr
			}
			parsed = pageResult
			matchedPath = candidate
			break
		}
		if parsed == nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, newUpstreamModelSyncUpstreamError("Kiro model list request produced no response", nil)
		}
		path = matchedPath

		for _, entry := range parsed.Models {
			if modelID := kiroAvailableModelID(entry); modelID != "" {
				models = append(models, modelID)
			}
		}
		if parsed.DefaultModel != nil {
			if modelID := kiroAvailableModelID(*parsed.DefaultModel); modelID != "" {
				models = append(models, modelID)
			}
		}

		nextToken = strings.TrimSpace(parsed.NextToken)
		if nextToken == "" {
			break
		}
	}

	models = dedupeAndSortModelIDs(models)
	if len(models) == 0 {
		return nil, newUpstreamModelSyncUpstreamError("Upstream returned no supported models", nil)
	}
	return models, nil
}

func (s *AccountTestService) resolveKiroModelSyncToken(ctx context.Context, account *Account) (string, error) {
	if account.Type == AccountTypeAPIKey {
		token := firstKiroCredential(account, "kiro_api_key", "kiroApiKey", "api_key")
		if strings.TrimSpace(token) == "" {
			return "", newUpstreamModelSyncConfigError("No Kiro API key is available", nil)
		}
		return token, nil
	}
	if s.kiroTokenProvider == nil {
		return "", newUpstreamModelSyncConfigError("Kiro token provider is not configured", nil)
	}
	token, err := s.kiroTokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return "", newUpstreamModelSyncUpstreamError("Failed to get Kiro access token", err)
	}
	if strings.TrimSpace(token) == "" {
		return "", newUpstreamModelSyncConfigError("No Kiro access token is available", nil)
	}
	return token, nil
}

func (s *AccountTestService) doKiroModelsRequest(
	ctx context.Context,
	account *Account,
	endpoint, path, profileArn, nextToken, token, proxyURL string,
) (*http.Response, error) {
	reqURL, err := url.Parse(strings.TrimRight(endpoint, "/") + path)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid Kiro model list URL", err)
	}
	query := reqURL.Query()
	query.Set("origin", kiroListAvailableModelsOrigin)
	if profileArn = strings.TrimSpace(profileArn); profileArn != "" {
		query.Set("profileArn", profileArn)
	}
	if nextToken = strings.TrimSpace(nextToken); nextToken != "" {
		query.Set("nextToken", nextToken)
	}
	reqURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, newUpstreamModelSyncConfigError("Invalid Kiro model list request", err)
	}
	applyKiroRuntimeGETHeaders(req, account, token)
	account.ApplyHeaderOverrides(req.Header)

	resp, err := s.doUpstreamModelsRequest(req, proxyURL, account)
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Failed to request upstream model list", err)
	}
	return resp, nil
}

func readKiroModelsResponse(resp *http.Response, bodyLimit int64) (*kiroListAvailableModelsResponse, error) {
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Failed to read upstream model list", err)
	}
	if int64(len(body)) > bodyLimit {
		return nil, newUpstreamModelSyncUpstreamError("Upstream model list response is too large", fmt.Errorf("response exceeds %d bytes", bodyLimit))
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &UpstreamModelSyncError{
			Kind:       UpstreamModelSyncErrorUpstream,
			Message:    fmt.Sprintf("Upstream model list request failed with HTTP %d", resp.StatusCode),
			StatusCode: resp.StatusCode,
			Err:        fmt.Errorf("upstream model list returned HTTP %d", resp.StatusCode),
		}
	}

	var parsed kiroListAvailableModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, newUpstreamModelSyncUpstreamError("Upstream model list response was not valid JSON", err)
	}
	return &parsed, nil
}
