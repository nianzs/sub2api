package fish

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// 错误类型常量（ErrorType）。
const (
	ErrorTypeStatus     = "status"
	ErrorTypeTimeout    = "timeout"
	ErrorTypeConnection = "connection"
	ErrorTypeProxy      = "proxy"
	ErrorTypeNetwork    = "network"
)

// RequestError 是 fish 上游调用的基础错误。fish API 的错误信封统一为
// {"status":N,"message":"..."}（2026-10 抓包确认）。
type RequestError struct {
	Message     string
	StatusCode  int
	ErrorType   string
	UserMessage string
	// Code 是错误信封里的稳定机器码（如 OUT_OF_CREDITS），部分错误没有。
	Code string
}

// CodeOutOfCredits 是 402 的机器码：钱包余额不够这一单的价格。它不代表
// 余额为零——响应不带余额，也不带所需积分。
const CodeOutOfCredits = "OUT_OF_CREDITS"

// IsOutOfCredits 报告错误是否为带 OUT_OF_CREDITS 机器码的 402。
func IsOutOfCredits(err error) bool {
	var insufficient *InsufficientCreditsError
	return errors.As(err, &insufficient) && insufficient.Code == CodeOutOfCredits
}

// withErrorCode 把信封机器码写到已分类错误内嵌的 RequestError 上。
func withErrorCode(err error, code string) error {
	var reqErr *RequestError
	if code != "" && errors.As(err, &reqErr) {
		reqErr.Code = code
	}
	return err
}

func (e *RequestError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("fish: %s (status %d)", e.Message, e.StatusCode)
	}
	return "fish: " + e.Message
}

// IsNetwork 报告错误是否为网络层（可换账号重试的环境问题）。
func (e *RequestError) IsNetwork() bool {
	switch e.ErrorType {
	case ErrorTypeTimeout, ErrorTypeConnection, ErrorTypeProxy, ErrorTypeNetwork:
		return true
	}
	return false
}

// AuthError 表示 token 失效（401 {"message":"Invalid token"}，实测签名）。
type AuthError struct{ RequestError }

func (e *AuthError) Unwrap() error { return &e.RequestError }

// RateLimitedError 表示上游限流（429）。
type RateLimitedError struct{ RequestError }

func (e *RateLimitedError) Unwrap() error { return &e.RequestError }

// InsufficientCreditsError 表示钱包 credits 不足或配额耗尽。
type InsufficientCreditsError struct{ RequestError }

func (e *InsufficientCreditsError) Unwrap() error { return &e.RequestError }

// InvalidCombinationError 表示模型/尺寸/质量组合不合法（价格表
// matches=0 或请求参数被 422/400 拒绝）——客户端参数问题，换账号无意义。
type InvalidCombinationError struct{ RequestError }

func (e *InvalidCombinationError) Unwrap() error { return &e.RequestError }

// UpstreamTemporaryError 表示上游临时故障（5xx），可重试。
type UpstreamTemporaryError struct{ RequestError }

func (e *UpstreamTemporaryError) Unwrap() error { return &e.RequestError }

// ForbiddenError 表示上游业务 API 拒绝该账号（403；实测样本为生图接口
// GENERATION_UNAVAILABLE，2026-10-24：账号级风控/封禁信号，token 鉴权
// 本身仍通过）。换账号可解。参考图 R2 预签名 PUT 的 403 不经
// classifyAPIError，不受此分类影响。
type ForbiddenError struct{ RequestError }

func (e *ForbiddenError) Unwrap() error { return &e.RequestError }

func NewRequestError(message string) *RequestError {
	return &RequestError{Message: message, ErrorType: ErrorTypeStatus}
}

// wrapNetworkError 把传输层错误归一为 RequestError（网络类）。
func wrapNetworkError(message string, err error) *RequestError {
	errorType := ErrorTypeConnection
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		errorType = ErrorTypeTimeout
	} else {
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			errorType = ErrorTypeTimeout
		} else if strings.Contains(strings.ToLower(err.Error()), "proxy") {
			errorType = ErrorTypeProxy
		}
	}
	return &RequestError{Message: fmt.Sprintf("%s: %v", message, err), ErrorType: errorType}
}

// insufficientCreditsMarkers 是余额/配额类错误的 message 特征（实测样本
// 有限，遇新样本补充）。
var insufficientCreditsMarkers = []string{
	"insufficient credit",
	"not enough credit",
	"insufficient balance",
	"daily limit",
	"quota exceeded",
	"exceeded your",
}

// classifyAPIError 按 fish 错误信封分类。message 为响应体里的 message 字段。
func classifyAPIError(statusCode int, message string) error {
	lower := strings.ToLower(message)
	switch {
	case statusCode == 401:
		return &AuthError{RequestError{Message: pickMessage(message, "Invalid token"), StatusCode: statusCode, ErrorType: ErrorTypeStatus}}
	case statusCode == 402:
		return &InsufficientCreditsError{RequestError{Message: pickMessage(message, "insufficient credits"), StatusCode: statusCode, ErrorType: ErrorTypeStatus}}
	case statusCode == 403:
		return &ForbiddenError{RequestError{Message: pickMessage(message, "forbidden"), StatusCode: statusCode, ErrorType: ErrorTypeStatus}}
	case statusCode == 429:
		return &RateLimitedError{RequestError{Message: pickMessage(message, "rate limited"), StatusCode: statusCode, ErrorType: ErrorTypeStatus}}
	case statusCode >= 500:
		return &UpstreamTemporaryError{RequestError{Message: pickMessage(message, "upstream error"), StatusCode: statusCode, ErrorType: ErrorTypeStatus}}
	}
	for _, marker := range insufficientCreditsMarkers {
		if strings.Contains(lower, marker) {
			return &InsufficientCreditsError{RequestError{Message: message, StatusCode: statusCode, ErrorType: ErrorTypeStatus}}
		}
	}
	if strings.Contains(lower, "pricing configuration") || strings.Contains(lower, "matches=0") {
		return &InvalidCombinationError{RequestError{Message: message, StatusCode: statusCode, ErrorType: ErrorTypeStatus}}
	}
	if statusCode == 400 || statusCode == 422 {
		return &InvalidCombinationError{RequestError{Message: pickMessage(message, "invalid request"), StatusCode: statusCode, ErrorType: ErrorTypeStatus}}
	}
	return &RequestError{Message: pickMessage(message, "request failed"), StatusCode: statusCode, ErrorType: ErrorTypeStatus}
}

func pickMessage(message, fallback string) string {
	if strings.TrimSpace(message) != "" {
		return message
	}
	return fallback
}
