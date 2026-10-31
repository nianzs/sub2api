package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	wsBridgeTestUserID   = int64(501)
	wsBridgeTestAPIKeyID = int64(601)
	wsBridgeTestGroupID  = int64(701)
)

// wsBridgeTestServer 是 ws_bridge 测试用的上游 WS：读取 response.create 后等待 delay，
// 再依次写出 events。
type wsBridgeTestServer struct {
	*httptest.Server
	dials      atomic.Int32
	conns      atomic.Int32
	requests   atomic.Int32
	lastStream atomic.Bool
	lastReq    atomic.Value
	opts       wsBridgeTestServerOptions
}

type wsBridgeTestServerOptions struct {
	// rejectStatus 非零时直接以该状态码拒绝握手。
	rejectStatus int
	delay        time.Duration
	// delayAt：在写第几个事件之前等待 delay（默认 0，即先等待再写全部事件）。
	delayAt int
	// closeFirstConns：前 N 个连接读到请求后直接断开（模拟池中连接被上游关闭）。
	closeFirstConns int32
}

func newWSBridgeTestServer(t *testing.T, rejectStatus int, events ...string) *wsBridgeTestServer {
	t.Helper()
	return newWSBridgeTestServerWithOptions(t, wsBridgeTestServerOptions{rejectStatus: rejectStatus}, events...)
}

func newWSBridgeTestServerWithDelay(t *testing.T, rejectStatus int, delay time.Duration, events ...string) *wsBridgeTestServer {
	t.Helper()
	return newWSBridgeTestServerWithOptions(t, wsBridgeTestServerOptions{rejectStatus: rejectStatus, delay: delay}, events...)
}

func newWSBridgeTestServerWithOptions(t *testing.T, opts wsBridgeTestServerOptions, events ...string) *wsBridgeTestServer {
	t.Helper()
	srv := &wsBridgeTestServer{opts: opts}
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.dials.Add(1)
		if srv.opts.rejectStatus != 0 {
			http.Error(w, "upstream unavailable", srv.opts.rejectStatus)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket failed: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		connIndex := srv.conns.Add(1)
		for {
			var request map[string]any
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			srv.requests.Add(1)
			requestJSON := requestToJSONString(request)
			srv.lastReq.Store(requestJSON)
			srv.lastStream.Store(gjson.Get(requestJSON, "stream").Bool())
			if connIndex <= srv.opts.closeFirstConns {
				return
			}
			for i, event := range events {
				if srv.opts.delay > 0 && i == srv.opts.delayAt {
					time.Sleep(srv.opts.delay)
				}
				if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
					t.Errorf("write ws event failed: %v", err)
					return
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newWSBridgeTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 30
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 10
	return cfg
}

func newWSBridgeTestAccount(baseURL, mode string) *Account {
	return &Account{
		ID:          8101,
		Name:        "openai-ws-bridge",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 2,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": baseURL,
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": mode,
		},
	}
}

func newWSBridgeTestContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Set("User-Agent", "custom-client/1.0")
	groupID := wsBridgeTestGroupID
	c.Set("api_key", &APIKey{ID: wsBridgeTestAPIKeyID, GroupID: &groupID})
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	SetOpenAIHTTPResponseOwner(c, wsBridgeTestUserID, wsBridgeTestAPIKeyID)
	return c, rec
}

func newWSBridgeTestService(cfg *config.Config, upstream HTTPUpstream) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}
}

func newWSBridgeHTTPSSEUpstream() *httpUpstreamRecorder {
	return &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_http_1\",\"model\":\"gpt-5.1\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n",
			)),
		},
	}
}

const (
	wsBridgeTestCreatedEvent   = `{"type":"response.created","response":{"id":"resp_ws_1","model":"gpt-5.1"}}`
	wsBridgeTestItemDoneEvent  = `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi"}]}}`
	wsBridgeTestCompletedEvent = `{"type":"response.completed","response":{"id":"resp_ws_1","model":"gpt-5.1","output":[],"usage":{"input_tokens":12,"output_tokens":7}}}`
)

