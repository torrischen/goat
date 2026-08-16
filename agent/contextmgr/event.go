package contextmgr

import (
	"errors"
	"fmt"

	"github.com/torrischen/goat/agent/common"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
)

// EventType identifies one atomic conversation state transition.
type EventType string

const (
	// CheckpointInterval bounds normal Load replay without discarding history.
	CheckpointInterval uint64 = 64

	EventMessagesAppended EventType = "messages_appended"
	EventMessagesReplaced EventType = "messages_replaced"
	EventPendingEnqueued  EventType = "pending_enqueued"
	EventTurnCommitted    EventType = "turn_committed"
	EventRunSettled       EventType = "run_settled"
)

// Event is the incremental persistence unit. A Store appends all events in a
// revision atomically and rebuilds State by applying them in order.
type Event struct {
	Type         EventType                `json:"type"`
	Messages     []*schema.AgenticMessage `json:"messages,omitempty"`
	RunUID       common.RunUID            `json:"run_uid,omitempty"`
	Outcome      RunOutcome               `json:"outcome,omitempty"`
	FinalMessage *schema.AgenticMessage   `json:"final_message,omitempty"`
}

// CloneEvents isolates nested message values at persistence boundaries.
func CloneEvents(events []Event) ([]Event, error) {
	payload, err := sonic.Marshal(events)
	if err != nil {
		return nil, fmt.Errorf("encode context events: %w", err)
	}
	var cloned []Event
	if err := sonic.Unmarshal(payload, &cloned); err != nil {
		return nil, fmt.Errorf("decode context events: %w", err)
	}
	return cloned, nil
}

// ValidateEvents rejects event batches that cannot be replayed safely.
func ValidateEvents(events []Event) error {
	if len(events) == 0 {
		return fmt.Errorf("%w: batch is empty", ErrInvalidEvent)
	}
	for i, event := range events {
		var err error
		switch event.Type {
		case EventMessagesAppended, EventMessagesReplaced, EventTurnCommitted:
			err = validateMessages(event.Messages)
		case EventPendingEnqueued:
			err = validatePendingMessages(event.Messages)
		case EventRunSettled:
			if event.RunUID == "" {
				err = errors.New("run UID is empty")
			} else {
				err = validateSettlement(&SettleRunArgs{
					Signature: common.RunSignature{ContextUID: "event", RunUID: event.RunUID},
					Outcome:   event.Outcome, FinalMessage: event.FinalMessage,
				})
			}
		default:
			err = fmt.Errorf("unknown event type %q", event.Type)
		}
		if err != nil {
			return fmt.Errorf("%w at index %d: %v", ErrInvalidEvent, i, err)
		}
	}
	return nil
}

// ValidateTransition checks event preconditions against a materialized view.
// Stores must perform the equivalent checks inside the same CAS transaction as
// the append. The returned messages are consumed by EventTurnCommitted.
func ValidateTransition(state *State, events []Event) ([]*schema.AgenticMessage, bool, error) {
	if state == nil {
		return nil, false, fmt.Errorf("validate context transition: state is nil")
	}
	var applied []*schema.AgenticMessage
	for _, event := range events {
		switch event.Type {
		case EventPendingEnqueued:
			if len(state.Messages) > 0 && isFinalAnswerMessage(state.Messages[len(state.Messages)-1]) {
				return nil, false, ErrConversationFinalized
			}
		case EventTurnCommitted:
			applied = common.CloneAgenticMessages(state.PendingMessages)
			if len(event.Messages) == 0 && len(applied) == 0 {
				return nil, true, nil
			}
		case EventRunSettled:
			if _, settled := state.RunSnapshots[event.RunUID]; settled {
				return nil, true, nil
			}
			if !runExists(state.Messages, event.RunUID) {
				return nil, false, ErrRunNotFound
			}
			var current common.RunUID
			for _, message := range state.Messages {
				if runUID, ok := common.RunUIDFromMessage(message); ok {
					current = runUID
				}
			}
			if current != event.RunUID {
				return nil, false, ErrRunNotCurrent
			}
		}
	}
	return applied, false, nil
}

// ApplyEvents applies one persisted revision to state.
func ApplyEvents(state *State, revision uint64, events []Event) error {
	if state == nil {
		return fmt.Errorf("apply context events: state is nil")
	}
	if revision != state.Revision+1 {
		return fmt.Errorf("apply context events: revision %d follows %d", revision, state.Revision)
	}
	if err := ValidateEvents(events); err != nil {
		return err
	}
	for _, event := range events {
		switch event.Type {
		case EventMessagesAppended:
			state.Messages = append(state.Messages, common.CloneAgenticMessages(event.Messages)...)
		case EventMessagesReplaced:
			state.Messages = common.CloneAgenticMessages(event.Messages)
		case EventPendingEnqueued:
			state.PendingMessages = append(
				state.PendingMessages,
				common.CloneAgenticMessages(event.Messages)...,
			)
		case EventTurnCommitted:
			state.Messages = append(state.Messages, common.CloneAgenticMessages(event.Messages)...)
			state.Messages = append(state.Messages, state.PendingMessages...)
			state.PendingMessages = []*schema.AgenticMessage{}
		case EventRunSettled:
			if event.Outcome == RunOutcomeCompleted {
				state.Messages = append(state.Messages, common.CloneAgenticMessages(
					[]*schema.AgenticMessage{event.FinalMessage},
				)...)
				state.PendingMessages = []*schema.AgenticMessage{}
			}
			state.RunSnapshots[event.RunUID] = RunSnapshot{
				Outcome:  event.Outcome,
				Revision: revision,
			}
		default:
			return fmt.Errorf("apply context events: unknown event type %q", event.Type)
		}
	}
	state.Revision = revision
	return nil
}
