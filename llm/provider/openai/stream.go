package openai

import (
	"io"

	"github.com/torrischen/goat/agent/message"

	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

// streamReader adapts the SDK's Responses SSE stream to llm.StreamReader.
//
// Text and reasoning arrive as incremental deltas, emitted one chunk each so the
// agent loop can render them live; message.Concat reassembles them. Tool calls
// and reasoning round-trip metadata (item id + encrypted content) are emitted
// once, when their output item completes. The terminal event carries usage.
type streamReader struct {
	stream *ssestream.Stream[responses.ResponseStreamEventUnion]
	done   bool
}

func newStreamReader(stream *ssestream.Stream[responses.ResponseStreamEventUnion]) *streamReader {
	return &streamReader{stream: stream}
}

func (r *streamReader) Recv() (*message.Message, error) {
	for {
		if r.done {
			return nil, io.EOF
		}
		if !r.stream.Next() {
			r.done = true
			if err := r.stream.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		msg, err := r.mapEvent(r.stream.Current())
		if err != nil {
			r.done = true
			return nil, err
		}
		if msg != nil {
			return msg, nil
		}
		// Event carried nothing the agent loop consumes; keep reading.
	}
}

func (r *streamReader) Close() {
	_ = r.stream.Close()
}

// mapEvent converts one stream event into an incremental message, or nil when
// the event carries no state the agent loop needs. A terminal error event
// returns a non-nil error.
func (r *streamReader) mapEvent(ev responses.ResponseStreamEventUnion) (*message.Message, error) {
	switch ev.Type {
	case "response.output_text.delta":
		if ev.Delta == "" {
			return nil, nil
		}
		return textChunk(ev.Delta), nil

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if ev.Delta == "" {
			return nil, nil
		}
		return reasoningTextChunk(ev.Delta), nil

	case "response.output_item.done":
		return r.mapCompletedItem(ev.Item), nil

	case "response.completed", "response.incomplete":
		// Terminal: carry final usage so Concat folds it into the response.
		return &message.Message{
			Role: message.RoleAssistant,
			Meta: &message.ResponseMeta{Usage: decodeUsage(ev.Response.Usage)},
		}, nil

	case "response.failed":
		return nil, responseFailedError(ev.Response)

	case "error":
		return nil, streamError(ev.Code, ev.Message)

	default:
		return nil, nil
	}
}

// mapCompletedItem emits the round-trip block for a finished output item:
// reasoning metadata (item id + encrypted content) or a fully-assembled tool
// call. Text-message items are ignored here since their text already streamed
// as deltas.
func (r *streamReader) mapCompletedItem(item responses.ResponseOutputItemUnion) *message.Message {
	switch item.Type {
	case "reasoning":
		reasoning := item.AsReasoning()
		meta := encodeReasoningMeta(reasoning.ID, reasoning.EncryptedContent)
		if meta == nil {
			return nil
		}
		// Empty text: the summary text already streamed as reasoning deltas. This
		// chunk exists only to attach round-trip metadata onto the reasoning block.
		return &message.Message{
			Role: message.RoleAssistant,
			Blocks: []*message.ContentBlock{{
				Kind:      message.BlockReasoning,
				Reasoning: &message.ReasoningData{},
				Provider:  meta,
			}},
		}
	case "function_call":
		call := item.AsFunctionCall()
		return &message.Message{
			Role: message.RoleAssistant,
			Blocks: []*message.ContentBlock{{
				Kind: message.BlockToolCall,
				ToolCall: &message.ToolCall{
					CallID:    call.CallID,
					Name:      call.Name,
					Arguments: call.Arguments,
				},
			}},
		}
	default:
		return nil
	}
}

func textChunk(text string) *message.Message {
	return &message.Message{
		Role:   message.RoleAssistant,
		Blocks: []*message.ContentBlock{message.TextBlock(text)},
	}
}

func reasoningTextChunk(text string) *message.Message {
	return &message.Message{
		Role:   message.RoleAssistant,
		Blocks: []*message.ContentBlock{message.ReasoningBlock(text)},
	}
}
