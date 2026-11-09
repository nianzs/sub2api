package kiro

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildHeaderEventStreamFrame 按给定字符串头（保持顺序）构造一帧，用于模拟 exception / error 帧。
func buildHeaderEventStreamFrame(t *testing.T, headers [][2]string, payload []byte) []byte {
	t.Helper()
	h := bytes.NewBuffer(nil)
	for _, kv := range headers {
		_ = h.WriteByte(byte(len(kv[0])))
		_, _ = h.WriteString(kv[0])
		_ = h.WriteByte(7)
		require.NoError(t, binary.Write(h, binary.BigEndian, uint16(len(kv[1]))))
		_, _ = h.WriteString(kv[1])
	}
	totalLength := uint32(12 + h.Len() + len(payload) + 4)
	frame := bytes.NewBuffer(nil)
	require.NoError(t, binary.Write(frame, binary.BigEndian, totalLength))
	require.NoError(t, binary.Write(frame, binary.BigEndian, uint32(h.Len())))
	require.NoError(t, binary.Write(frame, binary.BigEndian, uint32(0)))
	_, _ = frame.Write(h.Bytes())
	_, _ = frame.Write(payload)
	require.NoError(t, binary.Write(frame, binary.BigEndian, uint32(0)))
	return frame.Bytes()
}

func exceptionFrame(t *testing.T, exceptionType, payload string) []byte {
	t.Helper()
	return buildHeaderEventStreamFrame(t, [][2]string{
		{":message-type", "exception"},
		{":exception-type", exceptionType},
		{":content-type", "application/json"},
	}, []byte(payload))
}

func textFrame(t *testing.T, text string) []byte {
	t.Helper()
	return buildEventStreamFrame(t, "assistantResponseEvent", map[string]any{
		"assistantResponseEvent": map[string]any{"content": text},
	})
}

func TestStreamReturnsUpstreamExceptionBeforeAnyOutput(t *testing.T) {
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(buildEventStreamFrame(t, "contextUsageEvent", map[string]any{"contextUsagePercentage": 1.8}))
	_, _ = stream.Write(exceptionFrame(t, "ValidationException", `{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`))

	var out bytes.Buffer
	result, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, &out, "claude-opus-4-7", 100, KiroRequestContext{})
	require.Nil(t, result)
	var exc *KiroUpstreamException
	require.ErrorAs(t, err, &exc)
	require.Equal(t, "ValidationException", exc.Type)
	require.Equal(t, "Input is too long.", exc.Message)
	require.Contains(t, string(exc.Payload), "CONTENT_LENGTH_EXCEEDS_THRESHOLD")
	require.Zero(t, out.Len(), "nothing may reach the client before the first output")
	require.Nil(t, exc.PartialUsage, "no partial usage before any output")
}

func TestStreamReturnsUpstreamExceptionAfterPartialOutput(t *testing.T) {
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(textFrame(t, "partial answer"))
	_, _ = stream.Write(buildEventStreamFrame(t, "meteringEvent", map[string]any{"meteringEvent": map[string]any{"usage": 0.25}}))
	_, _ = stream.Write(exceptionFrame(t, "InternalServerException", `{"message":"boom"}`))

	var out bytes.Buffer
	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, &out, "claude-opus-4-7", 100, KiroRequestContext{})
	var exc *KiroUpstreamException
	require.ErrorAs(t, err, &exc)
	require.Equal(t, "InternalServerException", exc.Type)
	require.Contains(t, out.String(), "message_start")
	require.Contains(t, out.String(), "partial answer")

	// 已有输出：附上截至异常的用量，供调用方按流中断记账。
	require.NotNil(t, exc.PartialUsage)
	require.Equal(t, 100, exc.PartialUsage.InputTokens)
	require.Positive(t, exc.PartialUsage.OutputTokens)
	require.InDelta(t, 0.25, exc.PartialUsage.KiroCredits, 1e-9)
}

