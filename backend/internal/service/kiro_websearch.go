package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

const kiroMaxWebSearchIterations = 5

var (
	errKiroWebSearchFallback = errors.New("kiro web search fallback")
	kiroWebSearchDescCache   sync.Map
)

type kiroWebSearchExecution struct {
	ResponseBody []byte
	Usage        ClaudeUsage
	RequestID    string
}

type kiroWebSearchHTTPError struct {
	Response *http.Response
}

type kiroStreamChunkCollector struct {
	chunks [][]byte
}

func (e *kiroWebSearchHTTPError) Error() string {
	if e == nil || e.Response == nil {
		return "kiro web search http error"
	}
	return fmt.Sprintf("kiro web search http error: %d", e.Response.StatusCode)
}

func (w *kiroStreamChunkCollector) Write(p []byte) (int, error) {
	if len(p) > 0 {
		w.chunks = append(w.chunks, append([]byte(nil), p...))
	}
	return len(p), nil
}

func bufferKiroAnthropicStream(ctx context.Context, body io.Reader, responseModel string, inputTokens int, requestCtx kiropkg.KiroRequestContext) ([][]byte, *kiropkg.StreamResult, error) {
	collector := &kiroStreamChunkCollector{}
	result, err := kiropkg.StreamEventStreamAsAnthropicWithContext(ctx, body, collector, responseModel, inputTokens, requestCtx)
	if err != nil {
		return nil, nil, err
	}
	return collector.chunks, result, nil
}

func writeSSEChunks(w io.Writer, chunks [][]byte) error {
	for _, chunk := range chunks {
		if len(chunk) == 0 {
			continue
		}
		if _, err := w.Write(chunk); err != nil {
			return err
		}
	}
	return nil
}

func writeAnthropicMessageStart(w io.Writer, msgID, model string, inputTokens int, cacheUsage *kiroCacheEmulationUsage) error {
	if strings.TrimSpace(msgID) == "" {
		msgID = "msg_" + kiropkg.GenerateToolUseID()
	}
	if strings.TrimSpace(model) == "" {
		model = "kiro"
	}
	usage := map[string]any{
		"input_tokens":  inputTokens,
		"output_tokens": 0,
	}
	if cacheUsage != nil {
		usage["input_tokens"] = cacheUsage.InputTokens
		usage["cache_creation_input_tokens"] = cacheUsage.CacheCreationInputTokens
		usage["cache_read_input_tokens"] = cacheUsage.CacheReadInputTokens
	}
	payload, err := json.Marshal(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         usage,
		},
	})
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, "event: message_start\ndata: "+string(payload)+"\n\n")
	return err
}

func writeAnthropicMessageCompletion(w io.Writer, result *kiropkg.StreamResult) error {
	if result == nil {
		return errors.New("kiro web search missing stream result")
	}
	usage := result.Usage
	usagePayload := map[string]any{
		"input_tokens":                usage.InputTokens,
		"output_tokens":               usage.OutputTokens,
		"cache_read_input_tokens":     usage.CacheReadInputTokens,
		"cache_creation_input_tokens": usage.CacheCreationInputTokens,
	}
	if usage.CacheCreation5mInputTokens > 0 || usage.CacheCreation1hInputTokens > 0 {
		usagePayload["cache_creation"] = map[string]any{
			"ephemeral_5m_input_tokens": usage.CacheCreation5mInputTokens,
			"ephemeral_1h_input_tokens": usage.CacheCreation1hInputTokens,
		}
	}
	if usage.KiroCredits > 0 {
		usagePayload["_sub2api_kiro_credits"] = usage.KiroCredits
	}
	stopReason := strings.TrimSpace(result.StopReason)
	if stopReason == "" {
		stopReason = "end_turn"
	}
	payload, err := json.Marshal(map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nullableKiroStopSequence(result.StopSequence),
		},
		"usage": usagePayload,
	})
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "event: message_delta\ndata: "+string(payload)+"\n\n"); err != nil {
		return err
	}
	_, err = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return err
}