func TestShouldUseOpenAIWSBridgeForHTTPIngress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsDecision := OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2, Reason: "ws_v2_mode_ws_bridge"}
	streamBody := []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`)

	tests := []struct {
		name          string
		mutateCfg     func(*config.Config)
		mutateAccount func(*Account)
		path          string
		setup         func(*gin.Context)
		decision      *OpenAIWSProtocolDecision
		body          []byte
		responsesLite bool
		want          bool
		reason        string
	}{
		{name: "streaming ws_bridge request", want: true},
		{name: "ws client transport", setup: func(c *gin.Context) { SetOpenAIClientTransport(c, OpenAIClientTransportWS) }, reason: "client_transport_not_http"},
		{name: "mode router disabled", mutateCfg: func(cfg *config.Config) { cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = false }, reason: "mode_router_v2_disabled"},
		{name: "ctx_pool account", mutateAccount: func(a *Account) {
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
		}, reason: "mode_not_ws_bridge"},
		{name: "ws_bridge as ingress default", mutateCfg: func(cfg *config.Config) {
			cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeWSBridge
		}, mutateAccount: func(a *Account) {
			delete(a.Extra, "openai_apikey_responses_websockets_v2_mode")
		}, want: true},
		{name: "resolver chose http", decision: &OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportHTTPSSE}, reason: "decision_not_ws_v2"},
		{name: "non-streaming request", body: []byte(`{"model":"gpt-5.1","stream":false,"input":"hello"}`), reason: "not_stream"},
		{name: "stream omitted", body: []byte(`{"model":"gpt-5.1","input":"hello"}`), reason: "not_stream"},
		{name: "background request", body: []byte(`{"model":"gpt-5.1","stream":true,"background":true,"input":"hello"}`), reason: "background"},
		{name: "compact subpath", path: "/openai/v1/responses/compact", reason: "responses_subpath"},
		{name: "cancel subpath", path: "/openai/v1/responses/resp_1/cancel", reason: "responses_subpath"},
		{name: "native compaction v2", setup: MarkOpenAINativeCompactionV2, reason: "native_compaction_v2"},
		{name: "responses lite", responsesLite: true, want: true},
		{name: "account passthrough", mutateAccount: func(a *Account) { a.Extra["openai_passthrough"] = true }, reason: "passthrough_enabled"},
		{name: "payload above http bridge threshold", mutateCfg: func(cfg *config.Config) {
			cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
			cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 8
		}, reason: "http_bridge_threshold"},
		{name: "continuation above threshold stays on ws", mutateCfg: func(cfg *config.Config) {
			cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
			cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 8
		}, body: []byte(`{"model":"gpt-5.1","stream":true,"previous_response_id":"resp_prev","input":"hello"}`), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newWSBridgeTestConfig()
			if tt.mutateCfg != nil {
				tt.mutateCfg(cfg)
			}
			account := newWSBridgeTestAccount("https://api.openai.com", OpenAIWSIngressModeWSBridge)
			if tt.mutateAccount != nil {
				tt.mutateAccount(account)
			}
			path := tt.path
			if path == "" {
				path = "/openai/v1/responses"
			}
			c, _ := newWSBridgeTestContext(path)
			if tt.setup != nil {
				tt.setup(c)
			}
			decision := wsDecision
			if tt.decision != nil {
				decision = *tt.decision
			}
			body := tt.body
			if body == nil {
				body = streamBody
			}
			svc := newWSBridgeTestService(cfg, nil)
			got := svc.shouldUseOpenAIWSBridgeForHTTPIngress(context.Background(), c, account, decision, body, tt.responsesLite)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.reason, svc.openAIWSBridgeHTTPIngressRejectReason(context.Background(), c, account, decision, body, tt.responsesLite))
		})
	}
}

func TestOpenAIWSBridge_Forward_StreamingHTTPIngressUsesUpstreamWS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServer(t, 0, wsBridgeTestCreatedEvent, wsBridgeTestItemDoneEvent, wsBridgeTestCompletedEvent)
	cfg := newWSBridgeTestConfig()
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	body := []byte(`{"model":"gpt-5.1","stream":true,"input":[{"type":"input_text","text":"hello"}]}`)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.OpenAIWSMode)
	require.True(t, result.Stream)
	require.Equal(t, "resp_ws_1", result.ResponseID)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Nil(t, upstream.lastReq, "ws_bridge 流式 HTTP 请求不应命中 HTTP 上游")
	require.Equal(t, int32(1), wsServer.requests.Load())
	require.True(t, wsServer.lastStream.Load())

	reason, _ := c.Get("openai_ws_transport_reason")
	require.Equal(t, "ws_v2_mode_ws_bridge", reason)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	var completed string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "type").String() == "response.completed" {
			completed = data
		}
	}
	require.NotEmpty(t, completed, "客户端应收到 response.completed")
	require.Equal(t, "msg_1", gjson.Get(completed, "response.output.0.id").String(), "终态事件的空 output 应按 output_item.done 重建")

	owned, ownerErr := svc.ValidateOpenAIHTTPResponseOwner(context.Background(), wsBridgeTestGroupID, "resp_ws_1", wsBridgeTestUserID, wsBridgeTestAPIKeyID)
	require.NoError(t, ownerErr)
	require.True(t, owned, "ws_bridge 响应应绑定 HTTP 续接归属")
}

func TestOpenAIWSBridge_Forward_ResponsesLiteHTTPIngressUsesUpstreamWS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServer(t, 0, wsBridgeTestCreatedEvent, wsBridgeTestItemDoneEvent, wsBridgeTestCompletedEvent)
	cfg := newWSBridgeTestConfig()
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, _ := newWSBridgeTestContext("/openai/v1/responses")
	c.Request.Header.Set(responsesLiteHeader, "true")

	body := []byte(`{"model":"gpt-5.1","stream":true,"input":[{"type":"input_text","text":"hello"}],"client_metadata":{"keep":"me"}}`)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.OpenAIWSMode)
	require.Nil(t, upstream.lastReq, "Lite 流式请求应走上游 WS")
	require.Equal(t, int32(1), wsServer.requests.Load())

	sent, _ := wsServer.lastReq.Load().(string)
	require.True(t, isOpenAIResponsesLiteWebSocketPayload([]byte(sent)), "上游 WS payload 应带 Lite 标记: %s", sent)
	require.Equal(t, "me", gjson.Get(sent, "client_metadata.keep").String(), "已有 client_metadata 应保留")
}

func TestSetOpenAIWSResponsesLiteMetadata(t *testing.T) {
	t.Run("sets marker without mutating source metadata", func(t *testing.T) {
		source := map[string]any{"keep": "me"}
		payload := map[string]any{"model": "gpt-5.1", "client_metadata": source}
		setOpenAIWSResponsesLiteMetadata(payload, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
		require.True(t, isOpenAIResponsesLiteWebSocketPayload([]byte(requestToJSONString(payload))))
		require.NotContains(t, source, responsesLiteWSMetadataKey)
	})
	t.Run("oauth mapped gpt-5.5 skips marker", func(t *testing.T) {
		payload := map[string]any{"model": "gpt-5.5"}
		setOpenAIWSResponsesLiteMetadata(payload, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth})
		require.NotContains(t, payload, "client_metadata")
	})
}

func TestOpenAIWSBridge_Forward_CtxPoolHTTPIngressStaysHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServer(t, 0, wsBridgeTestCreatedEvent, wsBridgeTestCompletedEvent)
	cfg := newWSBridgeTestConfig()
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeCtxPool)
	c, _ := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.OpenAIWSMode)
	require.NotNil(t, upstream.lastReq)
	require.Zero(t, wsServer.requests.Load())
	reason, _ := c.Get("openai_ws_transport_reason")
	require.Equal(t, "client_protocol_http", reason)
}

func TestOpenAIWSBridge_Forward_NonStreamingHTTPIngressStaysHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServer(t, 0, wsBridgeTestCreatedEvent, wsBridgeTestCompletedEvent)
	cfg := newWSBridgeTestConfig()
	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_http_1","usage":{"input_tokens":1,"output_tokens":1}}`)),
		},
	}
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, _ := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":false,"input":"hello"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.OpenAIWSMode)
	require.NotNil(t, upstream.lastReq)
	require.Zero(t, wsServer.requests.Load())
}

