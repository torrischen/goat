# Agent SDK

`agent` is goat's Go agent SDK. Built on CloudWeGo Eino's `model.AgenticModel`, it provides native model tool calling, conversation context management, context compression, task planning, skills, MCP integration, tool plugins, multimodal input, and typed runtime events.

The `react` implementation lets the model decide whether and how to call tools. The `planexecute` implementation creates a dependency-aware plan and delegates each step to a React agent before producing one final answer.

## Features

- Native function calling with support for multiple tool calls in one model response.
- Compatibility with OpenAI, Claude, Gemini, and any other model that implements Eino's `model.AgenticModel`.
- File, in-memory, Redis, and MongoDB conversation context manager backends.
- Conversation continuation and persistent, protocol-safe steering through `ContextUID`.
- Per-`Do` `RunSignature` values and hidden context boundaries for grouping retained messages by run.
- Immutable terminal run snapshots and independent conversation branches through `Agent.Fork`.
- Precise, aggressive, and selective-discard context compression strategies.
- Task plan creation and updates, plus parallel execution of multiple tools.
- Per-run skill loading from a configurable directory, propagated to tools through `AgentContext` metadata.
- MCP tools, Go shared-library plugins, and gRPC tool plugins.
- Text, image URL, Base64 image, and binary image input.
- A per-run event stream returned directly by `Do`, with model deltas, tool lifecycle events, aggregate token usage, explicit terminal states, and final-answer webhooks.

## Directory structure

```text
agent/
├── common/                  # Shared agent, message, tool, context, and configuration types
│   ├── agent.go             # Agent, Do/Fork arguments, and compression configuration
│   ├── agentic_message.go   # Text and image message constructors
│   ├── ctx.go               # AgentContext with concurrency-safe metadata
│   ├── context_uid.go       # ContextUID conversation identifier
│   ├── run_signature.go     # RunUID, RunSignature, and context-boundary helpers
│   ├── event.go             # Strongly typed runtime events
│   ├── mcp_tool.go          # MCP Tool to common.Tool adapter
│   └── tool.go              # Tool, ToolResult, and JSON Schema helpers
├── contextmgr/
│   ├── context_manager.go   # Manager state machine and generic Storage contract
│   ├── file/                # File storage; defaults to data/conversations
│   ├── mongodb/             # MongoDB document storage
│   ├── ram/                 # In-process storage
│   └── redis/               # Redis key-value storage
├── planexecute/             # Planner and scheduler backed by a React step executor
├── react/                   # Native function-calling agent implementation
│   └── compression/         # Independent context-compression strategies
│       ├── precise.go       # Structured checkpoint strategy
│       ├── aggressive.go    # Text summarization strategy
│       └── discard_half.go  # Selective discard strategy
├── toolplugin/              # Shared-library and gRPC tool plugins
└── tools/                   # Built-in planning, skills, terminal, and shell tools
```

## Installation

The project requires Go 1.26.6 or newer.

```bash
go get github.com/torrischen/goat/agent/react
go get github.com/torrischen/goat/agent/contextmgr/ram
```

Install the Eino adapter for the model provider you plan to use. For example:

```bash
go get github.com/cloudwego/eino-ext/components/model/agenticopenai
```

## Quick start

The following example uses the OpenAI Responses API. `modelMaxTokensK` is measured in **thousands of tokens**; for example, `128` represents a model context limit of approximately 128K tokens.

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/react"
	"github.com/torrischen/goat/streaming"
)