func nullableKiroStopSequence(stopSequence string) any {
	if strings.TrimSpace(stopSequence) == "" {
		return nil
	}
	return stopSequence
}

func addKiroStreamUsage(total *kiropkg.Usage, usage kiropkg.Usage) {
	if total == nil {
		return
	}
	total.InputTokens += usage.InputTokens
	total.InputTokensReported = total.InputTokensReported || usage.InputTokensReported
	total.OutputTokens += usage.OutputTokens
	total.TotalTokens += usage.TotalTokens
	total.CacheReadInputTokens += usage.CacheReadInputTokens
	total.CacheCreationInputTokens += usage.CacheCreationInputTokens
	total.CacheCreation5mInputTokens += usage.CacheCreation5mInputTokens
	total.CacheCreation1hInputTokens += usage.CacheCreation1hInputTokens
	total.KiroCredits += usage.KiroCredits
}

func (s *GatewayService) streamKiroWebSearchAsAnthropic(
	ctx context.Context, account *Account, group *Group, anthropicBody []byte, mappedModel, requestModel, token string, headers http.Header, w io.Writer,
) error {
	query := kiropkg.ExtractSearchQuery(anthropicBody)
	if strings.TrimSpace(query) == "" {
		return errKiroWebSearchFallback
	}

	currentBody, err := kiropkg.ReplaceWebSearchToolDescription(anthropicBody)
	if err != nil {
		currentBody = anthropicBody
	}
	currentToolUseID := "srvtoolu_" + kiropkg.GenerateToolUseID()
	nextContentBlockIndex := 0
	var aggregateUsage kiropkg.Usage
	upstreamModel := resolveKiroUpstreamModel(mappedModel)

	// The actual first model request is built only after MCP results are injected.
	// Defer all usage to the terminal delta so a speculative estimate cannot win
	// over an authoritative zero-token cache hit in downstream SSE accounting.
	if err := writeAnthropicMessageStart(w, "", requestModel, 0, nil); err != nil {
		return err
	}

	for iteration := 0; iteration < kiroMaxWebSearchIterations; iteration++ {
		s.prefetchKiroWebSearchDescription(ctx, account, token)

		results, nextToken, mcpErr := s.callKiroWebSearchMCP(ctx, account, token, query)
		if strings.TrimSpace(nextToken) != "" {
			token = nextToken
		}
		if mcpErr != nil {
			results = nil
		}

		if err := writeSSEChunks(w, kiropkg.GenerateSearchIndicatorEvents(query, currentToolUseID, results, nextContentBlockIndex)); err != nil {
			return err
		}
		nextContentBlockIndex += 2

		currentBody, err = kiropkg.InjectToolResultsClaude(currentBody, currentToolUseID, query, results)
		if err != nil {
			return errKiroWebSearchFallback
		}

		iterationInputTokens := estimateKiroInputTokens(ctx, currentBody)
		iterationCachePlan := s.prepareKiroCacheEmulationUsage(ctx, account, group, currentBody, mappedModel, iterationInputTokens)
		resp, requestCtx, err := s.executeKiroUpstream(ctx, account, currentBody, mappedModel, requestModel, token, headers)
		if err != nil {
			return err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return &kiroWebSearchHTTPError{Response: resp}
		}
		iterationCachePlan.commit()
		iterationCacheUsage := iterationCachePlan.result()

		chunks, streamResult, streamErr := func() ([][]byte, *kiropkg.StreamResult, error) {
			defer func() { _ = resp.Body.Close() }()
			requestCtx.UpstreamModel = upstreamModel
			requestCtx.CacheEmulationUsage = iterationCacheUsage.toKiroUsage()
			return bufferKiroAnthropicStream(ctx, resp.Body, requestModel, iterationInputTokens, requestCtx)
		}()
		if streamErr != nil {
			return streamErr
		}
		addKiroStreamUsage(&aggregateUsage, streamResult.Usage)

		analysis := kiropkg.AnalyzeBufferedStream(chunks)
		if analysis.HasWebSearchToolUse && strings.TrimSpace(analysis.WebSearchQuery) != "" && iteration+1 < kiroMaxWebSearchIterations {
			filtered := kiropkg.FilterChunksForClient(chunks, analysis.WebSearchToolUseIndex, nextContentBlockIndex)
			if err := writeSSEChunks(w, filtered); err != nil {
				return err
			}
			if maxIndex := kiropkg.MaxContentBlockIndex(filtered); maxIndex >= nextContentBlockIndex {
				nextContentBlockIndex = maxIndex + 1
			}
			query = analysis.WebSearchQuery
			if strings.TrimSpace(analysis.WebSearchToolUseID) == "" {
				currentToolUseID = "srvtoolu_" + kiropkg.GenerateToolUseID()
			} else {
				currentToolUseID = analysis.WebSearchToolUseID
			}
			continue
		}

		for _, chunk := range chunks {
			adjusted, shouldForward := kiropkg.AdjustSSEChunk(chunk, nextContentBlockIndex)
			if !shouldForward {
				continue
			}
			if _, err := w.Write(adjusted); err != nil {
				return err
			}
		}
		streamResult.Usage = aggregateUsage
		return writeAnthropicMessageCompletion(w, streamResult)
	}

	return fmt.Errorf("kiro web search exceeded max iterations")
}

