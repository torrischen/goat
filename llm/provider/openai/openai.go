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
	config llm.Config
}

var _ llm.Client = (*client)(nil)

// New constructs an OpenAI Responses provider as an llm.Client.
func New(opts ...llm.Option) llm.Client {
	cfg := llm.ApplyOptions(opts...)
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	}

	reqOpts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(cfg.BaseURL))
	}

	return &client{
		client: oai.NewClient(reqOpts...),
		config: cfg,
	}
}

func (c *client) Generate(ctx context.Context, messages []*message.Message, opts ...llm.Option) (*message.Message, error) {
	params := c.buildParams(messages, opts...)
	resp, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return nil, err
	}
	return decodeResponse(resp), nil
}

func (c *client) Stream(ctx context.Context, messages []*message.Message, opts ...llm.Option) (llm.StreamReader, error) {
	params := c.buildParams(messages, opts...)
	stream := c.client.Responses.NewStreaming(ctx, params)
	return newStreamReader(stream), nil
}

// buildParams translates goat messages and call options into Responses request
// params. The system prompt is lifted into the "instructions" field.
func (c *client) buildParams(messages []*message.Message, opts ...llm.Option) responses.ResponseNewParams {
	cfg := c.config
	llm.ApplyOptionsTo(&cfg, opts...)
	system, rest := splitSystem(messages)

	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(cfg.Model),
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: encodeInput(rest)},
		// Stateless operation: replay the full conversation each turn.
		Store: param.NewOpt(false),
	}
	if system != "" {
		params.Instructions = param.NewOpt(system)
	}
	if cfg.MaxOutputTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(cfg.MaxOutputTokens))
	}
	if cfg.MaxToolCalls > 0 {
		params.MaxToolCalls = param.NewOpt(int64(cfg.MaxToolCalls))
	}
	// Reasoning models reject temperature. WithReasoningEffort is the explicit
	// signal that this request targets one, so do not emit both parameters.
	if cfg.Temperature != nil && cfg.ReasoningEffort == "" {
		params.Temperature = param.NewOpt(*cfg.Temperature)
	}
	if cfg.TopP != nil && cfg.ReasoningEffort == "" {
		params.TopP = param.NewOpt(*cfg.TopP)
	}
	if cfg.ReasoningEffort != "" || cfg.IncludeEncryptedReasoning {
		// Request a visible summary as well as encrypted reasoning. The latter is
		// useful for round-tripping, but does not cause the Responses API to emit
		// reasoning summary deltas by itself.
		params.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(cfg.ReasoningEffort),
			Summary: shared.ReasoningSummary(cfg.ReasoningSummary),
		}
	}
	if cfg.ParallelToolCalls != nil {
		params.ParallelToolCalls = param.NewOpt(*cfg.ParallelToolCalls)
	}
	if cfg.SafetyIdentifier != "" {
		params.SafetyIdentifier = param.NewOpt(cfg.SafetyIdentifier)
	}
	if cfg.ServiceTier != "" {
		params.ServiceTier = responses.ResponseNewParamsServiceTier(cfg.ServiceTier)
	}
	if cfg.Truncation != "" {
		params.Truncation = responses.ResponseNewParamsTruncation(cfg.Truncation)
	}
	if cfg.IncludeEncryptedReasoning {
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
