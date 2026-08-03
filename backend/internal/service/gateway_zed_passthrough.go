package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/zed"

	"github.com/gin-gonic/gin"
)

// forwardZedMessages relays an Anthropic Messages request through
// cloud.zed.dev/completions.
//
// Zed wraps a provider-native request in its own envelope and always answers
// with an NDJSON stream. For ProviderAnthropic that stream carries native
// Anthropic events, so the body is re-framed as SSE and handed to the existing
// Anthropic passthrough streaming handler; for ProviderOpenAI the Responses
// events are converted through apicompat's state machine first. A client that
// asked for a non-streaming response is served by accumulating the same stream.
func (s *GatewayService) forwardZedMessages(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	startTime time.Time,
) (*ForwardResult, error) {
	if account == nil || parsed == nil {
		return nil, fmt.Errorf("zed forward: missing account or request")
	}
	if s.zedTokenProvider == nil {
		return nil, fmt.Errorf("zed forward: token provider not configured")
	}

	originalModel := parsed.Model
	upstreamModel := zed.NormalizeModel(account.GetMappedModel(originalModel))
	provider := zed.ProviderForModel(upstreamModel)
	if !zed.IsSupportedProvider(provider) {
		return nil, fmt.Errorf("zed forward: model %q routes to unsupported provider %q", upstreamModel, provider)
	}

	body := parsed.Body.Bytes()
	if upstreamModel != originalModel {
		body = s.replaceModelInBody(body, upstreamModel)
	}

	envelopeBody, transform, err := buildZedEnvelope(provider, upstreamModel, body)
	if err != nil {
		return nil, err
	}

	isStream := parsed.Stream
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	systemID := strings.TrimSpace(account.GetCredential(zed.CredentialSystemID))
	version := strings.TrimSpace(account.GetCredential(zed.CredentialZedVersion))

	// Use the default system_id if the account doesn't have one configured.
	if systemID == "" {
		systemID = zed.DefaultSystemID
	}

	token, err := s.zedTokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	send := func(token string) (*http.Response, error) {
		upstreamCtx, release := detachStreamUpstreamContext(ctx, isStream)
		defer release()
		req, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost,
			zed.BaseURL+zed.CompletionsPath, bytes.NewReader(envelopeBody))
		if err != nil {
			return nil, err
		}
		zed.ApplyCompletionHeaders(req.Header, token, systemID, version)
		account.ApplyHeaderOverrides(req.Header)
		return s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account))
	}

	logger.LegacyPrintf("service.gateway", "[Zed] relay: account=%d name=%s model=%s upstream_model=%s provider=%s stream=%v",
		account.ID, account.Name, originalModel, upstreamModel, provider, isStream)

	var resp *http.Response
	var errBody []byte
	refreshed := false
	retryStart := time.Now()
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		errBody = nil
		resp, err = send(token)
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			return nil, s.zedRequestError(c, account, err)
		}

		// The minted LLM token is short-lived and cached; a 401 usually means the
		// cached copy aged out mid-flight rather than that the account is dead.
		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			refreshed = true
			_, _ = s.readUpstreamErrorBody(resp)
			_ = resp.Body.Close()
			next, refreshErr := s.zedTokenProvider.ForceRefreshAccessToken(ctx, account)
			if refreshErr != nil {
				logger.LegacyPrintf("service.gateway", "[Zed] token refresh after 401 failed: account=%d err=%v", account.ID, refreshErr)
				return nil, refreshErr
			}
			token = next
			resp, err = send(token)
			if err != nil {
				if resp != nil && resp.Body != nil {
					_ = resp.Body.Close()
				}
				return nil, s.zedRequestError(c, account, err)
			}
		}

		if resp.StatusCode < 400 {
			break
		}
		errBody = s.restoreZedErrorBody(resp)

		// trial_blocked is a permanent credential mismatch, but arrives as a 403 —
		// which is the one status retried for OAuth accounts. Retrying it only
		// burns the retry budget.
		if resp.StatusCode == http.StatusForbidden && zedIsTrialBlocked(errBody) {
			break
		}

		if resp.StatusCode != 400 && s.shouldRetryUpstreamError(account, resp.StatusCode) && attempt < maxRetryAttempts {
			elapsed := time.Since(retryStart)
			if elapsed >= maxRetryElapsed {
				break
			}
			delay := retryBackoffDelay(attempt)
			if remaining := maxRetryElapsed - elapsed; delay > remaining {
				delay = remaining
			}
			if delay <= 0 {
				break
			}
			s.appendZedUpstreamError(c, account, resp, errBody, "retry")
			logger.LegacyPrintf("service.gateway", "[Zed] upstream error %d, retry %d/%d after %v (elapsed=%v/%v): account=%d",
				resp.StatusCode, attempt, maxRetryAttempts, delay, elapsed, maxRetryElapsed, account.ID)
			if err := sleepWithContext(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		break
	}
	if resp == nil || resp.Body == nil {
		return nil, errors.New("zed forward: empty response")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return s.handleZedErrorResponse(ctx, c, account, resp, errBody, upstreamModel)
	}

	// Zed only speaks NDJSON; re-frame it so the Anthropic SSE consumers below see
	// the protocol they expect. resp.Body is passed as its own closer so the
	// deferred Close above still releases the upstream connection.
	sseBody := zed.NewSSEReader(resp.Body, resp.Body, transform)
	resp.Body = sseBody

	if !isStream {
		usage, err := s.writeZedNonStreamingResponse(c, sseBody, upstreamModel)
		if err != nil {
			return nil, err
		}
		return &ForwardResult{
			RequestID:     resp.Header.Get("x-request-id"),
			Usage:         *usage,
			Model:         originalModel,
			UpstreamModel: upstreamModel,
			Duration:      time.Since(startTime),
		}, nil
	}

	// The shared handler copies Content-Type from the upstream response; the body is
	// SSE now, so the upstream's application/json must not be forwarded.
	resp.Header.Set("Content-Type", "text/event-stream")
	streamResult, err := s.handleStreamingResponseAnthropicAPIKeyPassthrough(ctx, resp, c, account, startTime, upstreamModel)
	if err != nil {
		if partial := partialStreamUsageResult(resp, streamResult, originalModel, upstreamModel, startTime, err); partial != nil {
			return partial, err
		}
		return nil, err
	}
	usage := streamResult.usage
	if usage == nil {
		usage = &ClaudeUsage{}
	}
	return &ForwardResult{
		RequestID:        resp.Header.Get("x-request-id"),
		Usage:            *usage,
		Model:            originalModel,
		UpstreamModel:    upstreamModel,
		Stream:           true,
		Duration:         time.Since(startTime),
		FirstTokenMs:     streamResult.firstTokenMs,
		ClientDisconnect: streamResult.clientDisconnect,
	}, nil
}

