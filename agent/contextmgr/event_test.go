package contextmgr

import (
	"errors"
	"testing"

	"github.com/torrischen/goat/agent/common"

	"github.com/cloudwego/eino/schema"
)

func TestApplyEventsRejectsInvalidBatchBeforeMutation(t *testing.T) {
	state := NewState([]*schema.AgenticMessage{schema.UserAgenticMessage("initial")})
	state.Revision = 1
	err := ApplyEvents(state, 2, []Event{
		{Type: EventMessagesAppended, Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("next")}},
		{Type: EventType("unknown")},
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("ApplyEvents() error = %v", err)
	}
	if state.Revision != 1 || len(state.Messages) != 1 {
		t.Fatalf("invalid batch partially mutated state: %+v", state)
	}
}

func TestApplyRunSettledStoresRevisionOnly(t *testing.T) {
	runUID := common.RunUID("run")
	user := schema.UserAgenticMessage("request")
	common.MarkRunStart(user, runUID)
	state := NewState([]*schema.AgenticMessage{user})
	state.Revision = 4

	if err := ApplyEvents(state, 5, []Event{{
		Type:         EventRunSettled,
		RunUID:       runUID,
		Outcome:      RunOutcomeCompleted,
		FinalMessage: common.AssistantTextMessage("answer"),
	}}); err != nil {
		t.Fatal(err)
	}
	snapshot := state.RunSnapshots[runUID]
	if snapshot.Revision != 5 || snapshot.Outcome != RunOutcomeCompleted || len(snapshot.Messages) != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if state.Revision != 5 || len(state.Messages) != 2 || len(state.PendingMessages) != 0 {
		t.Fatalf("state = %+v", state)
	}
}

func TestValidateEvents(t *testing.T) {
	for _, events := range [][]Event{
		nil,
		{{Type: EventMessagesAppended, Messages: []*schema.AgenticMessage{nil}}},
		{{Type: EventPendingEnqueued, Messages: []*schema.AgenticMessage{common.AssistantTextMessage("no")}}},
		{{Type: EventRunSettled, Outcome: RunOutcomeCompleted}},
	} {
		if err := ValidateEvents(events); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("ValidateEvents(%+v) error = %v", events, err)
		}
	}
}
