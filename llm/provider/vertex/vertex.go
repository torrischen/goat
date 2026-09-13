// Package vertex implements goat's llm.Client with Google's Gen AI SDK on
// the Vertex AI backend. Authentication uses Application Default Credentials.
package vertex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"

	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
	"google.golang.org/genai"
)

const thoughtSignatureKey = "vertex"

type client struct {
	mu     sync.Mutex
	api    *genai.Client
	err    error
	config llm.Config
}

var _ llm.Client = (*client)(nil)

// New constructs a Vertex AI client. Project and location can be supplied via
// WithProject/WithLocation or GOOGLE_CLOUD_PROJECT/GOOGLE_CLOUD_LOCATION.
func New(opts ...llm.Option) llm.Client { return NewWithContext(context.Background(), opts...) }

// NewWithContext constructs a Vertex AI client using the supplied context for
// client initialization. Credentials are discovered through ADC by the SDK.
func NewWithContext(ctx context.Context, opts ...llm.Option) llm.Client {
	c := &client{config: llm.ApplyOptions(opts...)}
	c.init(ctx)
	return c
}

func (c *client) init(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.api != nil || c.err != nil {
		return
	}
	cfg := &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: c.config.Project, Location: c.config.Location}
	if c.config.APIKey != "" {
		cfg.APIKey = c.config.APIKey
	}
	if c.config.BaseURL != "" {
		cfg.HTTPOptions.BaseURL = c.config.BaseURL
	}
	c.api, c.err = genai.NewClient(ctx, cfg)
}

func (c *client) Generate(ctx context.Context, messages []*message.Message, opts ...llm.Option) (*message.Message, error) {
	c.init(ctx)
	if c.err != nil {
		return nil, c.err
	}
	cfg := c.config
	llm.ApplyOptionsTo(&cfg, opts...)
	requestConfig := encodeConfig(cfg)
	withSystemInstruction(requestConfig, messages)
	resp, err := c.api.Models.GenerateContent(ctx, cfg.Model, encodeContents(messages), requestConfig)
	if err != nil {
		return nil, err
	}
	return decodeResponse(resp), nil
}

func (c *client) Stream(ctx context.Context, messages []*message.Message, opts ...llm.Option) (llm.StreamReader, error) {
	c.init(ctx)
	if c.err != nil {
		return nil, c.err
	}
	cfg := c.config
	llm.ApplyOptionsTo(&cfg, opts...)
	streamCtx, cancel := context.WithCancel(ctx)
	requestConfig := encodeConfig(cfg)
	withSystemInstruction(requestConfig, messages)
	return newStreamReader(c.api.Models.GenerateContentStream(streamCtx, cfg.Model, encodeContents(messages), requestConfig), cancel), nil
}

func withSystemInstruction(cfg *genai.GenerateContentConfig, messages []*message.Message) {
	var parts []*genai.Part
	for _, msg := range messages {
		if msg == nil || msg.Role != message.RoleSystem {
			continue
		}
		for _, b := range msg.Blocks {
			if b != nil && b.Kind == message.BlockText && b.Text != nil && b.Text.Text != "" {
				parts = append(parts, &genai.Part{Text: b.Text.Text})
			}
		}
	}
	if len(parts) > 0 {
		cfg.SystemInstruction = &genai.Content{Role: genai.RoleUser, Parts: parts}
	}
}

func encodeConfig(cfg llm.Config) *genai.GenerateContentConfig {
	c := &genai.GenerateContentConfig{}
	if cfg.MaxOutputTokens > 0 {
		c.MaxOutputTokens = int32(cfg.MaxOutputTokens)
	}
	if cfg.Temperature != nil {
		v := float32(*cfg.Temperature)
		c.Temperature = &v
	}
	if cfg.TopP != nil {
		v := float32(*cfg.TopP)
		c.TopP = &v
	}
	if tools := encodeTools(cfg.Tools); len(tools) > 0 {
		c.Tools = []*genai.Tool{{FunctionDeclarations: tools}}
		mode := genai.FunctionCallingConfigModeAuto
		switch cfg.ToolChoice {
		case llm.ToolChoiceNone:
			mode = genai.FunctionCallingConfigModeNone
		case llm.ToolChoiceRequired:
			mode = genai.FunctionCallingConfigModeAny
		}
		c.ToolConfig = &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: mode}}
	}
	return c
}

func encodeTools(tools []llm.ToolDef) []*genai.FunctionDeclaration {
	result := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		result = append(result, &genai.FunctionDeclaration{Name: t.Name, Description: t.Description, ParametersJsonSchema: t.Parameters})
	}
	return result
}

