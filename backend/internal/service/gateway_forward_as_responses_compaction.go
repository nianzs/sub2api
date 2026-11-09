package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"go.uber.org/zap"
)

// compactionMinMaxTokens 是压缩轮次的 max_tokens 下限。Codex 的 compact 请求不带
// max_output_tokens，转换器会落到 8192 默认值，对覆盖数十万 token 前文的结构化
// 摘要偏紧（摘要被截断等于压缩失败）。
const compactionMinMaxTokens = 32000

// compactionHeartbeatInterval 是摘要生成期间向下游发送 SSE 保活注释的最小间隔。
const compactionHeartbeatInterval = 10 * time.Second

// handleResponsesCompactionResponse 处理 Codex remote compaction v2 的响应侧：
// 把上游这一轮普通对话的输出文本包装成**恰好一个** type="compaction" 的 output
// item 回给客户端。
//
// Codex 的约束（core/src/compact_remote_v2.rs 与 rollout-trace 的 normalize 阶段）：
//   - output 里必须恰好一个 compaction item，否则报 "expected exactly one
//     compaction output item, got N from M output items"；
//   - 该 item 必须携带非空字符串 encrypted_content，否则报 "did not contain
//     string encrypted_content"。
//
// Anthropic 协议不存在可复用的不透明载荷，故 encrypted_content 由本网关自造信封
// 承载摘要正文（apicompat.EncodeCompactionEnvelope）；下一轮 Codex 原样回放该
// item 时，转换器解码信封还原前文摘要。
func (s *GatewayService) handleResponsesCompactionResponse(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	mappedModel string,
	reasoningEffort *string,
	startTime time.Time,
	clientStream bool,
) (*ForwardResult, error) {
	return s.handleResponsesCompactionResponseWithRetry(resp, c, originalModel, mappedModel, reasoningEffort, startTime, clientStream, nil)
}

