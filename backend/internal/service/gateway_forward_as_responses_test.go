//go:build unit

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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardAsResponsesKiroDirectUsesResponsesCacheProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetKiroCacheTracker()

	account := &Account{
		ID:          301,
		Name:        "kiro-responses-cache",
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "kiro-access-token",
			"profile_arn":  "arn:aws:codewhisperer:us-east-1:123456789012:profile/RESPONSECACHE",
		},
	}
	group := kiroCacheGroup(1)
	body := kiroResponsesCacheRequestBody("gateway", "workspace-gateway", "resp-gateway")
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), "responses")
	require.NoError(t, err)
	parsed.Group = group

	upstream := &queuedHTTPUpstream{responses: []*http.Response{
		kiroResponsesCacheUpstreamResponse(t, 5),
		kiroResponsesCacheUpstreamResponse(t, 7),
	}}
	svc := &GatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout: 0,
			MaxLineSize:               defaultMaxLineSize,
		}},
		httpUpstream:        upstream,
		kiroCooldownStore:   &stubKiroCooldownStore{},
		tlsFPProfileService: &TLSFingerprintProfileService{},
		rateLimitService:    &RateLimitService{},
	}

	firstCtx, firstRec := newResponsesGatewayTestContext()
	firstResult, err := svc.ForwardAsResponses(firstCtx.Request.Context(), firstCtx, account, body, parsed)
	require.NoError(t, err)
	require.Equal(t, 0, firstResult.Usage.CacheReadInputTokens)
	require.Greater(t, firstResult.Usage.CacheCreationInputTokens, 0)
	require.Equal(t, firstResult.Usage.CacheCreationInputTokens, int(gjson.Get(firstRec.Body.String(), "usage.cache_creation_input_tokens").Int()))
	require.False(t, gjson.Get(firstRec.Body.String(), "usage.input_tokens_details.cached_tokens").Exists())

	secondCtx, secondRec := newResponsesGatewayTestContext()
	secondResult, err := svc.ForwardAsResponses(secondCtx.Request.Context(), secondCtx, account, body, parsed)
	require.NoError(t, err)
	require.Greater(t, secondResult.Usage.CacheReadInputTokens, 0)
	require.Equal(t, 0, secondResult.Usage.CacheCreationInputTokens)
	require.Equal(t, secondResult.Usage.CacheReadInputTokens, int(gjson.Get(secondRec.Body.String(), "usage.input_tokens_details.cached_tokens").Int()))
	require.Equal(t, 0, int(gjson.Get(secondRec.Body.String(), "usage.cache_creation_input_tokens").Int()))
	require.Len(t, upstream.requests, 2)
}

func newResponsesGatewayTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, rec
}

func kiroResponsesCacheUpstreamResponse(t *testing.T, outputTokens int) *http.Response {
	t.Helper()
	var upstreamBody bytes.Buffer
	_, _ = upstreamBody.Write(buildKiroEventStreamFrame(t, "assistantResponseEvent", map[string]any{
		"assistantResponseEvent": map[string]any{"content": "hello"},
	}))
	_, _ = upstreamBody.Write(buildKiroEventStreamFrame(t, "messageMetadataEvent", map[string]any{
		"messageMetadataEvent": map[string]any{
			"tokenUsage": map[string]any{
				"uncachedInputTokens": 99,
				"outputTokens":        outputTokens,
			},
		},
	}))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
		Body:       io.NopCloser(&upstreamBody),
	}
}