func main() {
	ctx := context.Background()

	llm, err := agenticopenai.NewResponsesModel(ctx, &agenticopenai.ResponsesConfig{
		APIKey: os.Getenv("OPENAI_API_KEY"),
		Model:  "gpt-5.2",
	})
	if err != nil {
		log.Fatal(err)
	}

	agent := react.NewAgent(llm, 128, ram.NewRAMContextManager())

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "Introduce the goat Agent SDK in three sentences."},
		MaxStep:   8,
	})
	if err != nil {
		log.Fatal(err)
	}

	var usage *common.AgentUsage
	for {
		event, err := eventStream.ReadWithContext(ctx)
		if errors.Is(err, streaming.ErrStreamClosed) {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		switch event := event.(type) {
		case common.AssistantTextDeltaEvent:
			fmt.Print(event.Delta)
		case common.RunCompletedEvent:
			usage = event.Usage
		case common.RunInterruptedEvent:
			log.Printf("agent interrupted: %s", event.Reason)
		case common.RunCanceledEvent:
			log.Fatalf("agent canceled: %s", event.Reason)
		case common.RunFailedEvent:
			log.Fatalf("agent failed during %s: %s", event.Operation, event.Error)
		}
	}
	if usage == nil {
		usage = &common.AgentUsage{}
	}

	fmt.Printf("\nContextUID: %s\n", signature.ContextUID)
	fmt.Printf("RunUID: %s\n", signature.RunUID)
	fmt.Printf("Token usage: prompt=%d cached=%d completion=%d\n",
		usage.PromptTokens, usage.CachedTokens, usage.CompletionTokens)
}
```

`Do` stores the current user message, starts the agent loop in the background, and immediately returns a `RunSignature` and `common.AgentEvent` stream. `RunSignature.ContextUID` identifies the persistent conversation while `RunSignature.RunUID` uniquely identifies this invocation. Every call has an independent stream, so callers do not need to poll the context manager to infer run boundaries. Each item is a concrete event value with no wrapper envelope. A normally consumed stream contains exactly one of `RunCompletedEvent`, `RunInterruptedEvent`, `RunCanceledEvent`, or `RunFailedEvent`, then closes. Errors returned directly by `Do` are synchronous setup failures; failures after `Do` returns are reported by `RunFailedEvent`.

The explicit user message for every successful `Do` stores its `RunUID` under `common.RunUIDExtraKey` in `AgenticMessage.Extra`. This metadata is persisted by every context-manager backend, is not added to model-visible text, and survives context compression because user inputs are protected. Use `common.RunUIDFromMessage` for individual boundaries or split retained history directly:

```go
messages, err := manager.Load(ctx, signature.ContextUID)
if err != nil {
	log.Fatal(err)
}
preamble, runs := common.SplitMessagesByRun(signature.ContextUID, messages)
```

`preamble` contains the system prompt and any legacy messages stored before run markers were introduced. Each `RunMessages` segment begins with one explicitly submitted `Do` user message and continues up to the next run boundary. Steering messages have no boundary of their own and remain in the surrounding run. Compression can replace detailed tool-process messages with cross-run artifacts, so these segments describe retained logical context rather than an immutable audit log.

## Forking a settled run

Drain the event stream through its terminal event and `streaming.ErrStreamClosed`, then pass the returned `RunSignature` to `Fork`:

```go
forkedContextUID, err := agent.Fork(ctx, &common.AgentForkArgs{
	From: signature,
})
if err != nil {
	log.Fatal(err)
}

