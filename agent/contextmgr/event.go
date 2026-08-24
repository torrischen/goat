package contextmgr

import (
	"github.com/torrischen/goat/agent/common"

	"github.com/cloudwego/eino/schema"
)

// EventType identifies a committed context transition in the audit log.
type EventType string

const (
	EventMessagesAppended EventType = "messages_appended"
	EventMessagesReplaced EventType = "messages_replaced"
	EventPendingEnqueued  EventType = "pending_enqueued"
	EventTurnCommitted    EventType = "turn_committed"
	EventRunSettled       EventType = "run_settled"
)

// Event is the append-only audit record for one Manager transition. It is not
// authoritative state; the context state document is the source of truth.
type Event struct {
	Type         EventType              `json:"type"`
	RunUID       common.RunUID          `json:"run_uid,omitempty"`
	Outcome      RunOutcome             `json:"outcome,omitempty"`
	FinalMessage *schema.AgenticMessage `json:"final_message,omitempty"`
}
