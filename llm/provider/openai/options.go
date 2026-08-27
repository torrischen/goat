package openai

import "github.com/openai/openai-go/v3/responses"

import "github.com/openai/openai-go/v3/shared"

// options contains provider-level configuration for the OpenAI Responses API.
type options struct {
	apiKey                    string
	baseURL                   string
	maxOutputTokens           int64
	maxToolCalls              int64
	reasoningEffort           string
	reasoningSummary          shared.ReasoningSummary
	temperature               *float64
	topP                      *float64
	parallelToolCalls         *bool
	safetyIdentifier          string
	serviceTier               responses.ResponseNewParamsServiceTier
	truncation                responses.ResponseNewParamsTruncation
	includeEncryptedReasoning bool
}

// Option configures the OpenAI provider.
type Option func(*options)

// WithAPIKey sets the API key. When unset, OPENAI_API_KEY is used.
func WithAPIKey(key string) Option { return func(o *options) { o.apiKey = key } }

// WithBaseURL overrides the API base URL (e.g. an Azure or proxy endpoint).
func WithBaseURL(url string) Option { return func(o *options) { o.baseURL = url } }

// WithMaxOutputTokens caps the number of generated tokens, including reasoning
// tokens. Zero leaves the model default.
func WithMaxOutputTokens(n int) Option { return func(o *options) { o.maxOutputTokens = int64(n) } }

// WithMaxToolCalls limits the number of built-in tool calls in one response.
func WithMaxToolCalls(n int) Option { return func(o *options) { o.maxToolCalls = int64(n) } }

// WithReasoningEffort sets the reasoning effort for reasoning models. Supported
// values include low, medium, and high. Empty leaves the model default.
func WithReasoningEffort(effort string) Option {
	return func(o *options) { o.reasoningEffort = effort }
}

// WithReasoningSummary controls the visible reasoning summary returned by the
// Responses API. Supported values include auto, concise, and detailed.
func WithReasoningSummary(summary string) Option {
	return func(o *options) { o.reasoningSummary = shared.ReasoningSummary(summary) }
}

// WithTemperature sets sampling temperature. It is omitted when reasoning
// effort is configured because reasoning models reject this parameter.
func WithTemperature(t float64) Option { return func(o *options) { o.temperature = &t } }

// WithTopP sets nucleus sampling. Do not combine it with WithTemperature.
func WithTopP(topP float64) Option { return func(o *options) { o.topP = &topP } }

// WithParallelToolCalls controls whether the model may emit parallel tool calls.
func WithParallelToolCalls(enabled bool) Option {
	return func(o *options) { o.parallelToolCalls = &enabled }
}

// WithEncryptedReasoning toggles requesting reasoning.encrypted_content. It is
// on by default; pass false for non-reasoning models.
func WithEncryptedReasoning(enabled bool) Option {
	return func(o *options) { o.includeEncryptedReasoning = enabled }
}

// WithSafetyIdentifier sets the stable identifier used for safety monitoring.
func WithSafetyIdentifier(identifier string) Option {
	return func(o *options) { o.safetyIdentifier = identifier }
}

// WithServiceTier selects the processing tier, such as auto, default, flex, or priority.
func WithServiceTier(tier string) Option {
	return func(o *options) { o.serviceTier = responses.ResponseNewParamsServiceTier(tier) }
}

// WithTruncation selects the context overflow behavior: auto or disabled.
func WithTruncation(strategy string) Option {
	return func(o *options) { o.truncation = responses.ResponseNewParamsTruncation(strategy) }
}
