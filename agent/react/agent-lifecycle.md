# Agent Lifecycle Events

This document describes goat's public Agent event stream. Events are sent
through `streaming.Stream[common.AgentEvent]`, and clients identify their
semantics with Go type assertions.

## Design Principles

Public events describe Agent behavior observable by clients without exposing
internal model-call phases. `ModelCallPhase`, model-call start/completion/failure
events, context-compression events, and steering events are not part of the
public event stream.

Output is split into content events with type identification:

- `ContentDeltaEvent`: streams content as it is generated, with `ContentType`
  identifying whether it is text or reasoning, and `Phase` indicating its
  semantic role (commentary, final_answer, or reasoning).
- `ContentCompletedEvent`: emitted once per content segment when a model turn
  finishes, always with a resolved `Phase`.

`ContentCompletedEvent` with `Phase` set to `final_answer` is the authoritative
confirmation that the final answer has been completed and written to conversation
history. Clients do not need to infer final-answer state from a model-call phase
or another field.

## Public Event Types

```go
const (
    AgentEventTypeTurnStarted      = "turn_started"
    AgentEventTypeContentDelta     = "content_delta"
    AgentEventTypeContentCompleted = "content_completed"
    AgentEventTypeToolCallStarted  = "tool_call_started"
    AgentEventTypeToolCallCompleted = "tool_call_completed"
    AgentEventTypeToolCallFailed   = "tool_call_failed"
    AgentEventTypeTurnCompleted    = "turn_completed"
    AgentEventTypeTurnInterrupted  = "turn_interrupted"
    AgentEventTypeTurnCanceled     = "turn_canceled"
    AgentEventTypeTurnFailed       = "turn_failed"
)
```

When using `planexecute.Agent`, the stream also contains package-specific
planning events. These events implement `common.AgentEvent` but are not emitted
by `react.Agent`:

- `planexecute.PlanCreatedEvent`
- `planexecute.PlanRevisedEvent`
- `planexecute.StepStartedEvent`
- `planexecute.StepCompletedEvent`

## Event Details

### `TurnStartedEvent`

The first event for every accepted turn.

```go
type TurnStartedEvent struct {
    Signature RunSignature `json:"run_signature"`
    MaxStep   int          `json:"max_step"`
}
```

### `ContentDeltaEvent`

A streamed piece of content as it is generated. `ContentType` identifies whether
it is text or reasoning. `Phase` is set when the provider labels the content
while streaming; otherwise it may be unspecified until `ContentCompletedEvent`.

```go
type ContentDeltaEvent struct {
    Delta       string       `json:"delta"`
    ContentType ContentType  `json:"content_type"`  // "text" or "reasoning"
    Phase       ContentPhase `json:"phase,omitempty"`  // "commentary", "final_answer", or "reasoning"
}
```

Clients may render reasoning content in gray, place it in a collapsible section,
or hide it completely. Text content with `Phase` set to `final_answer` is the
user-visible answer.

### `ContentCompletedEvent`

Emitted once per content segment when a model turn finishes. `ContentType`
identifies text vs reasoning. `Phase` is always resolved: reasoning for reasoning
content, commentary for text alongside tool calls, final_answer for the
user-facing answer.

```go
type ContentCompletedEvent struct {
    Content     string       `json:"content"`
    ContentType ContentType  `json:"content_type"`
    Phase       ContentPhase `json:"phase"`
}
```

Clients rendering deltas live can use this to reclassify content that streamed
with an unspecified phase. `ContentCompletedEvent` with `ContentType` "text" and
`Phase` "final_answer" is the authoritative confirmation that the final answer
has been completed and written to conversation history.

### `ToolCallStartedEvent`

Sent when a tool starts executing.

```go
type ToolCallStartedEvent struct {
    CallID    string         `json:"call_id"`
    Name      string         `json:"name"`
    Arguments map[string]any `json:"arguments"`
}
```

When tools execute in parallel, started and completed events may be
interleaved.

### `ToolCallCompletedEvent`

Sent when a tool returns successfully.

```go
type ToolCallCompletedEvent struct {
    CallID   string                 `json:"call_id"`
    Name     string                 `json:"name"`
    Result   string                 `json:"result"`
    Images   []*schema.ContentBlock `json:"images,omitempty"`
    Duration time.Duration          `json:"duration"`
}
```

### `ToolCallFailedEvent`

Sent when tool lookup, argument parsing, or execution fails. The `Stage` field
identifies the specific failure stage; the event type already indicates that
the call failed.

```go
type ToolCallFailedEvent struct {
    CallID string               `json:"call_id"`
    Name   string               `json:"name"`
    Stage  ToolCallFailureStage `json:"stage"`
    Error  string               `json:"error"`
}
```

### Terminal Events

Exactly one terminal event is sent last for every turn:

```go
type TurnCompletedEvent struct {
    Usage          *AgentUsage `json:"usage,omitempty"`
    IterationsUsed int         `json:"iterations_used"`
    ToolCalls      int         `json:"tool_calls"`
}

type TurnInterruptedEvent struct {
    Usage          *AgentUsage `json:"usage,omitempty"`
    IterationsUsed int         `json:"iterations_used"`
    Reason         string      `json:"reason"`
}

type TurnCanceledEvent struct {
    Usage          *AgentUsage `json:"usage,omitempty"`
    IterationsUsed int         `json:"iterations_used"`
    Reason         string      `json:"reason"`
}

type TurnFailedEvent struct {
    Usage          *AgentUsage `json:"usage,omitempty"`
    IterationsUsed int         `json:"iterations_used"`
    Operation      string      `json:"operation"`
    Error          string      `json:"error"`
}
```

Use `common.IsTerminalAgentEvent(event)` to identify terminal events.

## Typical Flows

### Direct Answer

```text
TurnStarted
ContentDelta (reasoning)*       optional
ContentDelta (text, final_answer)*
ContentCompleted (text, final_answer)
TurnCompleted
```

### Answer After Tool Calls

```text
TurnStarted
ContentDelta (reasoning)*       optional
ContentDelta (text, commentary)*  optional visible preamble
ContentCompleted (text, commentary)*
ToolCallStarted
ToolCallCompleted / ToolCallFailed
ContentDelta (reasoning)*       optional
ContentDelta (text, final_answer)*
ContentCompleted (text, final_answer)
TurnCompleted
```

### Tool-triggered Interruption

```text
TurnStarted
ToolCallStarted
ToolCallCompleted
TurnInterrupted
```

### Failure

```text
TurnStarted
ContentDelta (reasoning)*       optional
TurnFailed
```

### Plan-and-execute Turn

A `planexecute.Agent` adds planning events around the child React-agent
activity:

```text
TurnStarted
PlanCreated
StepStarted
  ContentDelta (reasoning)*     optional
  ContentDelta (text)*
  ToolCallStarted /
  ToolCallCompleted or ToolCallFailed
StepCompleted
PlanRevised                    optional after steering
...
ContentCompleted (text, final_answer)
TurnCompleted
```

The parent stream exposes child output and tool-execution events, but the child
turn's own terminal event is consumed by the plan executor. The parent terminal
event is the authoritative outcome for the plan-and-execute turn.

## Client Example

```go
signature, events, err := agent.Do(ctx, args)
if err != nil {
    return err
}

for {
    event, err := events.ReadWithContext(ctx)
    if errors.Is(err, streaming.ErrStreamClosed) {
        break
    }
    if err != nil {
        return err
    }

    switch event := event.(type) {
    case common.ContentDeltaEvent:
        if event.ContentType == common.ContentTypeReasoning {
            renderThinking(event.Delta)
        } else if event.ContentType == common.ContentTypeText {
            renderAnswer(event.Delta)
        }

    case common.ContentCompletedEvent:
        if event.ContentType == common.ContentTypeText && event.Phase == common.ContentPhaseFinalAnswer {
            commitAnswer(event.Content)
        }

    case common.ToolCallStartedEvent:
        renderToolStarted(event.Name, event.Arguments)

    case common.ToolCallCompletedEvent:
        renderToolResult(event.Name, event.Result)

    case common.ToolCallFailedEvent:
        renderToolError(event.Name, event.Error)

    case common.TurnCompletedEvent:
        finish(event.Usage)

    case common.TurnInterruptedEvent:
        handleInterrupted(event.Reason)

    case common.TurnCanceledEvent:
        handleCanceled(event.Reason)

    case common.TurnFailedEvent:
        handleFailure(event.Operation, event.Error)
    }
}
```

## Non-public Events

The following information still exists in Agent internals or callbacks, but is
not sent through the public `AgentEvent` stream:

- model-call start, completion, and failure
- model-call phases such as `think`, `compression`, and `final`
- context-compression start, completion, and failure
- applied steering messages
- the tool-request stage

Use callbacks or internal logging for metrics, auditing, or approval workflows.
Do not make a UI depend on the Agent's model-call phase.

## Callbacks and the Event Stream

The event stream is intended for client-facing rendering and turn lifecycle
control. React Agent callbacks provide observation points that align with the
public event stream:

- `OnTurnStart`
- `OnToolCallStarted`
- `OnToolCallCompleted`
- `OnToolCallFailed`
- `OnTurnComplete`
- `OnTurnFailed`
- `OnTurnInterrupted`
- `OnTurnCanceled`

The callback API and public event stream are aligned. Callbacks fire at the
same lifecycle points as their corresponding events.

## Related Files

- `agent/common/event.go`: public common event types
- `agent/react/agent.go`: React Agent lifecycle and tool execution
- `agent/react/think.go`: splits model output into reasoning and assistant deltas
- `agent/react/final.go`: final-answer generation after the maximum iteration count
- `agent/planexecute/agent.go`: plan-and-execute orchestration and child-event forwarding
- `goatc/tui/tui.go`: terminal UI event consumption