// handleResponsesCompactionResponseWithRetry 同 handleResponsesCompactionResponse，另外按
// 摘要的结束原因定终态（与 kiro.rs responses_compaction.rs 的 validate 一致）：
//   - max_tokens：摘要被截断，不当成功，返回 incomplete；
//   - model_context_window_exceeded：上下文已满。retry 非空（Kiro）时截断工具输出重试一次，
//     否则返回 failed / context_length_exceeded；Kiro 流内「输入太长」异常同样处理；
//   - refusal：返回 failed / content_filter；
//   - 流没有正常结束：返回 failed。例外是还没向客户端写过字节的 Kiro 异常，原样返回
//     sseStreamErrorEventError，由调用方按 Kiro HTTP 错误处理（换号或回 4xx）。
func (s *GatewayService) handleResponsesCompactionResponseWithRetry(
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	mappedModel string,
	reasoningEffort *string,
	startTime time.Time,
	clientStream bool,
	retry func() (*http.Response, error),
) (*ForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	// 大上下文摘要期间上游可能长时间不产出可见内容，而下游在拿到终态事件前收不到
	// 任何字节，反向代理容易按空闲超时掐断（OpenAI 侧同源问题见 #3887）。这里在
	// 上游每个事件到达时补发 SSE 注释行保活；非流式客户端不发（响应是 JSON）。
	heartbeat := func() {}
	if clientStream {
		hb := newCompactionHeartbeat(c)
		defer hb.stop()
		heartbeat = hb.tick
	}

	writerSizeBefore := c.Writer.Size()
	finalResp, usage, stream := s.collectAnthropicResponseFromSSE(resp.Body, requestID, heartbeat)
	failure := stream.failure()
	exc := failure.kiroException()
	// Kiro 在流里报「输入太长」与 HTTP 400 同义，和上下文已满一样先截断工具输出重试一次。
	contextFull := kiroUpstreamExceptionIsContextLength(exc) ||
		(failure == nil && finalResp != nil &&
			apicompat.AnthropicStopReasonString(finalResp.StopReason) == compactionStopContextWindowExceeded)
	if contextFull && retry != nil {
		retryResp, err := retry()
		switch {
		case errors.Is(err, errKiroCompactionNoRetry):
			// 没有可截的工具输出：保留首轮结果，下面按上下文已满失败。
		case err != nil:
			return nil, s.failCompaction(c, clientStream, http.StatusBadGateway, "Upstream request failed", requestID)
		default:
			defer func() { _ = retryResp.Body.Close() }()
			if retryResp.StatusCode >= 400 {
				body, _ := io.ReadAll(io.LimitReader(retryResp.Body, 2<<20))
				if _, ok := kiroContextLengthClientMessage(retryResp.StatusCode, body); ok {
					return nil, s.failCompactionWithCode(c, clientStream, http.StatusBadRequest,
						"context_length_exceeded", kiroCompactionOverflowMessage, requestID)
				}
				return nil, s.failCompaction(c, clientStream, mapUpstreamStatusCode(retryResp.StatusCode), "Upstream request failed", requestID)
			}
			retryFinal, retryUsage, retryStream := s.collectAnthropicResponseFromSSE(retryResp.Body, requestID, heartbeat)
			usage = addClaudeUsage(usage, retryUsage)
			finalResp = retryFinal
			failure = retryStream.failure()
			exc = failure.kiroException()
		}
	}
	if failure != nil {
		// 流没有正常结束（上游 error 事件、读错误、缺结束事件）：半截摘要不能当成功交给 Codex。
		if kiroUpstreamExceptionIsContextLength(exc) {
			return nil, s.failCompactionWithCode(c, clientStream, http.StatusBadRequest,
				"context_length_exceeded", kiroCompactionOverflowMessage, requestID)
		}
		if exc != nil && c.Writer.Size() == writerSizeBefore {
			// 客户端还没收到任何字节（心跳也没发过）：交回调用方按 Kiro HTTP 错误处理（换号或回 4xx）。
			return nil, failure.err
		}
		status := http.StatusBadGateway
		if exc != nil {
			status = mapUpstreamStatusCode(kiroUpstreamExceptionStatus(exc))
		}
		return nil, s.failCompaction(c, clientStream, status, failure.message, requestID)
	}
	if finalResp == nil {
		return nil, s.failCompaction(c, clientStream, http.StatusBadGateway,
			"Upstream stream ended without a response", requestID)
	}

	switch apicompat.AnthropicStopReasonString(finalResp.StopReason) {
	case "max_tokens":
		if err := writeCompactionIncomplete(c, clientStream, originalModel, usage); err != nil {
			return nil, s.failCompaction(c, clientStream, http.StatusBadGateway,
				"Failed to build compaction response", requestID)
		}
		logger.L().Warn("forward_as_responses compaction: summary hit max_tokens, reported as incomplete",
			zap.String("request_id", requestID),
			zap.String("model", mappedModel),
		)
		return &ForwardResult{
			RequestID:       requestID,
			Usage:           usage,
			Model:           originalModel,
			UpstreamModel:   mappedModel,
			ReasoningEffort: reasoningEffort,
			Stream:          clientStream,
			Duration:        time.Since(startTime),
		}, nil
	case compactionStopContextWindowExceeded:
		return nil, s.failCompactionWithCode(c, clientStream, http.StatusBadRequest,
			"context_length_exceeded", kiroCompactionOverflowMessage, requestID)
	case "refusal":
		return nil, s.failCompactionWithCode(c, clientStream, http.StatusBadRequest,
			"content_filter", "upstream content filter refused the compaction request", requestID)
	}

	summary := anthropicResponseText(finalResp)
	if strings.TrimSpace(summary) == "" {
		// 不能发空 encrypted_content——Codex 会拒收整个 payload。宁可显式失败，
		// 让客户端走它自己的压缩失败分支。
		return nil, s.failCompaction(c, clientStream, http.StatusBadGateway,
			"Upstream produced no compaction summary", requestID)
	}

	envelope := apicompat.EncodeCompactionEnvelope(summary)
	if envelope == "" {
		return nil, s.failCompaction(c, clientStream, http.StatusBadGateway,
			"Failed to encode compaction summary", requestID)
	}

	respJSON, err := buildCompactionResponseJSON(originalModel, summary, envelope, usage)
	if err != nil {
		return nil, s.failCompaction(c, clientStream, http.StatusBadGateway,
			"Failed to build compaction response", requestID)
	}

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}

	if clientStream {
		payload, ok := buildOpenAICompactSSEPayload(respJSON)
		if !ok {
			return nil, s.failCompaction(c, clientStream, http.StatusBadGateway,
				"Failed to render compaction SSE", requestID)
		}
		writeCompactionSSEHeaders(c)
		_, _ = c.Writer.Write(payload)
		c.Writer.Flush()
	} else {
		c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		c.Data(http.StatusOK, "application/json; charset=utf-8", respJSON)
	}

	logger.L().Info("forward_as_responses compaction: summary synthesized",
		zap.String("request_id", requestID),
		zap.String("model", mappedModel),
		zap.Int("summary_bytes", len(summary)),
		zap.Bool("client_stream", clientStream),
	)

	return &ForwardResult{
		RequestID:       requestID,
		Usage:           usage,
		Model:           originalModel,
		UpstreamModel:   mappedModel,
		ReasoningEffort: reasoningEffort,
		Stream:          clientStream,
		Duration:        time.Since(startTime),
	}, nil
}

// failCompaction 按客户端协议回写压缩失败。流式客户端只认 response.failed 作为
// 合法终止事件（普通 error 帧会被当成断流并盲重连），非流式走标准 JSON 错误。
func (s *GatewayService) failCompaction(
	c *gin.Context,
	clientStream bool,
	statusCode int,
	message string,
	requestID string,
) error {
	return s.failCompactionWithCode(c, clientStream, statusCode, "", message, requestID)
}

