package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/zed"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type zedUpstreamStub struct {
	responses []*http.Response
	requests  []*http.Request
	bodies    [][]byte
	tokens    []string
}

func (u *zedUpstreamStub) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	u.requests = append(u.requests, req)
	u.tokens = append(u.tokens, req.Header.Get("Authorization"))
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		u.bodies = append(u.bodies, b)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	resp := u.responses[0]
	if len(u.responses) > 1 {
		u.responses = u.responses[1:]
	}
	return resp, nil
}

func (u *zedUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

type zedMinterStub struct {
	token string
	calls int
}

func (m *zedMinterStub) MintToken(ctx context.Context, account *Account) (*ZedTokenInfo, error) {
	m.calls++
	return &ZedTokenInfo{Token: m.token}, nil
}

func (m *zedMinterStub) BuildAccountCredentials(tokenInfo *ZedTokenInfo) map[string]any {
	return map[string]any{zed.CredentialLLMToken: tokenInfo.Token}
}

func newZedAccountForTest() *Account {
	return &Account{
		ID:          701,
		Name:        "zed-relay-test",
		Platform:    PlatformZed,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			zed.CredentialUserID:   "42",
			zed.CredentialLLMToken: "minted-jwt",
			zed.CredentialSystemID: "system-abc",
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func newZedGatewayForTest(upstream HTTPUpstream) *GatewayService {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}
	svc.SetZedTokenProvider(&ZedTokenProvider{})
	return svc
}

func newZedTestContext() (*httptest.ResponseRecorder, *gin.Context) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return rec, c
}

func zedNDJSONResponse(lines ...string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-zed"}},
		Body:       io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")),
	}
}

func TestForwardZedMessages_AnthropicStreamRelay(t *testing.T) {
	rec, c := newZedTestContext()
	upstream := &zedUpstreamStub{responses: []*http.Response{zedNDJSONResponse(
		`{"type":"message_start","message":{"id":"msg_z","model":"claude-sonnet-4-5","usage":{"input_tokens":11,"cache_read_input_tokens":4}}}`,
		`{"type":"status_update","status":"working"}`,
		`{"event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}`,
		`{"type":"message_stop"}`,
	)}}
	svc := newZedGatewayForTest(upstream)

	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model:  "claude-sonnet-4-5",
		Stream: true,
	}

	result, err := svc.Forward(context.Background(), c, newZedAccountForTest(), parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, 11, result.Usage.InputTokens)
	require.Equal(t, 6, result.Usage.OutputTokens)
	require.Equal(t, 4, result.Usage.CacheReadInputTokens)
	require.Equal(t, "claude-sonnet-4-5", result.Model)

	// The client must receive Anthropic SSE, with Zed's transport messages filtered.
	out := rec.Body.String()
	require.Contains(t, out, "event: message_start\ndata: ")
	require.Contains(t, out, "event: content_block_delta\ndata: ")
	require.Contains(t, out, "event: message_stop\ndata: ")
	require.NotContains(t, out, "status_update")
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	require.Equal(t, zed.BaseURL+zed.CompletionsPath, upstream.requests[0].URL.String())
	require.Equal(t, "Bearer minted-jwt", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "system-abc", upstream.requests[0].Header.Get(zed.HeaderZedSystemID))
	require.Equal(t, "true", upstream.requests[0].Header.Get(zed.HeaderSupportsStatusMessages))
	require.Equal(t, "true", upstream.requests[0].Header.Get(zed.HeaderSupportsStreamEnded))
	require.Contains(t, upstream.requests[0].Header.Get("User-Agent"), "Zed/")

	envelope := upstream.bodies[0]
	require.Equal(t, "anthropic", gjson.GetBytes(envelope, "provider").String())
	require.Equal(t, "claude-sonnet-4-5", gjson.GetBytes(envelope, "model").String())
	require.Equal(t, "user_prompt", gjson.GetBytes(envelope, "intent").String())
	require.NotEqual(t, gjson.GetBytes(envelope, "thread_id").String(), gjson.GetBytes(envelope, "prompt_id").String())
	require.Equal(t, "claude-sonnet-4-5", gjson.GetBytes(envelope, "provider_request.model").String())
	require.False(t, gjson.GetBytes(envelope, "provider_request.stream").Exists(), "anthropic provider_request carries no stream field")
}