func (s *GatewayService) executeKiroWebSearch(ctx context.Context, account *Account, group *Group, anthropicBody []byte, mappedModel, requestModel, token string, headers http.Header) (*kiroWebSearchExecution, error) {
	query := kiropkg.ExtractSearchQuery(anthropicBody)
	if strings.TrimSpace(query) == "" {
		return nil, errKiroWebSearchFallback
	}

	currentBody, err := kiropkg.ReplaceWebSearchToolDescription(anthropicBody)
	if err != nil {
		currentBody = anthropicBody
	}

	currentToolUseID := "srvtoolu_" + kiropkg.GenerateToolUseID()
	searches := make([]kiropkg.SearchIndicator, 0, 2)
	requestID := ""
	var aggregateUsage kiropkg.Usage
	aggregateFallbackInputTokens := 0

	for iteration := 0; iteration < kiroMaxWebSearchIterations; iteration++ {
		s.prefetchKiroWebSearchDescription(ctx, account, token)

		results, nextToken, mcpErr := s.callKiroWebSearchMCP(ctx, account, token, query)
		if strings.TrimSpace(nextToken) != "" {
			token = nextToken
		}
		if mcpErr != nil {
			results = nil
		}
		searches = append(searches, kiropkg.SearchIndicator{
			ToolUseID: currentToolUseID,
			Query:     query,
			Results:   results,
		})

		currentBody, err = kiropkg.InjectToolResultsClaude(currentBody, currentToolUseID, query, results)
		if err != nil {
			return nil, errKiroWebSearchFallback
		}

		iterationInputTokens := estimateKiroInputTokens(ctx, currentBody)
		aggregateFallbackInputTokens += iterationInputTokens
		iterationCachePlan := s.prepareKiroCacheEmulationUsage(ctx, account, group, currentBody, mappedModel, iterationInputTokens)
		resp, requestCtx, err := s.executeKiroUpstream(ctx, account, currentBody, mappedModel, requestModel, token, headers)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, &kiroWebSearchHTTPError{Response: resp}
		}
		iterationCachePlan.commit()
		iterationCacheUsage := iterationCachePlan.result()

		parseResult, parseErr := func() (*kiropkg.ParseResult, error) {
			defer func() { _ = resp.Body.Close() }()
			requestCtx.UpstreamModel = resolveKiroUpstreamModel(mappedModel)
			requestCtx.CacheEmulationUsage = iterationCacheUsage.toKiroUsage()
			return kiropkg.ParseNonStreamingEventStreamWithContext(resp.Body, requestModel, requestCtx)
		}()
		if parseErr != nil {
			return nil, parseErr
		}
		if requestID == "" {
			requestID = buildKiroRequestID(resp)
		}
		parseResult.Usage = resolveKiroInputUsage(parseResult.Usage, iterationInputTokens)
		addKiroStreamUsage(&aggregateUsage, parseResult.Usage)

		nextToolUseID, nextQuery, hasNext := kiropkg.ExtractWebSearchToolUseFromResponse(parseResult.ResponseBody)
		if !hasNext || strings.TrimSpace(nextQuery) == "" || iteration+1 >= kiroMaxWebSearchIterations {
			parseResult.Usage = aggregateUsage
			parseResult.ResponseBody, err = kiropkg.RewriteClaudeResponseUsage(parseResult.ResponseBody, aggregateUsage)
			if err != nil {
				return nil, err
			}
			finalBody, injectErr := kiropkg.InjectSearchIndicatorsInResponse(parseResult.ResponseBody, searches)
			if injectErr == nil {
				parseResult.ResponseBody = finalBody
			}
			return &kiroWebSearchExecution{
				ResponseBody: parseResult.ResponseBody,
				Usage:        kiroUsageToClaude(parseResult.Usage, aggregateFallbackInputTokens),
				RequestID:    requestID,
			}, nil
		}

		query = nextQuery
		if strings.TrimSpace(nextToolUseID) == "" {
			nextToolUseID = "srvtoolu_" + kiropkg.GenerateToolUseID()
		}
		currentToolUseID = nextToolUseID
	}

	return nil, fmt.Errorf("kiro web search exceeded max iterations")
}