branchSignature, branchEvents, err := agent.Do(ctx, &common.AgentDoArgs{
	ContextUID: forkedContextUID,
	UserInput:  common.AgentUserInput{Text: "Take a different approach from here."},
})
```

`Fork` is inclusive: the new conversation contains committed history through the selected run. It does not start the agent loop and therefore returns only a new `ContextUID`; the next `Do` creates the first branch-specific `RunUID`. The source remains unchanged, and pending steering messages are not copied. Historical run markers and their snapshots are inherited, so a branch can be forked again at any inherited run that has a snapshot.

The React agent settles a run snapshot before emitting `RunCompletedEvent`, `RunInterruptedEvent`, `RunCanceledEvent`, or a run-failure terminal event. Calling `Fork` before that point returns an error wrapping `contextmgr.ErrRunNotSettled`. Missing contexts and unknown runs wrap `ErrContextNotFound` and `ErrRunNotFound`. All validation errors remain compatible with `errors.Is` through the Agent-level wrapper. If settlement persistence fails, the terminal event is `RunFailedEvent` with operation `settle run`, and that run is not forkable.

Snapshots preserve the exact retained context at the terminal boundary and are isolated from later `Replace` calls made by compression. They are not an uncompressed audit log: if the selected run had already compressed older tool-process messages, its revision contains that checkpoint or summary. New snapshots store the immutable revision key captured at settlement; `Fork` loads that revision when a fork is requested. `Delete` removes the context head, while unreachable immutable objects are reclaimed later by `CollectGarbage`.

## Steering a running conversation

`Steer` queues one or more independent user messages in the conversation's context-manager-backed inbox:

```go
err = agent.Steer(ctx, &common.AgentSteerArgs{
	ContextUID: signature.ContextUID,
	UserInputs: []common.AgentUserInput{
		{Text: "Do not deploy yet."},
		{Text: "Run the complete test suite first."},
	},
})
```

The current assistant turn is allowed to settle. If it contains tool calls, all corresponding tool results are committed before the queued messages. At that protocol-safe boundary, the context manager atomically appends the completed tool turn followed by the queued user messages, and the next `Think` sees them.

A final answer always wins: it is streamed and committed immediately, while any messages still pending at that boundary are discarded. Once a final assistant message is committed, `Steer` returns `contextmgr.ErrConversationFinalized`. Calling `Do` again appends a new user message and reopens steering for that run. Because final generation and `Steer` can race, a `Steer` accepted just before final commit may still be discarded. Concurrent `Do` calls for the same `ContextUID` remain unsupported.

## Registering custom tools

Use `common.NewDefaultTool` to define a tool quickly. Parameters must be a JSON Schema object; `common.NewToolParameters` is the recommended constructor.

```go
calculator := common.NewDefaultTool(
	"calculator",
	"Add two numbers.",
	common.NewToolParameters(
		common.ToolProperty{
			Name:        "a",
			Type:        "number",
			Required:    true,
			Description: "First number.",
		},
		common.ToolProperty{
			Name:        "b",
			Type:        "number",
			Required:    true,
			Description: "Second number.",
		},
	),
	func(_ *common.AgentContext, inputs map[string]any) common.ToolResult {
		a, aOK := inputs["a"].(float64)
		b, bOK := inputs["b"].(float64)
		if !aOK || !bOK {
			return common.NewDefaultToolResult("a and b must be numbers")
		}
		return common.NewDefaultToolResult(fmt.Sprintf("%g", a+b))
	},
)