func TestForwardZedMessages_NonStreamingAccumulates(t *testing.T) {
	rec, c := newZedTestContext()
	upstream := &zedUpstreamStub{responses: []*http.Response{zedNDJSONResponse(
		`{"type":"message_start","message":{"id":"msg_ns","model":"claude-sonnet-4-5","usage":{"input_tokens":7}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"acc"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"umulated"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)}}
	svc := newZedGatewayForTest(upstream)

	parsed := &ParsedRequest{
		Body:  NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model: "claude-sonnet-4-5",
	}

	result, err := svc.Forward(context.Background(), c, newZedAccountForTest(), parsed)
	require.NoError(t, err)
	require.False(t, result.Stream)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)

	require.Equal(t, "application/json", strings.Split(rec.Header().Get("Content-Type"), ";")[0])
	body := rec.Body.String()
	require.Equal(t, "message", gjson.Get(body, "type").String())
	require.Equal(t, "msg_ns", gjson.Get(body, "id").String())
	require.Equal(t, "accumulated", gjson.Get(body, "content.0.text").String())
	require.Equal(t, "end_turn", gjson.Get(body, "stop_reason").String())
	require.Equal(t, int64(5), gjson.Get(body, "usage.output_tokens").Int())
}

func TestForwardZedMessages_OpenAIProviderConvertsBothDirections(t *testing.T) {
	rec, c := newZedTestContext()
	upstream := &zedUpstreamStub{responses: []*http.Response{zedNDJSONResponse(
		`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-sol"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant"}}`,
		`{"event":{"type":"response.output_text.delta","output_index":0,"delta":"from gpt"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":13,"output_tokens":9,"total_tokens":22}}}`,
	)}}
	svc := newZedGatewayForTest(upstream)

	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef([]byte(`{"model":"gpt-5.6","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model:  "gpt-5.6",
		Stream: true,
	}

	result, err := svc.Forward(context.Background(), c, newZedAccountForTest(), parsed)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6", result.Model)
	require.Equal(t, "gpt-5.6-sol", result.UpstreamModel, "bare gpt-5.6 is normalized to the sol variant")
	require.Equal(t, 13, result.Usage.InputTokens, "response.completed usage is the billing source")
	require.Equal(t, 9, result.Usage.OutputTokens)

	out := rec.Body.String()
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, `"text_delta"`)
	require.Contains(t, out, "from gpt")
	require.Contains(t, out, "event: message_stop")

	envelope := upstream.bodies[0]
	require.Equal(t, "open_ai", gjson.GetBytes(envelope, "provider").String())
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(envelope, "model").String())
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(envelope, "provider_request.model").String())
	require.True(t, gjson.GetBytes(envelope, "provider_request.stream").Bool(), "responses provider_request must set stream")
	require.False(t, gjson.GetBytes(envelope, "provider_request.store").Bool(), "responses provider_request must set store=false")
}

// A stream that ends without response.completed must still be terminated, or the
// shared SSE consumer reports missing-terminal-event.
func TestForwardZedMessages_OpenAIStreamWithoutCompletedIsFinalized(t *testing.T) {
	rec, c := newZedTestContext()
	upstream := &zedUpstreamStub{responses: []*http.Response{zedNDJSONResponse(
		`{"type":"response.created","response":{"id":"resp_2","model":"gpt-5.6-sol"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant"}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"partial"}`,
	)}}
	svc := newZedGatewayForTest(upstream)

	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef([]byte(`{"model":"gpt-5.6-sol","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model:  "gpt-5.6-sol",
		Stream: true,
	}

	_, err := svc.Forward(context.Background(), c, newZedAccountForTest(), parsed)
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), "event: message_stop")
}

func TestForwardZedMessages_UnsupportedProviderNamesModel(t *testing.T) {
	_, c := newZedTestContext()
	svc := newZedGatewayForTest(&zedUpstreamStub{})

	parsed := &ParsedRequest{
		Body:  NewRequestBodyRef([]byte(`{"model":"gemini-3-pro","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model: "gemini-3-pro",
	}

	_, err := svc.Forward(context.Background(), c, newZedAccountForTest(), parsed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "gemini-3-pro")
	require.Contains(t, err.Error(), "google")
}

func TestForwardZedMessages_RetriesOnceAfter401(t *testing.T) {
	rec, c := newZedTestContext()
	upstream := &zedUpstreamStub{responses: []*http.Response{
		{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"error":"token expired"}`)),
		},
		zedNDJSONResponse(
			`{"type":"message_start","message":{"id":"msg_r","usage":{"input_tokens":1}}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		),
	}}
	svc := newZedGatewayForTest(upstream)
	svc.zedTokenProvider = &ZedTokenProvider{zedOAuthService: &zedMinterStub{token: "fresh-jwt"}}

	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model:  "claude-sonnet-4-5",
		Stream: true,
	}

	result, err := svc.Forward(context.Background(), c, newZedAccountForTest(), parsed)
	require.NoError(t, err)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Len(t, upstream.requests, 2, "401 must be retried exactly once")
	require.Equal(t, "Bearer minted-jwt", upstream.tokens[0])
	require.Equal(t, "Bearer fresh-jwt", upstream.tokens[1], "retry must carry the freshly minted token")
	require.Contains(t, rec.Body.String(), "event: message_stop")
}

func TestForwardZedMessages_SecondConsecutive401FailsOver(t *testing.T) {
	_, c := newZedTestContext()
	unauthorized := func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid token"}`)),
		}
	}
	upstream := &zedUpstreamStub{responses: []*http.Response{unauthorized(), unauthorized()}}
	svc := newZedGatewayForTest(upstream)
	svc.zedTokenProvider = &ZedTokenProvider{zedOAuthService: &zedMinterStub{token: "fresh-jwt"}}

	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model:  "claude-sonnet-4-5",
		Stream: true,
	}

	_, err := svc.Forward(context.Background(), c, newZedAccountForTest(), parsed)
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, http.StatusUnauthorized, failover.StatusCode)
	require.Contains(t, string(failover.ResponseBody), "invalid token")
}

