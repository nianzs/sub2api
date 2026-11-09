package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Kiro 在事件流里下发的失败（exception / error 帧，见 kiropkg.KiroUpstreamException）。
//
// 首个输出之前失败时客户端还没收到任何字节，按 Kiro 直接回了对应 HTTP 错误处理：
// 构造等价的错误响应交给 handleKiroHTTPError，换号、限流/额度标记、ops 事件和客户端
// 文案都走现有逻辑。已有输出时只能在流里以 SSE error 收尾。两种情况都不计费。

// kiroUpstreamExceptionSSEField 是 SSE error 事件里携带原始异常的私有字段，
// forwardKiroMessages 据此在输出开始前还原异常。
const kiroUpstreamExceptionSSEField = "kiro_exception"

// kiroUpstreamExceptionStatus 把流内失败映射成 Kiro 直接返回时的 HTTP 状态。
// 不映射成 401/403：账号级的鉴权问题在流开始前就以 HTTP 错误返回，流内的
// AccessDenied 等更可能只针对本次请求/模型，按 403 处理会走 handle403 停用账号，
// 所以归入默认的 500（换号重试，不动账号状态）。
func kiroUpstreamExceptionStatus(exc *kiropkg.KiroUpstreamException) int {
	typ := strings.ToLower(exc.Type)
	raw := exc.Type + " " + exc.Message + " " + string(exc.Payload)
	text := strings.ToLower(raw)
	switch {
	case strings.Contains(typ, "throttl") || strings.Contains(text, "too many requests") || strings.Contains(text, "rate exceeded"):
		return http.StatusTooManyRequests
	case strings.Contains(typ, "servicequotaexceeded") || looksLikeKiroOverageExhaustedError(text) ||
		looksLikeKiroQuotaExhaustedError(text) || looksLikeKiroMonthlyRequestCountError(raw):
		return http.StatusPaymentRequired
	case strings.Contains(typ, "validation"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// kiroUpstreamExceptionResponse 构造等价的 Kiro HTTP 错误响应。错误体沿用帧载荷
// （通常是 {"message":...,"reason":...}），补上 __type / message，与 Kiro 直接回
// HTTP 错误时的结构一致，classifyKiroHTTPError 等按原样识别。
func kiroUpstreamExceptionResponse(exc *kiropkg.KiroUpstreamException, header http.Header) *http.Response {
	body := map[string]any{}
	if err := json.Unmarshal(exc.Payload, &body); err != nil || body == nil {
		body = map[string]any{}
	}
	if _, ok := body["__type"]; !ok {
		body["__type"] = exc.Type
	}
	if _, ok := body["message"]; !ok && exc.Message != "" {
		body["message"] = exc.Message
	}
	raw, _ := json.Marshal(body)
	h := http.Header{}
	if header != nil {
		h = header.Clone()
	}
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: kiroUpstreamExceptionStatus(exc),
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(raw)),
	}
}

// handleKiroUpstreamException 处理首个输出之前的流内失败，返回值同 handleKiroHTTPError
// （可换号时为 *UpstreamFailoverError，否则已写好错误响应）。
func (s *GatewayService) handleKiroUpstreamException(ctx context.Context, c *gin.Context, account *Account, mappedModel string, requestBody []byte, exc *kiropkg.KiroUpstreamException, header http.Header) error {
	resp := kiroUpstreamExceptionResponse(exc, header)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests && account != nil {
		// HTTP 429 在 executeKiroUpstreamWithParsed 里已做冷却，handleKiroHTTPError 因此跳过；
		// 流内限流没走那里，这里补上。
		if _, err := s.markKiro429(ctx, account.ID, buildKiroAccountKey(account)); err != nil {
			logger.FromContext(ctx).Warn("kiro.upstream_exception_mark_429_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		}
	}
	// handleStreamingResponse 已设好 SSE 响应头（尚未写出）；直接回 JSON 错误时去掉，
	// 否则 gin 会保留 text/event-stream。
	if c != nil && c.Writer != nil && !c.Writer.Written() {
		for _, h := range []string{"Content-Type", "Cache-Control", "Connection", "X-Accel-Buffering"} {
			c.Writer.Header().Del(h)
		}
	}
	return s.handleKiroHTTPError(ctx, resp, c, account, mappedModel, requestBody)
}

// kiroUpstreamExceptionSSE 是转换流里以 Anthropic 格式下发的 SSE error 事件，
// 附带原始异常供 forwardKiroMessages 还原。
func kiroUpstreamExceptionSSE(exc *kiropkg.KiroUpstreamException) string {
	status := kiroUpstreamExceptionStatus(exc)
	message := exc.Type
	if exc.Message != "" {
		message += ": " + exc.Message
	}
	payload, _ := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    claudeErrorType(status),
			"message": truncateString(sanitizeUpstreamErrorMessage(message), 500),
		},
		kiroUpstreamExceptionSSEField: map[string]any{
			"type":    exc.Type,
			"message": exc.Message,
			"payload": string(exc.Payload),
		},
	})
	return "event: error\ndata: " + string(payload) + "\n\n"
}

// kiroUpstreamExceptionFromSSE 从 kiroUpstreamExceptionSSE 生成的事件里还原异常；其它 error 事件返回 nil。
func kiroUpstreamExceptionFromSSE(rawData string) *kiropkg.KiroUpstreamException {
	field := gjson.Get(rawData, kiroUpstreamExceptionSSEField)
	if !field.IsObject() || field.Get("type").String() == "" {
		return nil
	}
	return &kiropkg.KiroUpstreamException{
		Type:    field.Get("type").String(),
		Message: field.Get("message").String(),
		Payload: []byte(field.Get("payload").String()),
	}
}

func logKiroUpstreamException(ctx context.Context, account *Account, mappedModel, requestModel string, exc *kiropkg.KiroUpstreamException, outputStarted bool) {
	fields := []zap.Field{
		zap.String("mapped_model", mappedModel),
		zap.String("requested_model", requestModel),
		zap.String("exception_type", exc.Type),
		zap.String("message", truncateString(sanitizeUpstreamErrorMessage(exc.Message), 500)),
		zap.String("payload", truncateString(sanitizeUpstreamErrorMessage(string(exc.Payload)), 1000)),
		zap.Bool("output_started", outputStarted),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
	}
	logger.FromContext(ctx).Warn("kiro.upstream_exception", fields...)
}

// logKiroEmptyResponse 记录正常结束却没有任何输出的回复（不改行为，用于确认空回复是否都由上游异常帧导致）。
func logKiroEmptyResponse(ctx context.Context, account *Account, mappedModel string, usage kiropkg.Usage, stopReason string) {
	fields := []zap.Field{
		zap.String("mapped_model", mappedModel),
		zap.String("stop_reason", stopReason),
		zap.Float64("credits", usage.KiroCredits),
		zap.Int("input_tokens", usage.InputTokens),
		zap.Int("cache_read_tokens", usage.CacheReadInputTokens),
		zap.Int("cache_creation_tokens", usage.CacheCreationInputTokens),
		zap.Int("output_tokens", usage.OutputTokens),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
	}
	logger.FromContext(ctx).Warn("kiro.empty_response", fields...)
}

// kiroCountingWriter 记录写出的字节数，用来判断上游失败时客户端是否已收到输出。
type kiroCountingWriter struct {
	w io.Writer
	n int64
}

func (cw *kiroCountingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}