agent.AddTool(context.Background(), calculator)
```

Describe arrays and nested objects with `ToolProperty.Items` and `ToolProperty.Properties`. See [common/ARRAY_PARAMETERS.md](common/ARRAY_PARAMETERS.md) for additional examples.

Tool names are automatically converted to a model-compatible format. If names collide, the SDK appends a numeric suffix. Tool implementations can read per-run context metadata with `AgentContext.GetMeta`. The SDK reserves `context_uid` and `run_uid` metadata for the current invocation and overwrites caller-provided values for those keys; use `common.RunSignatureFromContext` to read them together.

To pause the background agent loop after a tool runs—for example, while waiting for human approval—wrap the tool with `common.InterruptLoopAfter`:

```go
agent.AddTool(ctx, common.InterruptLoopAfter(approvalTool))
```

The wrapped tool still executes and its result is persisted. After the current tool batch is stored, the SDK stops the background loop and submits a `RunInterruptedEvent` rather than treating the pause as an error from `Do`.

## Run options

The main fields in `common.AgentDoArgs` are:

| Field | Description |
| --- | --- |
| `UserInput` | Text and image input for the current run. |
| `ContextUID` | Creates a conversation when empty; continues an existing conversation when set. |
| `MaxStep` | Maximum execution rounds. Values at or below zero default to `8`. A batch of tool calls counts as one step. |
| `SpecialRequirements` | Additional requirements appended to the system prompt and used during final-answer generation. |
| `Compress` | Whether to compress context as it approaches the model limit. |
| `CompressionOptions` | Compression strategy and number of recent messages to retain. |
| `ContextMeta` | Concurrency-safe metadata injected into the run's `AgentContext`. |
| `FinalAnswerWebhook` | Sends an HTTP webhook after the final answer is persisted. |
| `EnablePlanning` | Exposes the built-in plan creation and update tools to the model. |
| `PlanUsageInstruction` | Tells the model when and how to create plans while planning is enabled. |
| `ToolExecutionOptions` | Controls parallel tool execution and maximum concurrency. |
| `SkillsDir` | Skill root for this run. Empty uses `skills`; the resolved path is available through `AgentContext` metadata. |
| `SkillUsageInstruction` | Tells the model when and how to use skills. |

### Context compression

```go
Compress: true,
CompressionOptions: common.CompressionOptions{
	Strategy:       common.CompressionStrategyPrecise,
	RecentMessages: 12,
},
```

All three strategies preserve system messages, every user input, final agent answers, and calls and results for `load_skills` and `read_specified_file_in_skill`. Only detailed tool-process messages are compressed. When a regular tool is called repeatedly, results from the same tool within a compression range are first merged into one message while retaining each call's `CallID`, original content blocks, and order. Protected messages are never included in this merge. `RecentMessages` preserves an additional number of recent messages in their original form.

Available strategies:

- `CompressionStrategyPrecise` converts older detailed tool-process messages into structured checkpoints, prioritizing exact references.
- `CompressionStrategyAggressive` summarizes older detailed tool-process messages as text while preserving recent raw messages.
- `CompressionStrategyDiscardHalf` calls no model and discards the oldest half of detailed tool-process messages.

## Conversation context management

Conversation behavior is centralized in `contextmgr.Manager`. Persistence backends implement the byte-oriented storage interface:

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

`Manager` owns conversation transitions, event records, immutable revisions, pending messages, run settlement, and forks. `CreateIfAbsent` atomically creates a key without overwriting an existing value. `CompareAndSwap` atomically publishes a new context head only when its previous value matches. Unknown keys return `contextmgr.ErrNotFound`, failed comparisons return `contextmgr.ErrCASConflict`, and `Delete` is idempotent. Manager retries revision conflicts, so concurrent steering and turn commits do not lose updates.

The Manager surface is intentionally concrete and small enough to read by workflow:

- `Create`, `Load`, `Append`, `Replace`, and `Delete` manage committed history. `Replace` preserves pending messages and run snapshots.
- `Enqueue` accepts only user messages. `CommitTurn` atomically appends a protocol-complete non-final turn and then applies every pending message in order.
- `SettleRun` atomically records the terminal outcome and snapshot revision. For `completed`, it also appends the final assistant answer and clears pending messages in the same event revision. `interrupted`, `canceled`, and `failed` preserve pending messages for the next run. Repeating a settled signature is idempotent.
- `Fork` creates a new conversation from a settled snapshot, excludes pending messages, and retains inherited historical fork points.

Application code normally calls `Agent.Do`, `Agent.Steer`, and `Agent.Fork`; Agent implementations use the Manager transition methods. Custom persistence code should not reproduce those transitions.

### Choosing a backend

```go
// In-process storage for tests and short-lived processes.
manager := ram.NewRAMContextManager()

// File storage; an empty path uses data/conversations.
manager, err := file.NewFileContextManager("")

// Redis; URL and namespace have local defaults when omitted.
manager, err := redis.NewRedisContextManager(redis.Config{
	URL: "redis://127.0.0.1:6379/0",
})

// MongoDB; database, collection, and namespace have defaults when omitted.
manager, err := mongodb.NewMongoDBContextManager(mongodb.Config{
	URI:      "mongodb://127.0.0.1:27017",
	Database: "goat",
})
```

`react.NewAgent(llm, modelMaxTokensK, nil)` uses a Manager over file storage by default.

Redis uses Lua for atomic head CAS. MongoDB uses a conditional single-document update. Both isolate context data through configurable namespaces; immutable objects are published only after the head CAS succeeds.

### Continuing a conversation

```go
firstSignature, firstRun, err := agent.Do(ctx, &common.AgentDoArgs{
	UserInput: common.AgentUserInput{Text: "Remember that the project codename is goat."},
})
// Read firstRun until it returns streaming.ErrStreamClosed.

