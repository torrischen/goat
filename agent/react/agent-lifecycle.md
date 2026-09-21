# Agent Lifecycle Events

This document describes goat's public Agent event stream. Events are sent
through `streaming.Stream[common.AgentEvent]`, and clients identify their
semantics with Go type assertions.

## Design Principles

Public events describe Agent behavior observable by clients without exposing
internal model-call phases. `ModelCallPhase`, model-call start/completion/failure
events, context-compression events, and steering events are not part of the
public event stream.

Output is split into two independent event types:

- `ReasoningDeltaEvent`: model reasoning content, suitable for a gray or
  collapsible area.
- `AssistantTextDeltaEvent`: user-visible assistant text, suitable for the
  normal answer area.

`FinalAnswerCompletedEvent` is the authoritative confirmation of the final
answer. It means the answer has been completed and written to conversation
history. Clients do not need to infer final-answer state from a model-call
phase or another field.

## Public Event Types

```go
const (
    AgentEventTypeRunStarted           = "run_started"
    AgentEventTypeAssistantTextDelta   = "assistant_text_delta"
    AgentEventTypeReasoningDelta       = "reasoning_delta"
    AgentEventTypeToolCallStarted      = "tool_call_started"
    AgentEventTypeToolCallCompleted    = "tool_call_completed"
    AgentEventTypeToolCallFailed       = "tool_call_failed"
    AgentEventTypeFinalAnswerCompleted = "final_answer_completed"
    AgentEventTypeRunCompleted         = "run_completed"
    AgentEventTypeRunInterrupted       = "run_interrupted"
    AgentEventTypeRunCanceled          = "run_canceled"
    AgentEventTypeRunFailed            = "run_failed"
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

### `RunStartedEvent`

The first event for every accepted run.

```go
type RunStartedEvent struct {
    Signature RunSignature `json:"run_signature"`
    MaxStep   int          `json:"max_step"`
}
```

### `ReasoningDeltaEvent`

A streamed piece of model reasoning content. Clients may render it in gray,
place it in a collapsible section, or hide it completely.

```go
type ReasoningDeltaEvent struct {
    Delta string `json:"delta"`
}
```

Reasoning availability depends on the model and provider adapter. If a
provider returns only ordinary assistant text, the Agent cannot reliably
recover reasoning from that text.

### `AssistantTextDeltaEvent`

A streamed piece of user-visible assistant text. Clients can append it
straight to the answer area.

```go
type AssistantTextDeltaEvent struct {
    Delta string `json:"delta"`
}
```

The React Agent may produce ordinary assistant text before the model returns a
tool call. This event therefore represents visible assistant text; clients do
not need to decide whether a particular model call is ultimately complete.

### `ToolCallStartedEvent`

Sent when a tool starts executing. It replaces the old
`ToolCallRequestedEvent`. Use `OnToolCallRequested` for observation or audit
before execution; the callback is not, by itself, an approval gate.

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

### `FinalAnswerCompletedEvent`

Sent after the Agent confirms the final answer and completes conversation
persistence.

```go
type FinalAnswerCompletedEvent struct {
    Answer string `json:"answer"`
}
```

This is the final-answer confirmation point. It may immediately follow
ordinary assistant deltas, or it may follow a separate final model call made
after the maximum iteration count is reached.

### Terminal Events

Exactly one terminal event is sent last for every run:

```go
type RunCompletedEvent struct {
    Usage          *AgentUsage `json:"usage,omitempty"`
    IterationsUsed int         `json:"iterations_used"`
    ToolCalls      int         `json:"tool_calls"`
}

type RunInterruptedEvent struct {
    Usage          *AgentUsage `json:"usage,omitempty"`
    IterationsUsed int         `json:"iterations_used"`
    Reason         string      `json:"reason"`
}

type RunCanceledEvent struct {
    Usage          *AgentUsage `json:"usage,omitempty"`
    IterationsUsed int         `json:"iterations_used"`
    Reason         string      `json:"reason"`
}

type RunFailedEvent struct {
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
RunStarted
ReasoningDelta*                 optional
AssistantTextDelta*
FinalAnswerCompleted
RunCompleted
```

### Answer After Tool Calls

```text
RunStarted
ReasoningDelta*                 optional
AssistantTextDelta*             optional visible preamble
ToolCallStarted
ToolCallCompleted / ToolCallFailed
ReasoningDelta*                 optional
AssistantTextDelta*
FinalAnswerCompleted
RunCompleted
```

### Tool-triggered Interruption

```text
RunStarted
ToolCallStarted
ToolCallCompleted
RunInterrupted
```

### Failure

```text
RunStarted
ReasoningDelta*                 optional
RunFailed
```

### Plan-and-execute Run

A `planexecute.Agent` adds planning events around the child React-agent
activity:

```text
RunStarted
PlanCreated
StepStarted
  ReasoningDelta*               optional
  AssistantTextDelta*
  ToolCallStarted /
  ToolCallCompleted or ToolCallFailed
StepCompleted
PlanRevised                    optional after steering
...
FinalAnswerCompleted
RunCompleted
```

The parent stream exposes child output and tool-execution events, but the child
run's own terminal event is consumed by the plan executor. The parent terminal
event is the authoritative outcome for the plan-and-execute run.

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
    case common.ReasoningDeltaEvent:
        renderThinking(event.Delta)

    case common.AssistantTextDeltaEvent:
        renderAnswer(event.Delta)

    case common.ToolCallStartedEvent:
        renderToolStarted(event.Name, event.Arguments)

    case common.ToolCallCompletedEvent:
        renderToolResult(event.Name, event.Result)

    case common.ToolCallFailedEvent:
        renderToolError(event.Name, event.Error)

    case common.FinalAnswerCompletedEvent:
        commitAnswer(event.Answer)

    case common.RunCompletedEvent:
        finish(event.Usage)

    case common.RunInterruptedEvent:
        handleInterrupted(event.Reason)

    case common.RunCanceledEvent:
        handleCanceled(event.Reason)

    case common.RunFailedEvent:
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

The event stream is intended for client-facing rendering and run lifecycle
control. React Agent callbacks provide finer-grained internal observation
points, including:

- `OnThinkComplete`
- `OnToolCallRequested`
- `OnToolCallStarted`
- `OnToolCallCompleted`
- `OnToolCallFailed`
- `OnSteeringApplied`
- `OnFinalAnswer`
- `OnRunComplete`
- `OnRunFailed`

The callback API and public event stream are independent. Removing public
model-call events does not remove these callbacks.

## Related Files

- `agent/common/event.go`: public common event types
- `agent/react/agent.go`: React Agent lifecycle and tool execution
- `agent/react/think.go`: splits model output into reasoning and assistant deltas
- `agent/react/final.go`: final-answer generation after the maximum iteration count
- `agent/planexecute/agent.go`: plan-and-execute orchestration and child-event forwarding
- `goatc/tui/tui.go`: terminal UI event consumption
