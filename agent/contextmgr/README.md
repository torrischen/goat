# Context manager

`contextmgr.Manager` owns the conversation state machine. Storage packages own incremental persistence only.

```text
React Agent
    |
    v
contextmgr.Manager
    |
    v
contextmgr.Store  ->  RAM | files | SQLite | MySQL
```

Applications normally use a backend constructor:

```go
manager := ram.NewRAMContextManager()
manager := file.NewFileContextManager("data/conversations")
manager, err := sqlite.NewSQLiteContextManager("data/context.sqlite")
manager, err := mysql.NewMysqlContextManager(host, port, user, password, database)
```

## Manager workflows

- `Create` creates a conversation with its initial committed messages. `Load` returns committed history and distinguishes an unknown ID with `ErrContextNotFound`.
- `Append` adds committed messages. `Replace` replaces committed history while retaining the pending inbox and immutable run snapshots; the React Agent uses it after context compression.
- `Enqueue` adds user messages to the pending inbox. `CommitTurn` atomically appends a complete non-final assistant/tool turn, applies all pending messages after it, and clears the inbox.
- `SettleRun` atomically appends a completed final answer when present and records the terminal revision. Interrupted, canceled, and failed settlements preserve pending messages.
- `Fork` reconstructs a settled revision, excludes pending messages, and carries inherited fork points into an independent context.
- `Delete` removes the stream, events, and checkpoints for a context.

`SettleRun` is idempotent by `RunSignature`. Only the current retained run can settle; `ErrRunNotCurrent`, `ErrRunNotFound`, and `ErrRunNotSettled` distinguish invalid terminal and fork requests.

## Store contract

Custom backends implement five methods:

```go
type Store interface {
    Create(context.Context, *State) (common.ContextUID, error)
    Load(context.Context, common.ContextUID) (*State, error)
    LoadAt(context.Context, common.ContextUID, uint64) (*State, error)
    Append(context.Context, common.ContextUID, uint64, []Event) error
    Delete(context.Context, common.ContextUID) error
}
```

`State` is the materialized read model. `Event` is the incremental persistence unit. A conforming Store must:

1. Generate a new `ContextUID` and persist the initial checkpoint as revision `1`.
2. Isolate nested message values at the Store boundary so caller mutation cannot change persisted state or events.
3. Return `ErrContextNotFound` when a requested context does not exist and `ErrRevisionNotFound` when `LoadAt` cannot reconstruct a requested revision.
4. In `Append`, atomically append the complete event batch as revision `expectedRevision + 1` only when the stream is currently at `expectedRevision`. A mismatch returns `ErrRevisionConflict` without a partial write.
5. Reject malformed batches with `ErrInvalidEvent` and make `Delete` idempotent.

Manager retries revision conflicts up to a bounded limit. State transitions and event production remain in Manager; custom stores should use `ValidateEvents` and `ApplyEvents` rather than reproduce conversation semantics.

## Persistence formats

The File Store retains the existing versioned JSON file as the initial checkpoint and writes each later revision to an atomically renamed file under `<context>.jsonl.events`. Every 64 revisions it writes a replaceable checkpoint cache; events remain available for `LoadAt`.

SQLite and MySQL keep the stream head and compatibility baseline in `goat_context_conversations`, append revisions to `goat_context_events`, and write periodic materialized states to `goat_context_checkpoints`. Event and checkpoint writes share the stream-head transaction. Existing payload rows continue as baselines. For v0.2 databases, legacy message, pending-message, and run-snapshot rows are reconstructed and fixed as a baseline on the first append, so no offline migration is required.

New run snapshots retain only a revision rather than copying cumulative message history. Legacy snapshots with embedded messages remain readable. Inherited fork points are materialized when a fork is created so deleting the parent does not invalidate the child.

Use RAM for tests and short-lived processes, files for simple single-process local persistence, SQLite for durable single-node applications, and MySQL when multiple processes share a context store.
