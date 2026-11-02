package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// anthropicStopSSE 构造一条以指定 stop_reason 结束的上游 Anthropic 流。
func anthropicStopSSE(text, stopReason string, outputTokens int) string {
	block, _ := json.Marshal(text)
	return strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_cmp","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","usage":{"input_tokens":1000,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":` + string(block) + `}}`,
		``,
		`event: message_delta`,
		fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":%d}}`, stopReason, outputTokens),
		``,
	}, "\n")
}

func toolResultBody(t *testing.T, outputs ...any) []byte {
	t.Helper()
	var messages []any
	for i, output := range outputs {
		id := fmt.Sprintf("call_%d", i)
		messages = append(messages,
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": id, "name": "exec", "input": map[string]any{}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": id, "content": output},
			}},
		)
	}
	body, err := json.Marshal(map[string]any{"model": "claude-sonnet-4.5", "messages": messages})
	require.NoError(t, err)
	return body
}

func TestBoundAnthropicToolResultsUnderBudgetUnchanged(t *testing.T) {
	body := toolResultBody(t, strings.Repeat("a", 10_000), strings.Repeat("b", 10_000))
	out, stats := boundAnthropicToolResultsForCompactionRetry(body)
	require.Equal(t, body, out)
	require.Zero(t, stats.Items)
	require.Equal(t, 20_000, stats.OriginalBytes)
}

// 超出部分从最旧的开始削，每条至少留 1KB；最新的结果在预算允许时保持完整。
func TestBoundAnthropicToolResultsTrimsOldestFirst(t *testing.T) {
	body := toolResultBody(t, strings.Repeat("a", 20_000), strings.Repeat("b", 20_000), strings.Repeat("c", 20_000))
	out, stats := boundAnthropicToolResultsForCompactionRetry(body)

	require.Equal(t, 2, stats.Items)
	require.Equal(t, 60_000, stats.OriginalBytes)
	require.Equal(t, kiroCompactionToolOutputBudgetBytes, stats.RetainedBytes)
	first := gjson.GetBytes(out, "messages.1.content.0.content").String()
	second := gjson.GetBytes(out, "messages.3.content.0.content").String()
	third := gjson.GetBytes(out, "messages.5.content.0.content").String()
	require.Len(t, first, kiroCompactionToolOutputMinBytes)
	require.Len(t, second, kiroCompactionToolOutputBudgetBytes-kiroCompactionToolOutputMinBytes-20_000)
	require.Equal(t, strings.Repeat("c", 20_000), third)
	require.Contains(t, second, "[... tool output truncated for compaction retry; original size: 20000 bytes ...]")
	for i := 0; i < 3; i++ {
		require.Equal(t, fmt.Sprintf("call_%d", i), gjson.GetBytes(out, fmt.Sprintf("messages.%d.content.0.tool_use_id", i*2+1)).String())
	}
}

func TestBoundAnthropicToolResultsHandlesTextBlocksAndKeepsImages(t *testing.T) {
	blocks := []any{
		map[string]any{"type": "text", "text": strings.Repeat("x", 40_000)},
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "IMG"}},
	}
	out, stats := boundAnthropicToolResultsForCompactionRetry(toolResultBody(t, blocks))

	require.Equal(t, 1, stats.Items)
	require.Len(t, gjson.GetBytes(out, "messages.1.content.0.content.0.text").String(), kiroCompactionToolOutputBudgetBytes)
	require.Equal(t, "IMG", gjson.GetBytes(out, "messages.1.content.0.content.1.source.data").String())
}

func TestTruncateMiddleForKiroCompactionKeepsUTF8(t *testing.T) {
	text := strings.Repeat("中文", 5_000)
	got := truncateMiddleForKiroCompaction(text, 2_000)
	require.LessOrEqual(t, len(got), 2_000)
	require.True(t, utf8.ValidString(got))
	require.True(t, strings.HasPrefix(got, "中"))
	require.True(t, strings.HasSuffix(got, "文"))

	require.Equal(t, "short", truncateMiddleForKiroCompaction("short", 100))
	require.Len(t, truncateMiddleForKiroCompaction(strings.Repeat("z", 500), 10), 10)
}

func runCompactionHandler(t *testing.T, sse string, stream bool, retry func() (*http.Response, error)) (*httptest.ResponseRecorder, *ForwardResult, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	svc := &GatewayService{}
	result, err := svc.handleResponsesCompactionResponseWithRetry(
		newCompactionUpstream(sse), c, "gpt-5.6-sol", "claude-sonnet-4.5", nil, time.Now(), stream, retry,
	)
	return rec, result, err
}

