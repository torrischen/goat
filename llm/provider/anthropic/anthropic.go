// Package anthropic implements goat's llm.Client against the Anthropic Messages API.
//
// The provider is stateless: the full conversation is replayed on every request.
// Anthropic-specific thinking signatures are kept on the neutral reasoning block
// so assistant turns can be sent back verbatim on the next request.
package anthropic

import (
	"context"
	"os"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
)

const defaultMaxOutputTokens = 4096

// client implements llm.Client against the Anthropic Messages API.
type client struct {
	client anthropicapi.Client
	config llm.Config
}

var _ llm.Client = (*client)(nil)

// New constructs an Anthropic Messages provider as an llm.Client.
func New(opts ...llm.Option) llm.Client {
	cfg := llm.ApplyOptions(opts...)
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	reqOpts := []option.RequestOption{}
	if cfg.APIKey != "" {
		reqOpts = append(reqOpts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(cfg.BaseURL))
	}

	return &client{
		client: anthropicapi.NewClient(reqOpts...),
		config: cfg,
	}
}

func (c *client) Generate(ctx context.Context, messages []*message.Message, opts ...llm.Option) (*message.Message, error) {
	params := c.buildParams(messages, opts...)
	resp, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return nil, err
	}
	return decodeResponse(resp), nil
}

func (c *client) Stream(ctx context.Context, messages []*message.Message, opts ...llm.Option) (llm.StreamReader, error) {
	params := c.buildParams(messages, opts...)
	stream := c.client.Messages.NewStreaming(ctx, params)
	if err := stream.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return newStreamReader(stream), nil
}

// buildParams translates goat messages and call options into an Anthropic
// Messages API request. Anthropic requires max_tokens even when no explicit
// value was supplied through the provider-neutral options.
func (c *client) buildParams(messages []*message.Message, opts ...llm.Option) anthropicapi.MessageNewParams {
	cfg := c.config
	llm.ApplyOptionsTo(&cfg, opts...)

	system, rest := splitSystem(messages)
	maxTokens := cfg.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxOutputTokens
	}

	params := anthropicapi.MessageNewParams{
		Model:     anthropicapi.Model(cfg.Model),
		MaxTokens: int64(maxTokens),
		Messages:  encodeMessages(rest),
		System:    system,
	}
	if cfg.Temperature != nil {
		params.Temperature = param.NewOpt(*cfg.Temperature)
	}
	if cfg.TopP != nil {
		params.TopP = param.NewOpt(*cfg.TopP)
	}
	if tools := encodeTools(cfg.Tools); len(tools) > 0 {
		params.Tools = tools
	}
	params.ToolChoice = encodeToolChoice(cfg.ToolChoice)
	return params
}

func encodeToolChoice(choice llm.ToolChoice) anthropicapi.ToolChoiceUnionParam {
	switch choice {
	case llm.ToolChoiceNone:
		return anthropicapi.ToolChoiceUnionParam{
			OfNone: func() *anthropicapi.ToolChoiceNoneParam {
				value := anthropicapi.NewToolChoiceNoneParam()
				return &value
			}(),
		}
	case llm.ToolChoiceRequired:
		// Anthropic calls the "at least one tool" mode "any".
		return anthropicapi.ToolChoiceUnionParam{OfAny: &anthropicapi.ToolChoiceAnyParam{}}
	default:
		return anthropicapi.ToolChoiceUnionParam{OfAuto: &anthropicapi.ToolChoiceAutoParam{}}
	}
}