func TestOpenAIWSBridge_Forward_RateLimitEventBeforeOutputFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServer(t, 0,
		wsBridgeTestCreatedEvent,
		`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached"}}`,
	)
	cfg := newWSBridgeTestConfig()
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "首输出前的 429 应交给 handler 切号: %v", err)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.False(t, c.Writer.Written(), "切号前不应写出任何响应")
	require.Empty(t, rec.Body.String())
	require.Empty(t, c.Writer.Header().Get("Content-Type"), "切号前应清除本次 attempt 的 SSE 头")
	require.Empty(t, c.Writer.Header().Get(openAICodexTurnStateHeader))
	require.Nil(t, upstream.lastReq)
}

func TestOpenAIWSBridge_Forward_DialFailureFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServer(t, http.StatusServiceUnavailable)
	cfg := newWSBridgeTestConfig()
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "WS 拨号失败应交给 handler 切号: %v", err)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.Equal(t, int32(1), wsServer.dials.Load(), "上游 5xx 应立即切号，不在同账号重试")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
	require.Nil(t, upstream.lastReq, "ws_bridge 不回退 HTTP 上游，由 handler 换号")
}

func TestOpenAIWSBridge_Forward_PreviousResponseNotFoundIsNotSilentlyDropped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServer(t, 0,
		`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"Previous response with id 'resp_prev' not found."}}`,
	)
	cfg := newWSBridgeTestConfig()
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"previous_response_id":"resp_prev","input":"hello"}`))
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "续链不存在是请求级错误，不应换号")
	require.Equal(t, int32(1), wsServer.requests.Load(), "HTTP 客户端不应静默去掉 previous_response_id 重放")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	require.True(t, gjson.Valid(rec.Body.String()), "响应体应是单个 JSON，不能拼接 SSE: %s", rec.Body.String())
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.True(t, IsResponseCommitted(c), "已写出 JSON 必须标记已提交，handler 才不会追加 response.failed")
	require.Nil(t, upstream.lastReq)
}

func TestOpenAIWSBridge_Forward_KeepaliveBeforeOutputDoesNotBlockFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServerWithDelay(t, 0, 1500*time.Millisecond,
		`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached"}}`,
	)
	cfg := newWSBridgeTestConfig()
	cfg.Gateway.StreamKeepaliveInterval = 1
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "保活后、首输出前的 429 仍应切号: %v", err)
	require.True(t, strings.HasPrefix(rec.Body.String(), ":\n\n"), "首输出前应写出 SSE 保活注释")
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c), "保活字节不算语义输出，handler 仍可切号")
}

