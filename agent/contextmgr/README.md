# Context manager

`contextmgr.Manager` owns the conversation state machine. Storage packages own incremental persistence only.

```text
React Agent
    |
    v
contextmgr.Manager
    |
    v
contextmgr.ContextStore  ->  RAM | files | SQLite | MySQL
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

The persistence boundary is intentionally split between a small head and the
append-only event log. Mutations read only `ContextHead`; complete history is
loaded only for reads such as `Load` and `Fork`.

```go
type ContextStore interface {
    Create(context.Context, CreateRequest) (CreateResult, error)
    ReadHead(context.Context, common.ContextUID) (ContextHead, error)
    Append(context.Context, AppendRequest) (AppendResult, error)
    ReadEvents(context.Context, common.ContextUID, uint64) ([]RevisionedEvent, error)
    ReadView(context.Context, ReadViewRequest) (ContextView, error)
    Delete(context.Context, common.ContextUID) error
}
```

`ReadHead` returns only revision, finalized state, current run, and pending
count. `Append` performs compare-and-swap on
`AppendRequest.ExpectedRevision`, validates the event batch, updates the head,
and commits the event and projections atomically. A mismatch returns
`ErrRevisionConflict` without a partial write. `ReadEvents` reads an event
suffix for incremental projections. `ReadView` treats revision `0` as latest;
a non-zero revision is exact and may limit the returned message tail.

`Event` is the durable fact. `ContextHead` is the bounded mutation state.
`ContextView` is an on-demand read model and must not be used as the input to
ordinary mutations. Backends must isolate nested message values, return
`ErrContextNotFound` for unknown IDs, return `ErrRevisionNotFound` for missing
historical revisions, reject malformed batches with `ErrInvalidEvent`, and make
`Delete` idempotent.

The manager retries revision conflicts with a bounded loop. Database backends
should use a conditional head update as the cross-process CAS boundary. A
per-context writer queue may reduce retries within one server instance, but it
is not a replacement for the durable CAS check.

## Persistence formats

The File Store retains the existing versioned JSON file as the initial checkpoint and writes each later revision to an atomically renamed file under `<context>.jsonl.events`. Every 64 revisions it writes a replaceable checkpoint cache; events remain available for `LoadAt`.

SQLite and MySQL keep the stream head and compatibility baseline in `goat_context_conversations`, append revisions to `goat_context_events`, and write periodic materialized states to `goat_context_checkpoints`. Event and checkpoint writes share the stream-head transaction. Existing payload rows continue as baselines. For v0.2 databases, legacy message, pending-message, and run-snapshot rows are reconstructed and fixed as a baseline on the first append, so no offline migration is required.

New run snapshots retain only a revision rather than copying cumulative message history. Legacy snapshots with embedded messages remain readable. Inherited fork points are materialized when a fork is created so deleting the parent does not invalidate the child.

Use RAM for tests and short-lived processes, files for simple single-process local persistence, SQLite for durable single-node applications, and MySQL when multiple processes share a context store.
