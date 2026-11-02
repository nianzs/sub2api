//go:build unit

package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

// 流式联网搜索：最后一轮的结束事件要交给客户端，最终用量要和开头的缓存拆分一致。
// 之前 message_delta / message_stop 被一起过滤，网关判为 "missing terminal event"，请求必失败。
func TestKiroWebSearchStreamEndsWithMessageStopAndConsistentUsage(t *testing.T) {
	account := newKiroErrorReportingTestAccount()
	// 预置工具描述缓存，跳过 tools/list 预取，响应队列只需 MCP 搜索 + 上游生成两步。
	endpoint := kiropkg.BuildMcpEndpoint(kiroAPIRegion(account))
	kiroWebSearchDescCache.Store(endpoint, "Search the web.")
	t.Cleanup(func() { kiroWebSearchDescCache.Delete(endpoint) })

	var upstreamStream bytes.Buffer
	upstreamStream.Write(kiroChatTestEventStreamFrame(t, "assistantResponseEvent", map[string]any{
		"assistantResponseEvent": map[string]any{"content": "Go 1.26 was released in February."},
	}))
	// claude-sonnet-4.5 窗口 200k，1% → 上下文总量 2000。
	upstreamStream.Write(kiroChatTestEventStreamFrame(t, "contextUsageEvent", map[string]any{
		"contextUsageEvent": map[string]any{"contextUsagePercentage": 1.0},
	}))
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":"web_search_1","result":{"content":[]}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}},
			Body:       io.NopCloser(bytes.NewReader(upstreamStream.Bytes())),
		},
	}}
	svc := newKiroErrorReportingTestService(upstream)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"stream":true,` +
		`"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":8}],` +
		`"messages":[{"role":"user","content":"Perform a web search for the query: go release"}]}`)
	plan := &kiroCacheEmulationPlan{usage: &kiroCacheEmulationUsage{
		InputTokens: 20, CacheReadInputTokens: 70, CacheCreationInputTokens: 10,
	}}

	var out bytes.Buffer
	err := svc.streamKiroWebSearchAsAnthropic(context.Background(), account, body,
		"claude-sonnet-4-5", "claude-sonnet-4-5", "ksk_test", 100, http.Header{}, &out, plan)
	require.NoError(t, err)
	require.Len(t, upstream.requests, 2, "expected MCP search + one generation call")

	var types []string
	usage := &ClaudeUsage{}
	scanner := bufio.NewScanner(&out)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
		eventType, _ := event["type"].(string)
		types = append(types, eventType)
		mergeSSEUsagePatch(usage, svc.extractSSEUsagePatch(event))
	}
	require.NoError(t, scanner.Err())

	require.Equal(t, "message_start", types[0])
	require.Equal(t, "message_stop", types[len(types)-1], out.String())
	require.Equal(t, 1, countString(types, "message_start"))
	require.Equal(t, 1, countString(types, "message_delta"))
	require.Equal(t, 1, countString(types, "message_stop"))

	// 网关按 message_start + message_delta 合并后的计费用量：input + 缓存 = 上下文总量 − 本轮输出，
	// 缓存不能沿用 message_start 的原始数再叠加到全量 input 上。
	require.Positive(t, usage.OutputTokens)
	require.Positive(t, usage.CacheReadInputTokens)
	require.Equal(t, 2000-usage.OutputTokens,
		usage.InputTokens+usage.CacheReadInputTokens+usage.CacheCreationInputTokens)
}

func countString(items []string, want string) int {
	n := 0
	for _, item := range items {
		if item == want {
			n++
		}
	}
	return n
}
