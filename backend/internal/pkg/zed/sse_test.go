package zed

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestSSEReaderAnthropicPassthrough(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare and wrapped events both re-framed",
			in: `{"type":"message_start","message":{"id":"msg_1"}}` + "\n" +
				`{"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}}` + "\n" +
				`{"type":"message_stop"}` + "\n",
			want: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		},
		{
			name: "zed transport messages are dropped",
			in: `{"type":"status_update","status":"started"}` + "\n" +
				`{"event":"stream_ended"}` + "\n" +
				`{"type":"message_stop"}` + "\n",
			want: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		},
		{
			name: "blank lines and missing trailing newline tolerated",
			in:   "\n\n" + `{"type":"ping"}`,
			want: "event: ping\ndata: {\"type\":\"ping\"}\n\n",
		},
		{
			name: "empty body yields empty stream",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := io.ReadAll(NewSSEReader(strings.NewReader(tc.in), nil, nil))
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if string(out) != tc.want {
				t.Errorf("SSEReader output =\n%q\nwant\n%q", out, tc.want)
			}
		})
	}
}

// usage must survive byte-for-byte: it is the billing source for the relay.
func TestSSEReaderPreservesUsagePayload(t *testing.T) {
	in := `{"event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},` +
		`"usage":{"output_tokens":42,"cache_read_input_tokens":7}}}` + "\n"

	out, err := io.ReadAll(NewSSEReader(strings.NewReader(in), nil, nil))
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !strings.Contains(string(out), `"usage":{"output_tokens":42,"cache_read_input_tokens":7}`) {
		t.Errorf("usage payload altered: %q", out)
	}
}

type stubTransform struct {
	finalize string
}

func (s *stubTransform) Transform(event json.RawMessage) []byte {
	if EventType(event) == "skip" {
		return nil
	}
	return FormatSSE("converted", event)
}

func (s *stubTransform) Finalize() []byte { return []byte(s.finalize) }

func TestSSEReaderInjectableTransformAndFinalize(t *testing.T) {
	in := `{"type":"skip"}` + "\n" + `{"type":"keep"}` + "\n"
	reader := NewSSEReader(strings.NewReader(in), nil, &stubTransform{
		finalize: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	})

	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	want := "event: converted\ndata: {\"type\":\"keep\"}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	if string(out) != want {
		t.Errorf("output =\n%q\nwant\n%q", out, want)
	}
}

// A short p must not lose bytes: the passthrough writes into a fixed-size buffer.
func TestSSEReaderHonoursShortReads(t *testing.T) {
	in := `{"type":"message_stop"}` + "\n"
	reader := NewSSEReader(strings.NewReader(in), nil, nil)

	var got []byte
	p := make([]byte, 3)
	for {
		n, err := reader.Read(p)
		got = append(got, p[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
	}
	want := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	if string(got) != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

type recordingCloser struct{ closed bool }

func (c *recordingCloser) Close() error {
	c.closed = true
	return nil
}

func TestSSEReaderCloseClosesUpstream(t *testing.T) {
	closer := &recordingCloser{}
	reader := NewSSEReader(strings.NewReader(""), closer, nil)
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !closer.closed {
		t.Error("Close() did not close the upstream body")
	}
}
