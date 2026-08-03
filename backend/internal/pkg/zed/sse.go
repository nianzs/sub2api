package zed

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// EventTransform converts one unwrapped upstream event into zero or more
// Anthropic SSE frames. Returning nil drops the event, which is how Zed's
// client-status messages (no Anthropic equivalent) are filtered out.
type EventTransform interface {
	Transform(event json.RawMessage) []byte
	// Finalize runs once at end of stream, for transforms that must synthesize
	// termination events the upstream protocol does not carry. The Anthropic
	// passthrough needs none; the Responses state machine does.
	Finalize() []byte
}

// anthropicStreamEventTypes are the Anthropic SSE event types forwarded verbatim.
// Anything outside this set is a Zed transport-level message rather than model
// output, and passing it on would put a frame the client cannot parse into an
// otherwise valid Anthropic stream.
var anthropicStreamEventTypes = map[string]bool{
	"message_start":       true,
	"message_delta":       true,
	"message_stop":        true,
	"content_block_start": true,
	"content_block_delta": true,
	"content_block_stop":  true,
	"ping":                true,
	"error":               true,
}

// AnthropicPassthroughTransform re-frames the upstream's native Anthropic events
// as SSE without touching their payloads, so usage and cache_control fields reach
// the caller's usage parser byte-for-byte.
type AnthropicPassthroughTransform struct{}

func (AnthropicPassthroughTransform) Transform(event json.RawMessage) []byte {
	eventType := EventType(event)
	if !anthropicStreamEventTypes[eventType] {
		return nil
	}
	return FormatSSE(eventType, event)
}

func (AnthropicPassthroughTransform) Finalize() []byte { return nil }

// FormatSSE renders one Anthropic SSE frame.
func FormatSSE(eventType string, data []byte) []byte {
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data))
}

// SSEReader adapts an upstream NDJSON body into an Anthropic SSE byte stream, so
// a caller holding an *http.Response can swap the body and reuse an existing
// Anthropic SSE consumer unchanged.
type SSEReader struct {
	src       *bufio.Reader
	closer    io.Closer
	transform EventTransform
	buf       bytes.Buffer
	drained   bool // upstream body fully read
	finalized bool // Finalize() already appended
	err       error
}

// NewSSEReader wraps an upstream NDJSON body. closer may be nil; when non-nil it
// is closed by Close so callers can hand ownership of the original body over.
func NewSSEReader(src io.Reader, closer io.Closer, transform EventTransform) *SSEReader {
	if transform == nil {
		transform = AnthropicPassthroughTransform{}
	}
	return &SSEReader{
		src:       bufio.NewReader(src),
		closer:    closer,
		transform: transform,
	}
}

func (r *SSEReader) Read(p []byte) (int, error) {
	for r.buf.Len() == 0 {
		if r.finalized {
			if r.err != nil {
				return 0, r.err
			}
			return 0, io.EOF
		}
		r.fill()
	}
	return r.buf.Read(p)
}

// fill consumes upstream lines until at least one produced output, or the body
// ends. Reading line-wise (rather than with a fixed-size scanner) keeps a single
// oversized event from failing the stream.
func (r *SSEReader) fill() {
	for r.buf.Len() == 0 && !r.finalized {
		if r.drained {
			if out := r.transform.Finalize(); len(out) > 0 {
				r.buf.Write(out)
			}
			r.finalized = true
			return
		}

		line, err := r.src.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if out := r.transform.Transform(UnwrapEvent(line)); len(out) > 0 {
				r.buf.Write(out)
			}
		}
		if err != nil {
			r.drained = true
			if err != io.EOF {
				r.err = err
			}
		}
	}
}

func (r *SSEReader) Close() error {
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}
