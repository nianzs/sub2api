package zed

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAccumulatorEvents(t *testing.T) {
	cases := []struct {
		name   string
		events []string
		want   AccumulatedMessage
	}{
		{
			name: "message_start supplies id and model",
			events: []string{
				`{"type":"message_start","message":{"id":"msg_01","model":"claude-sonnet-4-5","role":"assistant","usage":{"input_tokens":12,"cache_read_input_tokens":3}}}`,
			},
			want: AccumulatedMessage{
				ID: "msg_01", Type: "message", Role: "assistant", Model: "claude-sonnet-4-5",
				Content: []MessageBlock{},
				Usage:   AccumulatedUsage{InputTokens: 12, CacheReadInputTokens: 3},
			},
		},
		{
			name: "text block accumulates deltas",
			events: []string{
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
				`{"type":"content_block_stop","index":0}`,
			},
			want: AccumulatedMessage{
				Type: "message", Role: "assistant", Model: "m",
				Content: []MessageBlock{{Type: "text", Text: "Hello world"}},
			},
		},
		{
			name: "thinking block keeps thinking and signature",
			events: []string{
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step 1"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`,
			},
			want: AccumulatedMessage{
				Type: "message", Role: "assistant", Model: "m",
				Content: []MessageBlock{{Type: "thinking", Thinking: "step 1", Signature: "sig123"}},
			},
		},
		{
			name: "tool_use input rebuilt from partial_json",
			events: []string{
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"/tmp/a\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
			},
			want: AccumulatedMessage{
				Type: "message", Role: "assistant", Model: "m",
				Content: []MessageBlock{{
					Type: "tool_use", ID: "toolu_1", Name: "read_file",
					Input: json.RawMessage(`{"path":"/tmp/a"}`),
				}},
			},
		},
		{
			name: "tool_use with no deltas falls back to empty object",
			events: []string{
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_2","name":"noop"}}`,
			},
			want: AccumulatedMessage{
				Type: "message", Role: "assistant", Model: "m",
				Content: []MessageBlock{{
					Type: "tool_use", ID: "toolu_2", Name: "noop", Input: json.RawMessage(`{}`),
				}},
			},
		},
		{
			name: "truncated partial_json falls back rather than emitting invalid JSON",
			events: []string{
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_3","name":"x"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`,
			},
			want: AccumulatedMessage{
				Type: "message", Role: "assistant", Model: "m",
				Content: []MessageBlock{{
					Type: "tool_use", ID: "toolu_3", Name: "x", Input: json.RawMessage(`{}`),
				}},
			},
		},
		{
			name: "message_delta supplies stop_reason and usage",
			events: []string{
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":57}}`,
				`{"type":"message_stop"}`,
			},
			want: AccumulatedMessage{
				Type: "message", Role: "assistant", Model: "m",
				Content:    []MessageBlock{},
				StopReason: strPtr("end_turn"),
				Usage:      AccumulatedUsage{OutputTokens: 57},
			},
		},
		{
			name: "message_delta zero input_tokens does not clobber message_start",
			events: []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":99}}}`,
				`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"input_tokens":0,"output_tokens":8}}`,
			},
			want: AccumulatedMessage{
				Type: "message", Role: "assistant", Model: "m",
				Content:    []MessageBlock{},
				StopReason: strPtr("max_tokens"),
				Usage:      AccumulatedUsage{InputTokens: 99, OutputTokens: 8},
			},
		},
		{
			name: "multiple blocks keep stream order",
			events: []string{
				`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"n"}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			},
			want: AccumulatedMessage{
				Type: "message", Role: "assistant", Model: "m",
				Content: []MessageBlock{
					{Type: "text", Text: "a"},
					{Type: "tool_use", ID: "t1", Name: "n", Input: json.RawMessage(`{}`)},
				},
			},
		},
		{
			name: "delta for unknown index is ignored",
			events: []string{
				`{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"orphan"}}`,
			},
			want: AccumulatedMessage{
				Type: "message", Role: "assistant", Model: "m",
				Content: []MessageBlock{},
			},
		},
		{
			name:   "malformed event is skipped",
			events: []string{`not json`},
			want: AccumulatedMessage{
				Type: "message", Role: "assistant", Model: "m",
				Content: []MessageBlock{},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := tc.want.Model
			acc := NewAccumulator(model)
			for _, ev := range tc.events {
				acc.AddEvent(json.RawMessage(ev))
			}
			assertMessageEqual(t, acc.Result(), &tc.want)
		})
	}
}

func TestAccumulateSSE(t *testing.T) {
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_9\",\"model\":\"gpt-5.6-sol\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		": comment line that is not data\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	got, err := AccumulateSSE(strings.NewReader(stream), "requested-model")
	if err != nil {
		t.Fatalf("AccumulateSSE() error = %v", err)
	}
	assertMessageEqual(t, got, &AccumulatedMessage{
		ID: "msg_9", Type: "message", Role: "assistant", Model: "gpt-5.6-sol",
		Content:    []MessageBlock{{Type: "text", Text: "ok"}},
		StopReason: strPtr("end_turn"),
		Usage:      AccumulatedUsage{InputTokens: 5, OutputTokens: 3},
	})
}

// The accumulator is fed by SSEReader in the relay; the two must compose.
func TestAccumulateSSEOverSSEReader(t *testing.T) {
	ndjson := `{"event":{"type":"message_start","message":{"id":"msg_x","model":"claude-opus-4-6"}}}` + "\n" +
		`{"type":"status_update","status":"working"}` + "\n" +
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n" +
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}` + "\n" +
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":4,"output_tokens":2}}` + "\n" +
		`{"type":"message_stop"}` + "\n"

	got, err := AccumulateSSE(NewSSEReader(strings.NewReader(ndjson), nil, nil), "claude-opus-4-6")
	if err != nil {
		t.Fatalf("AccumulateSSE() error = %v", err)
	}
	assertMessageEqual(t, got, &AccumulatedMessage{
		ID: "msg_x", Type: "message", Role: "assistant", Model: "claude-opus-4-6",
		Content:    []MessageBlock{{Type: "text", Text: "done"}},
		StopReason: strPtr("end_turn"),
		Usage:      AccumulatedUsage{InputTokens: 4, OutputTokens: 2},
	})
}

// A response with no stop_reason must still marshal stop_reason as null, which is
// what strict Anthropic clients expect rather than an absent key.
func TestAccumulatedMessageMarshalsNullStopReason(t *testing.T) {
	out, err := json.Marshal(NewAccumulator("m").Result())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(out), `"stop_reason":null`) {
		t.Errorf("stop_reason not null in %s", out)
	}
	if !strings.Contains(string(out), `"content":[]`) {
		t.Errorf("content not an empty array in %s", out)
	}
}

func assertMessageEqual(t *testing.T, got, want *AccumulatedMessage) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("message =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}

func strPtr(s string) *string { return &s }