const wsBridgeTestContextWindowErrorEvent = `{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`

func TestOpenAIWSBridge_Forward_ClientErrorEventBeforeOutputReturnsJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServer(t, 0, wsBridgeTestCreatedEvent, wsBridgeTestContextWindowErrorEvent)
	cfg := newWSBridgeTestConfig()
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	require.Nil(t, result)
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "上下文超限是请求级错误，不应换号")
	require.True(t, strings.HasPrefix(err.Error(), "upstream response failed:"), err.Error())
	require.Equal(t, http.StatusBadRequest, rec.Code, "应与 HTTP 上游一致返回 400")
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	body := rec.Body.String()
	require.True(t, gjson.Valid(body), "响应体应是单个 JSON: %s", body)
	require.Equal(t, "invalid_request_error", gjson.Get(body, "error.type").String())
	require.Equal(t, "context_length_exceeded", gjson.Get(body, "error.code").String())
	require.True(t, IsResponseCommitted(c), "必须标记已提交，handler 才不会追加通用 response.failed")
	require.Equal(t, int32(1), wsServer.requests.Load())
	require.Nil(t, upstream.lastReq)
}

// wsBridgeTestDataFrames 返回 SSE body 中所有 data 帧的 JSON。
func wsBridgeTestDataFrames(body string) []string {
	var frames []string
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			frames = append(frames, data)
		}
	}
	return frames
}