// 摘要被 max_tokens 截断时不能当成功：返回 incomplete，没有 compaction 项。
func TestCompactionMaxTokensReportsIncomplete(t *testing.T) {
	rec, result, err := runCompactionHandler(t, anthropicStopSSE("partial summary", "max_tokens", 32000), true, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	out := rec.Body.String()
	require.Contains(t, out, "event: response.incomplete")
	require.Contains(t, out, `"reason":"max_output_tokens"`)
	require.NotContains(t, out, `"type":"compaction"`)

	rec, _, err = runCompactionHandler(t, anthropicStopSSE("partial summary", "max_tokens", 32000), false, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "incomplete", gjson.Get(rec.Body.String(), "status").String())
	require.Empty(t, gjson.Get(rec.Body.String(), "output").Array())
}

func TestCompactionContextWindowExceededWithoutRetryFails(t *testing.T) {
	rec, _, err := runCompactionHandler(t, anthropicStopSSE("partial", compactionStopContextWindowExceeded, 10), true, nil)
	require.Error(t, err)
	out := rec.Body.String()
	require.Contains(t, out, "response.failed")
	require.Contains(t, out, `"code":"context_length_exceeded"`)
	require.NotContains(t, out, `"type":"compaction"`)
}

func TestCompactionRefusalFailsWithContentFilter(t *testing.T) {
	rec, _, err := runCompactionHandler(t, anthropicStopSSE("", "refusal", 0), false, nil)
	require.Error(t, err)
	require.Equal(t, "content_filter", gjson.Get(rec.Body.String(), "error.code").String())
}

// 上下文已满时调用一次重试，成功后返回 compaction 项，用量为两次之和。
func TestCompactionContextWindowExceededRetriesOnce(t *testing.T) {
	calls := 0
	retry := func() (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(anthropicStopSSE("<summary>ok</summary>", "end_turn", 1200)))}, nil
	}
	rec, result, err := runCompactionHandler(t, anthropicStopSSE("partial", compactionStopContextWindowExceeded, 10), true, retry)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Contains(t, rec.Body.String(), `"type":"compaction"`)
	require.Equal(t, 1210, result.Usage.OutputTokens)
	require.Equal(t, 2000, result.Usage.InputTokens)
}

func TestCompactionRetryStillOverflowingFails(t *testing.T) {
	retry := func() (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(
			`{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`))}, nil
	}
	rec, _, err := runCompactionHandler(t, anthropicStopSSE("partial", compactionStopContextWindowExceeded, 10), false, retry)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "context_length_exceeded", gjson.Get(rec.Body.String(), "error.code").String())
	require.Equal(t, kiroCompactionOverflowMessage, gjson.Get(rec.Body.String(), "error.message").String())
}

func TestCompactionRetryUnavailableFailsAsContextLength(t *testing.T) {
	retry := func() (*http.Response, error) { return nil, errKiroCompactionNoRetry }
	rec, _, err := runCompactionHandler(t, anthropicStopSSE("partial", compactionStopContextWindowExceeded, 10), false, retry)
	require.Error(t, err)
	require.Equal(t, "context_length_exceeded", gjson.Get(rec.Body.String(), "error.code").String())
}

func TestRetryKiroCompactionOnContextLength(t *testing.T) {
	badRequest := func(body string) *http.Response {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(body))}
	}
	ok := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("retried"))}

	calls := 0
	retry := func() (*http.Response, error) { calls++; return ok, nil }
	got, err := retryKiroCompactionOnContextLength(badRequest(`{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`), retry)
	require.NoError(t, err)
	require.Same(t, ok, got)
	require.Equal(t, 1, calls)

	// 其它 400 不重试，body 仍可读。
	other := badRequest(`{"message":"bad request"}`)
	got, err = retryKiroCompactionOnContextLength(other, retry)
	require.NoError(t, err)
	require.Same(t, other, got)
	require.Equal(t, 1, calls)
	raw, _ := io.ReadAll(got.Body)
	require.Equal(t, `{"message":"bad request"}`, string(raw))

	// 无法重试时原样返回。
	tooLong := badRequest(`{"message":"Input is too long."}`)
	got, err = retryKiroCompactionOnContextLength(tooLong, func() (*http.Response, error) { return nil, errKiroCompactionNoRetry })
	require.NoError(t, err)
	require.Same(t, tooLong, got)
	raw, _ = io.ReadAll(got.Body)
	require.Equal(t, `{"message":"Input is too long."}`, string(raw))
}

func TestNewKiroCompactionRetryRunsAtMostOnce(t *testing.T) {
	sends := 0
	send := func(body []byte) (*http.Response, error) {
		sends++
		require.Contains(t, string(body), "tool output truncated for compaction retry")
		return &http.Response{StatusCode: http.StatusOK}, nil
	}
	retry := newKiroCompactionRetry(toolResultBody(t, strings.Repeat("a", 30_000)), "m", send)
	_, err := retry()
	require.NoError(t, err)
	_, err = retry()
	require.ErrorIs(t, err, errKiroCompactionNoRetry)
	require.Equal(t, 1, sends)

	// 没有可截的工具输出：不重发。
	retry = newKiroCompactionRetry(toolResultBody(t, "small"), "m", send)
	_, err = retry()
	require.ErrorIs(t, err, errKiroCompactionNoRetry)
	require.Equal(t, 1, sends)
}
