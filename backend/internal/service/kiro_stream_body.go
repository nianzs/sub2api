package service

import (
	"context"
	"io"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// Track the AWS transport, including partial EventStream frames that have not
// produced an Anthropic SSE block yet.
type kiroActivityReader struct {
	io.ReadCloser
	lastReadAt atomic.Int64
}

func (r *kiroActivityReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.lastReadAt.Store(time.Now().UnixNano())
	}
	return n, err
}

type kiroAnthropicStreamBody struct {
	*io.PipeReader
	upstream *kiroActivityReader
}

func (b *kiroAnthropicStreamBody) lastUpstreamReadAt() time.Time {
	return time.Unix(0, b.upstream.lastReadAt.Load())
}

func (b *kiroAnthropicStreamBody) Close() error {
	err := b.PipeReader.Close()
	// Also unblock the converter if it is waiting on a stalled AWS response.
	_ = b.upstream.Close()
	return err
}

func newKiroAnthropicStreamBody(ctx context.Context, body io.ReadCloser, model string, inputTokens int, requestCtx kiro.KiroRequestContext) io.ReadCloser {
	upstream := &kiroActivityReader{ReadCloser: body}
	upstream.lastReadAt.Store(time.Now().UnixNano())
	pr, pw := io.Pipe()
	go func() {
		defer upstream.Close()
		_, err := kiro.StreamEventStreamAsAnthropicWithContext(ctx, upstream, pw, model, inputTokens, requestCtx)
		if err != nil {
			_, _ = io.WriteString(pw, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"stream interrupted\"}}\n\n")
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	return &kiroAnthropicStreamBody{PipeReader: pr, upstream: upstream}
}
