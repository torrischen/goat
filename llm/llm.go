// Package llm defines goat's provider-neutral LLM client interface and call
// options. It is the seam between the agent loops (react, planexecute) and a
// concrete model provider. It depends only on agent/message and imports no
// provider SDK; concrete implementations live under llm/provider (e.g. the
// OpenAI Responses provider in llm/provider/openai).
package llm

import (
	"context"

	"github.com/torrischen/goat/agent/message"
)

// Client generates and streams model responses over goat's neutral message
// model.
type Client interface {
	// ModelID returns the underlying model identifier.
	ModelID() string
	// Generate returns the complete assistant message for the given input.
	Generate(ctx context.Context, messages []*message.Message, opts ...CallOption) (*message.Message, error)
	// Stream returns a reader that yields incremental assistant message chunks.
	// The caller accumulates chunks into the final message via message.Concat.
	Stream(ctx context.Context, messages []*message.Message, opts ...CallOption) (StreamReader, error)
}

// StreamReader yields incremental assistant message chunks. Recv returns io.EOF
// when the stream is exhausted, and Close releases resources.
type StreamReader interface {
	Recv() (*message.Message, error)
	Close()
}

// ToolDef is a function-tool definition passed to the model. Parameters holds a
// JSON Schema object (the same shape goat tools already produce).
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolChoice controls whether/which tools the model may call.
type ToolChoice string

const (
	// ToolChoiceAuto lets the model decide (default).
	ToolChoiceAuto ToolChoice = "auto"
	// ToolChoiceNone forbids tool calls; used for final-answer generation.
	ToolChoiceNone ToolChoice = "none"
	// ToolChoiceRequired forces at least one tool call.
	ToolChoiceRequired ToolChoice = "required"
)

// CallConfig is the resolved per-call configuration that options build up. A
// provider implementation reads it to construct the request.
type CallConfig struct {
	Tools          []ToolDef
	ToolChoice     ToolChoice
	PromptCacheKey string
}

// CallOption mutates a CallConfig.
type CallOption func(*CallConfig)

// ApplyOptions resolves a CallConfig from options. Intended for use by provider
// implementations.
func ApplyOptions(opts ...CallOption) CallConfig {
	cfg := CallConfig{ToolChoice: ToolChoiceAuto}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// WithTools sets the tools available for the call. Passing nil or an empty slice
// leaves the model with no tools.
func WithTools(tools []ToolDef) CallOption {
	return func(c *CallConfig) { c.Tools = tools }
}

// WithToolChoiceNone forbids tool calls for this call (final-answer generation).
func WithToolChoiceNone() CallOption {
	return func(c *CallConfig) { c.ToolChoice = ToolChoiceNone }
}

// WithToolChoice sets the tool-choice policy for this call.
func WithToolChoice(choice ToolChoice) CallOption {
	return func(c *CallConfig) { c.ToolChoice = choice }
}

// WithPromptCacheKey sets a stable prompt-cache key for the call. OpenAI
// Responses caches on this key; providers that do not support it ignore it.
func WithPromptCacheKey(key string) CallOption {
	return func(c *CallConfig) { c.PromptCacheKey = key }
}
