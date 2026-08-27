package openai

import (
	"context"
	"os"

	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// client implements llm.Client against the OpenAI Responses API.
type client struct {
	client oai.Client
	opts   options
}

var _ llm.Client = (*client)(nil)

// New constructs an OpenAI Responses provider as an llm.Client.
func New(opts ...Option) llm.Client {
	o := options{
		includeEncryptedReasoning: true,
		reasoningSummary:          shared.ReasoningSummaryAuto,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.apiKey == "" {
		o.apiKey = os.Getenv("OPENAI_API_KEY")
	}

	reqOpts := []option.RequestOption{option.WithAPIKey(o.apiKey)}
	if o.baseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(o.baseURL))
	}

	return &client{
		client: oai.NewClient(reqOpts...),
		opts:   o,
	}
}

func (c *client) Generate(ctx context.Context, messages []*message.Message, opts ...llm.CallOption) (*message.Message, error) {
	params := c.buildParams(messages, opts...)
	resp, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return nil, err
	}
	return decodeResponse(resp), nil
}

func (c *client) Stream(ctx context.Context, messages []*message.Message, opts ...llm.CallOption) (llm.StreamReader, error) {
	params := c.buildParams(messages, opts...)
	stream := c.client.Responses.NewStreaming(ctx, params)
	return newStreamReader(stream), nil
}

// buildParams translates goat messages and call options into Responses request
// params. The system prompt is lifted into the "instructions" field.
func (c *client) buildParams(messages []*message.Message, opts ...llm.CallOption) responses.ResponseNewParams {
	cfg := llm.ApplyOptions(opts...)
	system, rest := splitSystem(messages)

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(c.opts.model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: encodeInput(rest)},
		// Stateless operation: replay the full conversation each turn.
		Store: param.NewOpt(false),
	}
	if system != "" {
		params.Instructions = param.NewOpt(system)
	}
	if c.opts.maxOutputTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(c.opts.maxOutputTokens)
	}
	if c.opts.maxToolCalls > 0 {
		params.MaxToolCalls = param.NewOpt(c.opts.maxToolCalls)
	}
	// Reasoning models reject temperature. WithReasoningEffort is the explicit
	// signal that this request targets one, so do not emit both parameters.
	if c.opts.temperature != nil && c.opts.reasoningEffort == "" {
		params.Temperature = param.NewOpt(*c.opts.temperature)
	}
	if c.opts.topP != nil && c.opts.reasoningEffort == "" {
		params.TopP = param.NewOpt(*c.opts.topP)
	}
	if c.opts.reasoningEffort != "" || c.opts.includeEncryptedReasoning {
		// Request a visible summary as well as encrypted reasoning. The latter is
		// useful for round-tripping, but does not cause the Responses API to emit
		// reasoning summary deltas by itself.
		params.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(c.opts.reasoningEffort),
			Summary: c.opts.reasoningSummary,
		}
	}
	if c.opts.parallelToolCalls != nil {
		params.ParallelToolCalls = param.NewOpt(*c.opts.parallelToolCalls)
	}
	if c.opts.safetyIdentifier != "" {
		params.SafetyIdentifier = param.NewOpt(c.opts.safetyIdentifier)
	}
	if c.opts.serviceTier != "" {
		params.ServiceTier = c.opts.serviceTier
	}
	if c.opts.truncation != "" {
		params.Truncation = c.opts.truncation
	}
	if c.opts.includeEncryptedReasoning {
		params.Include = []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent}
	}

	if tools := encodeTools(cfg.Tools); len(tools) > 0 {
		params.Tools = tools
	}
	params.ToolChoice = encodeToolChoice(cfg.ToolChoice)

	if cfg.PromptCacheKey != "" {
		params.PromptCacheKey = param.NewOpt(cfg.PromptCacheKey)
	}
	return params
}

func encodeToolChoice(choice llm.ToolChoice) responses.ResponseNewParamsToolChoiceUnion {
	switch choice {
	case llm.ToolChoiceNone:
		return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsNone)}
	case llm.ToolChoiceRequired:
		return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsRequired)}
	default:
		return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsAuto)}
	}
}
