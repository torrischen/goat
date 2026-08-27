package common

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/torrischen/goat/agent/message"
)

func TestRunBoundaryRoundTripAndSplit(t *testing.T) {
	contextUID := ContextUID("conversation")
	firstRunUID := RunUID("run-1")
	secondRunUID := RunUID("run-2")

	firstUser := message.UserMessage("first request")
	firstUser.Extra = map[string]json.RawMessage{"provider": json.RawMessage(`"preserved"`)}
	MarkRunStart(firstUser, firstRunUID)
	if got, ok := RunUIDFromMessage(firstUser); !ok || got != firstRunUID {
		t.Fatalf("RunUIDFromMessage() = %q, %v", got, ok)
	}
	if string(firstUser.Extra["provider"]) != `"preserved"` {
		t.Fatal("MarkRunStart() replaced existing message metadata")
	}

	encoded, err := json.Marshal(firstUser)
	if err != nil {
		t.Fatal(err)
	}
	var decoded message.Message
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, ok := RunUIDFromMessage(&decoded); !ok || got != firstRunUID {
		t.Fatalf("RunUIDFromMessage() after JSON round trip = %q, %v", got, ok)
	}

	secondUser := message.UserMessage("second request")
	MarkRunStart(secondUser, secondRunUID)
	messages := []*message.Message{
		message.SystemMessage("system"),
		message.UserMessage("legacy request"),
		AssistantTextMessage("legacy answer"),
		firstUser,
		AssistantTextMessage("first intermediate"),
		message.UserMessage("steering within first run"),
		AssistantTextMessage("first answer"),
		secondUser,
		AssistantTextMessage("second answer"),
	}

	preamble, runs := SplitMessagesByRun(contextUID, messages)
	if len(preamble) != 3 {
		t.Fatalf("preamble length = %d, want 3", len(preamble))
	}
	if len(runs) != 2 {
		t.Fatalf("run count = %d, want 2", len(runs))
	}
	if runs[0].Signature != (RunSignature{ContextUID: contextUID, RunUID: firstRunUID}) ||
		len(runs[0].Messages) != 4 {
		t.Fatalf("first run = %+v", runs[0])
	}
	if runs[1].Signature != (RunSignature{ContextUID: contextUID, RunUID: secondRunUID}) ||
		len(runs[1].Messages) != 2 {
		t.Fatalf("second run = %+v", runs[1])
	}
}

func TestRunBoundaryValidationAndContextMetadata(t *testing.T) {
	firstRunUID := NewRunUID()
	secondRunUID := NewRunUID()
	if firstRunUID == "" || secondRunUID == "" || firstRunUID == secondRunUID {
		t.Fatal("NewRunUID() did not generate unique non-empty identifiers")
	}
	if !(RunSignature{}).IsZero() || (RunSignature{ContextUID: "context", RunUID: "run"}).IsZero() {
		t.Fatal("RunSignature.IsZero() returned the wrong result")
	}

	MarkRunStart(nil, "run")
	msg := message.UserMessage("request")
	MarkRunStart(msg, "")
	if _, ok := RunUIDFromMessage(msg); ok {
		t.Fatal("empty RunUID created a run boundary")
	}
	toolResult := FunctionToolResultMessage(&message.ToolResult{CallID: "call"})
	toolResult.Extra = map[string]json.RawMessage{RunUIDExtraKey: json.RawMessage(`"not-a-boundary"`)}
	if _, ok := RunUIDFromMessage(toolResult); ok {
		t.Fatal("tool-result message was treated as a run boundary")
	}

	actx := NewAgentContext(context.Background())
	if _, ok := RunSignatureFromContext(actx); ok {
		t.Fatal("empty AgentContext returned a run signature")
	}
	actx.SetMeta(InternalToolContextUIDMetaKey, "context")
	actx.SetMeta(InternalToolRunUIDMetaKey, RunUID("run"))
	got, ok := RunSignatureFromContext(actx)
	want := RunSignature{ContextUID: "context", RunUID: "run"}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("RunSignatureFromContext() = %+v, %v, want %+v", got, ok, want)
	}
}
