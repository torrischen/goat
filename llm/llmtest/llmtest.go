// Package llmtest provides test doubles for llm.Client and llm.StreamReader.
//
// It replaces the ad-hoc eino model.AgenticModel mocks and
// schema.StreamReaderFromArray helper that the agent tests previously relied on.
package llmtest

import (
	"context"
	"io"
	"sync"

	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
)

// SliceStreamReader yields a fixed slice of message chunks, then io.EOF. It
// mirrors the streaming consumption contract the agent loops expect.
type SliceStreamReader struct {
	chunks []*message.Message
	index  int
}

// NewStreamReader builds a StreamReader that replays the given chunks in order.
func NewStreamReader(chunks []*message.Message) *SliceStreamReader {
	return &SliceStreamReader{chunks: chunks}
}

func (r *SliceStreamReader) Recv() (*message.Message, error) {
	if r.index >= len(r.chunks) {
		return nil, io.EOF
	}
	msg := r.chunks[r.index]
	r.index++
	return msg, nil
}

func (r *SliceStreamReader) Close() {}

// Mock is a configurable llm.Client test double. Each call to Generate/Stream
// consumes the next scripted response; inputs are recorded for assertions.
//
// GenerateFunc/StreamFunc, when set, override the scripted behavior and receive
// the call index (0-based). Use them for blocking or input-dependent mocks.
type Mock struct {
	ModelIDValue string

	// GenerateResponses supplies one assistant message per Generate call.
	GenerateResponses []*message.Message
	// StreamResponses supplies one chunk slice per Stream call.
	StreamResponses [][]*message.Message

	// GenerateFunc overrides scripted Generate behavior when non-nil.
	GenerateFunc func(call int, messages []*message.Message, cfg llm.Config) (*message.Message, error)
	// StreamFunc overrides scripted Stream behavior when non-nil.
	StreamFunc func(call int, messages []*message.Message, cfg llm.Config) (llm.StreamReader, error)

	mu           sync.Mutex
	generateCall int
	streamCall   int
	genInputs    [][]*message.Message
	streamInputs [][]*message.Message
}

var _ llm.Client = (*Mock)(nil)

func (m *Mock) ModelID() string {
	if m.ModelIDValue == "" {
		return "mock-model"
	}
	return m.ModelIDValue
}

func (m *Mock) Generate(ctx context.Context, messages []*message.Message, opts ...llm.Option) (*message.Message, error) {
	cfg := llm.ApplyOptions(opts...)
	m.mu.Lock()
	call := m.generateCall
	m.generateCall++
	m.genInputs = append(m.genInputs, messages)
	fn := m.GenerateFunc
	var resp *message.Message
	if fn == nil && call < len(m.GenerateResponses) {
		resp = m.GenerateResponses[call]
	}
	m.mu.Unlock()

	if fn != nil {
		return fn(call, messages, cfg)
	}
	return resp, nil
}

func (m *Mock) Stream(ctx context.Context, messages []*message.Message, opts ...llm.Option) (llm.StreamReader, error) {
	cfg := llm.ApplyOptions(opts...)
	m.mu.Lock()
	call := m.streamCall
	m.streamCall++
	m.streamInputs = append(m.streamInputs, messages)
	fn := m.StreamFunc
	var chunks []*message.Message
	if fn == nil && call < len(m.StreamResponses) {
		chunks = m.StreamResponses[call]
	}
	m.mu.Unlock()

	if fn != nil {
		return fn(call, messages, cfg)
	}
	return NewStreamReader(chunks), nil
}

// GenerateInputs returns the message slices passed to each Generate call.
func (m *Mock) GenerateInputs() [][]*message.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]*message.Message(nil), m.genInputs...)
}

// StreamInputs returns the message slices passed to each Stream call.
func (m *Mock) StreamInputs() [][]*message.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]*message.Message(nil), m.streamInputs...)
}

// WithUsage attaches token usage to an assistant message for scripted responses.
func WithUsage(msg *message.Message, prompt, cached, completion int) *message.Message {
	if msg.Meta == nil {
		msg.Meta = &message.ResponseMeta{}
	}
	msg.Meta.Usage = &message.Usage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		CachedTokens:     cached,
	}
	return msg
}

// ToolCallMessage builds an assistant message with a single function tool-call
// block, matching how the react loop reads model tool calls.
func ToolCallMessage(callID, name, arguments string) *message.Message {
	return &message.Message{
		Role: message.RoleAssistant,
		Blocks: []*message.ContentBlock{{
			Kind: message.BlockToolCall,
			ToolCall: &message.ToolCall{
				CallID:    callID,
				Name:      name,
				Arguments: arguments,
			},
		}},
	}
}
