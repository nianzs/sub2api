package zed

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// AccumulatedUsage carries the token counts observed on the stream.
type AccumulatedUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// AccumulatedMessage is a non-streaming Anthropic Messages response rebuilt from
// a stream. Zed has no non-streaming upstream mode, so a client that asked for
// one is served by collecting the stream into this shape.
type AccumulatedMessage struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Model        string           `json:"model"`
	Content      []MessageBlock   `json:"content"`
	StopReason   *string          `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence"`
	Usage        AccumulatedUsage `json:"usage"`
}

// MessageBlock is one content block of an accumulated response.
type MessageBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// Accumulator folds Anthropic stream events into a single Messages response.
type Accumulator struct {
	msg    AccumulatedMessage
	blocks []*accumulatingBlock
	// index of blocks by their stream "index", since content_block_delta
	// addresses blocks by index and gaps/reordering are permitted by the protocol.
	byIndex map[int]*accumulatingBlock
}

type accumulatingBlock struct {
	typ       string
	text      strings.Builder
	thinking  strings.Builder
	signature strings.Builder
	partial   strings.Builder
	id        string
	name      string
	input     json.RawMessage
}

// NewAccumulator returns an Accumulator with the response scaffold already set,
// so a stream that never sends message_start still yields a valid response.
func NewAccumulator(model string) *Accumulator {
	return &Accumulator{
		msg: AccumulatedMessage{
			Type:  "message",
			Role:  "assistant",
			Model: model,
		},
		byIndex: make(map[int]*accumulatingBlock),
	}
}

// AddEvent folds one Anthropic stream event (the SSE data payload) into the
// response. Unknown event types are ignored.
func (a *Accumulator) AddEvent(event json.RawMessage) {
	var e struct {
		Type    string `json:"type"`
		Index   *int   `json:"index"`
		Message *struct {
			ID    string            `json:"id"`
			Model string            `json:"model"`
			Role  string            `json:"role"`
			Usage *AccumulatedUsage `json:"usage"`
		} `json:"message"`
		ContentBlock *struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Text  string          `json:"text"`
			Input json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta *struct {
			Type        string  `json:"type"`
			Text        string  `json:"text"`
			Thinking    string  `json:"thinking"`
			Signature   string  `json:"signature"`
			PartialJSON string  `json:"partial_json"`
			StopReason  *string `json:"stop_reason"`
			StopSeq     *string `json:"stop_sequence"`
		} `json:"delta"`
		Usage *AccumulatedUsage `json:"usage"`
	}
	if err := json.Unmarshal(event, &e); err != nil {
		return
	}

	switch e.Type {
	case "message_start":
		if e.Message == nil {
			return
		}
		if e.Message.ID != "" {
			a.msg.ID = e.Message.ID
		}
		if e.Message.Model != "" {
			a.msg.Model = e.Message.Model
		}
		if e.Message.Role != "" {
			a.msg.Role = e.Message.Role
		}
		if e.Message.Usage != nil {
			a.msg.Usage = *e.Message.Usage
		}

	case "content_block_start":
		if e.ContentBlock == nil || e.Index == nil {
			return
		}
		block := &accumulatingBlock{
			typ:   e.ContentBlock.Type,
			id:    e.ContentBlock.ID,
			name:  e.ContentBlock.Name,
			input: e.ContentBlock.Input,
		}
		// Anthropic may seed a text block with an initial non-empty text.
		block.text.WriteString(e.ContentBlock.Text)
		a.blocks = append(a.blocks, block)
		a.byIndex[*e.Index] = block

	case "content_block_delta":
		if e.Delta == nil || e.Index == nil {
			return
		}
		block, ok := a.byIndex[*e.Index]
		if !ok {
			return
		}
		block.text.WriteString(e.Delta.Text)
		block.thinking.WriteString(e.Delta.Thinking)
		block.signature.WriteString(e.Delta.Signature)
		block.partial.WriteString(e.Delta.PartialJSON)

	case "message_delta":
		if e.Delta != nil {
			if e.Delta.StopReason != nil && *e.Delta.StopReason != "" {
				a.msg.StopReason = e.Delta.StopReason
			}
			if e.Delta.StopSeq != nil {
				a.msg.StopSequence = e.Delta.StopSeq
			}
		}
		if e.Usage != nil {
			// message_delta carries the authoritative output_tokens; input side is
			// only reported on some upstreams, so zero values must not clobber
			// what message_start already established.
			a.msg.Usage.OutputTokens = e.Usage.OutputTokens
			if e.Usage.InputTokens > 0 {
				a.msg.Usage.InputTokens = e.Usage.InputTokens
			}
			if e.Usage.CacheCreationInputTokens > 0 {
				a.msg.Usage.CacheCreationInputTokens = e.Usage.CacheCreationInputTokens
			}
			if e.Usage.CacheReadInputTokens > 0 {
				a.msg.Usage.CacheReadInputTokens = e.Usage.CacheReadInputTokens
			}
		}
	}
}

// Result finalizes and returns the accumulated response.
func (a *Accumulator) Result() *AccumulatedMessage {
	out := a.msg
	out.Content = make([]MessageBlock, 0, len(a.blocks))
	for _, block := range a.blocks {
		out.Content = append(out.Content, block.finish())
	}
	return &out
}

func (b *accumulatingBlock) finish() MessageBlock {
	out := MessageBlock{Type: b.typ}
	switch b.typ {
	case "thinking":
		out.Thinking = b.thinking.String()
		out.Signature = b.signature.String()
	case "tool_use":
		out.ID = b.id
		out.Name = b.name
		// tool_use input arrives as input_json_delta fragments; an unparseable or
		// absent accumulation must still yield a valid JSON object.
		out.Input = json.RawMessage("{}")
		if partial := strings.TrimSpace(b.partial.String()); partial != "" && json.Valid([]byte(partial)) {
			out.Input = json.RawMessage(partial)
		} else if len(b.input) > 0 && json.Valid(b.input) {
			out.Input = b.input
		}
	default:
		out.Text = b.text.String()
	}
	return out
}

// AccumulateSSE reads an Anthropic SSE stream and folds it into one response.
// It is the non-streaming counterpart to handing SSEReader straight to a client.
func AccumulateSSE(r io.Reader, model string) (*AccumulatedMessage, error) {
	acc := NewAccumulator(model)
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if data, ok := sseDataPayload(line); ok {
			acc.AddEvent(data)
		}
		if err != nil {
			if err == io.EOF {
				return acc.Result(), nil
			}
			return acc.Result(), err
		}
	}
}

// sseDataPayload extracts the JSON payload of a `data:` SSE line.
func sseDataPayload(line []byte) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil, false
	}
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if len(payload) == 0 || payload[0] != '{' {
		return nil, false
	}
	return payload, true
}
