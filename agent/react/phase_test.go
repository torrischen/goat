package react

import (
	"context"
	"testing"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm/llmtest"
	"github.com/torrischen/goat/streaming"
)

func drainEvents(t *testing.T, events streaming.Stream[common.AgentEvent]) []common.AgentEvent {
	t.Helper()
	events.Close()
	var out []common.AgentEvent
	for {
		ev, err := events.ReadWithContext(context.Background())
		if err != nil {
			return out
		}
		out = append(out, ev)
	}
}

func TestStreamModelResponseResolvesUnlabelledPreambleAsCommentary(t *testing.T) {
	model := &llmtest.Mock{StreamResponses: [][]*message.Message{{
		{Role: message.RoleAssistant, Blocks: []*message.ContentBlock{message.ReasoningBlock("plan")}},
		message.AssistantTextMessage("I'll gather evidence."),
		{Role: message.RoleAssistant, Blocks: []*message.ContentBlock{{
			Kind:     message.BlockToolCall,
			ToolCall: &message.ToolCall{CallID: "c1", Name: "get_metrics", Arguments: "{}"},
		}}},
	}}}
	agent := &Agent{llmClient: model}
	events := streaming.NewStream[common.AgentEvent](16)

	raw, err := agent.streamModelResponse(common.NewAgentContext(context.Background()), nil, events)
	if err != nil {
		t.Fatalf("streamModelResponse() error = %v", err)
	}
	if got := assistantText(raw); got != "" {
		t.Fatalf("assistantText() = %q, want empty for a tool turn", got)
	}

	var delta *common.AssistantTextDeltaEvent
	var completed *common.AssistantTextCompletedEvent
	for _, ev := range drainEvents(t, events) {
		switch typed := ev.(type) {
		case common.AssistantTextDeltaEvent:
			delta = &typed
		case common.AssistantTextCompletedEvent:
			completed = &typed
		}
	}
	if delta == nil || delta.Phase != message.TextPhaseUnspecified {
		t.Fatalf("delta = %+v, want unspecified phase", delta)
	}
	if completed == nil || completed.Phase != message.TextPhaseCommentary || completed.Text != "I'll gather evidence." {
		t.Fatalf("completed = %+v, want commentary", completed)
	}
}

func TestStreamModelResponseForwardsProviderPhases(t *testing.T) {
	commentary := message.TextBlock("Checking. ")
	commentary.Text.Phase = message.TextPhaseCommentary
	answer := message.TextBlock("Root cause found.")
	answer.Text.Phase = message.TextPhaseFinalAnswer
	model := &llmtest.Mock{StreamResponses: [][]*message.Message{{
		{Role: message.RoleAssistant, Blocks: []*message.ContentBlock{commentary}},
		{Role: message.RoleAssistant, Blocks: []*message.ContentBlock{answer}},
	}}}
	agent := &Agent{llmClient: model}
	events := streaming.NewStream[common.AgentEvent](16)

	raw, err := agent.streamModelResponse(common.NewAgentContext(context.Background()), nil, events)
	if err != nil {
		t.Fatalf("streamModelResponse() error = %v", err)
	}
	if got := assistantText(raw); got != "Root cause found." {
		t.Fatalf("assistantText() = %q, want only the final answer", got)
	}

	var phases []message.TextPhase
	for _, ev := range drainEvents(t, events) {
		switch typed := ev.(type) {
		case common.AssistantTextDeltaEvent:
			phases = append(phases, typed.Phase)
		case common.AssistantTextCompletedEvent:
			phases = append(phases, typed.Phase)
		}
	}
	want := []message.TextPhase{
		message.TextPhaseCommentary, message.TextPhaseFinalAnswer, // deltas
		message.TextPhaseCommentary, message.TextPhaseFinalAnswer, // completed
	}
	if len(phases) != len(want) {
		t.Fatalf("phases = %v, want %v", phases, want)
	}
	for i := range want {
		if phases[i] != want[i] {
			t.Fatalf("phases = %v, want %v", phases, want)
		}
	}
}
