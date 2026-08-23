# Context manager

`contextmgr.Manager` owns conversation, run, settle, fork, snapshot, compaction, and garbage-collection semantics. Storage implementations only provide generic byte-oriented key-value operations.

```text
React Agent
    |
    v
contextmgr.Manager
    |
    v
contextmgr.Storage  ->  RAM | files | Redis | MongoDB
```

Applications normally use a backend constructor:

```go
manager := ram.NewRAMContextManager()
manager, err := file.NewFileContextManager("data/conversations")
manager, err := redis.NewRedisContextManager(redis.Config{
    URL: "redis://localhost:6379/0",
})
manager, err := mongodb.NewMongoDBContextManager(mongodb.Config{
    URI:      "mongodb://localhost:27017",
    Database: "goat",
})
```

## Manager workflows

- `Create` creates a conversation with its initial committed messages.
- `Load` returns committed history and distinguishes an unknown ID with `ErrContextNotFound`.
- `Append` adds committed messages.
- `Replace` replaces committed history while retaining pending messages and immutable run snapshots.
- `Enqueue` adds user messages to the pending inbox.
- `CommitTurn` atomically appends a complete assistant/tool turn, applies pending messages, and clears the inbox.
- `SettleRun` atomically records a terminal outcome and appends a completed final answer when present.
- `Fork` creates an independent context sharing the immutable roots of a settled revision.
- `Delete` removes the context head. `CollectGarbage` later removes unreachable immutable objects after an age guard.

Run snapshot lookup uses a stable per-context run UID index (the context and run segments are encoded for key safety), with fallback to the immutable run chain for data written by older versions.

`SettleRun` is idempotent by `RunSignature`. Only the current retained run can settle; `ErrRunNotCurrent`, `ErrRunNotFound`, and `ErrRunNotSettled` distinguish invalid terminal and fork requests.

## Storage contract

Third-party backends implement six generic operations:

```go
type Storage interface {
    Get(context.Context, string) ([]byte, error)
    Set(context.Context, string, []byte) error
    CreateIfAbsent(context.Context, string, []byte) error
    CompareAndSwap(context.Context, string, []byte, []byte) error
    Delete(context.Context, string) error
    List(context.Context, string) ([]string, error)
}
```

Values must be isolated from caller mutation. Unknown keys return `ErrNotFound`; `CreateIfAbsent` returns `ErrCASConflict` when the key already exists; failed comparisons return `ErrCASConflict`; `Delete` is idempotent. `CreateIfAbsent` and `CompareAndSwap` must be atomic across all processes sharing the backend. File storage coordinates instances within one process; Redis and MongoDB provide cross-process atomicity. Third-party implementations should preserve these semantics when testing custom backends.

The Manager writes immutable sequence, run-index, and revision objects first, then publishes a small context head with one CAS. Failed or conflicting mutations therefore cannot expose partial state. Ordinary revisions do not retain a parent history; settled run snapshots are kept by their immutable run index and a stable per-context `runs:<encoded context UID>:<encoded run UID>:snapshot` lookup. Message sequences are compacted automatically when their tree becomes too deep.

## Backend notes

- RAM is intended for tests and short-lived processes.
- Files provide local persistence and coordinate Manager instances within one process.
- Redis uses a Lua script for atomic CAS and SCAN for prefix listing. `KeyPrefix` isolates an application namespace.
- MongoDB stores one key per document and uses a conditional single-document update for CAS. `Database`, `Collection`, and `KeyPrefix` isolate application data.

Run `CollectGarbage` periodically with an age greater than the maximum expected mutation duration. The age guard protects immutable objects written by concurrent mutations before their head CAS is published.
