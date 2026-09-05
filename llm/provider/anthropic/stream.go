package anthropic

import (
	"encoding/json"
	"io"
	"strings"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/torrischen/goat/agent/message"
)

// streamReader adapts the Anthropic Messages SSE stream to llm.StreamReader.
// Text and thinking deltas are emitted immediately. Tool-use input is assembled
// from input_json_delta events and emitted once the content block stops.
type streamReader struct {
	stream *ssestream.Stream[anthropicapi.MessageStreamEventUnion]
	blocks map[int64]*streamBlock
	usage  streamUsage
	done   bool
}

type streamBlock struct {
	kind      string
	id        string
	name      string
	arguments string
	signature string
	data      string
}

type streamUsage struct {
	inputTokens              int64
	cacheCreationInputTokens int64
	cacheReadInputTokens     int64
	outputTokens             int64
	seen                     bool
}

func newStreamReader(stream *ssestream.Stream[anthropicapi.MessageStreamEventUnion]) *streamReader {
	return &streamReader{
		stream: stream,
		blocks: make(map[int64]*streamBlock),
	}
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
	}
}

func (r *streamReader) Close() {
	if r.stream != nil {
		_ = r.stream.Close()
	}
}

func (r *streamReader) mapEvent(ev anthropicapi.MessageStreamEventUnion) (*message.Message, error) {
	switch ev.Type {
	case "message_start":
		r.usage.applyMessage(ev.Message.Usage)
		return nil, nil
	case "content_block_start":
		return r.mapBlockStart(ev.Index, ev.ContentBlock), nil
	case "content_block_delta":
		return r.mapBlockDelta(ev.Index, ev.Delta), nil
	case "content_block_stop":
		return r.mapBlockStop(ev.Index), nil
	case "message_delta":
		r.usage.applyDelta(ev.Usage)
		return r.usageMessage(), nil
	case "message_stop":
		r.done = true
		if r.usage.seen {
			return r.usageMessage(), nil
		}
		return &message.Message{Role: message.RoleAssistant}, nil
	default:
		return nil, nil
	}
}

func (r *streamReader) mapBlockStart(index int64, block anthropicapi.ContentBlockStartEventContentBlockUnion) *message.Message {
	state := &streamBlock{
		kind:      block.Type,
		id:        block.ID,
		name:      block.Name,
		signature: block.Signature,
		data:      block.Data,
		arguments: initialToolInput(block.Input),
	}
	r.blocks[index] = state

	switch block.Type {
	case "text":
		if block.Text != "" {
			return textChunk(block.Text)
		}
	case "thinking":
		if block.Thinking != "" {
			return reasoningTextChunk(block.Thinking)
		}
	}
	return nil
}

func (r *streamReader) mapBlockDelta(index int64, delta anthropicapi.MessageStreamEventUnionDelta) *message.Message {
	switch delta.Type {
	case "text_delta":
		if delta.Text != "" {
			return textChunk(delta.Text)
		}
	case "thinking_delta":
		state := r.ensureBlock(index, "thinking")
		state.kind = "thinking"
		if delta.Thinking != "" {
			return reasoningTextChunk(delta.Thinking)
		}
	case "signature_delta":
		state := r.ensureBlock(index, "thinking")
		state.kind = "thinking"
		state.signature += delta.Signature
		if state.signature != "" {
			return reasoningSignatureChunk(state.signature)
		}
	case "input_json_delta":
		state := r.ensureBlock(index, "tool_use")
		state.kind = "tool_use"
		state.arguments += delta.PartialJSON
	}
	return nil
}

func (r *streamReader) mapBlockStop(index int64) *message.Message {
	state := r.blocks[index]
	delete(r.blocks, index)
	if state == nil {
		return nil
	}

	switch state.kind {
	case "tool_use":
		arguments := state.arguments
		if arguments == "" {
			arguments = "{}"
		}
		return &message.Message{
			Role: message.RoleAssistant,
			Blocks: []*message.ContentBlock{{
				Kind: message.BlockToolCall,
				ToolCall: &message.ToolCall{
					CallID:    state.id,
					Name:      state.name,
					Arguments: arguments,
				},
			}},
		}
	case "thinking", "redacted_thinking":
		if state.data != "" {
			return &message.Message{
				Role: message.RoleAssistant,
				Blocks: []*message.ContentBlock{{
					Kind:      message.BlockReasoning,
					Reasoning: &message.ReasoningData{},
					Provider:  encodeRedactedThinkingMeta(state.data),
				}},
			}
		}
		if state.signature != "" {
			return &message.Message{
				Role: message.RoleAssistant,
				Blocks: []*message.ContentBlock{{
					Kind: message.BlockReasoning,
					Reasoning: &message.ReasoningData{
						Signature: state.signature,
					},
				}},
			}
		}
	}
	return nil
}

func (r *streamReader) ensureBlock(index int64, kind string) *streamBlock {
	if block := r.blocks[index]; block != nil {
		return block
	}
	block := &streamBlock{kind: kind}
	r.blocks[index] = block
	return block
}

func initialToolInput(input any) string {
	if input == nil {
		return ""
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return ""
	}
	return trimmed
}

func (u *streamUsage) applyMessage(usage anthropicapi.Usage) {
	u.inputTokens = usage.InputTokens
	u.cacheCreationInputTokens = usage.CacheCreationInputTokens
	u.cacheReadInputTokens = usage.CacheReadInputTokens
	u.outputTokens = usage.OutputTokens
	u.seen = true
}

func (u *streamUsage) applyDelta(usage anthropicapi.MessageDeltaUsage) {
	if usage.InputTokens != 0 {
		u.inputTokens = usage.InputTokens
	}
	if usage.CacheCreationInputTokens != 0 {
		u.cacheCreationInputTokens = usage.CacheCreationInputTokens
	}
	if usage.CacheReadInputTokens != 0 {
		u.cacheReadInputTokens = usage.CacheReadInputTokens
	}
	u.outputTokens = usage.OutputTokens
	u.seen = true
}

func (u streamUsage) messageUsage() *message.Usage {
	return &message.Usage{
		PromptTokens:     int(u.inputTokens + u.cacheCreationInputTokens + u.cacheReadInputTokens),
		CompletionTokens: int(u.outputTokens),
		CachedTokens:     int(u.cacheReadInputTokens),
	}
}

func (r *streamReader) usageMessage() *message.Message {
	return &message.Message{
		Role: message.RoleAssistant,
		Meta: &message.ResponseMeta{Usage: r.usage.messageUsage()},
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

func reasoningSignatureChunk(signature string) *message.Message {
	return &message.Message{
		Role: message.RoleAssistant,
		Blocks: []*message.ContentBlock{{
			Kind: message.BlockReasoning,
			Reasoning: &message.ReasoningData{
				Signature: signature,
			},
		}},
	}
}