// buildZedEnvelope wraps the client request in Zed's envelope and returns the
// response transform matching the routed provider.
func buildZedEnvelope(provider, model string, body []byte) ([]byte, zed.EventTransform, error) {
	var providerRequest json.RawMessage
	var transform zed.EventTransform

	switch provider {
	case zed.ProviderAnthropic:
		req, err := zed.BuildAnthropicProviderRequest(body, model)
		if err != nil {
			return nil, nil, fmt.Errorf("build zed anthropic request: %w", err)
		}
		providerRequest = req
		transform = zed.AnthropicPassthroughTransform{}
	case zed.ProviderOpenAI:
		req, err := buildZedResponsesProviderRequest(body, model)
		if err != nil {
			return nil, nil, err
		}
		providerRequest = req
		transform = newZedResponsesTransform(model)
	default:
		return nil, nil, fmt.Errorf("zed forward: unsupported provider %q", provider)
	}

	envelopeBody, err := json.Marshal(zed.NewEnvelope(provider, model, providerRequest))
	if err != nil {
		return nil, nil, fmt.Errorf("marshal zed envelope: %w", err)
	}
	return envelopeBody, transform, nil
}

// buildZedResponsesProviderRequest converts the client's Anthropic request into
// the OpenAI Responses request Zed's open_ai provider expects. stream and store
// are pinned to the values the real client sends.
func buildZedResponsesProviderRequest(body []byte, model string) (json.RawMessage, error) {
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, fmt.Errorf("parse anthropic request for zed open_ai: %w", err)
	}
	anthropicReq.Model = model

	responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("convert anthropic request to responses: %w", err)
	}
	responsesReq.Stream = true
	storeFalse := false
	responsesReq.Store = &storeFalse

	out, err := json.Marshal(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("marshal zed responses request: %w", err)
	}
	return out, nil
}

