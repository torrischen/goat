package common

import (
	"time"

	"github.com/torrischen/goat/agent/message"
)

type AgentEventType string

const (
	AgentEventTypeTurnStarted       AgentEventType = "turn_started"
	AgentEventTypeContentDelta      AgentEventType = "content_delta"
	AgentEventTypeContentCompleted  AgentEventType = "content_completed"
	AgentEventTypeToolCallStarted   AgentEventType = "tool_call_started"
	AgentEventTypeToolCallCompleted AgentEventType = "tool_call_completed"
	AgentEventTypeToolCallFailed    AgentEventType = "tool_call_failed"
	AgentEventTypeTurnCompleted     AgentEventType = "turn_completed"
	AgentEventTypeTurnInterrupted   AgentEventType = "turn_interrupted"
	AgentEventTypeTurnCanceled      AgentEventType = "turn_canceled"
	AgentEventTypeTurnFailed        AgentEventType = "turn_failed"
)

// ContentType identifies the type of content in ContentDeltaEvent and ContentCompletedEvent
type ContentType string

const (
	ContentTypeText      ContentType = "text"
	ContentTypeReasoning ContentType = "reasoning"
)

// ContentPhase identifies the semantic phase of content
type ContentPhase string

const (
	ContentPhaseCommentary  ContentPhase = "commentary"
	ContentPhaseFinalAnswer ContentPhase = "final_answer"
	ContentPhaseReasoning   ContentPhase = "reasoning"
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

type TurnStartedEvent struct {
	Signature RunSignature `json:"run_signature"`
	MaxStep   int          `json:"max_step"`
}

func (TurnStartedEvent) Type() AgentEventType { return AgentEventTypeTurnStarted }

// ContentDeltaEvent streams content as it is generated. ContentType identifies whether
// it is text or reasoning. Phase is set when the provider labels the content
// while streaming; otherwise it may be unspecified until ContentCompletedEvent.
type ContentDeltaEvent struct {
	Delta       string       `json:"delta"`
	ContentType ContentType  `json:"content_type"`
	Phase       ContentPhase `json:"phase,omitempty"`
}

func (ContentDeltaEvent) Type() AgentEventType { return AgentEventTypeContentDelta }

// ContentCompletedEvent is emitted once per content segment when a model turn
// finishes. ContentType identifies text vs reasoning. Phase is always resolved:
// reasoning for reasoning content, commentary for text alongside tool calls,
// final_answer for the user-facing answer. Clients rendering deltas live can
// use this to reclassify content that streamed with an unspecified phase.
type ContentCompletedEvent struct {
	Content     string       `json:"content"`
	ContentType ContentType  `json:"content_type"`
	Phase       ContentPhase `json:"phase"`
}

func (ContentCompletedEvent) Type() AgentEventType { return AgentEventTypeContentCompleted }

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

type TurnCompletedEvent struct {
	Usage          *AgentUsage `json:"usage,omitempty"`
	IterationsUsed int         `json:"iterations_used"`
	ToolCalls      int         `json:"tool_calls"`
}

func (TurnCompletedEvent) Type() AgentEventType { return AgentEventTypeTurnCompleted }

type TurnInterruptedEvent struct {
	Usage          *AgentUsage `json:"usage,omitempty"`
	IterationsUsed int         `json:"iterations_used"`
	Reason         string      `json:"reason"`
}

func (TurnInterruptedEvent) Type() AgentEventType { return AgentEventTypeTurnInterrupted }

type TurnCanceledEvent struct {
	Usage          *AgentUsage `json:"usage,omitempty"`
	IterationsUsed int         `json:"iterations_used"`
	Reason         string      `json:"reason"`
}

func (TurnCanceledEvent) Type() AgentEventType { return AgentEventTypeTurnCanceled }

type TurnFailedEvent struct {
	Usage          *AgentUsage `json:"usage,omitempty"`
	IterationsUsed int         `json:"iterations_used"`
	Operation      string      `json:"operation"`
	Error          string      `json:"error"`
}

func (TurnFailedEvent) Type() AgentEventType { return AgentEventTypeTurnFailed }

func IsTerminalAgentEvent(event AgentEvent) bool {
	if event == nil {
		return false
	}
	switch event.Type() {
	case AgentEventTypeTurnCompleted,
		AgentEventTypeTurnInterrupted,
		AgentEventTypeTurnCanceled,
		AgentEventTypeTurnFailed:
		return true
	default:
		return false
	}
}
