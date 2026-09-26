package openai

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/responses"
)

func TestDecodeResponseReadsReasoningContent(t *testing.T) {
	payload := []byte(`{
		"id":"resp_1",
		"output":[
			{
				"id":"rs_1",
				"type":"reasoning",
				"content":[{"type":"reasoning_text","text":"Bifrost reasoning"}],
				"summary":[],
				"encrypted_content":""
			},
			{
				"type":"message",
				"content":[{"type":"output_text","text":"answer"}]
			}
		],
		"usage":{"input_tokens":1,"output_tokens":2}
	}`)

	var response responses.Response
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	decoded := decodeResponse(&response)
	if got := decoded.Reasoning(); got != "Bifrost reasoning" {
		t.Fatalf("reasoning = %q, want Bifrost reasoning", got)
	}
	if got := decoded.Text(); got != "answer" {
		t.Fatalf("text = %q, want answer", got)
	}
}

func TestDecodeResponseReadsReasoningSummary(t *testing.T) {
	payload := []byte(`{
		"id":"resp_2",
		"output":[
			{
				"id":"rs_2",
				"type":"reasoning",
				"summary":[{"type":"summary_text","text":"OpenAI summary"}]
			}
		]
	}`)

	var response responses.Response
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	decoded := decodeResponse(&response)
	if got := decoded.Reasoning(); got != "OpenAI summary" {
		t.Fatalf("reasoning = %q, want OpenAI summary", got)
	}
}
