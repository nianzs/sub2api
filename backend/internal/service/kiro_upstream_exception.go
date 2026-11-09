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

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Kiro 在事件流里下发的失败（exception / error 帧，见 kiropkg.KiroUpstreamException）。
//
// 首个输出之前失败时客户端还没收到任何字节，按 Kiro 直接回了对应 HTTP 错误处理：
// 构造等价的错误响应交给 handleKiroHTTPErrorWith，换号、限流冷却、ops 事件和客户端
// 文案都走现有逻辑；额度用尽（402）单独处理，见 handleKiroUpstreamQuotaException。
// Messages / Chat Completions / Responses 三个入口共用，各自传入协议对应的错误写出函数。
// 已有输出时只能在流里以 SSE error 收尾。两种情况都不计费。

// kiroUpstreamExceptionSSEField 是 SSE error 事件里携带原始异常的私有字段，
// forwardKiroMessages 据此在输出开始前还原异常。
const kiroUpstreamExceptionSSEField = "kiro_exception"

// kiroOverageLimitExceededCode 是 Kiro 超刷额度用尽的明确原因码。
const kiroOverageLimitExceededCode = "OVERAGE_REQUEST_LIMIT_EXCEEDED"

// kiroUpstreamExceptionStatus 把流内失败映射成 Kiro 直接返回时的 HTTP 状态。
// 不映射成 401/403：账号级的鉴权问题在流开始前就以 HTTP 错误返回，流内的
// AccessDenied 等更可能只针对本次请求/模型，按 403 处理会走 handle403 停用账号，
// 所以归入默认的 500（换号重试，不动账号状态）。
//
// 402（停到月度重置）只认明确的额度码 MONTHLY_REQUEST_COUNT / OVERAGE_REQUEST_LIMIT_EXCEEDED，
// 与 HTTP 402 的口径（kiroQuotaExhausted）一致。其余像额度的措辞（ServiceQuotaExceeded、
// "quota exceeded"、"usage limit reached" 等）可能只针对本次请求，而换号会把同一请求发给下一个
// 账号：按 402 停到月底会让一个请求停掉一串账号，所以按 429 冷却几分钟再换号。
func kiroUpstreamExceptionStatus(exc *kiropkg.KiroUpstreamException) int {
	typ := strings.ToLower(exc.Type)
	raw := exc.Type + " " + exc.Message + " " + string(exc.Payload)
	text := strings.ToLower(raw)
	switch {
	case looksLikeKiroMonthlyRequestCountError(raw) || strings.Contains(raw, kiroOverageLimitExceededCode):
		return http.StatusPaymentRequired
	case strings.Contains(typ, "throttl") || strings.Contains(text, "too many requests") || strings.Contains(text, "rate exceeded"):
		return http.StatusTooManyRequests
	case strings.Contains(typ, "servicequotaexceeded") || looksLikeKiroOverageExhaustedError(text) ||
		looksLikeKiroQuotaExhaustedError(text):
		return http.StatusTooManyRequests
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
// （可换号时为 *UpstreamFailoverError，否则已用 writeError 按入口协议写好错误响应）。
func (s *GatewayService) handleKiroUpstreamException(ctx context.Context, c *gin.Context, account *Account, mappedModel string, requestBody []byte, exc *kiropkg.KiroUpstreamException, header http.Header, writeError kiroClientErrorWriter) error {
	resp := kiroUpstreamExceptionResponse(exc, header)
	defer func() { _ = resp.Body.Close() }()
	// 流式处理已设好 SSE 响应头（尚未写出）；直接回 JSON 错误时去掉，
	// 否则 gin 会保留 text/event-stream。
	if c != nil && c.Writer != nil && !c.Writer.Written() {
		for _, h := range []string{"Content-Type", "Cache-Control", "Connection", "X-Accel-Buffering"} {
			c.Writer.Header().Del(h)
		}
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		// HTTP 429 在 executeKiroUpstreamWithParsed 里已做冷却，handleKiroHTTPError 因此跳过；
		// 流内限流没走那里，这里补上。
		if account != nil {
			if _, err := s.markKiro429(ctx, account.ID, buildKiroAccountKey(account)); err != nil {
				logger.FromContext(ctx).Warn("kiro.upstream_exception_mark_429_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			}
		}
	case http.StatusPaymentRequired:
		return s.handleKiroUpstreamQuotaException(ctx, c, account, resp)
	}
	return s.handleKiroHTTPErrorWith(ctx, resp, c, account, mappedModel, requestBody, writeError)
}

// handleKiroStreamErrorEvent 处理流式转换返回的 error 事件，Messages / Chat Completions /
// Responses 三个入口共用。handled=false 表示不是 Kiro 异常，由调用方按入口原有逻辑处理。
//   - 客户端还没收到任何字节：按 Kiro HTTP 错误处理（换号或按入口协议回 4xx）。
//   - 已有输出（或保活 ping）：不能换号，按流中断返回普通错误（不是 failover 错误），
//     handler 补写协议内的终止错误，并记录截至异常的用量（与流中断的部分入账一致）。
//     交给 sseStreamErrorEventFailover 会被当成 403，客户端看到"无权访问"且这部分用量漏记。
func (s *GatewayService) handleKiroStreamErrorEvent(ctx context.Context, c *gin.Context, account *Account, resp *http.Response, requestBody []byte, originalModel, mappedModel, upstreamModel string, startTime time.Time, rawData string, outputStarted bool, writeError kiroClientErrorWriter) (result *ForwardResult, handled bool, err error) {
	exc := kiroUpstreamExceptionFromSSE(rawData)
	if exc == nil {
		return nil, false, nil
	}
	if !outputStarted {
		return nil, true, s.handleKiroUpstreamException(ctx, c, account, mappedModel, requestBody, exc, resp.Header, writeError)
	}
	return kiroPartialForwardResult(resp, exc, originalModel, upstreamModel, startTime), true,
		fmt.Errorf("kiro upstream exception after output started: %w", exc)
}

// kiroPartialForwardResult 用异常携带的截至此刻用量构造部分结果；没有用量时返回 nil。
func kiroPartialForwardResult(resp *http.Response, exc *kiropkg.KiroUpstreamException, model, upstreamModel string, startTime time.Time) *ForwardResult {
	if exc == nil || exc.PartialUsage == nil {
		return nil
	}
	return &ForwardResult{
		RequestID:     buildKiroRequestID(resp),
		Usage:         kiroUsageToClaude(*exc.PartialUsage, exc.PartialUsage.InputTokens),
		Model:         model,
		UpstreamModel: upstreamModel,
		Stream:        true,
		Duration:      time.Since(startTime),
	}
}

// handleKiroUpstreamQuotaException 处理映射成 402 的流内失败（ServiceQuotaExceeded、
// 额度/用量/积分/超刷用尽）。这些信号都表示本计费周期额度已用尽，不再细分是否
// MONTHLY_REQUEST_COUNT，一律停到月度重置后换号。不交给 handleKiroHTTPError：
// 那里的通用 402 处理是为 HTTP 402 准备的，也不能让流内信号碰账号状态。
func (s *GatewayService) handleKiroUpstreamQuotaException(ctx context.Context, c *gin.Context, account *Account, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	category := classifyKiroHTTPError(resp.StatusCode, string(body)).Category
	if category != kiroErrorMonthlyRequest && category != kiroErrorOverageExhausted {
		category = kiroErrorQuotaExhausted
	}
	s.markKiroQuotaExhaustedRateLimited(ctx, account, category, string(body))
	if account != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  buildKiroRequestID(resp),
			Kind:               "failover",
			Message:            sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body))),
		})
	}
	return &UpstreamFailoverError{
		StatusCode:      resp.StatusCode,
		ResponseBody:    body,
		ResponseHeaders: resp.Header.Clone(),
	}
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
		kiroUpstreamExceptionSSEField: kiroExceptionSSEField(exc),
	})
	return "event: error\ndata: " + string(payload) + "\n\n"
}

