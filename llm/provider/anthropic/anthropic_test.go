package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
)

func TestBuildParamsUsesUnifiedOptions(t *testing.T) {
	model := New(
		llm.WithAPIKey("test-key"),
		llm.WithModel("default-model"),
		llm.WithMaxOutputTokens(128),
	).(*client)

	params := model.buildParams(
		[]*message.Message{
			message.SystemMessage("system prompt"),
			message.UserMessage("hello"),
		},
		llm.WithModel("call-model"),
		llm.WithMaxOutputTokens(256),
		llm.WithTemperature(0.2),
		llm.WithTopP(0.9),
		llm.WithTools([]llm.ToolDef{{
			Name:        "lookup",
			Description: "Look up a value.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key": map[string]any{"type": "string"},
				},
				"required": []string{"key"},
			},
		}}),
		llm.WithToolChoice(llm.ToolChoiceRequired),
	)

	if params.Model != "call-model" || params.MaxTokens != 256 {
		t.Fatalf("model/max_tokens = (%q, %d), want (call-model, 256)", params.Model, params.MaxTokens)
	}
	if !params.Temperature.Valid() || params.Temperature.Value != 0.2 {
		t.Fatalf("temperature = %#v, want 0.2", params.Temperature)
	}
	if !params.TopP.Valid() || params.TopP.Value != 0.9 {
		t.Fatalf("top_p = %#v, want 0.9", params.TopP)
	}
	if len(params.System) != 1 || params.System[0].Text != "system prompt" {
		t.Fatalf("system = %#v", params.System)
	}
	if len(params.Messages) != 1 || params.Messages[0].Role != anthropicapi.MessageParamRoleUser {
		t.Fatalf("messages = %#v, want one user message", params.Messages)
	}
	if len(params.Tools) != 1 || params.Tools[0].OfTool == nil {
		t.Fatalf("tools = %#v, want one custom tool", params.Tools)
	}
	if params.ToolChoice.OfAny == nil {
		t.Fatalf("tool choice = %#v, want any", params.ToolChoice)
	}

	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if got := decoded["tool_choice"].(map[string]any)["type"]; got != "any" {
		t.Fatalf("tool_choice.type = %v, want any", got)
	}
}

func TestEncodeMessagesPreservesBlocksAndMergesToolResults(t *testing.T) {
	messages := []*message.Message{
		message.UserMessage("question"),
		{
			Role: message.RoleAssistant,
			Blocks: []*message.ContentBlock{
				{Kind: message.BlockReasoning, Reasoning: &message.ReasoningData{
					Text: "thinking", Signature: "sig",
				}},
				message.TextBlock("answer"),
				{Kind: message.BlockToolCall, ToolCall: &message.ToolCall{
					CallID: "call_1", Name: "lookup", Arguments: `{"key":"a"}`,
				}},
			},
		},
		message.FunctionToolResultMessage(&message.ToolResult{
			CallID: "call_1",
			Name:   "lookup",
			Content: []*message.ToolResultContent{
				{Kind: message.ToolResultText, Text: &message.TextData{Text: "first"}},
			},
		}),
		message.FunctionToolResultMessage(&message.ToolResult{
			CallID: "call_2",
			Name:   "lookup",
			Content: []*message.ToolResultContent{
				{Kind: message.ToolResultText, Text: &message.TextData{Text: "second"}},
			},
		}),
	}

	encoded := encodeMessages(messages)
	if len(encoded) != 3 {
		t.Fatalf("len(encoded) = %d, want 3", len(encoded))
	}
	if len(encoded[1].Content) != 3 || encoded[1].Content[0].OfThinking == nil || encoded[1].Content[1].OfText == nil || encoded[1].Content[2].OfToolUse == nil {
		t.Fatalf("assistant content = %#v, want thinking/text/tool_use", encoded[1].Content)
	}
	if len(encoded[2].Content) != 2 || encoded[2].Content[0].OfToolResult == nil || encoded[2].Content[1].OfToolResult == nil {
		t.Fatalf("merged tool results = %#v", encoded[2].Content)
	}
}

func TestDecodeResponse(t *testing.T) {
	var response anthropicapi.Message
	payload := []byte(`{
		"id":"msg_1",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-4-6",
		"content":[
			{"type":"thinking","thinking":"plan","signature":"sig"},
			{"type":"text","text":"done"},
			{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"key":"a"}}
		],
		"stop_reason":"tool_use",
		"usage":{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":7}
	}`)
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	decoded := decodeResponse(&response)
	if decoded.Role != message.RoleAssistant || len(decoded.Blocks) != 3 {
		t.Fatalf("decoded = %#v, want assistant with 3 blocks", decoded)
	}
	if decoded.Blocks[0].Reasoning.Text != "plan" || decoded.Blocks[0].Reasoning.Signature != "sig" {
		t.Fatalf("reasoning = %#v", decoded.Blocks[0])
	}
	if decoded.Text() != "done" {
		t.Fatalf("text = %q, want done", decoded.Text())
	}
	calls := decoded.ToolCalls()
	if len(calls) != 1 || calls[0].CallID != "toolu_1" || calls[0].Arguments != `{"key":"a"}` {
		t.Fatalf("tool calls = %#v", calls)
	}
	prompt, completion, cached := decoded.Tokens()
	if prompt != 15 || completion != 7 || cached != 3 {
		t.Fatalf("usage = (%d, %d, %d), want (15, 7, 3)", prompt, completion, cached)
	}
}

func TestStream(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %q, want test-key", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Errorf("anthropic-version header is missing")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("cache-control", "no-cache")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-6\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":4,\"cache_creation_input_tokens\":1,\"cache_read_input_tokens\":2,\"output_tokens\":0}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"lookup\",\"input\":{}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"key\\\":\\\"a\\\"}\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":2}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":4,\"cache_creation_input_tokens\":1,\"cache_read_input_tokens\":2,\"output_tokens\":6}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	model := New(
		llm.WithAPIKey("test-key"),
		llm.WithBaseURL(server.URL),
		llm.WithModel("claude-sonnet-4-6"),
		llm.WithMaxOutputTokens(128),
	)
	reader, err := model.Stream(context.Background(), []*message.Message{message.UserMessage("hello")})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer reader.Close()

	var chunks []*message.Message
	for {
		chunk, err := reader.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv() error = %v", err)
		}
		chunks = append(chunks, chunk)
	}
	combined := message.Concat(chunks)
	if combined.Text() != "Hello" {
		t.Fatalf("text = %q, want Hello", combined.Text())
	}
	if combined.Reasoning() != "plan" || combined.Blocks[0].Reasoning.Signature != "sig" {
		t.Fatalf("reasoning = %#v", combined.Blocks[0])
	}
	calls := combined.ToolCalls()
	if len(calls) != 1 || calls[0].Arguments != `{"key":"a"}` {
		t.Fatalf("tool calls = %#v", calls)
	}
	prompt, completion, cached := combined.Tokens()
	if prompt != 7 || completion != 6 || cached != 2 {
		t.Fatalf("stream usage = (%d, %d, %d), want (7, 6, 2)", prompt, completion, cached)
	}
	if got := gotBody["model"]; got != "claude-sonnet-4-6" {
		t.Fatalf("request model = %v", got)
	}
	if !reflect.DeepEqual(gotBody["max_tokens"], float64(128)) {
		t.Fatalf("request max_tokens = %v", gotBody["max_tokens"])
	}
}
