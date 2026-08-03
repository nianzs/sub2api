package zed

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// UnwrapEvent normalizes one upstream line.
//
// cloud.zed.dev responds with NDJSON — one bare JSON object per line, not
// `data:`-prefixed SSE. Some lines arrive wrapped as {"event": {...}}; the inner
// object is the real provider event, so it is unwrapped here. Lines that are not
// wrapped are returned unchanged.
func UnwrapEvent(line []byte) json.RawMessage {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return trimmed
	}

	var probe struct {
		Event json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return trimmed
	}
	if inner := bytes.TrimSpace(probe.Event); len(inner) > 0 && inner[0] == '{' {
		return inner
	}
	return trimmed
}

// EventType reads the "type" discriminator from an unwrapped event, returning ""
// when absent (as on OpenAI Chat-style chunks that use "choices" instead).
func EventType(event json.RawMessage) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(event, &probe); err != nil {
		return ""
	}
	return probe.Type
}

// NewLineScanner builds a scanner over an upstream NDJSON body using the
// caller's buffer, so callers can supply a pooled one.
func NewLineScanner(r io.Reader, buf []byte, maxLineSize int) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(buf, maxLineSize)
	return scanner
}
