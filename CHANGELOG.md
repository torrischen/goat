# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Highlights

- React system prompts no longer embed per-run skill descriptions. Enabled skills are discovered at runtime, keeping prompts stable across skill directories and runs.
- Conversation compression now runs before each model call, so oversized history is compacted before it is sent to the model while normal execution remains available when compression cannot be completed.
- System message content is now compared by hash before replacement, significantly improving LLM prompt cache hit rates by avoiding unnecessary message object recreation when content is unchanged.

### Added

- Added generic byte-oriented context storage backends for RAM, local files, Redis, and MongoDB, with reusable storage contract tests and immutable-object garbage collection.
- Added atomic `CreateIfAbsent` storage support so contexts created with a caller-provided UID cannot be overwritten by concurrent creators.
- Added the `list_available_skills` tool through `tools.ListAvailableSkills` for runtime skill discovery using the current run's `SkillsDir`.

### Changed

- **Breaking:** replaced `react.Agent.AddSkills` and `planexecute.Agent.AddSkills` with `EnableSkills()`. Applications must enable skill tools once and select the skill root per run through `AgentDoArgs.SkillsDir`.
- Skill descriptions are loaded by `list_available_skills` instead of being copied into the system prompt. `SKILL.md` files must now begin with a frontmatter block enclosed by `---` delimiters.
- Refactored React run-loop state, tool execution, finalization, and terminal outcome handling into a dedicated run implementation while preserving lifecycle events, callbacks, parallel tool execution, and usage accounting.
- Compression results are persisted only when they change the conversation. Compression usage is included in run usage and compression callbacks.
- System message updates now use FNV-1a hash comparison to detect content changes, avoiding unnecessary message replacement and context manager operations when system prompt content is unchanged between runs.
- **Breaking:** replaced the stateful context manager backends and removed the SQLite/MySQL context-manager packages. `contextmgr.Manager` now owns conversation workflows over the byte-oriented `contextmgr.Storage` contract (`Get`, `Set`, `CreateIfAbsent`, `CompareAndSwap`, `Delete`, and `List`). File, Redis, and MongoDB configurations are available through `goatc`; existing SQLite/MySQL integrations require migration to a supported backend.

### Fixed

- Context mutations now publish immutable sequence, revision, and run-index objects through one head CAS, preventing partial state visibility under concurrent updates and preserving historical fork snapshots.
- Compression failures now fall back to the original conversation context instead of preventing the normal model call.
- Compression strategies that make no changes no longer replace the persisted conversation with an equivalent context.

## [0.2.3] - 2026-08-21

### Highlights

- Simplified the public Agent event stream around run lifecycle, reasoning and
  assistant output, tool execution, final-answer confirmation, aggregate usage,
  and terminal outcomes.
- Added an English Agent lifecycle reference at
  `agent/react/agent-lifecycle.md`, including React and plan-execute event
  flows.

### Added

- `goatc` can now build `plan_execute` agents with configurable plan size, executor step limits, replanning limits, and plan lifecycle rendering in the TUI.
- `goatc` YAML now supports builtin terminal and asynchronous subagent tools, including optional bubblewrap sandbox configuration.
- Added `goatc init`, `goatc inspect`, `goatc run`, and runtime-aware validation through `goatc validate --check-runtime`.
- Added comprehensive callback system for React agent lifecycle events with 15 callback points covering run lifecycle, think cycles, tool execution, compression, steering, and final answers. Callbacks include panic recovery and thread-safe handling for use cases like cost tracking, performance monitoring, distributed tracing, and audit logging.

### Changed

- **Breaking:** the public `common.AgentEvent` set no longer includes model-call
  phase, context-compression, steering, or tool-request events. Model-call
  details remain available through React callbacks or internal metrics.
  `ReasoningDeltaEvent` and `AssistantTextDeltaEvent` are separate output
  channels, and `ToolCallStartedEvent` represents the beginning of execution.
- **Breaking:** replaced `contextmgr.Store` with `ContextStore`, separating lightweight `ContextHead` mutation state from on-demand `ContextView` reads. Mutations now use revision-based CAS through `ReadHead` and `Append`; `ReadEvents` exposes incremental log suffixes, while `ReadView` handles latest and historical materialization. SQLite and MySQL atomically update head, pending/run projections, events, and checkpoints, so ordinary mutations no longer load complete conversation history. Existing file and SQL state payloads remain readable without an offline migration.
- Simplified React agent planning prompt by consolidating 14 verbose constants into 4 concise ones, reducing planning prompt tokens by ~77% (~1,698 tokens per run) while preserving all core logic including decision boundaries, information gathering rules, granularity guidelines, and mandatory update requirements.

