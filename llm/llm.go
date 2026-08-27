// Package llm defines goat's provider-neutral LLM client interface and model
// options. Concrete implementations live under llm/provider.
package llm

import (
	"context"

	"github.com/torrischen/goat/agent/message"
)

// Client generates and streams model responses over goat's neutral message
// model.
type Client interface {
	Generate(ctx context.Context, messages []*message.Message, opts ...Option) (*message.Message, error)
	Stream(ctx context.Context, messages []*message.Message, opts ...Option) (StreamReader, error)
}

// StreamReader yields incremental assistant message chunks. Recv returns io.EOF
// when the stream is exhausted, and Close releases resources.
type StreamReader interface {
	Recv() (*message.Message, error)
	Close()
}

// ToolDef is a function-tool definition passed to the model. Parameters holds a
// JSON Schema object.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolChoice controls whether or which tools the model may call.
type ToolChoice string

const (
	ToolChoiceAuto     ToolChoice = "auto"
	ToolChoiceNone     ToolChoice = "none"
	ToolChoiceRequired ToolChoice = "required"
)

// Config is the resolved model configuration. Options passed to Client methods
// are applied over the defaults supplied when the provider client is created.
type Config struct {
	Tools          []ToolDef
	ToolChoice     ToolChoice
	PromptCacheKey string

	// APIKey and BaseURL configure provider transport during client creation;
	// changing them on an individual model call has no effect.
	APIKey  string
	BaseURL string

	Model           string
	MaxOutputTokens int
	Temperature     *float64
	TopP            *float64

	// OpenAI-specific Responses API options. Other providers ignore them.
	MaxToolCalls              int
	ReasoningEffort           string
	ReasoningSummary          string
	ParallelToolCalls         *bool
	SafetyIdentifier          string
	ServiceTier               string
	Truncation                string
	IncludeEncryptedReasoning bool
}

// Option mutates model configuration. Both common and provider-specific
// settings use this type.
type Option func(*Config)

// ApplyOptions resolves Config from opts using package defaults.
func ApplyOptions(opts ...Option) Config {
	cfg := Config{
		ToolChoice:                ToolChoiceAuto,
		ReasoningSummary:          "auto",
		IncludeEncryptedReasoning: true,
	}
	ApplyOptionsTo(&cfg, opts...)
	return cfg
}

// ApplyOptionsTo applies opts over an existing Config.
func ApplyOptionsTo(cfg *Config, opts ...Option) {
	if cfg == nil {
		return
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
}

func WithTools(tools []ToolDef) Option {
	return func(c *Config) {
		c.Tools = tools
	}
}

func WithToolChoiceNone() Option {
	return func(c *Config) {
		c.ToolChoice = ToolChoiceNone
	}
}

func WithToolChoice(choice ToolChoice) Option {
	return func(c *Config) {
		c.ToolChoice = choice
	}
}

// WithPromptCacheKey sets a stable prompt-cache key. Providers without prompt
// caching ignore it.
func WithPromptCacheKey(key string) Option {
	return func(c *Config) {
		c.PromptCacheKey = key
	}
}

func WithAPIKey(key string) Option {
	return func(c *Config) {
		c.APIKey = key
	}
}

func WithBaseURL(url string) Option {
	return func(c *Config) {
		c.BaseURL = url
	}
}

func WithModel(model string) Option {
	return func(c *Config) {
		c.Model = model
	}
}

func WithMaxOutputTokens(n int) Option {
	return func(c *Config) {
		c.MaxOutputTokens = n
	}
}

func WithTemperature(temperature float64) Option {
	return func(c *Config) {
		c.Temperature = &temperature
	}
}

func WithTopP(topP float64) Option {
	return func(c *Config) {
		c.TopP = &topP
	}
}

// WithMaxToolCalls sets the OpenAI Responses API built-in tool-call limit.
func WithMaxToolCalls(n int) Option {
	return func(c *Config) {
		c.MaxToolCalls = n
	}
}

// WithReasoningEffort sets OpenAI reasoning effort.
func WithReasoningEffort(effort string) Option {
	return func(c *Config) {
		c.ReasoningEffort = effort
	}
}

// WithReasoningSummary controls OpenAI's visible reasoning summary.
func WithReasoningSummary(summary string) Option {
	return func(c *Config) {
		c.ReasoningSummary = summary
	}
}

// WithParallelToolCalls controls OpenAI parallel function calls.
func WithParallelToolCalls(enabled bool) Option {
	return func(c *Config) {
		c.ParallelToolCalls = &enabled
	}
}

// WithEncryptedReasoning controls OpenAI encrypted reasoning output.
func WithEncryptedReasoning(enabled bool) Option {
	return func(c *Config) {
		c.IncludeEncryptedReasoning = enabled
	}
}

// WithSafetyIdentifier sets OpenAI's stable safety-monitoring identifier.
func WithSafetyIdentifier(identifier string) Option {
	return func(c *Config) {
		c.SafetyIdentifier = identifier
	}
}

// WithServiceTier selects the OpenAI processing tier.
func WithServiceTier(tier string) Option {
	return func(c *Config) {
		c.ServiceTier = tier
	}
}

// WithTruncation selects OpenAI context-overflow behavior.
func WithTruncation(strategy string) Option {
	return func(c *Config) {
		c.Truncation = strategy
	}
}
