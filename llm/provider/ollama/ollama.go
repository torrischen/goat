// Package ollama implements goat's llm.Client against Ollama's local HTTP API.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
)

const defaultBaseURL = "http://localhost:11434"

var _ llm.Client = (*client)(nil)

type client struct {
	config llm.Config
	http   *http.Client
}

func New(opts ...llm.Option) llm.Client {
	return &client{config: llm.ApplyOptions(opts...), http: http.DefaultClient}
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []chatMessage  `json:"messages"`
	Tools    []chatTool     `json:"tools,omitempty"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type chatMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content,omitempty"`
	Images    []string       `json:"images,omitempty"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Message         chatMessage `json:"message"`
	Done            bool        `json:"done"`
	PromptEvalCount int         `json:"prompt_eval_count"`
	EvalCount       int         `json:"eval_count"`
	Error           string      `json:"error"`
}

func (c *client) Generate(ctx context.Context, messages []*message.Message, opts ...llm.Option) (*message.Message, error) {
	cfg := c.config
	llm.ApplyOptionsTo(&cfg, opts...)
	var response chatResponse
	if err := c.do(ctx, cfg, messages, false, &response); err != nil {
		return nil, err
	}
	return decode(response), nil
}

func (c *client) Stream(ctx context.Context, messages []*message.Message, opts ...llm.Option) (llm.StreamReader, error) {
	cfg := c.config
	llm.ApplyOptionsTo(&cfg, opts...)
	body, err := json.Marshal(buildRequest(messages, cfg, true))
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(newRequest(ctx, c.baseURL(cfg), body))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, apiError(resp)
	}
	return &streamReader{body: resp.Body}, nil
}

func (c *client) do(ctx context.Context, cfg llm.Config, messages []*message.Message, stream bool, response *chatResponse) error {
	body, err := json.Marshal(buildRequest(messages, cfg, stream))
	if err != nil {
		return err
	}
	resp, err := c.http.Do(newRequest(ctx, c.baseURL(cfg), body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return err
	}
	if response.Error != "" {
		return fmt.Errorf("ollama: %s", response.Error)
	}
	return nil
}

func buildRequest(messages []*message.Message, cfg llm.Config, stream bool) chatRequest {
	request := chatRequest{Model: cfg.Model, Messages: encodeMessages(messages), Tools: encodeTools(cfg.Tools), Stream: stream}
	if cfg.Temperature != nil || cfg.TopP != nil || cfg.MaxOutputTokens > 0 {
		request.Options = map[string]any{}
		if cfg.Temperature != nil {
			request.Options["temperature"] = *cfg.Temperature
		}
		if cfg.TopP != nil {
			request.Options["top_p"] = *cfg.TopP
		}
		if cfg.MaxOutputTokens > 0 {
			request.Options["num_predict"] = cfg.MaxOutputTokens
		}
	}
	return request
}

func (c *client) baseURL(cfg llm.Config) string {
	if cfg.BaseURL == "" {
		return defaultBaseURL
	}
	return strings.TrimRight(cfg.BaseURL, "/")
}

func newRequest(ctx context.Context, url string, body []byte) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/api/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func apiError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("ollama: HTTP %s: %s", resp.Status, strings.TrimSpace(string(data)))
}

func decode(response chatResponse) *message.Message {
	result := &message.Message{Role: message.RoleAssistant}
	if response.Message.Content != "" {
		result.Blocks = append(result.Blocks, message.TextBlock(response.Message.Content))
	}
	for _, call := range response.Message.ToolCalls {
		args := string(call.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		result.Blocks = append(result.Blocks, &message.ContentBlock{Kind: message.BlockToolCall, ToolCall: &message.ToolCall{Name: call.Function.Name, Arguments: args}})
	}
	result.Meta = &message.ResponseMeta{Usage: &message.Usage{PromptTokens: response.PromptEvalCount, CompletionTokens: response.EvalCount}}
	return result
}

type streamReader struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
	done    bool
}

func (r *streamReader) Recv() (*message.Message, error) {
	if r.done {
		return nil, io.EOF
	}
	if r.scanner == nil {
		r.scanner = bufio.NewScanner(r.body)
		r.scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	}
	for r.scanner.Scan() {
		var response chatResponse
		if err := json.Unmarshal(r.scanner.Bytes(), &response); err != nil {
			r.done = true
			return nil, err
		}
		if response.Error != "" {
			r.done = true
			return nil, fmt.Errorf("ollama: %s", response.Error)
		}
		msg := decode(response)
		if response.Done {
			r.done = true
			return msg, nil
		}
		if len(msg.Blocks) > 0 {
			return msg, nil
		}
	}
	r.done = true
	if err := r.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (r *streamReader) Close() {
	r.done = true
	_ = r.body.Close()
}

func encodeMessages(messages []*message.Message) []chatMessage {
	out := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		encoded := chatMessage{Role: string(msg.Role)}
		for _, block := range msg.Blocks {
			if block == nil {
				continue
			}
			switch block.Kind {
			case message.BlockText:
				if block.Text != nil {
					encoded.Content += block.Text.Text
				}
			case message.BlockImage:
				if block.Image != nil && block.Image.Base64Data != "" {
					encoded.Images = append(encoded.Images, block.Image.Base64Data)
				}
			case message.BlockToolCall:
				if block.ToolCall != nil {
					var call chatToolCall
					call.Function.Name = block.ToolCall.Name
					call.Function.Arguments = json.RawMessage(block.ToolCall.Arguments)
					encoded.ToolCalls = append(encoded.ToolCalls, call)
				}
			case message.BlockToolResult:
				if block.ToolResult != nil {
					encoded.Role = "tool"
					encoded.Content += block.ToolResult.Text()
				}
			}
		}
		if encoded.Content != "" || len(encoded.Images) > 0 || len(encoded.ToolCalls) > 0 {
			out = append(out, encoded)
		}
	}
	return out
}

func encodeTools(tools []llm.ToolDef) []chatTool {
	out := make([]chatTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, chatTool{Type: "function", Function: chatFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}})
	}
	return out
}