func encodeContents(messages []*message.Message) []*genai.Content {
	contents := make([]*genai.Content, 0, len(messages))
	for _, msg := range messages {
		if msg == nil || msg.Role == message.RoleSystem {
			continue
		}
		parts := make([]*genai.Part, 0, len(msg.Blocks))
		for _, b := range msg.Blocks {
			if b == nil {
				continue
			}
			switch b.Kind {
			case message.BlockText:
				if b.Text != nil && b.Text.Text != "" {
					parts = append(parts, &genai.Part{Text: b.Text.Text})
				}
			case message.BlockImage:
				if b.Image != nil {
					if b.Image.URL != "" {
						parts = append(parts, genai.NewPartFromURI(b.Image.URL, imageMIME(b.Image)))
					} else if b.Image.Base64Data != "" {
						if data, err := decodeBase64(b.Image.Base64Data); err == nil {
							parts = append(parts, genai.NewPartFromBytes(data, imageMIME(b.Image)))
						}
					}
				}
			case message.BlockReasoning:
				if msg.Role == message.RoleAssistant && b.Reasoning != nil {
					p := &genai.Part{Text: b.Reasoning.Text, Thought: true}
					p.ThoughtSignature = thoughtSignature(b)
					parts = append(parts, p)
				}
			case message.BlockToolCall:
				if msg.Role == message.RoleAssistant && b.ToolCall != nil {
					args := map[string]any{}
					_ = json.Unmarshal([]byte(b.ToolCall.Arguments), &args)
					parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: b.ToolCall.CallID, Name: b.ToolCall.Name, Args: args}})
				}
			case message.BlockToolResult:
				if msg.Role != message.RoleAssistant && b.ToolResult != nil {
					out := ""
					for _, p := range b.ToolResult.Content {
						if p != nil && p.Text != nil {
							out += p.Text.Text
						}
					}
					parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: b.ToolResult.CallID, Name: b.ToolResult.Name, Response: map[string]any{"output": out}}})
				}
			}
		}
		if len(parts) > 0 {
			role := genai.RoleUser
			if msg.Role == message.RoleAssistant {
				role = genai.RoleModel
			}
			contents = append(contents, &genai.Content{Role: role, Parts: parts})
		}
	}
	return contents
}

func imageMIME(img *message.ImageData) string {
	if img.MIMEType != "" {
		return img.MIMEType
	}
	return "image/png"
}

func thoughtSignature(b *message.ContentBlock) []byte {
	if raw := b.Provider[thoughtSignatureKey]; len(raw) > 0 {
		var sig []byte
		if json.Unmarshal(raw, &sig) == nil {
			return sig
		}
	}
	if b.Reasoning != nil && b.Reasoning.Signature != "" {
		return []byte(b.Reasoning.Signature)
	}
	return nil
}

func decodeResponse(resp *genai.GenerateContentResponse) *message.Message {
	msg := &message.Message{Role: message.RoleAssistant}
	if resp == nil {
		return msg
	}
	if len(resp.Candidates) > 0 && resp.Candidates[0] != nil && resp.Candidates[0].Content != nil {
		for _, p := range resp.Candidates[0].Content.Parts {
			decodePart(msg, p)
		}
	}
	if u := resp.UsageMetadata; u != nil {
		msg.Meta = &message.ResponseMeta{Usage: &message.Usage{PromptTokens: int(u.PromptTokenCount), CompletionTokens: int(u.CandidatesTokenCount + u.ThoughtsTokenCount), CachedTokens: int(u.CachedContentTokenCount)}}
	}
	return msg
}

func decodePart(msg *message.Message, p *genai.Part) {
	if p == nil {
		return
	}
	if p.FunctionCall != nil {
		args, _ := json.Marshal(p.FunctionCall.Args)
		msg.Blocks = append(msg.Blocks, &message.ContentBlock{Kind: message.BlockToolCall, ToolCall: &message.ToolCall{CallID: p.FunctionCall.ID, Name: p.FunctionCall.Name, Arguments: string(args)}})
		return
	}
	if p.Text != "" {
		if p.Thought {
			b := &message.ContentBlock{Kind: message.BlockReasoning, Reasoning: &message.ReasoningData{Text: p.Text}}
			if len(p.ThoughtSignature) > 0 {
				b.Provider = map[string]json.RawMessage{thoughtSignatureKey: mustJSON(p.ThoughtSignature)}
			}
			msg.Blocks = append(msg.Blocks, b)
		} else {
			msg.Blocks = append(msg.Blocks, message.TextBlock(p.Text))
		}
	}
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// decodeBase64 is kept local to avoid accepting malformed image data silently.
func decodeBase64(s string) ([]byte, error) {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[i+1:]
	}
	return base64.StdEncoding.DecodeString(s)
}
