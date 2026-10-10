package react

import (
	"context"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/util/logging"
)

// AgentCallbacks defines callback functions for agent lifecycle events
type AgentCallbacks struct {
	// OnTurnStart is called when a turn starts
	OnTurnStart func(ctx context.Context, args *CallbackTurnStartArgs) error

	// OnTurnComplete is called when a turn completes successfully
	OnTurnComplete func(ctx context.Context, args *CallbackTurnCompleteArgs) error

	// OnTurnFailed is called when a turn fails
	OnTurnFailed func(ctx context.Context, args *CallbackTurnFailedArgs) error

	// OnTurnInterrupted is called when a turn is interrupted
	OnTurnInterrupted func(ctx context.Context, args *CallbackTurnInterruptedArgs) error

	// OnTurnCanceled is called when a turn is canceled
	OnTurnCanceled func(ctx context.Context, args *CallbackTurnCanceledArgs) error

	// OnToolCallStarted is called when a tool execution starts
	OnToolCallStarted func(ctx context.Context, args *CallbackToolCallStartedArgs) error

	// OnToolCallCompleted is called when a tool execution completes successfully
	OnToolCallCompleted func(ctx context.Context, args *CallbackToolCallCompletedArgs) error

	// OnToolCallFailed is called when a tool execution fails
	OnToolCallFailed func(ctx context.Context, args *CallbackToolCallFailedArgs) error
}

// CallbackTurnStartArgs arguments for turn start
type CallbackTurnStartArgs struct {
	Signature  common.RunSignature
	ContextUID common.ContextUID
	MaxStep    int
	UserInput  string
}

// CallbackTurnCompleteArgs arguments for turn complete
type CallbackTurnCompleteArgs struct {
	Signature      common.RunSignature
	Usage          *common.AgentUsage
	IterationsUsed int
	ToolCallsUsed  int
	FinalAnswer    string
}

// CallbackTurnFailedArgs arguments for turn failed
type CallbackTurnFailedArgs struct {
	Signature      common.RunSignature
	Usage          *common.AgentUsage
	IterationsUsed int
	Operation      string
	Error          error
}

// CallbackTurnInterruptedArgs arguments for turn interrupted
type CallbackTurnInterruptedArgs struct {
	Signature      common.RunSignature
	Usage          *common.AgentUsage
	IterationsUsed int
	Reason         string
}

// CallbackTurnCanceledArgs arguments for turn canceled
type CallbackTurnCanceledArgs struct {
	Signature      common.RunSignature
	Usage          *common.AgentUsage
	IterationsUsed int
	Reason         string
}

// CallbackToolCallStartedArgs arguments for tool call started
type CallbackToolCallStartedArgs struct {
	Signature common.RunSignature
	Iteration int
	CallID    string
	Name      string
	Arguments map[string]any
}

// CallbackToolCallCompletedArgs arguments for tool call completed
type CallbackToolCallCompletedArgs struct {
	Signature common.RunSignature
	Iteration int
	CallID    string
	Name      string
	Result    string
	Images    []*message.ContentBlock
	Duration  time.Duration
	Usage     *common.AgentUsage
}

// CallbackToolCallFailedArgs arguments for tool call failed
type CallbackToolCallFailedArgs struct {
	Signature common.RunSignature
	Iteration int
	CallID    string
	Name      string
	Stage     common.ToolCallFailureStage
	Error     string
}

// safeCallback safely invokes a callback function, preventing panics
func safeCallback(ctx context.Context, name string, fn func() error) error {
	if fn == nil {
		return nil
	}

	defer func() {
		if r := recover(); r != nil {
			logging.Errorf("Callback %s panicked: %v", name, r)
		}
	}()

	if err := fn(); err != nil {
		logging.Warnf("Callback %s returned error: %v", name, err)
		return err
	}
	return nil
}

// cloneCallbacks clones the callback configuration to avoid concurrent modification
func cloneCallbacks(src *AgentCallbacks) *AgentCallbacks {
	if src == nil {
		return nil
	}
	clone := *src
	return &clone
}