func TestAdaptResponsesClientToolsForAnthropic_FlattensNamespace(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"claude-fable-5",
		"input":[{"type":"function_call","call_id":"call_1","namespace":"codex_app","name":"read_thread","arguments":"{}"}],
		"tools":[{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"read_thread","description":"Read a task","parameters":{"type":"object","properties":{}}}]}]
	}`)

	adapted, mapping, err := adaptResponsesClientToolsForAnthropic(body)
	require.NoError(t, err)
	require.Equal(t, apicompat.ResponsesNamespaceName{Namespace: "codex_app", Name: "read_thread"}, mapping.NamespaceTools["codex_app__read_thread"])

	var request map[string]any
	require.NoError(t, json.Unmarshal(adapted, &request))
	tools := request["tools"].([]any)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	require.Equal(t, "function", tool["type"])
	require.Equal(t, "codex_app__read_thread", tool["name"])

	input := request["input"].([]any)
	call := input[0].(map[string]any)
	require.Equal(t, "codex_app__read_thread", call["name"])
	require.NotContains(t, call, "namespace")
}

func TestAdaptResponsesClientToolsForAnthropic_LiftsAdditionalTools(t *testing.T) {
	body := []byte(`{
		"model":"claude-fable-5",
		"input":[
			{"type":"additional_tools","tools":[
				{"type":"custom","name":"exec","description":"Run a command"},
				{"type":"namespace","name":"codex_app","tools":[
					{"type":"function","name":"read_thread","parameters":{"type":"object"}}
				]}
			]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]}
		]
	}`)

	adapted, mapping, err := adaptResponsesClientToolsForAnthropic(body)
	require.NoError(t, err)
	require.True(t, mapping.CustomTools["exec"])
	require.Equal(t, apicompat.ResponsesNamespaceName{Namespace: "codex_app", Name: "read_thread"}, mapping.NamespaceTools["codex_app__read_thread"])

	var request map[string]any
	require.NoError(t, json.Unmarshal(adapted, &request))
	tools := request["tools"].([]any)
	require.Len(t, tools, 2)
	require.Equal(t, "function", tools[0].(map[string]any)["type"])
	require.Equal(t, "codex_app__read_thread", tools[1].(map[string]any)["name"])

	input := request["input"].([]any)
	require.Len(t, input, 1)
	require.Equal(t, "message", input[0].(map[string]any)["type"])
}

func TestAdaptResponsesClientToolsForAnthropic_NormalizesBareNamespaceHistoryForKiro(t *testing.T) {
	requestBody := map[string]any{
		"model": "gpt-5.6-sol",
		"input": []any{
			map[string]any{
				"type": "additional_tools", "role": "developer",
				"tools": []any{map[string]any{
					"type": "namespace", "name": "functions",
					"tools": []any{map[string]any{"type": "custom", "name": "exec", "description": "Run JavaScript"}},
				}},
			},
			map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "custom_1", "input": "run()"},
			map[string]any{"type": "custom_tool_call_output", "call_id": "custom_1", "output": "ok"},
			map[string]any{"type": "function_call", "name": "exec", "call_id": "legacy_1", "arguments": "{\"input\":\"legacy()\"}"},
			map[string]any{"type": "function_call_output", "call_id": "legacy_1", "output": "legacy ok"},
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "continue"}}},
		},
	}
	body, err := json.Marshal(requestBody)
	require.NoError(t, err)

	adapted, mapping, err := adaptResponsesClientToolsForAnthropic(body)
	require.NoError(t, err)
	require.True(t, mapping.CustomTools["functions__exec"])
	require.Equal(t, apicompat.ResponsesNamespaceName{Namespace: "functions", Name: "exec"}, mapping.NamespaceTools["functions__exec"])

	var request apicompat.ResponsesRequest
	require.NoError(t, json.Unmarshal(adapted, &request))
	require.Len(t, request.Tools, 1)
	require.Equal(t, "function", request.Tools[0].Type)
	require.Equal(t, "functions__exec", request.Tools[0].Name)

	anthropicRequest, err := apicompat.ResponsesToAnthropicRequest(&request)
	require.NoError(t, err)
	anthropicBody, err := json.Marshal(anthropicRequest)
	require.NoError(t, err)
	kiroBuildResult, err := kiro.BuildKiroPayloadWithContext(anthropicBody, "gpt-5.6-sol", "", "AI_EDITOR", nil)
	require.NoError(t, err)

	tools := gjson.GetBytes(kiroBuildResult.Payload, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools").Array()
	require.Len(t, tools, 1, "裸 exec 历史不得触发额外占位工具")
	require.Equal(t, "functions__exec", tools[0].Get("toolSpecification.name").String())

	history := gjson.GetBytes(kiroBuildResult.Payload, "conversationState.history").Array()
	var toolUseNames []string
	for _, message := range history {
		for _, toolUse := range message.Get("assistantResponseMessage.toolUses").Array() {
			toolUseNames = append(toolUseNames, toolUse.Get("name").String())
		}
	}
	require.Equal(t, []string{"functions__exec", "functions__exec"}, toolUseNames)
}

func TestAdaptResponsesClientToolsForAnthropic_FlattensRootUnionSchema(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"claude-opus-5",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"create an automation"}]}],
		"tools":[{"type":"namespace","name":"codex_app","tools":[{
			"type":"function",
			"name":"automation_update",
			"description":"Create, update, view, or delete recurring automations",
			"parameters":{"oneOf":[
				{"type":"object","properties":{"mode":{"enum":["view"]},"id":{"type":"string"}},"required":["mode","id"]},
				{"type":"object","properties":{"mode":{"enum":["update"]},"id":{"type":"string"}},"required":["mode","id"]}
			]}
		}]}]
	}`)

	adapted, _, err := adaptResponsesClientToolsForAnthropic(body)
	require.NoError(t, err)

	var responsesReq apicompat.ResponsesRequest
	require.NoError(t, json.Unmarshal(adapted, &responsesReq))

	claudeReq, err := apicompat.ResponsesToAnthropicRequest(&responsesReq)
	require.NoError(t, err)
	require.Len(t, claudeReq.Tools, 1)

	tool := claudeReq.Tools[0]
	require.Equal(t, "codex_app__automation_update", tool.Name)

	var schema map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))
	require.JSONEq(t, `"object"`, string(schema["type"]))
	require.NotContains(t, schema, "oneOf")
	require.NotContains(t, schema, "anyOf")
	require.NotContains(t, schema, "allOf")
	require.JSONEq(t, `["mode","id"]`, string(schema["required"]))

	wire, err := json.Marshal(tool)
	require.NoError(t, err)
	require.NotContains(t, string(wire), `"input_schema":{"oneOf"`)
	require.NotContains(t, string(wire), `"input_schema":{"anyOf"`)
	require.NotContains(t, string(wire), `"input_schema":{"allOf"`)
}