// trial_blocked is a permanent system_id mismatch: it must not consume the retry
// budget and the surfaced message must say what is actually wrong.
func TestForwardZedMessages_TrialBlockedIsNotRetried(t *testing.T) {
	_, c := newZedTestContext()
	upstream := &zedUpstreamStub{responses: []*http.Response{{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"code":"trial_blocked","message":"trial usage blocked"}`)),
	}}}
	svc := newZedGatewayForTest(upstream)

	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model:  "claude-sonnet-4-5",
		Stream: true,
	}

	_, err := svc.Forward(context.Background(), c, newZedAccountForTest(), parsed)
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, http.StatusForbidden, failover.StatusCode)
	require.Equal(t, GatewayFailureStageAccountAuth, failover.Stage)
	require.Contains(t, failover.ClientMessage, zed.CredentialSystemID)
	require.Len(t, upstream.requests, 1, "trial_blocked must not be retried")
}

func TestForwardZedMessages_AppliesAccountModelMapping(t *testing.T) {
	_, c := newZedTestContext()
	upstream := &zedUpstreamStub{responses: []*http.Response{zedNDJSONResponse(
		`{"type":"message_start","message":{"id":"msg_m"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`,
	)}}
	svc := newZedGatewayForTest(upstream)

	account := newZedAccountForTest()
	account.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-5": "claude-opus-4-6"}

	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model:  "claude-sonnet-4-5",
		Stream: true,
	}

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-5", result.Model)
	require.Equal(t, "claude-opus-4-6", result.UpstreamModel)
	require.Equal(t, "claude-opus-4-6", gjson.GetBytes(upstream.bodies[0], "model").String())
	require.Equal(t, "claude-opus-4-6", gjson.GetBytes(upstream.bodies[0], "provider_request.model").String())
}

func TestForwardZedMessages_MissingTokenProviderIsRejected(t *testing.T) {
	_, c := newZedTestContext()
	svc := newZedGatewayForTest(&zedUpstreamStub{})
	svc.zedTokenProvider = nil

	parsed := &ParsedRequest{
		Body:  NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model: "claude-sonnet-4-5",
	}

	_, err := svc.Forward(context.Background(), c, newZedAccountForTest(), parsed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "token provider not configured")
}

// GetAccessToken can serve a cached token without minting, which would skip the
// mint-path system_id guard. Without this check the request reaches the upstream
// and comes back as an opaque trial_blocked 403.
func TestForwardZedMessages_MissingSystemIDFailsBeforeUpstream(t *testing.T) {
	for _, systemID := range []string{"", "   "} {
		_, c := newZedTestContext()
		upstream := &zedUpstreamStub{}
		svc := newZedGatewayForTest(upstream)

		account := newZedAccountForTest()
		account.Credentials[zed.CredentialSystemID] = systemID

		parsed := &ParsedRequest{
			Body:  NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
			Model: "claude-sonnet-4-5",
		}

		_, err := svc.Forward(context.Background(), c, account, parsed)
		require.Error(t, err)
		require.Contains(t, err.Error(), "system_id")
		require.Empty(t, upstream.requests, "no upstream request may be sent for a misconfigured account")
	}
}

// The relay must not leak the upstream body once it has been wrapped by the SSE
// adapter.
func TestForwardZedMessages_ClosesUpstreamBody(t *testing.T) {
	_, c := newZedTestContext()
	tracked := &zedTrackingBody{Reader: strings.NewReader(
		`{"type":"message_start","message":{"id":"msg_c"}}` + "\n" +
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n" +
			`{"type":"message_stop"}` + "\n",
	)}
	upstream := &zedUpstreamStub{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       tracked,
	}}}
	svc := newZedGatewayForTest(upstream)

	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)),
		Model:  "claude-sonnet-4-5",
		Stream: true,
	}

	_, err := svc.Forward(context.Background(), c, newZedAccountForTest(), parsed)
	require.NoError(t, err)
	require.True(t, tracked.closed, "upstream body must be closed after the relay finishes")
}

type zedTrackingBody struct {
	io.Reader
	closed bool
}

func (b *zedTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestBuildZedEnvelopeRejectsUnsupportedProvider(t *testing.T) {
	_, _, err := buildZedEnvelope(zed.ProviderGoogle, "gemini-3-pro", []byte(`{}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported provider")
}

func TestBuildZedResponsesProviderRequestPreservesSystemAndTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","max_tokens":64,"system":"be brief",` +
		`"messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"name":"read","description":"d","input_schema":{"type":"object"}}]}`)

	raw, err := buildZedResponsesProviderRequest(body, "gpt-5.6-sol")
	require.NoError(t, err)

	var req map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &req))
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(raw, "model").String())
	require.True(t, gjson.GetBytes(raw, "stream").Bool())
	require.False(t, gjson.GetBytes(raw, "store").Bool())
	// System text stays in input as a developer message (apicompat keeps the
	// conversation/cache shape rather than hoisting it into instructions).
	require.Equal(t, "developer", gjson.GetBytes(raw, "input.0.role").String())
	require.Contains(t, gjson.GetBytes(raw, "input.0.content").String(), "be brief")
	require.Equal(t, "read", gjson.GetBytes(raw, "tools.0.name").String())
}