### Fixed

- Raised the minimum Go version to `1.26.6`, fixing the reachable standard-library vulnerabilities reported by `govulncheck`.

## [0.2.2] - 2026-08-11

### Highlights

- Added a dependency-aware plan-and-execute agent that plans multi-step tasks,
  delegates each ready step to a React agent, and synthesizes one final answer.
- React agents can now launch independent subagents in the background and poll
  their execution status and results through built-in tools.
- The terminal tool now offers an optional Linux sandbox backed by bubblewrap
  for isolated command execution with explicit filesystem and environment
  access.

### Added

- Added `agent/planexecute.Agent`, including validated dependency plans,
  configurable planning and execution limits, steering-triggered replanning,
  aggregate usage accounting, streamed final answers, and plan/step lifecycle
  events. The agent supports the standard `Do`, `Steer`, and `Fork` conversation
  workflows and delegates tool use to an existing `react.Agent`.
- Added `tools.SpawnSubAgent` and `tools.GetSubAgentStatus`. Spawned agents run
  asynchronously with inherited context metadata and skill configuration, and
  expose their run signature, terminal status, result or error, duration,
  iterations, tool calls, and token usage through status queries.
- Added `tools.TerminalSandboxed`, `tools.TerminalWithSandbox`, and
  `tools.TerminalWithConfig` with `SandboxConfig` for bubblewrap-based command
  isolation on Linux. Callers can configure writable and read-only bind mounts,
  environment preservation, the bubblewrap executable, and network namespace
  behavior while the existing `tools.Terminal` remains unsandboxed.
- Added examples and tests for plan-and-execute orchestration, concurrent
  subagents, and sandboxed terminal execution, plus terminal sandbox setup and
  security documentation.

### Fixed

- Fixed a redundant newline in the subagent example that caused a `go vet`
  warning.

## [0.2.1] - 2026-08-02

This release introduces per-invocation run identities, a typed execution event
stream, forkable settled runs, and a versioned persistence contract. These
changes make long-running agent activity easier to observe and correlate while
keeping conversation state transitions consistent across storage backends.

### Highlights

- Every `Agent.Do` call now has a `RunSignature` containing both the persistent
  `ContextUID` and a unique `RunUID`, so callers can correlate events, tools,
  webhooks, and retained messages with one invocation.
- The former step stream is replaced by typed lifecycle events covering model
  calls, compression, tool calls, steering, final-answer generation, usage,
  and terminal outcomes.
- Settled runs can be forked into independent conversations without copying
  pending steering messages or changing the source conversation.
- Conversation transitions now live in `contextmgr.Manager`; storage backends
  implement a small versioned compare-and-swap contract.

### Changed

- **Breaking:** `common.Agent.Do` now returns `common.RunSignature` instead of `common.ContextUID`. The signature contains the persistent conversation ID and a unique ID for that invocation.
- **Breaking:** `common.Agent.Do` now returns `streaming.Stream[common.AgentEvent]` while preserving its existing method shape. Concrete lifecycle, model, compression, tool, steering, final-answer, and terminal events replace the former `common.Step` stream.
- **Breaking:** `common.Agent` adds `Fork`.
- **Breaking:** the stateful `contextmgr.ContextManager` backend interface is replaced by a concrete `contextmgr.Manager` and a four-method versioned `contextmgr.Store` (`Create`, `Load`, `CompareAndSwap`, and `Delete`). Custom backends now implement persistence only.
- **Breaking:** `CommitFinal` and `SealRun` are replaced by one atomic `SettleRun`; `InitNew`, `GetAll`, `Reset`, and `EnqueuePendingMessages` become `Create`, `Load`, `Replace`, and `Enqueue`.
- Model thinking and final-answer generation now stream text through `AssistantTextDeltaEvent`; terminal events expose aggregate run usage and asynchronous failure, cancellation, or interruption state.
- Parallel tool completion events now arrive in actual completion order while tool results passed back to the model retain request order.
- `common.ToolResult` adds `Usage`, allowing nested agent tools to contribute token usage to the parent run total. `DefaultToolResult.AddUsage` accumulates this usage, and `AgentUsage` now has stable JSON field names.

