package kiro

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStreamToolsDeduplicateOnlyByID(t *testing.T) {
	for _, source := range []string{"stream", "snapshot", "stream_then_snapshot", "snapshot_then_stream"} {
		for _, sameID := range []bool{false, true} {
			name := source + "/distinct_ids"
			if sameID {
				name = source + "/same_id"
			}
			t.Run(name, func(t *testing.T) {
				var upstream bytes.Buffer
				expected := map[string]string{"first": `{"command":"pwd"}`}
				for i, id := range []string{"first", "second"} {
					if sameID {
						id = "first"
					}
					expected[id] = `{"command":"pwd"}`
					snapshot := source == "snapshot" || (source == "stream_then_snapshot" && i == 1) || (source == "snapshot_then_stream" && i == 0)
					if snapshot {
						upstream.Write(buildEventStreamFrame(t, "assistantResponseEvent", map[string]any{"toolUses": []map[string]any{
							{"toolUseId": id, "name": "Bash", "input": map[string]any{"command": "pwd"}},
						}}))
					} else {
						upstream.Write(buildEventStreamFrame(t, "toolUseEvent", map[string]any{
							"toolUseId": id, "name": "Bash", "input": `{"command":"pwd"}`, "stop": true,
						}))
					}
				}
				var output bytes.Buffer
				_, err := StreamEventStreamAsAnthropicWithContext(context.Background(), &upstream, &output, "model", 1, KiroRequestContext{})
				require.NoError(t, err)
				assertCompleteKiroToolStream(t, output.String(), expected)
			})
		}
	}
}

// Use genuine AWS headers and CRCs, including header-only unmodeled errors.
func buildAWSErrorFrame(t *testing.T, messageType, code, payload string) []byte {
	t.Helper()
	var headers, frame bytes.Buffer
	codeHeader := ":exception-type"
	if messageType == "error" {
		codeHeader = ":error-code"
	}
	for _, header := range [][2]string{
		{":content-type", "application/json"}, {codeHeader, code},
		{":message-type", messageType}, {":error-message", "secret request contents"},
	} {
		headers.WriteByte(byte(len(header[0])))
		headers.WriteString(header[0])
		headers.WriteByte(7)
		require.NoError(t, binary.Write(&headers, binary.BigEndian, uint16(len(header[1]))))
		headers.WriteString(header[1])
	}
	require.NoError(t, binary.Write(&frame, binary.BigEndian, uint32(16+headers.Len()+len(payload))))
	require.NoError(t, binary.Write(&frame, binary.BigEndian, uint32(headers.Len())))
	require.NoError(t, binary.Write(&frame, binary.BigEndian, crc32.ChecksumIEEE(frame.Bytes())))
	frame.Write(headers.Bytes())
	frame.WriteString(payload)
	require.NoError(t, binary.Write(&frame, binary.BigEndian, crc32.ChecksumIEEE(frame.Bytes())))
	return frame.Bytes()
}

func TestAWSErrorFramesNeverBecomeSuccessfulResponses(t *testing.T) {
	for _, tc := range []struct{ name, kind, code, payload, wantCode string }{
		{"modeled", "exception", "InternalServerException", `{"message":"secret request contents"}`, "InternalServerException"},
		{"header_only", "error", "InternalError", "", "InternalError"},
		{"invalid_payload", "exception", "ThrottlingException", "not-json", "ThrottlingException"},
		{"unsafe_code", "error", "secret request\ncontents", "", "unknown"},
		{"missing_code", "exception", "", "{}", "unknown"},
	} {
		for _, partial := range []bool{false, true} {
			name := tc.name + "/first_frame"
			if partial {
				name = tc.name + "/after_text"
			}
			t.Run(name, func(t *testing.T) {
				var upstream bytes.Buffer
				if partial {
					upstream.Write(buildEventStreamFrame(t, "assistantResponseEvent", map[string]any{"content": "working"}))
				}
				upstream.Write(buildAWSErrorFrame(t, tc.kind, tc.code, tc.payload))
				for _, stream := range []bool{false, true} {
					var err error
					var output bytes.Buffer
					if stream {
						result, streamErr := StreamEventStreamAsAnthropicWithContext(context.Background(), bytes.NewReader(upstream.Bytes()), &output, "model", 1, KiroRequestContext{})
						err = streamErr
						require.Nil(t, result)
						require.NotContains(t, output.String(), "event: message_stop")
					} else {
						result, parseErr := ParseNonStreamingEventStreamWithContext(bytes.NewReader(upstream.Bytes()), "model", KiroRequestContext{})
						err = parseErr
						require.Nil(t, result)
					}
					var upstreamError *EventStreamError
					require.ErrorAs(t, err, &upstreamError)
					require.Equal(t, tc.kind, upstreamError.MessageType)
					require.Equal(t, tc.wantCode, upstreamError.Code)
					require.False(t, strings.Contains(err.Error(), "secret"))
				}
			})
		}
	}
}