func namespaceToolAnthropicStream() string {
	return strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_namespace","type":"message","role":"assistant","content":[],"model":"claude-fable-5","stop_reason":"","usage":{"input_tokens":10}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_namespace","name":"codex_app__read_thread","input":{"thread_id":"123"}}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
}

func namespaceToolMapping() apicompat.ResponsesClientToolMapping {
	return apicompat.ResponsesClientToolMapping{NamespaceTools: map[string]apicompat.ResponsesNamespaceName{
		"codex_app__read_thread": {Namespace: "codex_app", Name: "read_thread"},
	}}
}

func TestHandleResponsesBufferedStreamingResponse_RestoresNamespaceTool(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}

	svc := &GatewayService{}
	_, err := svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-fable-5", "claude-fable-5", nil, time.Now(), namespaceToolMapping())
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `"type":"function_call"`)
	require.Contains(t, rec.Body.String(), `"name":"read_thread"`)
	require.Contains(t, rec.Body.String(), `"namespace":"codex_app"`)
	require.NotContains(t, rec.Body.String(), `"name":"codex_app__read_thread"`)
}

func TestHandleResponsesBufferedStreamingResponse_ToolArgumentsAreValidJSON(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(toolAnthropicSSEStream()))}

	_, err := (&GatewayService{}).handleResponsesBufferedStreamingResponse(resp, c, "claude-fable-5", "claude-fable-5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)

	var body struct {
		Output []struct {
			Type      string `json:"type"`
			Arguments string `json:"arguments"`
		} `json:"output"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Output, 1)
	require.Equal(t, "function_call", body.Output[0].Type)
	require.JSONEq(t, `{"query":"status"}`, body.Output[0].Arguments)
}

func TestAppendRawJSON_EmptyObjectPlaceholder(t *testing.T) {
	t.Parallel()

	fragment := `{"query":"status"}`
	require.JSONEq(t, fragment, string(appendRawJSON(json.RawMessage("{ \n\t }"), fragment)))
	require.Equal(t, `{"existing":true}{"query":"status"}`, string(appendRawJSON(json.RawMessage(`{"existing":true}`), fragment)))
}

func TestHandleResponsesStreamingResponse_RestoresNamespaceTool(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}

	svc := &GatewayService{}
	_, err := svc.handleResponsesStreamingResponse(resp, c, "claude-fable-5", "claude-fable-5", nil, time.Now(), namespaceToolMapping())
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `response.output_item.added`)
	require.Contains(t, rec.Body.String(), `"name":"read_thread"`)
	require.Contains(t, rec.Body.String(), `"namespace":"codex_app"`)
	require.NotContains(t, rec.Body.String(), `"name":"codex_app__read_thread"`)
}

func TestExtractResponsesReasoningEffortFromBody(t *testing.T) {
	t.Parallel()

	got := ExtractResponsesReasoningEffortFromBody([]byte(`{"model":"claude-sonnet-4.5","reasoning":{"effort":"HIGH"}}`))
	require.NotNil(t, got)
	require.Equal(t, "high", *got)

	maxGot := ExtractResponsesReasoningEffortFromBody([]byte(`{"model":"deepseek-v4-pro","reasoning":{"effort":"max"}}`))
	require.NotNil(t, maxGot)
	require.Equal(t, "max", *maxGot)

	mappedMax := ExtractResponsesReasoningEffortFromBody(
		[]byte(`{"model":"public-alias","reasoning":{"effort":"max"}}`),
		"provider/glm-5.2",
		"public-alias",
	)
	require.NotNil(t, mappedMax)
	require.Equal(t, "max", *mappedMax)

	legacyMax := ExtractResponsesReasoningEffortFromBody([]byte(`{"model":"gpt-5.5","reasoning":{"effort":"max"}}`))
	require.NotNil(t, legacyMax)
	require.Equal(t, "xhigh", *legacyMax)

	require.Nil(t, ExtractResponsesReasoningEffortFromBody([]byte(`{"model":"claude-sonnet-4.5"}`)))
}

func TestHandleResponsesBufferedStreamingResponse_PreservesMessageStartCacheUsage(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_buffered"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":12,"cache_read_input_tokens":9,"cache_creation_input_tokens":3}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 7, result.Usage.OutputTokens)
	require.Equal(t, 9, result.Usage.CacheReadInputTokens)
	require.Equal(t, 3, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `"cached_tokens":9`)
}

func TestHandleResponsesStreamingResponse_PreservesMessageStartCacheUsage(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":20,"cache_read_input_tokens":11,"cache_creation_input_tokens":4}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleResponsesStreamingResponse(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 20, result.Usage.InputTokens)
	require.Equal(t, 8, result.Usage.OutputTokens)
	require.Equal(t, 11, result.Usage.CacheReadInputTokens)
	require.Equal(t, 4, result.Usage.CacheCreationInputTokens)
	require.Contains(t, rec.Body.String(), `response.completed`)
}

func TestHandleResponsesStreamingResponse_NormalizesTerminalUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name              string
		startUsage        string
		deltaUsage        string
		wantInput         int
		wantOutput        int
		wantCached        int
		wantCacheCreation int
	}{
		{
			name:       "cache_read_input_tokens full cache",
			startUsage: `"input_tokens":173306,"prompt_tokens":173306`,
			deltaUsage: `"input_tokens":0,"output_tokens":8,"prompt_tokens":173306,"cache_read_input_tokens":173306`,
			wantOutput: 8,
			wantCached: 173306,
		},
		{
			name:       "cached_tokens full cache",
			startUsage: `"input_tokens":173306,"prompt_tokens":173306`,
			deltaUsage: `"input_tokens":0,"output_tokens":8,"prompt_tokens":173306,"cached_tokens":173306`,
			wantOutput: 8,
			wantCached: 173306,
		},
		{
			name:       "prompt_tokens_details full cache",
			startUsage: `"input_tokens":173306,"prompt_tokens":173306`,
			deltaUsage: `"input_tokens":0,"output_tokens":8,"prompt_tokens":173306,"prompt_tokens_details":{"cached_tokens":173306}`,
			wantOutput: 8,
			wantCached: 173306,
		},
		{
			name:       "DeepSeek input total with hit and miss buckets",
			startUsage: `"input_tokens":0`,
			deltaUsage: `"input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400`,
			wantInput:  400,
			wantOutput: 30,
			wantCached: 800,
		},
		{
			name:       "OpenAI prompt total with cached details",
			startUsage: `"input_tokens":0`,
			deltaUsage: `"prompt_tokens":1200,"output_tokens":30,"prompt_tokens_details":{"cached_tokens":800}`,
			wantInput:  400,
			wantOutput: 30,
			wantCached: 800,
		},
		{
			name:              "cache creation only without prompt total or miss bucket keeps independent input",
			startUsage:        `"input_tokens":1200`,
			deltaUsage:        `"input_tokens":0,"output_tokens":30,"cache_creation_input_tokens":800`,
			wantInput:         1200,
			wantOutput:        30,
			wantCacheCreation: 800,
		},
	}

	for _, tt := range tests {
		for _, terminal := range []string{"message_stop", "eof"} {
			t.Run(tt.name+"/"+terminal, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				lines := []string{
					`event: message_start`,
					`data: {"type":"message_start","message":{"id":"msg_usage","type":"message","role":"assistant","content":[],"model":"k3","stop_reason":"","usage":{` + tt.startUsage + `}}}`,
					``,
					`event: message_delta`,
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{` + tt.deltaUsage + `}}`,
					``,
				}
				if terminal == "message_stop" {
					lines = append(lines, `event: message_stop`, `data: {"type":"message_stop"}`, ``)
				}
				resp := &http.Response{Body: io.NopCloser(strings.NewReader(strings.Join(lines, "\n")))}

				result, err := (&GatewayService{}).handleResponsesStreamingResponse(resp, c, "k3", "k3", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				require.NoError(t, err)
				require.Equal(t, tt.wantInput, result.Usage.InputTokens)
				require.Equal(t, tt.wantCached, result.Usage.CacheReadInputTokens)
				require.Equal(t, tt.wantCacheCreation, result.Usage.CacheCreationInputTokens)

				var completed apicompat.ResponsesStreamEvent
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event apicompat.ResponsesStreamEvent
					require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
					if event.Type == "response.completed" {
						completed = event
					}
				}
				require.NotNil(t, completed.Response)
				require.NotNil(t, completed.Response.Usage)
				require.Equal(t, tt.wantInput+tt.wantCached+tt.wantCacheCreation, completed.Response.Usage.InputTokens)
				require.Equal(t, tt.wantOutput, completed.Response.Usage.OutputTokens)
				require.Equal(t, completed.Response.Usage.InputTokens+tt.wantOutput, completed.Response.Usage.TotalTokens)
				require.Equal(t, tt.wantCacheCreation, completed.Response.Usage.CacheCreationInputTokens)
				if tt.wantCached == 0 {
					require.Nil(t, completed.Response.Usage.InputTokensDetails)
				} else {
					require.Equal(t, tt.wantCached, completed.Response.Usage.InputTokensDetails.CachedTokens)
				}
			})
		}
	}
}

func TestParseAnthropicSSEField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		line      string
		field     string
		wantValue string
		wantOK    bool
	}{
		{
			name:      "standard format with space",
			line:      "event: message_start",
			field:     "event",
			wantValue: "message_start",
			wantOK:    true,
		},
		{
			name:      "compact format without space",
			line:      "event:message_start",
			field:     "event",
			wantValue: "message_start",
			wantOK:    true,
		},
		{
			name:      "data field with space",
			line:      "data: {\"type\":\"message_start\"}",
			field:     "data",
			wantValue: "{\"type\":\"message_start\"}",
			wantOK:    true,
		},
		{
			name:      "data field without space",
			line:      "data:{\"type\":\"message_start\"}",
			field:     "data",
			wantValue: "{\"type\":\"message_start\"}",
			wantOK:    true,
		},
		{
			name:      "field with multiple spaces after colon",
			line:      "event:  message_delta",
			field:     "event",
			wantValue: "message_delta",
			wantOK:    true,
		},
		{
			name:      "wrong field name",
			line:      "event: message_start",
			field:     "data",
			wantValue: "",
			wantOK:    false,
		},
		{
			name:      "empty line",
			line:      "",
			field:     "event",
			wantValue: "",
			wantOK:    false,
		},
		{
			name:      "line without colon",
			line:      "invalid line",
			field:     "event",
			wantValue: "",
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotValue, gotOK := parseAnthropicSSEField(tt.line, tt.field)
			require.Equal(t, tt.wantOK, gotOK, "parseAnthropicSSEField() ok")
			require.Equal(t, tt.wantValue, gotValue, "parseAnthropicSSEField() value")
		})
	}
}

func TestHandleResponsesBufferedStreamingResponse_CompactSSEFormat(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// Simulate compact SSE format without spaces after colons (e.g. Kimi API)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_compact"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_compact","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":10}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleResponsesBufferedStreamingResponse(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 10, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
}

func TestHandleResponsesStreamingResponse_CompactSSEFormat(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// Simulate compact SSE format without spaces after colons (e.g. Kimi API)
	resp := &http.Response{
		Header: http.Header{"x-request-id": []string{"rid_compact_stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event:message_start`,
			`data:{"type":"message_start","message":{"id":"msg_compact_stream","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":15}}}`,
			``,
			`event:content_block_start`,
			`data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"OK"}}`,
			``,
			`event:message_delta`,
			`data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}`,
			``,
			`event:message_stop`,
			`data:{"type":"message_stop"}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{}
	result, err := svc.handleResponsesStreamingResponse(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 15, result.Usage.InputTokens)
	require.Equal(t, 6, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), `response.completed`)
}

func TestClaude55ResponsesSignedThinkingBufferedAndStreamed(t *testing.T) {
	payload := strings.Join([]string{
		"event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":10}}}`,
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}`,
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}`,
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
		"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":1}`,
		"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		"event: message_stop\n" + `data: {"type":"message_stop"}`,
	}, "\n\n") + "\n\n"
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		modelPayload := strings.ReplaceAll(payload, "claude-opus-5-5", model)
		for _, stream := range []bool{false, true} {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(modelPayload))}
			svc := &GatewayService{}
			var err error
			if stream {
				_, err = svc.handleResponsesStreamingResponse(resp, c, "public-claude", model, nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			} else {
				_, err = svc.handleResponsesBufferedStreamingResponse(resp, c, "public-claude", model, nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			}
			require.NoError(t, err)
			require.Contains(t, rec.Body.String(), "anthropic-thinking-v1:")
			require.Contains(t, rec.Body.String(), "public-claude")
			require.Contains(t, rec.Body.String(), `"text":"ok"`)
		}
	}
}

func TestClaude55BridgeUsesMappedModelBeforeThinkingConversion(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		for _, chat := range []bool{false, true} {
			for _, forced := range []bool{false, true} {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				body := `{"model":"public-claude","input":"hello","reasoning":{"effort":"xhigh"}}`
				if chat {
					body = `{"model":"public-claude","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"xhigh"}`
				}
				if forced {
					body = body[:len(body)-1] + `,"tool_choice":"required","tools":[{"type":"function","name":"lookup","function":{"name":"lookup"}}]}`
				}
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}}
				svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture-key", "model_mapping": map[string]any{"public-claude": model}}}
				var err error
				var result *ForwardResult
				if chat {
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), nil)
				} else {
					result, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(body), nil)
				}
				if forced {
					require.Error(t, err)
					require.Equal(t, 400, rec.Code)
					require.Nil(t, upstream.lastReq)
				} else {
					require.NoError(t, err)
					require.NotNil(t, result)
					require.NotNil(t, upstream.lastReq)
					require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
					require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
					require.Equal(t, "xhigh", gjson.GetBytes(upstream.lastBody, "output_config.effort").String())
					require.False(t, gjson.GetBytes(upstream.lastBody, "thinking.budget_tokens").Exists())
				}
			}
		}
	}
}