func TestOpenAIWSBridge_Forward_ClientErrorEventAfterKeepaliveKeepsSingleTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServerWithDelay(t, 0, 1500*time.Millisecond, wsBridgeTestContextWindowErrorEvent)
	cfg := newWSBridgeTestConfig()
	cfg.Gateway.StreamKeepaliveInterval = 1
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	require.Nil(t, result)
	require.Error(t, err)
	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, ":\n\n"), body)
	frames := wsBridgeTestDataFrames(body)
	require.Len(t, frames, 2, "API Key 账号：上游 error 帧 + 一个终止事件: %s", body)
	require.Equal(t, "error", gjson.Get(frames[0], "type").String())
	require.Equal(t, "response.failed", gjson.Get(frames[1], "type").String())
	require.Equal(t, "context_length_exceeded", gjson.Get(frames[1], "response.error.code").String())
	require.True(t, IsResponseCommitted(c))
}

func TestOpenAIWSBridge_Forward_ErrorEventAfterOutputIsCommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServer(t, 0,
		wsBridgeTestCreatedEvent,
		`{"type":"response.output_text.delta","delta":"hi"}`,
		`{"type":"error","error":{"type":"invalid_request_error","code":"bad_request","message":"stream aborted"}}`,
	)
	cfg := newWSBridgeTestConfig()
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	require.Nil(t, result)
	require.Error(t, err)
	body := rec.Body.String()
	frames := wsBridgeTestDataFrames(body)
	require.GreaterOrEqual(t, len(frames), 3, body)
	require.Contains(t, body, `"delta":"hi"`)
	require.Equal(t, "error", gjson.Get(frames[len(frames)-2], "type").String(), "终止事件前应先转发上游 error: %s", body)
	last := frames[len(frames)-1]
	require.Equal(t, "response.failed", gjson.Get(last, "type").String(), "最后一帧应是终止事件: %s", body)
	require.Equal(t, "resp_ws_1", gjson.Get(last, "response.id").String())
	require.Equal(t, "bad_request", gjson.Get(last, "response.error.code").String())
	require.Equal(t, 1, strings.Count(body, `"response.failed"`))
	require.True(t, IsResponseCommitted(c))
}

func TestOpenAIWSBridgeCommittedErrorFrames(t *testing.T) {
	const overloaded = `{"type":"error","error":{"type":"server_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	t.Run("oauth capacity shed is rewritten to retryable code", func(t *testing.T) {
		frames := openAIWSBridgeCommittedErrorFrames(oauth, "resp_1", "gpt-5.1", []byte(overloaded), "overloaded", nil, true)
		require.Len(t, frames, 1, "OAuth 账号只发 Codex 认可的终止事件")
		require.Equal(t, "response.failed", gjson.GetBytes(frames[0], "type").String())
		require.Equal(t, openAICapacityShedRetryableClientCode, gjson.GetBytes(frames[0], "response.error.code").String())
		require.NotContains(t, string(frames[0]), "server_is_overloaded")
	})

	t.Run("oauth keeps real request error code", func(t *testing.T) {
		frames := openAIWSBridgeCommittedErrorFrames(oauth, "resp_1", "gpt-5.1", []byte(wsBridgeTestContextWindowErrorEvent), "context", nil, true)
		require.Len(t, frames, 1)
		require.Equal(t, "resp_1", gjson.GetBytes(frames[0], "response.id").String())
		require.Equal(t, "failed", gjson.GetBytes(frames[0], "response.status").String())
		require.Equal(t, "context_length_exceeded", gjson.GetBytes(frames[0], "response.error.code").String())
		require.Equal(t, "invalid_request_error", gjson.GetBytes(frames[0], "response.error.type").String())
	})

	t.Run("api key forwards error frame then terminal", func(t *testing.T) {
		frames := openAIWSBridgeCommittedErrorFrames(apiKey, "resp_1", "gpt-5.1", []byte(overloaded), "overloaded", newOpenAIWSBridgeStreamOutput(), true)
		require.Len(t, frames, 2)
		require.Equal(t, "error", gjson.GetBytes(frames[0], "type").String())
		require.Equal(t, openAICapacityShedRetryableClientCode, gjson.GetBytes(frames[0], "error.code").String())
		require.Equal(t, "response.failed", gjson.GetBytes(frames[1], "type").String())
		require.Equal(t, openAICapacityShedRetryableClientCode, gjson.GetBytes(frames[1], "response.error.code").String())
	})
}

func TestOpenAIWSBridgeClientErrorPayloadRedactsAgentIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key, privateKey := newTestAgentIdentityKey(t)
	account := &Account{
		ID:       26,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":         OpenAIAuthModeAgentIdentity,
			"agent_runtime_id":  key.runtimeID,
			"agent_private_key": privateKey,
			"task_id":           key.taskID,
			"access_token":      key.runtimeID + "-oauth-value",
		},
	}
	secret := account.GetCredential("access_token")
	payload := []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"bad_request","message":"bad token ` + secret + `"}}`)
	svc := &OpenAIGatewayService{}

	clientPayload, clientMsg := svc.openAIWSBridgeClientErrorPayload(context.Background(), account, payload)
	require.NotContains(t, string(clientPayload), secret)
	require.NotContains(t, clientMsg, secret)

	c, rec := newWSBridgeTestContext("/openai/v1/responses")
	svc.writeOpenAIWSBridgeUncommittedErrorEvent(c, account, clientPayload, clientMsg, http.StatusBadRequest)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.NotContains(t, rec.Body.String(), secret)
	require.Contains(t, rec.Body.String(), "[redacted]")

	for _, frame := range openAIWSBridgeCommittedErrorFrames(account, "resp_1", "gpt-5.1", clientPayload, clientMsg, nil, true) {
		require.NotContains(t, string(frame), secret)
	}
}

