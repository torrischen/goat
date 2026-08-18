package react

import (
	"context"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/util/logging"
	"github.com/cloudwego/eino/schema"
)

// AgentCallbacks defines callback functions for agent lifecycle events
type AgentCallbacks struct {
	// OnRunStart is called when a run starts
	OnRunStart func(ctx context.Context, args *CallbackRunStartArgs) error

	// OnRunComplete is called when a run completes successfully
	OnRunComplete func(ctx context.Context, args *CallbackRunCompleteArgs) error

	// OnRunFailed is called when a run fails
	OnRunFailed func(ctx context.Context, args *CallbackRunFailedArgs) error

	// OnRunInterrupted is called when a run is interrupted
	OnRunInterrupted func(ctx context.Context, args *CallbackRunInterruptedArgs) error

	// OnRunCanceled is called when a run is canceled
	OnRunCanceled func(ctx context.Context, args *CallbackRunCanceledArgs) error

	// OnThinkStart is called before each think (LLM call)
	OnThinkStart func(ctx context.Context, args *CallbackThinkStartArgs) error

	// OnThinkComplete is called after each think completes
	OnThinkComplete func(ctx context.Context, args *CallbackThinkCompleteArgs) error

	// OnToolCallRequested is called when a tool is requested (after parsing arguments)
	OnToolCallRequested func(ctx context.Context, args *CallbackToolCallRequestedArgs) error

	// OnToolCallStarted is called when a tool execution starts
	OnToolCallStarted func(ctx context.Context, args *CallbackToolCallStartedArgs) error

	// OnToolCallCompleted is called when a tool execution completes successfully
	OnToolCallCompleted func(ctx context.Context, args *CallbackToolCallCompletedArgs) error

	// OnToolCallFailed is called when a tool execution fails
	OnToolCallFailed func(ctx context.Context, args *CallbackToolCallFailedArgs) error

	// OnIterationComplete is called after each iteration completes
	OnIterationComplete func(ctx context.Context, args *CallbackIterationCompleteArgs) error

	// OnFinalAnswer is called when a final answer is generated
	OnFinalAnswer func(ctx context.Context, args *CallbackFinalAnswerArgs) error

	// OnSteeringApplied is called when steering messages are applied
	OnSteeringApplied func(ctx context.Context, args *CallbackSteeringAppliedArgs) error

	// OnCompressionComplete is called after context compression completes
	OnCompressionComplete func(ctx context.Context, args *CallbackCompressionCompleteArgs) error
}

// CallbackRunStartArgs arguments for run start
type CallbackRunStartArgs struct {
	Signature  common.RunSignature
	ContextUID common.ContextUID
	MaxStep    int
	UserInput  string
}

// CallbackRunCompleteArgs arguments for run complete
type CallbackRunCompleteArgs struct {
	Signature      common.RunSignature
	Usage          *common.AgentUsage
	IterationsUsed int
	ToolCallsUsed  int
	FinalAnswer    string
}

// CallbackRunFailedArgs arguments for run failed
type CallbackRunFailedArgs struct {
	Signature      common.RunSignature
	Usage          *common.AgentUsage
	IterationsUsed int
	Operation      string
	Error          error
}

// CallbackRunInterruptedArgs arguments for run interrupted
type CallbackRunInterruptedArgs struct {
	Signature      common.RunSignature
	Usage          *common.AgentUsage
	IterationsUsed int
	Reason         string
}

// CallbackRunCanceledArgs arguments for run canceled
type CallbackRunCanceledArgs struct {
	Signature      common.RunSignature
	Usage          *common.AgentUsage
	IterationsUsed int
	Reason         string
}

// CallbackThinkStartArgs arguments for think start
type CallbackThinkStartArgs struct {
	Signature       common.RunSignature
	Iteration       int
	MessageCount    int
	WillCompress    bool
}

// CallbackThinkCompleteArgs arguments for think complete
type CallbackThinkCompleteArgs struct {
	Signature        common.RunSignature
	Iteration        int
	ModelUsage       *common.AgentUsage
	HasToolCalls     bool
	ToolCallCount    int
	HasFinalAnswer   bool
	ReasoningContent string
	WasCompressed    bool
	CompressionUsage *common.AgentUsage
}

// CallbackToolCallRequestedArgs arguments for tool call requested
type CallbackToolCallRequestedArgs struct {
	Signature common.RunSignature
	Iteration int
	CallID    string
	Name      string
	Arguments map[string]any
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
	Images    []*schema.ContentBlock
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

// CallbackIterationCompleteArgs arguments for iteration complete
type CallbackIterationCompleteArgs struct {
	Signature      common.RunSignature
	Iteration      int
	ToolCallsCount int
	UsageSoFar     *common.AgentUsage
}

// CallbackFinalAnswerArgs arguments for final answer
type CallbackFinalAnswerArgs struct {
	Signature common.RunSignature
	Answer    string
	Usage     *common.AgentUsage
}

// CallbackSteeringAppliedArgs arguments for steering applied
type CallbackSteeringAppliedArgs struct {
	Signature common.RunSignature
	Count     int
	BeforeRun bool
}

// CallbackCompressionCompleteArgs arguments for compression complete
type CallbackCompressionCompleteArgs struct {
	Signature            common.RunSignature
	Iteration            int
	OriginalMessageCount int
	CompressedMessageCount int
	Usage                *common.AgentUsage
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
