# Context Manager

`contextmgr.Manager` owns conversation, run, settlement, fork, and compaction semantics. Storage implementations provide typed heads and sequence-addressed message logs.

```text
React Agent
    |
    v
contextmgr.Manager
    |
    v
contextmgr.Store  ->  RAM | MongoDB
```

Applications normally use one of the supported constructors:

```go
manager := ram.NewRAMContextManager()
manager, err := mongodb.NewMongoDBContextManager(mongodb.Config{
    URI:      "mongodb://localhost:27017",
    Database: "goat",
})
```

## Manager Workflows

- `Create` creates a conversation with initial committed messages.
- `Load` returns committed history.
- `Append` adds committed messages and reopens a finalized context.
- `Replace` replaces committed history while retaining pending messages.
- `Enqueue` adds user messages to the pending inbox.
- `CommitTurn` appends a complete turn, applies pending messages, and clears the inbox.
- `SettleRun` records a terminal run outcome and optionally appends the final answer.
- `Fork` creates a new context from a settled run snapshot.
- `Delete` removes a context and its stored messages.

`Store` implementations must isolate returned values, preserve ascending sequence order, and provide atomic head version checks. The RAM store is intended for tests and short-lived processes. MongoDB stores heads and messages in separate collections and creates the `{uid: 1, lane: 1, seq: 1}` message index when initialized.

## Store Contract

```go
type Store interface {
    CreateHead(context.Context, *Head) error
    LoadHead(context.Context, common.ContextUID) (*Head, error)
    AppendMessages(context.Context, []MessageRow) error
    ReadMessages(context.Context, common.ContextUID, Lane, uint64, uint64) ([]MessageRow, error)
    ClearLane(context.Context, common.ContextUID, Lane) error
    CommitHead(context.Context, *Head, uint64) error
    DeleteContext(context.Context, common.ContextUID) error
    ListContexts(context.Context) ([]common.ContextUID, error)
}
```

`CreateHead` returns `ErrCASConflict` for an existing UID. `LoadHead` and `CommitHead` return `ErrContextNotFound` for missing contexts. `CommitHead` compares the expected head version atomically. `DeleteContext` is idempotent.

The head stores committed and pending watermarks, finalization state, and settled run metadata. Message rows are addressed by context UID, lane, and sequence. Pending rows become visible to `Load` only after `CommitTurn` or a completed `SettleRun` advances the committed watermark.

MongoDB configuration supports `URI`, `Database`, `ContextCollection`, and `MessageCollection`. `Collection` remains a shorthand for the context collection; its message collection is `<collection>_messages`.