secondSignature, secondRun, err := agent.Do(ctx, &common.AgentDoArgs{
	ContextUID: firstSignature.ContextUID,
	UserInput: common.AgentUserInput{Text: "What is the project codename?"},
})
// Read secondRun until it returns streaming.ErrStreamClosed.
```

`firstSignature.ContextUID == secondSignature.ContextUID`, while their `RunUID` values differ. When continuing a conversation, the SDK loads its history and updates the system prompt with the current run options. Appending the new `Do` user input reopens steering after the previous final answer. Because `Do` starts the agent loop asynchronously, drain the previous event stream until it closes before starting another run with the same `ContextUID`. This confirms that the previous run has finished or paused.

## Multimodal input

```go
input := common.AgentUserInput{
	Text: "Describe the contents of this image.",
	Images: []*schema.ContentBlock{
		common.ImageURLWithDetailBlock("https://example.com/image.png", "high"),
		common.BinaryImageBlock("image/png", imageBytes),
	},
}
```

Available helpers include:

- `ImageURLBlock` / `ImageURLWithDetailBlock`
- `BinaryImageBlock`
- `Base64ImageBlock`
- `TextBlock` / `AssistantTextBlock` / `ReasoningBlock`

Image support and support for the `detail` parameter depend on the selected `model.AgenticModel` implementation.

## Planning and parallel tools

`NewAgent` registers the built-in `generate_plan` and `update_plan` tools, but exposes them to the model only when planning is enabled.

```go
_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
	UserInput:      common.AgentUserInput{Text: "Analyze the project and complete the refactor."},
	EnablePlanning: true,
	PlanUsageInstruction: "Create a plan for complex tasks and update it after completing each step.",
	ToolExecutionOptions: &common.ToolExecutionOptions{
		EnableParallel: true,
		MaxConcurrency: 4,
	},
})
```

When `MaxConcurrency` is not set, parallel mode defaults to a maximum concurrency of `3`. When parallel mode is disabled, tools execute sequentially.

## Skills

Skills are loaded from `skills/` in the current working directory by default. The root can be changed independently for every `Do` call. Each skill is a subdirectory containing a `SKILL.md` file:

```text
skills/
└── code-review/
    ├── SKILL.md
    └── references/
```

`SKILL.md` must begin with a frontmatter block enclosed by `---` delimiters. Enable skill tools once after creating the agent, then select the directory on each run:

```go
agent.EnableSkills()

_, events, err := agent.Do(ctx, &common.AgentDoArgs{
	UserInput: common.AgentUserInput{Text: "Review this change."},
	SkillsDir: "./project-skills",
})
```

An empty `SkillsDir` uses `common.SkillDefaultFolder` (`skills`). Skill descriptions are discovered dynamically by the `list_available_skills` tool instead of being embedded in the system prompt. The resolved directory is stored under `common.InternalToolSkillsDirMetaKey` in the run's `AgentContext`; `list_available_skills`, `load_skills`, `read_specified_file_in_skill`, and custom tools therefore use the same per-run root:

```go
skillsDir := common.SkillsDirFromContext(agentContext)
```

The model reads full skill files on demand instead of placing all skill content in context up front.

## MCP and tool plugins

### MCP

```go
err := agent.RegisterMCPTools(ctx, mcpClient)
```

You can also call `common.ListMCPTools(ctx, mcpClient)` directly to obtain `[]common.Tool`. MCP text, resource, and structured results are converted into agent tool results.

### Plugins

```go
// Load Go .so plugins from a directory.
err := agent.LoadSharedLibPluginTools(ctx, "./plugins")