// zedResponsesTransform converts upstream OpenAI Responses events into Anthropic
// SSE frames, carrying the conversion state across lines.
type zedResponsesTransform struct {
	state *apicompat.ResponsesEventToAnthropicState
}

func newZedResponsesTransform(model string) *zedResponsesTransform {
	state := apicompat.NewResponsesEventToAnthropicState()
	state.Model = model
	return &zedResponsesTransform{state: state}
}

func (t *zedResponsesTransform) Transform(event json.RawMessage) []byte {
	var evt apicompat.ResponsesStreamEvent
	if err := json.Unmarshal(event, &evt); err != nil {
		return nil
	}
	return zedAnthropicEventsToSSE(apicompat.ResponsesEventToAnthropicEvents(&evt, t.state))
}

// Finalize covers streams that end without response.completed; without it the
// caller's SSE consumer would never see a terminal event.
func (t *zedResponsesTransform) Finalize() []byte {
	return zedAnthropicEventsToSSE(apicompat.FinalizeResponsesAnthropicStream(t.state))
}

func zedAnthropicEventsToSSE(events []apicompat.AnthropicStreamEvent) []byte {
	if len(events) == 0 {
		return nil
	}
	var out bytes.Buffer
	for _, evt := range events {
		sse, err := apicompat.ResponsesAnthropicEventToSSE(evt)
		if err != nil {
			continue
		}
		out.WriteString(sse)
	}
	return out.Bytes()
}

// writeZedNonStreamingResponse collects the SSE stream into a single Messages
// response for clients that did not ask for streaming.
func (s *GatewayService) writeZedNonStreamingResponse(c *gin.Context, body io.Reader, model string) (*ClaudeUsage, error) {
	message, err := zed.AccumulateSSE(body, model)
	if err != nil {
		return nil, fmt.Errorf("accumulate zed stream: %w", err)
	}
	out, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("marshal zed response: %w", err)
	}
	out = reverseToolNamesIfPresent(c, out)
	c.Data(http.StatusOK, "application/json", out)
	return &ClaudeUsage{
		InputTokens:              message.Usage.InputTokens,
		OutputTokens:             message.Usage.OutputTokens,
		CacheCreationInputTokens: message.Usage.CacheCreationInputTokens,
		CacheReadInputTokens:     message.Usage.CacheReadInputTokens,
	}, nil
}

