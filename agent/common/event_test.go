package common

import "testing"

func TestAgentEventTypesAndTerminalClassification(t *testing.T) {
	events := []AgentEvent{
		RunStartedEvent{},
		AssistantTextDeltaEvent{},
		ReasoningDeltaEvent{},
		ToolCallStartedEvent{},
		ToolCallCompletedEvent{},
		ToolCallFailedEvent{},
		FinalAnswerCompletedEvent{},
		RunCompletedEvent{},
		RunInterruptedEvent{},
		RunCanceledEvent{},
		RunFailedEvent{},
	}

	seen := make(map[AgentEventType]struct{}, len(events))
	for _, event := range events {
		if event.Type() == "" {
			t.Fatalf("%T has an empty event type", event)
		}
		if _, exists := seen[event.Type()]; exists {
			t.Fatalf("duplicate event type %q", event.Type())
		}
		seen[event.Type()] = struct{}{}
	}

	for _, event := range events[:len(events)-4] {
		if IsTerminalAgentEvent(event) {
			t.Fatalf("%T unexpectedly classified as terminal", event)
		}
	}
	for _, event := range events[len(events)-4:] {
		if !IsTerminalAgentEvent(event) {
			t.Fatalf("%T was not classified as terminal", event)
		}
	}
	if IsTerminalAgentEvent(nil) {
		t.Fatal("nil event unexpectedly classified as terminal")
	}
}