// Kiro 直连会把客户端的 thinking.enabled/disabled 改写成自己的 adaptive 载荷，
// 所以 chat/responses 兼容入口只对 Anthropic 官方账号执行 Claude 5.5 校验。
func TestClaude55BridgeValidatesThinkingOnlyForAnthropic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	thinkingFields := map[string]string{
		"enabled":  `"thinking":{"type":"enabled","budget_tokens":16000}`,
		"disabled": `"thinking":{"type":"disabled"}`,
	}
	for _, platform := range []string{PlatformKiro, PlatformAnthropic} {
		for thinkingType, thinkingField := range thinkingFields {
			for _, chat := range []bool{false, true} {
				endpoint := "responses"
				if chat {
					endpoint = "chat"
				}
				t.Run(platform+"/"+thinkingType+"/"+endpoint, func(t *testing.T) {
					path := "/v1/responses"
					body := `{"model":"public-claude","input":"hello",` + thinkingField + `}`
					if chat {
						path = "/v1/chat/completions"
						body = `{"model":"public-claude","messages":[{"role":"user","content":"hello"}],` + thinkingField + `}`
					}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					c.Request.Header.Set("Content-Type", "application/json")

					upstream := &httpUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
						Body:       io.NopCloser(bytes.NewReader(kiroChatTestEventStream(t, "ok", 10, 2))),
					}}
					svc := &GatewayService{
						cfg: &config.Config{Gateway: config.GatewayConfig{
							MaxLineSize: defaultMaxLineSize,
						}},
						httpUpstream:        upstream,
						kiroCooldownStore:   noopKiroChatCooldownStore{},
						tlsFPProfileService: &TLSFingerprintProfileService{},
					}
					account := &Account{
						ID:          802,
						Name:        "claude55-bridge",
						Platform:    platform,
						Type:        AccountTypeAPIKey,
						Concurrency: 1,
						Credentials: map[string]any{
							"api_key":       "ksk_test",
							"model_mapping": map[string]any{"public-claude": "claude-opus-5-5"},
						},
					}

					var err error
					if chat {
						_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), &ParsedRequest{})
					} else {
						_, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(body), &ParsedRequest{})
					}

					if platform == PlatformAnthropic {
						require.Error(t, err)
						require.Equal(t, http.StatusBadRequest, rec.Code)
						require.Contains(t, rec.Body.String(), "claude-opus-5-5 requires adaptive thinking")
						require.Nil(t, upstream.lastReq)
						return
					}
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, rec.Code)
					require.Len(t, upstream.requests, 1)
					require.Equal(t, "claude-opus-5.5", gjson.GetBytes(upstream.lastBody, "conversationState.currentMessage.userInputMessage.modelId").String())
					require.Equal(t, "adaptive", gjson.GetBytes(upstream.lastBody, "additionalModelRequestFields.thinking.type").String())
					require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "additionalModelRequestFields.output_config.effort").String())
				})
			}
		}
	}
}
