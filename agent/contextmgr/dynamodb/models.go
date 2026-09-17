package dynamodb

import (
	"fmt"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
)

// HeadItem represents a context head in DynamoDB.
type HeadItem struct {
	PK           string                            `dynamodbav:"pk"`             // Primary key: "HEAD#<uid>"
	SK           string                            `dynamodbav:"sk"`             // Sort key: "META"
	UID          string                            `dynamodbav:"uid"`            // Context UID
	Generation   string                            `dynamodbav:"generation"`     // Generation ID
	Version      uint64                            `dynamodbav:"version"`        // Optimistic lock version
	CommittedSeq uint64                            `dynamodbav:"committed_seq"`  // Highest committed message seq
	PendingStart uint64                            `dynamodbav:"pending_start"`  // First seq in pending batch
	PendingSeq   uint64                            `dynamodbav:"pending_seq"`    // Highest pending message seq
	Finalized    bool                              `dynamodbav:"finalized"`      // True after final answer
	Runs         map[string]contextmgr.RunSnapshot `dynamodbav:"runs,omitempty"` // Run snapshots
	Type         string                            `dynamodbav:"type"`           // "HEAD" for queries
}

// MessageItem represents a message row in DynamoDB.
type MessageItem struct {
	PK      string `dynamodbav:"pk"`      // Primary key: "CTX#<uid>#<lane>"
	SK      string `dynamodbav:"sk"`      // Sort key: "MSG#<seq padded>"
	UID     string `dynamodbav:"uid"`     // Context UID
	Lane    string `dynamodbav:"lane"`    // "committed" or "pending"
	Seq     uint64 `dynamodbav:"seq"`     // Message sequence number
	Message []byte `dynamodbav:"message"` // Serialized message (JSON)
	Type    string `dynamodbav:"type"`    // "MESSAGE" for queries
}

// headItemToHead converts a DynamoDB item to a Head.
func headItemToHead(item *HeadItem) *contextmgr.Head {
	runs := make(map[common.RunUID]contextmgr.RunSnapshot)
	for k, v := range item.Runs {
		runs[common.RunUID(k)] = v
	}
	return &contextmgr.Head{
		UID:          common.ContextUID(item.UID),
		Generation:   item.Generation,
		Version:      item.Version,
		CommittedSeq: item.CommittedSeq,
		PendingStart: item.PendingStart,
		PendingSeq:   item.PendingSeq,
		Finalized:    item.Finalized,
		Runs:         runs,
	}
}

// headToHeadItem converts a Head to a DynamoDB item.
func headToHeadItem(head *contextmgr.Head) *HeadItem {
	runs := make(map[string]contextmgr.RunSnapshot)
	for k, v := range head.Runs {
		runs[string(k)] = v
	}
	return &HeadItem{
		PK:           headPK(head.UID),
		SK:           "META",
		UID:          string(head.UID),
		Generation:   head.Generation,
		Version:      head.Version,
		CommittedSeq: head.CommittedSeq,
		PendingStart: head.PendingStart,
		PendingSeq:   head.PendingSeq,
		Finalized:    head.Finalized,
		Runs:         runs,
		Type:         "HEAD",
	}
}

// headPK returns the partition key for a context head.
func headPK(uid common.ContextUID) string {
	return "HEAD#" + string(uid)
}

// messagePK returns the partition key for messages.
func messagePK(uid common.ContextUID, lane contextmgr.Lane) string {
	return "CTX#" + string(uid) + "#" + string(lane)
}

// messageSK returns the sort key for a message (zero-padded seq).
func messageSK(seq uint64) string {
	// Pad to 20 digits for proper lexicographic sorting
	return "MSG#" + padSeq(seq)
}

// padSeq pads a sequence number to 20 digits.
func padSeq(seq uint64) string {
	// Format as 20-digit zero-padded string
	return fmt.Sprintf("%020d", seq)
}