// handleZedErrorResponse runs the shared retry-exhausted/failover/error ladder.
// respBody is the already-drained error body, restored onto resp for the shared
// side-effect helpers.
func (s *GatewayService) handleZedErrorResponse(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	resp *http.Response,
	respBody []byte,
	upstreamModel string,
) (*ForwardResult, error) {
	retryable := s.shouldRetryUpstreamError(account, resp.StatusCode)
	failover := s.shouldFailoverUpstreamError(resp.StatusCode)

	// A 401 that survived the forced token refresh, and a trial_blocked 403, are
	// both credential-scoped: neither is retryable on this account.
	//
	// The shared failover side-effects are deliberately skipped here. They treat an
	// OAuth 401 without a refresh_token as permanently fatal, and Zed accounts
	// never carry one — they mint short-lived tokens instead, so the account is
	// recoverable and must not be disabled. Dropping the cached token is enough.
	if resp.StatusCode == http.StatusUnauthorized {
		if s.zedTokenProvider != nil {
			s.zedTokenProvider.InvalidateToken(ctx, account)
		}
		s.appendZedUpstreamError(c, account, resp, respBody, "failover")
		logger.LegacyPrintf("service.gateway", "[Zed] upstream 401 after token refresh, failing over: account=%d(%s) body=%s",
			account.ID, account.Name, truncateString(string(respBody), 512))
		return nil, &UpstreamFailoverError{StatusCode: http.StatusUnauthorized, ResponseBody: respBody}
	}

	if resp.StatusCode == http.StatusForbidden {
		if zedIsTrialBlocked(respBody) {
			// trial_blocked means the minted token's system_id is not the one this
			// account was registered under, so retrying or switching accounts will
			// not help — the credential needs re-registration.
			message := fmt.Sprintf("Zed rejected this account as trial_blocked: its %s credential does not match the client it was registered under. Re-authorize the account. Upstream: %s",
				zed.CredentialSystemID, truncateString(string(respBody), 512))
			logger.LegacyPrintf("service.gateway", "[Zed] trial_blocked: account=%d(%s) model=%s body=%s",
				account.ID, account.Name, upstreamModel, truncateString(string(respBody), 512))
			setOpsUpstreamError(c, resp.StatusCode, message, "")
			s.appendZedUpstreamError(c, account, resp, respBody, "trial_blocked")
			return nil, &UpstreamFailoverError{
				StatusCode:       resp.StatusCode,
				ResponseBody:     respBody,
				Stage:            GatewayFailureStageAccountAuth,
				ClientStatusCode: http.StatusForbidden,
				ClientMessage:    message,
			}
		}
	}

	if retryable {
		if failover {
			s.handleRetryExhaustedSideEffects(ctx, resp, account)
			s.appendZedUpstreamError(c, account, resp, respBody, "retry_exhausted_failover")
			logger.LegacyPrintf("service.gateway", "[Zed] upstream error (retry exhausted, failover): account=%d(%s) status=%d body=%s",
				account.ID, account.Name, resp.StatusCode, truncateString(string(respBody), 1000))
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
			}
		}
		return s.handleRetryExhaustedError(ctx, resp, c, account)
	}

	if failover {
		s.handleFailoverSideEffects(ctx, resp, account, upstreamModel)
		s.appendZedUpstreamError(c, account, resp, respBody, "failover")
		logger.LegacyPrintf("service.gateway", "[Zed] upstream error (failover): account=%d(%s) status=%d body=%s",
			account.ID, account.Name, resp.StatusCode, truncateString(string(respBody), 1000))
		return nil, &UpstreamFailoverError{
			StatusCode:             resp.StatusCode,
			ResponseBody:           respBody,
			RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
		}
	}

	return s.handleErrorResponse(ctx, resp, c, account, upstreamModel)
}

// restoreZedErrorBody drains the error body and puts it back on the response, so
// the shared side-effect helpers can read it again.
func (s *GatewayService) restoreZedErrorBody(resp *http.Response) []byte {
	respBody, _ := s.readUpstreamErrorBody(resp)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	return respBody
}

func zedIsTrialBlocked(body []byte) bool {
	return bytes.Contains(bytes.ToLower(body), []byte("trial_blocked"))
}

func (s *GatewayService) zedRequestError(c *gin.Context, account *Account, err error) error {
	safeErr := sanitizeUpstreamErrorMessage(err.Error())
	setOpsUpstreamError(c, 0, safeErr, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: 0,
		UpstreamURL:        zed.BaseURL + zed.CompletionsPath,
		Kind:               "request_error",
		Message:            safeErr,
	})
	if c != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "upstream_error",
				"message": "Upstream request failed",
			},
		})
	}
	return fmt.Errorf("zed upstream request failed: %s", safeErr)
}

func (s *GatewayService) appendZedUpstreamError(c *gin.Context, account *Account, resp *http.Response, respBody []byte, kind string) {
	detail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		detail = truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		UpstreamURL:        zed.BaseURL + zed.CompletionsPath,
		Kind:               kind,
		Message:            extractUpstreamErrorMessage(respBody),
		Detail:             detail,
	})
}
