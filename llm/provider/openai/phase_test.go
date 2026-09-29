package openai

import (
	"encoding/json"
	"testing"

	"github.com/torrischen/goat/agent/message"

	"github.com/openai/openai-go/v3/responses"
)

func streamEvent(t *testing.T, payload string) responses.ResponseStreamEventUnion {
	t.Helper()
	var ev responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal stream event: %v", err)
	}
	return ev
}

func TestStreamLabelsTextDeltasWithItemPhase(t *testing.T) {
	r := &streamReader{phases: make(map[string]message.TextPhase)}
	events := []string{
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","phase":"commentary","content":[]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"I'll start."}`,
		`{"type":"response.output_item.added","output_index":3,"item":{"id":"msg_2","type":"message","role":"assistant","status":"in_progress","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_2","output_index":3,"content_index":0,"delta":"Answer."}`,
		`{"type":"response.output_text.delta","item_id":"msg_unknown","output_index":4,"content_index":0,"delta":"Unlabelled."}`,
	}

	var chunks []*message.Message
	for _, payload := range events {
		msg, err := r.mapEvent(streamEvent(t, payload))
		if err != nil {
			t.Fatalf("mapEvent() error = %v", err)
		}
		if msg != nil {
			chunks = append(chunks, msg)
		}
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	want := []message.TextPhase{message.TextPhaseCommentary, message.TextPhaseFinalAnswer, message.TextPhaseUnspecified}
	for i, chunk := range chunks {
		if got := chunk.Blocks[0].Text.Phase; got != want[i] {
			t.Fatalf("chunk %d phase = %q, want %q", i, got, want[i])
		}
	}
}

func TestDecodeResponseReadsMessagePhase(t *testing.T) {
	payload := []byte(`{
		"id":"resp_3",
		"output":[
			{"id":"msg_1","type":"message","phase":"commentary","content":[{"type":"output_text","text":"Checking."}]},
			{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}
		]
	}`)
	var response responses.Response
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	decoded := decodeResponse(&response)
	if got := decoded.Blocks[0].Text.Phase; got != message.TextPhaseCommentary {
		t.Fatalf("phase = %q, want commentary", got)
	}
}

func TestEncodeAssistantTextEchoesOnlyProviderPhase(t *testing.T) {
	commentary := message.TextBlock("Checking.")
	commentary.Text.Phase = message.TextPhaseCommentary
	msg := &message.Message{Role: message.RoleAssistant, Blocks: []*message.ContentBlock{
		commentary,
		message.TextBlock("unlabelled"),
	}}

	items := encodeAssistantBlocks(msg)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	first, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["phase"] != "commentary" {
		t.Fatalf("first item phase = %v, want commentary (item %s)", decoded["phase"], first)
	}

	second, err := json.Marshal(items[1])
	if err != nil {
		t.Fatal(err)
	}
	decoded = map[string]any{}
	if err := json.Unmarshal(second, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["phase"]; ok {
		t.Fatalf("unlabelled item must not carry phase: %s", second)
	}
}
