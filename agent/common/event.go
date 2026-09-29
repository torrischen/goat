package common

import (
	"time"

	"github.com/torrischen/goat/agent/message"
)

type AgentEventType string

const (
	AgentEventTypeRunStarted             AgentEventType = "run_started"
	AgentEventTypeAssistantTextDelta     AgentEventType = "assistant_text_delta"
	AgentEventTypeAssistantTextCompleted AgentEventType = "assistant_text_completed"
	AgentEventTypeReasoningDelta         AgentEventType = "reasoning_delta"
	AgentEventTypeToolCallStarted        AgentEventType = "tool_call_started"
	AgentEventTypeToolCallCompleted      AgentEventType = "tool_call_completed"
	AgentEventTypeToolCallFailed         AgentEventType = "tool_call_failed"
	AgentEventTypeFinalAnswerCompleted   AgentEventType = "final_answer_completed"
	AgentEventTypeRunCompleted           AgentEventType = "run_completed"
	AgentEventTypeRunInterrupted         AgentEventType = "run_interrupted"
	AgentEventTypeRunCanceled            AgentEventType = "run_canceled"
	AgentEventTypeRunFailed              AgentEventType = "run_failed"
)

type ToolCallFailureStage string

const (
	ToolCallFailureStageLookup    ToolCallFailureStage = "lookup"
	ToolCallFailureStageArguments ToolCallFailureStage = "arguments"
	ToolCallFailureStageExecution ToolCallFailureStage = "execution"
)

type AgentEvent interface {
	Type() AgentEventType
}

type RunStartedEvent struct {
	Signature RunSignature `json:"run_signature"`
	MaxStep   int          `json:"max_step"`
}

func (RunStartedEvent) Type() AgentEventType { return AgentEventTypeRunStarted }

// AssistantTextDeltaEvent streams assistant text as it is generated. Phase is
// set when the provider labels the text while streaming (OpenAI Responses);
// otherwise it is unspecified until the turn's AssistantTextCompletedEvent.
type AssistantTextDeltaEvent struct {
	Delta string            `json:"delta"`
	Phase message.TextPhase `json:"phase,omitempty"`
}

func (AssistantTextDeltaEvent) Type() AgentEventType { return AgentEventTypeAssistantTextDelta }

// AssistantTextCompletedEvent is emitted once per text segment when a model
// turn finishes, before any of that turn's tool calls start. Phase is always
// resolved: commentary for text the model wrote alongside tool calls (or that
// the provider labelled as commentary), final_answer for the user-facing
// answer. Clients that render deltas live can use it to reclassify text that
// streamed with an unspecified phase.
type AssistantTextCompletedEvent struct {
	Text  string            `json:"text"`
	Phase message.TextPhase `json:"phase"`
}

func (AssistantTextCompletedEvent) Type() AgentEventType { return AgentEventTypeAssistantTextCompleted }

type ReasoningDeltaEvent struct {
	Delta string `json:"delta"`
}

func (ReasoningDeltaEvent) Type() AgentEventType { return AgentEventTypeReasoningDelta }

type ToolCallStartedEvent struct {
	CallID    string         `json:"call_id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (ToolCallStartedEvent) Type() AgentEventType { return AgentEventTypeToolCallStarted }

type ToolCallCompletedEvent struct {
	CallID   string                  `json:"call_id"`
	Name     string                  `json:"name"`
	Result   string                  `json:"result"`
	Images   []*message.ContentBlock `json:"images,omitempty"`
	Duration time.Duration           `json:"duration"`
}

func (ToolCallCompletedEvent) Type() AgentEventType { return AgentEventTypeToolCallCompleted }

type ToolCallFailedEvent struct {
	CallID string               `json:"call_id"`
	Name   string               `json:"name"`
	Stage  ToolCallFailureStage `json:"stage"`
	Error  string               `json:"error"`
}

func (ToolCallFailedEvent) Type() AgentEventType { return AgentEventTypeToolCallFailed }

type FinalAnswerCompletedEvent struct {
	Answer string `json:"answer"`
}

func (FinalAnswerCompletedEvent) Type() AgentEventType { return AgentEventTypeFinalAnswerCompleted }

type RunCompletedEvent struct {
	Usage          *AgentUsage `json:"usage,omitempty"`
	IterationsUsed int         `json:"iterations_used"`
	ToolCalls      int         `json:"tool_calls"`
}

func (RunCompletedEvent) Type() AgentEventType { return AgentEventTypeRunCompleted }

type RunInterruptedEvent struct {
	Usage          *AgentUsage `json:"usage,omitempty"`
	IterationsUsed int         `json:"iterations_used"`
	Reason         string      `json:"reason"`
}

func (RunInterruptedEvent) Type() AgentEventType { return AgentEventTypeRunInterrupted }

type RunCanceledEvent struct {
	Usage          *AgentUsage `json:"usage,omitempty"`
	IterationsUsed int         `json:"iterations_used"`
	Reason         string      `json:"reason"`
}

func (RunCanceledEvent) Type() AgentEventType { return AgentEventTypeRunCanceled }

type RunFailedEvent struct {
	Usage          *AgentUsage `json:"usage,omitempty"`
	IterationsUsed int         `json:"iterations_used"`
	Operation      string      `json:"operation"`
	Error          string      `json:"error"`
}

func (RunFailedEvent) Type() AgentEventType { return AgentEventTypeRunFailed }

func IsTerminalAgentEvent(event AgentEvent) bool {
	if event == nil {
		return false
	}
	switch event.Type() {
	case AgentEventTypeRunCompleted,
		AgentEventTypeRunInterrupted,
		AgentEventTypeRunCanceled,
		AgentEventTypeRunFailed:
		return true
	default:
		return false
	}
}