func (s *GatewayService) prefetchKiroWebSearchDescription(ctx context.Context, account *Account, token string) {
	endpoint := kiropkg.BuildMcpEndpoint(kiroAPIRegion(account))
	if cached, ok := kiroWebSearchDescCache.Load(endpoint); ok {
		if desc, ok := cached.(string); ok && strings.TrimSpace(desc) != "" {
			kiropkg.SetCachedWebSearchDescription(desc)
		}
		return
	}

	reqBody, _ := json.Marshal(kiropkg.MCPRequest{
		ID:      "tools_list",
		JSONRPC: "2.0",
		Method:  "tools/list",
	})
	resp, _, err := s.doKiroMCPJSONRequest(ctx, account, endpoint, reqBody, token)
	if err != nil || resp == nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	var result kiropkg.MCPResponse
	if err := json.Unmarshal(body, &result); err != nil || result.Result == nil {
		return
	}
	for _, tool := range result.Result.Tools {
		if strings.EqualFold(tool.Name, "web_search") && strings.TrimSpace(tool.Description) != "" {
			kiroWebSearchDescCache.Store(endpoint, tool.Description)
			kiropkg.SetCachedWebSearchDescription(tool.Description)
			return
		}
	}
}

func (s *GatewayService) callKiroWebSearchMCP(ctx context.Context, account *Account, token, query string) (*kiropkg.WebSearchResults, string, error) {
	reqBody, err := json.Marshal(buildKiroWebSearchMCPRequest(query))
	if err != nil {
		return nil, token, err
	}

	endpoint := kiropkg.BuildMcpEndpoint(kiroAPIRegion(account))
	resp, nextToken, err := s.doKiroMCPJSONRequest(ctx, account, endpoint, reqBody, token)
	if err != nil {
		return nil, nextToken, err
	}
	if resp == nil {
		return nil, nextToken, fmt.Errorf("kiro web search returned nil response")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nextToken, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nextToken, fmt.Errorf("kiro mcp status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed kiropkg.MCPResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nextToken, err
	}
	if parsed.Error != nil {
		msg := "unknown error"
		if parsed.Error.Message != nil && strings.TrimSpace(*parsed.Error.Message) != "" {
			msg = strings.TrimSpace(*parsed.Error.Message)
		}
		code := 0
		if parsed.Error.Code != nil {
			code = *parsed.Error.Code
		}
		return nil, nextToken, fmt.Errorf("kiro mcp error %d: %s", code, msg)
	}

	return kiropkg.ParseSearchResults(&parsed), nextToken, nil
}

func buildKiroWebSearchMCPRequest(query string) kiropkg.MCPRequest {
	return kiropkg.MCPRequest{
		ID:      fmt.Sprintf("web_search_%s", kiropkg.GenerateToolUseID()),
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: map[string]any{
			"name": "web_search",
			"arguments": map[string]any{
				"query": query,
				"_meta": map[string]any{
					"_isValid":        true,
					"_activePath":     []string{"query"},
					"_completedPaths": [][]string{{"query"}},
				},
			},
		},
	}
}

func (s *GatewayService) doKiroMCPJSONRequest(ctx context.Context, account *Account, endpoint string, payload []byte, token string) (*http.Response, string, error) {
	currentToken := token
	accountKey := buildKiroAccountKey(account)
	proxyURL := kiroProxyURL(account)
	tlsProfile := s.tlsFPProfileService.ResolveTLSProfile(account)

	for attempt := 0; attempt < 3; attempt++ {
		if err := s.checkAndWaitKiroCooldown(ctx, accountKey); err != nil {
			if failoverErr := asKiroCooldownFailoverError(err); failoverErr != nil {
				return nil, currentToken, failoverErr
			}
			return nil, currentToken, err
		}

		req, err := newKiroJSONRequest(ctx, endpoint, payload, currentToken, accountKey, buildKiroMachineID(account), "", account)
		if err != nil {
			return nil, currentToken, err
		}

		resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, tlsProfile)
		if err != nil {
			return nil, currentToken, err
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			respBody, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				return nil, currentToken, readErr
			}
			if resp.StatusCode == http.StatusForbidden && isKiroSuspendedBody(respBody) {
				if _, err := s.markKiroSuspended(ctx, accountKey); err != nil {
					return nil, currentToken, err
				}
				resp.Body = io.NopCloser(strings.NewReader(string(respBody)))
				return resp, currentToken, nil
			}
			if resp.StatusCode == http.StatusForbidden && !isKiroTokenErrorBody(respBody) {
				resp.Body = io.NopCloser(strings.NewReader(string(respBody)))
				return resp, currentToken, nil
			}
			if s.kiroTokenProvider == nil {
				resp.Body = io.NopCloser(strings.NewReader(string(respBody)))
				return resp, currentToken, nil
			}
			refreshedToken, refreshErr := s.kiroTokenProvider.ForceRefreshAccessToken(ctx, account)
			if refreshErr != nil {
				resp.Body = io.NopCloser(strings.NewReader(string(respBody)))
				return resp, currentToken, nil
			}
			currentToken = refreshedToken
			accountKey = buildKiroAccountKey(account)
			if sleepErr := sleepKiroRetry(ctx, attempt); sleepErr != nil {
				return nil, currentToken, sleepErr
			}
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			if _, err := s.markKiro429(ctx, account.ID, accountKey); err != nil {
				_ = resp.Body.Close()
				return nil, currentToken, err
			}
		}
		if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500 {
			if attempt < 2 {
				_ = resp.Body.Close()
				if sleepErr := sleepKiroRetry(ctx, attempt); sleepErr != nil {
					return nil, currentToken, sleepErr
				}
				continue
			}
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if err := s.markKiroSuccess(ctx, account.ID, accountKey); err != nil {
				_ = resp.Body.Close()
				return nil, currentToken, err
			}
		}

		return resp, currentToken, nil
	}

	return nil, currentToken, fmt.Errorf("kiro mcp request retries exhausted")
}