func kiroExceptionSSEField(exc *kiropkg.KiroUpstreamException) map[string]any {
	field := map[string]any{
		"type":    exc.Type,
		"message": exc.Message,
		"payload": string(exc.Payload),
	}
	if exc.PartialUsage != nil {
		field["usage"] = exc.PartialUsage
	}
	return field
}

// kiroUpstreamExceptionFromError 从流处理返回的 sseStreamErrorEventError 里还原 Kiro 异常；
// 其它错误（含非 Kiro 的 error 事件）返回 nil。
func kiroUpstreamExceptionFromError(err error) *kiropkg.KiroUpstreamException {
	var sseErr *sseStreamErrorEventError
	if !errors.As(err, &sseErr) {
		return nil
	}
	return kiroUpstreamExceptionFromSSE(sseErr.RawData)
}

// kiroException 返回 compat 流失败里携带的 Kiro 异常；不是 Kiro 异常（或没有失败）时为 nil。
func (f *anthropicCompatStreamFailure) kiroException() *kiropkg.KiroUpstreamException {
	if f == nil {
		return nil
	}
	return kiroUpstreamExceptionFromError(f.err)
}

// kiroUpstreamExceptionIsContextLength 判断异常是否为「输入太长」（与 HTTP 400 同口径）。
func kiroUpstreamExceptionIsContextLength(exc *kiropkg.KiroUpstreamException) bool {
	if exc == nil {
		return false
	}
	resp := kiroUpstreamExceptionResponse(exc, nil)
	body, _ := io.ReadAll(resp.Body)
	_, ok := kiroContextLengthClientMessage(resp.StatusCode, body)
	return ok
}

// kiroUpstreamExceptionFromSSE 从 kiroUpstreamExceptionSSE 生成的事件里还原异常；其它 error 事件返回 nil。
func kiroUpstreamExceptionFromSSE(rawData string) *kiropkg.KiroUpstreamException {
	field := gjson.Get(rawData, kiroUpstreamExceptionSSEField)
	if !field.IsObject() || field.Get("type").String() == "" {
		return nil
	}
	exc := &kiropkg.KiroUpstreamException{
		Type:    field.Get("type").String(),
		Message: field.Get("message").String(),
		Payload: []byte(field.Get("payload").String()),
	}
	if usage := field.Get("usage"); usage.IsObject() {
		var partial kiropkg.Usage
		if err := json.Unmarshal([]byte(usage.Raw), &partial); err == nil {
			exc.PartialUsage = &partial
		}
	}
	return exc
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
