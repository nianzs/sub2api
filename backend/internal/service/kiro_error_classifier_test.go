package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyKiroHTTPErrorBadRequestCategories(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "schema",
			body: `{"message":"Improperly formed request: inputSchema.properties must be an object"}`,
			want: kiroErrorBadRequestSchema,
		},
		{
			name: "tool pairing",
			body: `{"message":"tool_use must be paired with a matching tool_result"}`,
			want: kiroErrorBadRequestToolPairing,
		},
		{
			name: "invalid model id",
			body: `{"message":"invalid modelId: model not supported"}`,
			want: kiroErrorBadRequestInvalidModel,
		},
		{
			name: "invalid model upstream",
			body: `{"error":{"message":"Invalid model. Please select a different model to continue.","type":"upstream_error"}}`,
			want: kiroErrorBadRequestInvalidModel,
		},
		{
			name: "invalid model reason",
			body: `{"message":"model route unavailable","reason":"INVALID_MODEL_ID"}`,
			want: kiroErrorBadRequestInvalidModel,
		},
		{
			name: "auth",
			body: `{"error":"invalid_grant","message":"Invalid refresh token provided"}`,
			want: kiroErrorBadRequestAuth,
		},
		{
			name: "quota",
			body: `{"message":"resource has been exhausted"}`,
			want: kiroErrorBadRequestQuota,
		},
		{
			name: "content length threshold",
			body: `{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`,
			want: kiroErrorBadRequestContextLength,
		},
		{
			name: "input is too long",
			body: `{"message":"Input is too long for requested model."}`,
			want: kiroErrorBadRequestContextLength,
		},
		{
			name: "unknown",
			body: `{"message":"bad request"}`,
			want: kiroErrorBadRequestUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classification := classifyKiroHTTPError(http.StatusBadRequest, tt.body)
			require.Equal(t, tt.want, classification.Category)
			require.Equal(t, http.StatusBadRequest, classification.StatusCode)
			require.Equal(t, tt.body, classification.Message)
		})
	}
}

// 上游真实的超刷用尽响应（402 OVERAGE_REQUEST_LIMIT_EXCEEDED，样本来自 kiro.rs）
// 归为 overage_exhausted，且会让账号停到月度重置。
func TestClassifyKiroHTTPErrorOverageRequestLimitExceeded(t *testing.T) {
	for _, body := range []string{
		`{"message":"You have reached the limit for overages.","reason":"OVERAGE_REQUEST_LIMIT_EXCEEDED"}`,
		`{"error":{"reason":"OVERAGE_REQUEST_LIMIT_EXCEEDED"}}`,
	} {
		classification := classifyKiroHTTPError(http.StatusPaymentRequired, body)
		require.Equal(t, kiroErrorOverageExhausted, classification.Category, body)
		require.True(t, kiroQuotaExhausted(classification), body)
	}

	// 关键词匹配不区分状态码；429 不能把账号停到月底。
	require.False(t, kiroQuotaExhausted(classifyKiroHTTPError(http.StatusTooManyRequests,
		`{"message":"overage exhausted for this billing window"}`)))
	require.True(t, kiroQuotaExhausted(classifyKiroHTTPError(http.StatusPaymentRequired,
		`{"message":"You have reached the limit.","reason":"MONTHLY_REQUEST_COUNT"}`)))
}

// 超刷额度用尽的 429 文案归为 overage_exhausted，优先于普通限流/配额耗尽。
func TestClassifyKiroHTTPErrorOverageExhausted(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "overage exhausted",
			body: `{"message":"overage exhausted for this billing window"}`,
		},
		{
			name: "overage disabled",
			body: `{"message":"overage is not enabled for this account"}`,
		},
		{
			name: "overage not allowed",
			body: `{"message":"overage usage not allowed"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classification := classifyKiroHTTPError(http.StatusTooManyRequests, tt.body)
			require.Equal(t, kiroErrorOverageExhausted, classification.Category)
			require.Equal(t, http.StatusTooManyRequests, classification.StatusCode)
		})
	}
}

// Kiro「输入太长」返回固定文案，不回显上游原文；区分窗口满与单次输入太长（与 kiro.rs 一致）。
func TestKiroContextLengthClientMessage(t *testing.T) {
	msg, ok := kiroContextLengthClientMessage(http.StatusBadRequest, []byte(`{"message":"Input is too long. requestId=abc","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`))
	require.True(t, ok)
	require.Equal(t, kiroContextWindowFullMessage, msg)

	msg, ok = kiroContextLengthClientMessage(http.StatusBadRequest, []byte(`{"message":"Input is too long for requested model. requestId=abc"}`))
	require.True(t, ok)
	require.Equal(t, kiroInputTooLongMessage, msg)

	_, ok = kiroContextLengthClientMessage(http.StatusBadRequest, []byte(`{"message":"bad request"}`))
	require.False(t, ok)
	_, ok = kiroContextLengthClientMessage(http.StatusInternalServerError, []byte(`{"message":"Input is too long."}`))
	require.False(t, ok)
}