// failCompactionWithCode 同 failCompaction，但用指定的错误码（如 context_length_exceeded、
// content_filter）。code 为空时沿用默认：流式 upstream_error，非流式 server_error。
func (s *GatewayService) failCompactionWithCode(
	c *gin.Context,
	clientStream bool,
	statusCode int,
	code string,
	message string,
	requestID string,
) error {
	logger.L().Warn("forward_as_responses compaction: failed",
		zap.String("request_id", requestID),
		zap.String("code", code),
		zap.String("message", message),
	)
	if clientStream {
		if code == "" {
			code = "upstream_error"
		}
		writeCompactionSSEHeaders(c)
		writeOpenAICompactSSEFailureMessage(c, statusCode, code, message)
	} else {
		if code == "" {
			code = "server_error"
		}
		writeResponsesError(c, statusCode, code, message)
	}
	return fmt.Errorf("compaction failed: %s", message)
}

// compactionStopContextWindowExceeded 是上游上下文已满时的 stop_reason。
const compactionStopContextWindowExceeded = "model_context_window_exceeded"

// writeCompactionIncomplete 在摘要被 max_tokens 截断时回写 incomplete 终态（无 output），
// 让 Codex 走它自己的压缩未完成分支，而不是拿残缺摘要替换前文。与 kiro.rs 一致。
func writeCompactionIncomplete(c *gin.Context, clientStream bool, model string, usage ClaudeUsage) error {
	inputTokens := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	response := map[string]any{
		"id":                 "resp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"object":             "response",
		"created_at":         time.Now().Unix(),
		"model":              model,
		"status":             "incomplete",
		"incomplete_details": map[string]any{"reason": "max_output_tokens"},
		"output":             []any{},
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": usage.OutputTokens,
			"total_tokens":  inputTokens + usage.OutputTokens,
		},
	}
	if !clientStream {
		c.JSON(http.StatusOK, response)
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"type":            "response.incomplete",
		"sequence_number": 0,
		"response":        response,
	})
	if err != nil {
		return err
	}
	writeCompactionSSEHeaders(c)
	_, _ = c.Writer.Write([]byte("event: response.incomplete\ndata: " + string(payload) + "\n\n"))
	c.Writer.Flush()
	return nil
}

func writeCompactionSSEHeaders(c *gin.Context) {
	if c == nil || c.Writer == nil || c.Writer.Written() {
		return
	}
	header := c.Writer.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()
}

// buildCompactionResponseJSON 构造 output 恰好一项 compaction item 的 Responses
// 响应。summary 同时以明文放入 summary 字段：本网关下一轮回放时优先读它，
// 无需解码信封，Codex 侧也能直接展示。
func buildCompactionResponseJSON(model, summary, envelope string, usage ClaudeUsage) ([]byte, error) {
	item := map[string]any{
		"id":                "cmp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"type":              apicompat.CompactionItemType,
		"status":            "completed",
		"encrypted_content": envelope,
		"summary": []any{map[string]any{
			"type": "summary_text",
			"text": summary,
		}},
	}
	inputTokens := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	payload := map[string]any{
		"id":     "resp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"object": "response",
		"model":  model,
		"status": "completed",
		"output": []any{item},
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": usage.OutputTokens,
			"total_tokens":  inputTokens + usage.OutputTokens,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal compaction response: %w", err)
	}
	return encoded, nil
}

// compactionHeartbeat 在摘要生成期间向下游发送 SSE 注释行保活。
//
// 只在上游事件到达时被动触发（tick 由 collectAnthropicResponseFromSSE 回调），
// 不另起 goroutine：写入与最终事件因此天然发生在同一 goroutine，不需要额外
// 同步，也不会与终态事件交错。Anthropic 上游在生成期间会周期性发 ping 事件，
// 足以驱动这个节拍。
type compactionHeartbeat struct {
	c        *gin.Context
	last     time.Time
	started  bool
	disabled bool
}

func newCompactionHeartbeat(c *gin.Context) *compactionHeartbeat {
	return &compactionHeartbeat{c: c, last: time.Now()}
}

func (h *compactionHeartbeat) tick() {
	if h == nil || h.disabled || h.c == nil || h.c.Writer == nil {
		return
	}
	if time.Since(h.last) < compactionHeartbeatInterval {
		return
	}
	h.last = time.Now()
	if !h.started {
		// 首次写入前必须先提交 SSE 响应头，否则 Gin 会按默认 Content-Type 提交。
		writeCompactionSSEHeaders(h.c)
		h.started = true
	}
	if _, err := h.c.Writer.Write([]byte(": keepalive\n\n")); err != nil {
		// 下游已断开：停止后续心跳，避免在死连接上反复写。
		h.disabled = true
		return
	}
	h.c.Writer.Flush()
}

func (h *compactionHeartbeat) stop() {
	if h != nil {
		h.disabled = true
	}
}

// anthropicResponseText 拼接 Anthropic 响应里的全部 text 块。thinking 块不计入：
// 摘要正文只应来自模型的可见输出。
func anthropicResponseText(resp *apicompat.AnthropicResponse) string {
	if resp == nil {
		return ""
	}
	parts := make([]string, 0, len(resp.Content))
	for _, block := range resp.Content {
		if block.Type != "text" {
			continue
		}
		if text := strings.TrimSpace(block.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}