// Connect to one or more gRPC tool-plugin services.
err := agent.LoadRPCPluginTools(ctx, "127.0.0.1:50051")
```

See the [tool plugin cookbook](toolplugin/README.md) for plugin interfaces, build instructions, and a gRPC service example.

## Events and webhooks

### Reading the event stream

`Do` keeps its single-call shape and returns `streaming.Stream[common.AgentEvent]`. The public stream uses concrete event types for user-observable semantics; consumers do not need to inspect a model-call phase or maintain a model-call state machine.

```go
signature, eventStream, err := agent.Do(ctx, args)
if err != nil {
	return err
}
for {
	event, err := eventStream.ReadWithContext(ctx)
	if errors.Is(err, streaming.ErrStreamClosed) {
		break
	}
	if err != nil {
		return err
	}

	switch event := event.(type) {
	case common.ReasoningDeltaEvent:
		renderThinking(event.Delta)
	case common.AssistantTextDeltaEvent:
		renderAnswer(event.Delta)
	case common.ToolCallStartedEvent:
		fmt.Printf("tool started: %s(%v)\n", event.Name, event.Arguments)
	case common.ToolCallCompletedEvent:
		fmt.Printf("tool completed: %s -> %s\n", event.Name, event.Result)
	case common.ToolCallFailedEvent:
		fmt.Printf("tool failed: %s -> %s\n", event.Name, event.Error)
	case common.FinalAnswerCompletedEvent:
		fmt.Printf("final answer stored for %s/%s\n", signature.ContextUID, signature.RunUID)
	case common.RunFailedEvent:
		return fmt.Errorf("agent failed during %s: %s", event.Operation, event.Error)
	}
}
```

The public event set is:

| Family | Events |
| --- | --- |
| Run lifecycle | `RunStartedEvent`, `RunCompletedEvent`, `RunInterruptedEvent`, `RunCanceledEvent`, `RunFailedEvent` |
| Output | `ReasoningDeltaEvent`, `AssistantTextDeltaEvent`, `FinalAnswerCompletedEvent` |
| Tool execution | `ToolCallStartedEvent`, `ToolCallCompletedEvent`, `ToolCallFailedEvent` |

`ReasoningDeltaEvent` and `AssistantTextDeltaEvent` are separate output channels. A UI can render the former as gray or collapsible thinking and the latter as normal assistant text. `FinalAnswerCompletedEvent` is the authoritative confirmation that the answer has been settled and committed to conversation history. The terminal event's `Usage` is the aggregate for the run. With parallel tools, completion and failure events arrive in actual completion order, while tool-result messages sent back to the model retain the model's original request order.

Model-call phase, compression, steering, and tool-request details are not public stream events. Use React callbacks or internal logging when those implementation details are required for metrics, auditing, or approval workflows.

Always inspect the terminal event. A stream close is only the transport boundary; `RunFailedEvent` is how asynchronous model, persistence, or runtime failures are surfaced after `Do` has returned.

### Final-answer webhook

```go
FinalAnswerWebhook: &common.FinalAnswerWebhookConfig{
	URL: "https://example.com/webhooks/final-answer",
	Headers: map[string]string{
		"Authorization": "Bearer <token>",
	},
	Timeout: 5 * time.Second,
},
```

The webhook payload contains the event name, agent name, `ContextUID`, `RunUID`, user input, final answer, and generation time. Runtime lifecycle and tool details remain in the event stream and are not duplicated in the webhook payload.

## Built-in tools

`agent/tools` provides these constructors:

- `GeneratePlan()` / `UpdatePlan()` maintain the current task plan.
- `LoadSkills()` / `ReadSpecifiedFileInSkill()` discover and read skills.
- `Terminal()` executes a parameterized command.
- `ShellCommand()` executes a command string through a shell.

The terminal tool limits execution time and output size. It can execute local commands directly, so register it only when needed in a controlled environment and combine it with working-directory, permission, and container isolation policies.

## Testing

Run all agent tests:

```bash
go test ./agent/...
```

Run the primary submodule tests:

```bash
go test ./agent/react/... ./agent/tools ./agent/contextmgr/... ./agent/toolplugin
```

## Best practices

- Set `modelMaxTokensK` to the model's real context length so compression starts at the correct time.
- Prefer Redis or MongoDB context managers for shared production deployments. The RAM context manager is intended for tests and short-lived processes.
- Validate tool parameter types; never trust model-generated arguments directly.
- Add authorization, idempotency, timeouts, and audit logging to tools with side effects.
- Read aggregate token usage from the terminal event. Use callbacks or internal metrics for per-model-call accounting.
- Use `context.WithTimeout` or `context.WithCancel` to control the lifecycle of the complete agent run.