func TestStreamContentLengthExceededEndsWithMaxTokens(t *testing.T) {
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(textFrame(t, "long answer"))
	_, _ = stream.Write(exceptionFrame(t, KiroContentLengthExceededException, `{"message":"output too long"}`))

	var out bytes.Buffer
	result, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, &out, "claude-opus-4-7", 100, KiroRequestContext{})
	require.NoError(t, err)
	require.Equal(t, "max_tokens", result.StopReason)
	require.Contains(t, out.String(), `"stop_reason":"max_tokens"`)
	require.False(t, result.NoOutput)
}

func TestStreamErrorFrameIsUpstreamException(t *testing.T) {
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(buildHeaderEventStreamFrame(t, [][2]string{
		{":message-type", "error"},
		{":error-code", "InternalFailure"},
		{":error-message", "something broke"},
	}, nil))

	var out bytes.Buffer
	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, &out, "claude-opus-4-7", 100, KiroRequestContext{})
	var exc *KiroUpstreamException
	require.ErrorAs(t, err, &exc)
	require.Equal(t, "InternalFailure", exc.Type)
	require.Equal(t, "something broke", exc.Message)
	require.Zero(t, out.Len())
}

func TestUpstreamExceptionTypeFallsBackToPayloadType(t *testing.T) {
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(buildHeaderEventStreamFrame(t, [][2]string{{":message-type", "exception"}},
		[]byte(`{"__type":"com.amazon.aws.codewhisperer#ThrottlingException","message":"slow down"}`)))

	var out bytes.Buffer
	_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), stream, &out, "claude-opus-4-7", 100, KiroRequestContext{})
	var exc *KiroUpstreamException
	require.ErrorAs(t, err, &exc)
	require.Equal(t, "ThrottlingException", exc.Type)
	require.Equal(t, "slow down", exc.Message)
}

func TestStreamNoOutputFlag(t *testing.T) {
	empty := bytes.NewBuffer(nil)
	_, _ = empty.Write(buildEventStreamFrame(t, "contextUsageEvent", map[string]any{"contextUsagePercentage": 1.8}))
	var out bytes.Buffer
	result, err := StreamEventStreamAsAnthropicWithContext(context.Background(), empty, &out, "claude-opus-4-7", 100, KiroRequestContext{})
	require.NoError(t, err)
	require.True(t, result.NoOutput)

	withText := bytes.NewBuffer(nil)
	_, _ = withText.Write(textFrame(t, "hi"))
	out.Reset()
	result, err = StreamEventStreamAsAnthropicWithContext(context.Background(), withText, &out, "claude-opus-4-7", 100, KiroRequestContext{})
	require.NoError(t, err)
	require.False(t, result.NoOutput)
}

func TestNonStreamReturnsUpstreamException(t *testing.T) {
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(exceptionFrame(t, "ValidationException", `{"message":"Input is too long."}`))

	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-opus-4-7", KiroRequestContext{})
	require.Nil(t, result)
	var exc *KiroUpstreamException
	require.ErrorAs(t, err, &exc)
	require.Equal(t, "ValidationException", exc.Type)
}

func TestNonStreamContentLengthExceededEndsWithMaxTokens(t *testing.T) {
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(textFrame(t, "long answer"))
	_, _ = stream.Write(exceptionFrame(t, KiroContentLengthExceededException, `{"message":"output too long"}`))
	_, _ = stream.Write(buildEventStreamFrame(t, "messageMetadataEvent", map[string]any{"stopReason": "end_turn"}))

	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-opus-4-7", KiroRequestContext{})
	require.NoError(t, err)
	require.Equal(t, "max_tokens", result.StopReason)
	require.False(t, result.NoOutput)
}

func TestNonStreamNoOutputFlag(t *testing.T) {
	stream := bytes.NewBuffer(nil)
	_, _ = stream.Write(buildEventStreamFrame(t, "contextUsageEvent", map[string]any{"contextUsagePercentage": 1.8}))
	result, err := ParseNonStreamingEventStreamWithContext(stream, "claude-opus-4-7", KiroRequestContext{})
	require.NoError(t, err)
	require.True(t, result.NoOutput)
}
