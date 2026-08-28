package contextmgr

import (
	"context"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/message"
)

// Lane distinguishes committed messages from pending user messages in the log.
type Lane string

const (
	LaneCommitted Lane = "committed"
	LanePending   Lane = "pending"
)

// Head is the small versioned document that anchors one context's current state.
// Backends store this as a single record with atomic CAS on Version.
type Head struct {
	UID        common.ContextUID `json:"uid"`
	Generation string            `json:"generation"` // changes on Delete+Recreate to invalidate old run snapshots
	Version    uint64            `json:"version"`    // optimistic lock, incremented on every mutation

	// Watermarks: seq values are 1-indexed and inclusive.
	CommittedSeq uint64 `json:"committed_seq"` // highest committed message seq visible to Load
	PendingStart uint64 `json:"pending_start"` // first seq in the currently visible pending batch; zero when empty
	PendingSeq   uint64 `json:"pending_seq"`   // highest pending message seq ever allocated

	Finalized bool `json:"finalized"` // true after a completed run's final answer

	// Runs maps runUID -> outcome+seq snapshot. Bounded to dozens of entries.
	Runs map[common.RunUID]RunSnapshot `json:"runs,omitempty"`
}

// MessageRow is one sequence-addressed log entry. Rows are invisible until the
// head's watermark advances past their seq; replacement operations may clear a lane.
type MessageRow struct {
	UID     common.ContextUID `json:"uid"`
	Lane    Lane              `json:"lane"`
	Seq     uint64            `json:"seq"`
	Message *message.Message  `json:"message"`
}

// Store is the persistence boundary. Implementations provide domain-typed
// operations over Head documents and MessageRow logs, replacing the old
// byte-KV Storage interface.
//
// Crash safety: AppendMessages writes are invisible until a CommitHead CAS
// moves the watermark past them. Failed or conflicting head updates leave
// the log intact but invisible.
//
// Backends must provide:
// - Atomic CAS on Head.Version (cross-process for MongoDB, single-process for RAM)
// - Atomic ReplaceCommitted publication of committed messages and the head
// - Stable ordering for ReadMessages (by seq ascending)
// - Isolation: returned Head/MessageRow values must be independent of caller mutation
type Store interface {
	// CreateHead creates a new context head. Returns ErrCASConflict if uid already exists.
	CreateHead(ctx context.Context, head *Head) error

	// LoadHead returns the current head. Returns ErrContextNotFound if uid doesn't exist.
	LoadHead(ctx context.Context, uid common.ContextUID) (*Head, error)

	// AppendMessages writes message log rows. Rows stay invisible until the head's
	// watermark advances. Backends may reject out-of-order seq or lane mismatches.
	AppendMessages(ctx context.Context, rows []MessageRow) error

	// ReadMessages returns message rows in [fromSeq, toSeq] inclusive, ordered by seq ascending.
	// Returns empty slice if the range is empty or no messages exist in the range.
	ReadMessages(ctx context.Context, uid common.ContextUID, lane Lane, fromSeq, toSeq uint64) ([]MessageRow, error)

	// ClearLane removes all messages in the specified lane for the given context.
	// It is retained for storage maintenance and must not be composed with
	// CommitHead for user-visible replacement operations.
	ClearLane(ctx context.Context, uid common.ContextUID, lane Lane) error

	// ReplaceCommitted atomically replaces the committed lane and publishes the
	// supplied head if its version matches expectVersion. Implementations must
	// make message replacement and head publication one atomic operation.
	ReplaceCommitted(ctx context.Context, uid common.ContextUID, rows []MessageRow, next *Head, expectVersion uint64) error

	// CommitHead atomically updates the head via CAS on expectVersion.
	// Returns ErrCASConflict if the stored version doesn't match expectVersion.
	// Returns ErrContextNotFound if uid doesn't exist.
	CommitHead(ctx context.Context, next *Head, expectVersion uint64) error

	// DeleteContext removes the head and all message rows for uid. Idempotent.
	DeleteContext(ctx context.Context, uid common.ContextUID) error

	// ListContexts returns all context UIDs, used by garbage collection.
	ListContexts(ctx context.Context) ([]common.ContextUID, error)
}