func TestOpenAIWSBridge_Forward_FallbackErrorAfterKeepaliveWritesResponseFailed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServerWithDelay(t, 0, 1500*time.Millisecond,
		`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"Previous response with id 'resp_prev' not found."}}`,
	)
	cfg := newWSBridgeTestConfig()
	cfg.Gateway.StreamKeepaliveInterval = 1
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"previous_response_id":"resp_prev","input":"hello"}`))
	require.Nil(t, result)
	require.Error(t, err)
	body := rec.Body.String()
	require.True(t, strings.HasPrefix(body, ":\n\n"), body)
	require.Equal(t, 1, strings.Count(body, "event: response.failed"), body)
	var failedData string
	for _, line := range strings.Split(body, "\n") {
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			failedData = data
		}
	}
	require.Equal(t, "previous_response_not_found", gjson.Get(failedData, "response.error.code").String(), body)
	require.True(t, IsResponseCommitted(c))
	require.Equal(t, int32(1), wsServer.requests.Load())
}

func TestOpenAIWSBridge_Forward_RetriesClosedConnectionOnceOnSameAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServerWithOptions(t, wsBridgeTestServerOptions{closeFirstConns: 1},
		wsBridgeTestCreatedEvent, wsBridgeTestCompletedEvent)
	cfg := newWSBridgeTestConfig()
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, _ := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.OpenAIWSMode)
	require.Equal(t, int32(2), wsServer.requests.Load(), "连接被关闭后应换新连接在同账号重试一次")
}

func TestOpenAIWSBridge_Forward_RepeatedConnectionFailureFailsOverAfterOneRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServerWithOptions(t, wsBridgeTestServerOptions{closeFirstConns: 100},
		wsBridgeTestCreatedEvent, wsBridgeTestCompletedEvent)
	cfg := newWSBridgeTestConfig()
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "%v", err)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, int32(2), wsServer.requests.Load(), "同账号最多重试一次就切号")
	require.Empty(t, rec.Body.String())
}

func TestOpenAIWSBridge_Forward_FirstOutputTimeoutDoesNotAffectTimelyOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsServer := newWSBridgeTestServer(t, 0,
		wsBridgeTestCreatedEvent,
		`{"type":"response.output_text.delta","delta":"hi"}`,
		wsBridgeTestCompletedEvent,
	)
	cfg := newWSBridgeTestConfig()
	cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 1
	upstream := newWSBridgeHTTPSSEUpstream()
	svc := newWSBridgeTestService(cfg, upstream)
	account := newWSBridgeTestAccount(wsServer.URL, OpenAIWSIngressModeWSBridge)
	c, rec := newWSBridgeTestContext("/openai/v1/responses")

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":true,"input":"hello"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.OpenAIWSMode)
	require.Contains(t, rec.Body.String(), `"delta":"hi"`)
}