### Added

- Explicit `Do` user messages carry a hidden `RunUID` context boundary, with helpers for extracting boundaries and grouping retained conversation messages by run.
- `RunStartedEvent`, tool context metadata, and final-answer webhooks expose the current run signature or `RunUID` for correlation.
- `Agent.Fork` creates an independent `ContextUID` from any settled `RunSignature`. RAM, file, SQLite, and MySQL managers persist immutable terminal snapshots, exclude pending steering from branches, and retain inherited fork points.
- SQLite and MySQL transparently read v0.2 context rows and upgrade them to complete versioned state payloads on the first successful compare-and-swap.

### Removed

- Removed `common.Step`, execution callbacks, `OptimizationAdvice`, and `AgentDoArgs.FinalAnswerStreamingFunc`. Event consumers observe execution directly through the stream returned by `Do`.
- Removed the unused `steps` field from final-answer webhook payloads.

### Fixed

- `goatc` now closes gRPC tool-provider connections during runtime shutdown and
  also cleans up connections opened before a later provider fails to load.

### Migration notes

- Update `Agent.Do` call sites to receive `(RunSignature, Stream[AgentEvent],
  error)`. Continue a conversation with `signature.ContextUID`, and drain the
  stream through exactly one terminal event before starting another run on the
  same conversation.
- Replace `common.Step` switches and callbacks with switches over concrete
  `common.AgentEvent` values. Text arrives in `AssistantTextDeltaEvent`; inspect
  `RunCompletedEvent`, `RunInterruptedEvent`, `RunCanceledEvent`, or
  `RunFailedEvent` for the terminal outcome and aggregate usage.
- Add `Usage() *common.AgentUsage` to custom `common.ToolResult`
  implementations. Return `nil` when the tool does not incur nested agent
  usage.
- Custom context backends must implement `contextmgr.Store` (`Create`, `Load`,
  `CompareAndSwap`, and `Delete`) and pass the Store to `contextmgr.NewManager`.
  Applications using the built-in RAM, file, SQLite, or MySQL constructors do
  not need to wire the Store directly.
- Replace direct uses of the old context-manager transition methods with
  `Create`, `Load`, `Replace`, `Enqueue`, and atomic `SettleRun` as applicable.
  Existing SQLite and MySQL v0.2 data is upgraded online on the first successful
  compare-and-swap; no offline migration is required.

### Maintenance

- Updated the release artifact pipeline to `actions/download-artifact@v8` and
  `softprops/action-gh-release@v3`.
- Pinned CodeQL actions to `v4.37.3`.

## [0.2.0] - 2026-07-31

### Added

- `goatc`, a YAML-driven Agent compiler that embeds local Go plugins in a Bubble Tea executable and supports provider-based loading of multiple gRPC tool services and MCP servers over stdio, SSE, or Streamable HTTP.
- Tagged releases publish versioned `goatc` archives for Linux, macOS, Windows, and FreeBSD on `amd64` and `arm64`, plus SHA-256 checksums.

### Changed

- React Agent skill roots are configurable per `Do` call through `AgentDoArgs.SkillsDir` and propagated to skill tools and callbacks through `AgentContext` metadata.

## [0.1.0] - 2026-07-29

### Added

- Asynchronous native tool-calling agent runtime with live steering, context compression, multimodal messages, callbacks, webhooks, and typed step streams.
- Persistent conversation context backed by RAM, files, SQLite, or MySQL.
- Built-in planning, skills, terminal, and shell tools, plus MCP, Go shared-library, and gRPC tool integrations.
- OpenAI-compatible, Gemini, Cohere, Voyage AI, and Ollama embedding clients.
- Dense vector, BM25, and hybrid Milvus retrievers, along with reusable prompt and streaming packages.
- Examples and package documentation for the agent, embedding, retrieval, prompt, and streaming APIs.
- GitHub Actions checks for formatting, vetting, race-enabled tests, coverage, vulnerability scanning, and CodeQL analysis.
- Contribution, security, code of conduct, and GitHub issue and pull request guidance.
- Dependabot configuration for Go modules and GitHub Actions.

[Unreleased]: https://github.com/torrischen/goat/compare/v0.2.3...HEAD
[0.2.3]: https://github.com/torrischen/goat/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/torrischen/goat/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/torrischen/goat/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/torrischen/goat/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/torrischen/goat/releases/tag/v0.1.0
