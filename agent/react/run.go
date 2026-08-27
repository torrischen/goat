package react

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/streaming"
	"github.com/torrischen/goat/util/logging"

	"github.com/alitto/pond/v2"
	"github.com/bytedance/sonic"
)

type reactRun struct {
	agent       *Agent
	parentCtx   context.Context
	ctx         *common.AgentContext
	args        *common.AgentDoArgs
	callOpts    []llm.CallOption
	callbacks   *AgentCallbacks
	signature   common.RunSignature
	contextUID  common.ContextUID
	messages    []*message.Message
	eventStream streaming.Stream[common.AgentEvent]
	maxStep     int
	iterations  int
	toolCalls   int
	usage       *common.AgentUsage
	settled     bool
}

type preparedToolCall struct {
	call      *message.ToolCall
	tool      common.Tool
	arguments map[string]any
	execute   bool
}

type toolExecutionErrors struct {
	mu  sync.Mutex
	err error
}

func (e *toolExecutionErrors) record(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err == nil {
		e.err = err
	}
}

func (e *toolExecutionErrors) get() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (r *reactRun) prepareContext() (*common.AgentUsage, bool, error) {
	prepared, err := r.agent.prepareConversationContext(
		r.ctx,
		r.contextUID,
		r.messages,
		r.args.Compress,
		r.args.CompressionOptions,
		r.callOpts...,
	)
	if err != nil {
		return nil, false, operationError("prepare context", err)
	}
	if !prepared.compressed {
		return nil, false, nil
	}

	r.messages = prepared.messages
	addRunUsage(r.usage, prepared.usage)

	if r.callbacks != nil && r.callbacks.OnCompressionComplete != nil {
		_ = safeCallback(r.ctx, "OnCompressionComplete", func() error {
			return r.callbacks.OnCompressionComplete(r.ctx, &CallbackCompressionCompleteArgs{
				Signature:              r.signature,
				Iteration:              r.iterations,
				OriginalMessageCount:   prepared.originalMessageCount,
				CompressedMessageCount: len(prepared.messages),
				Usage:                  prepared.usage,
			})
		})
	}
	return prepared.usage, true, nil
}

func (r *reactRun) writeFinal() error {
	if _, _, err := r.prepareContext(); err != nil {
		return err
	}

	finalMessage, usage, err := r.agent.generateFinalAnswer(
		r.ctx,
		r.messages,
		r.args.SpecialRequirements,
		r.eventStream,
		r.callOpts...,
	)
	if err != nil {
		return operationError("generate final answer", err)
	}
	addRunUsage(r.usage, usage)
	finalAnswer := assistantText(finalMessage)
	if err := settleConversationFinal(
		r.ctx,
		r.agent.contextManager,
		r.signature,
		&r.messages,
		finalMessage,
	); err != nil {
		return operationError("settle run", err)
	}
	r.settled = true

	r.iterations++
	if err := r.eventStream.WriteWithContext(r.ctx, common.FinalAnswerCompletedEvent{Answer: finalAnswer}); err != nil {
		return operationError("write final answer event", err)
	}

	// Callback: OnFinalAnswer (from writeFinal)
	if r.callbacks != nil && r.callbacks.OnFinalAnswer != nil {
		_ = safeCallback(r.ctx, "OnFinalAnswer", func() error {
			return r.callbacks.OnFinalAnswer(r.ctx, &CallbackFinalAnswerArgs{
				Signature: r.signature,
				Answer:    finalAnswer,
				Usage:     snapshotRunUsage(r.usage),
			})
		})
	}

	r.agent.sendFinalAnswerWebhook(
		r.ctx,
		r.args.FinalAnswerWebhook,
		r.agent.buildFinalAnswerWebhookPayload(r.signature, r.args, finalAnswer),
	)

	return nil
}

func (r *reactRun) completeDirectAnswer(raw *message.Message) error {
	finalAnswer := assistantText(raw)
	if err := settleConversationFinal(
		r.ctx,
		r.agent.contextManager,
		r.signature,
		&r.messages,
		raw,
	); err != nil {
		return operationError("settle run", err)
	}
	r.settled = true

	r.iterations++
	if err := r.eventStream.WriteWithContext(r.ctx, common.FinalAnswerCompletedEvent{Answer: finalAnswer}); err != nil {
		return operationError("write final answer event", err)
	}

	// Callback: OnFinalAnswer (from direct response)
	if r.callbacks != nil && r.callbacks.OnFinalAnswer != nil {
		_ = safeCallback(r.ctx, "OnFinalAnswer", func() error {
			return r.callbacks.OnFinalAnswer(r.ctx, &CallbackFinalAnswerArgs{
				Signature: r.signature,
				Answer:    finalAnswer,
				Usage:     snapshotRunUsage(r.usage),
			})
		})
	}

	r.agent.sendFinalAnswerWebhook(
		r.ctx,
		r.args.FinalAnswerWebhook,
		r.agent.buildFinalAnswerWebhookPayload(r.signature, r.args, finalAnswer),
	)

	return nil
}

func (r *reactRun) executeToolCalls(toolCalls []*message.ToolCall) ([]*message.ToolResult, error) {
	toolResults := make([]*message.ToolResult, len(toolCalls))
	toolUsages := make([]*common.AgentUsage, len(toolCalls))
	prepared := make([]preparedToolCall, len(toolCalls))

	for i, toolCall := range toolCalls {
		if toolCall == nil {
			continue
		}
		r.toolCalls++
		item := preparedToolCall{
			call:      toolCall,
			tool:      r.agent.toolsMap[toolCall.Name],
			arguments: map[string]any{},
		}
		var failureStage common.ToolCallFailureStage
		var failureMessage string
		if item.tool == nil {
			failureStage = common.ToolCallFailureStageLookup
			failureMessage = "Tool not found: " + toolCall.Name
		} else if err := sonic.UnmarshalString(toolCall.Arguments, &item.arguments); err != nil {
			failureStage = common.ToolCallFailureStageArguments
			failureMessage = "Failed to parse arguments: " + err.Error()
			item.arguments = map[string]any{}
		} else {
			if item.arguments == nil {
				item.arguments = map[string]any{}
			}
			item.execute = true
		}
		prepared[i] = item

		// Callback: OnToolCallRequested
		if r.callbacks != nil && r.callbacks.OnToolCallRequested != nil {
			_ = safeCallback(r.ctx, "OnToolCallRequested", func() error {
				return r.callbacks.OnToolCallRequested(r.ctx, &CallbackToolCallRequestedArgs{
					Signature: r.signature,
					Iteration: r.iterations,
					CallID:    toolCall.CallID,
					Name:      toolCall.Name,
					Arguments: cloneToolArguments(item.arguments),
				})
			})
		}

		if !item.execute {
			observation := "Error: " + failureMessage
			toolResults[i] = &message.ToolResult{
				CallID:  toolCall.CallID,
				Name:    toolCall.Name,
				Content: toolResultContentBlocks(observation, nil),
			}
			if err := r.eventStream.WriteWithContext(r.ctx, common.ToolCallFailedEvent{
				CallID: toolCall.CallID,
				Name:   toolCall.Name,
				Stage:  failureStage,
				Error:  failureMessage,
			}); err != nil {
				return nil, operationError("write tool failure event", err)
			}

			// Callback: OnToolCallFailed
			if r.callbacks != nil && r.callbacks.OnToolCallFailed != nil {
				_ = safeCallback(r.ctx, "OnToolCallFailed", func() error {
					return r.callbacks.OnToolCallFailed(r.ctx, &CallbackToolCallFailedArgs{
						Signature: r.signature,
						Iteration: r.iterations,
						CallID:    toolCall.CallID,
						Name:      toolCall.Name,
						Stage:     failureStage,
						Error:     failureMessage,
					})
				})
			}
		}
	}

	concurr := 1
	if r.args.ToolExecutionOptions != nil && r.args.ToolExecutionOptions.EnableParallel {
		if r.args.ToolExecutionOptions.MaxConcurrency > 0 {
			concurr = r.args.ToolExecutionOptions.MaxConcurrency
		} else {
			concurr = 3
		}
	}
	pool := pond.NewPool(concurr, pond.WithQueueSize(len(toolCalls)))
	batchErrors := &toolExecutionErrors{}
	for i := range prepared {
		if !prepared[i].execute {
			continue
		}
		index := i
		pool.Submit(func() {
			r.executeToolCall(index, prepared[index], toolResults, toolUsages, batchErrors)
		})
	}

	pool.StopAndWait()
	for _, usage := range toolUsages {
		addRunUsage(r.usage, usage)
	}
	if err := batchErrors.get(); err != nil {
		return nil, err
	}
	return toolResults, nil
}

func (r *reactRun) executeToolCall(
	index int,
	item preparedToolCall,
	toolResults []*message.ToolResult,
	toolUsages []*common.AgentUsage,
	batchErrors *toolExecutionErrors,
) {
	if err := r.eventStream.WriteWithContext(r.ctx, common.ToolCallStartedEvent{
		CallID:    item.call.CallID,
		Name:      item.call.Name,
		Arguments: cloneToolArguments(item.arguments),
	}); err != nil {
		batchErrors.record(operationError("write tool started event", err))
		return
	}

	// Callback: OnToolCallStarted
	if r.callbacks != nil && r.callbacks.OnToolCallStarted != nil {
		_ = safeCallback(r.ctx, "OnToolCallStarted", func() error {
			return r.callbacks.OnToolCallStarted(r.ctx, &CallbackToolCallStartedArgs{
				Signature: r.signature,
				Iteration: r.iterations,
				CallID:    item.call.CallID,
				Name:      item.call.Name,
				Arguments: cloneToolArguments(item.arguments),
			})
		})
	}

	startedAt := time.Now()
	result := item.tool.Execute(r.ctx, item.arguments)
	if result == nil {
		msg := "tool returned a nil result"
		toolResults[index] = &message.ToolResult{
			CallID:  item.call.CallID,
			Name:    item.call.Name,
			Content: toolResultContentBlocks("Error: "+msg, nil),
		}
		if err := r.eventStream.WriteWithContext(r.ctx, common.ToolCallFailedEvent{
			CallID: item.call.CallID,
			Name:   item.call.Name,
			Stage:  common.ToolCallFailureStageExecution,
			Error:  msg,
		}); err != nil {
			batchErrors.record(operationError("write tool failure event", err))
		}

		// Callback: OnToolCallFailed (nil result)
		if r.callbacks != nil && r.callbacks.OnToolCallFailed != nil {
			_ = safeCallback(r.ctx, "OnToolCallFailed", func() error {
				return r.callbacks.OnToolCallFailed(r.ctx, &CallbackToolCallFailedArgs{
					Signature: r.signature,
					Iteration: r.iterations,
					CallID:    item.call.CallID,
					Name:      item.call.Name,
					Stage:     common.ToolCallFailureStageExecution,
					Error:     msg,
				})
			})
		}
		return
	}

	observation := result.String()
	images := append([]*message.ContentBlock(nil), result.ImageParts()...)
	toolUsages[index] = result.Usage()
	toolResults[index] = &message.ToolResult{
		CallID:  item.call.CallID,
		Name:    item.call.Name,
		Content: toolResultContentBlocks(observation, images),
	}
	if err := r.eventStream.WriteWithContext(r.ctx, common.ToolCallCompletedEvent{
		CallID:   item.call.CallID,
		Name:     item.call.Name,
		Result:   observation,
		Images:   append([]*message.ContentBlock(nil), images...),
		Duration: time.Since(startedAt),
	}); err != nil {
		batchErrors.record(operationError("write tool completed event", err))
	}

	// Callback: OnToolCallCompleted
	if r.callbacks != nil && r.callbacks.OnToolCallCompleted != nil {
		_ = safeCallback(r.ctx, "OnToolCallCompleted", func() error {
			return r.callbacks.OnToolCallCompleted(r.ctx, &CallbackToolCallCompletedArgs{
				Signature: r.signature,
				Iteration: r.iterations,
				CallID:    item.call.CallID,
				Name:      item.call.Name,
				Result:    observation,
				Images:    append([]*message.ContentBlock(nil), images...),
				Duration:  time.Since(startedAt),
				Usage:     result.Usage(),
			})
		})
	}
}

func (r *reactRun) completeToolTurn(toolCalls []*message.ToolCall, raw *message.Message) error {
	toolResults, err := r.executeToolCalls(toolCalls)
	if err != nil {
		return err
	}

	pendingMessages := []*message.Message{assistantMessageFromResponse(raw)}
	for _, tr := range toolResults {
		if tr == nil {
			continue
		}
		pendingMessages = append(pendingMessages, common.FunctionToolResultMessage(tr))
	}

	appliedSteering, err := commitConversationTurn(
		r.ctx,
		r.agent.contextManager,
		r.contextUID,
		&r.messages,
		pendingMessages...,
	)
	if err != nil {
		return operationError("commit tool turn", err)
	}
	r.iterations++

	// Callback: OnIterationComplete
	if r.callbacks != nil && r.callbacks.OnIterationComplete != nil {
		_ = safeCallback(r.ctx, "OnIterationComplete", func() error {
			return r.callbacks.OnIterationComplete(r.ctx, &CallbackIterationCompleteArgs{
				Signature:      r.signature,
				Iteration:      r.iterations,
				ToolCallsCount: len(toolCalls),
				UsageSoFar:     snapshotRunUsage(r.usage),
			})
		})
	}

	if len(appliedSteering) > 0 {
		logging.Infof(
			"Agent.Do: applied %d steering messages after tool turn in conversation %s",
			len(appliedSteering),
			r.contextUID,
		)

		// Callback: OnSteeringApplied (after iteration)
		if r.callbacks != nil && r.callbacks.OnSteeringApplied != nil {
			_ = safeCallback(r.ctx, "OnSteeringApplied", func() error {
				return r.callbacks.OnSteeringApplied(r.ctx, &CallbackSteeringAppliedArgs{
					Signature: r.signature,
					Count:     len(appliedSteering),
					BeforeRun: false,
				})
			})
		}
	}

	if common.ConsumeInterruptSignal(r.ctx) {
		logging.Infof("Agent.Do: interrupt signal received, stopping agent loop for conversation %s", r.contextUID)
		return errAgentLoopInterrupted
	}
	return nil
}

func (r *reactRun) runLoop() error {
	for {
		select {
		case <-r.ctx.Done():
			logging.Infof("Agent.Do: context canceled, stopping agent")
			return r.ctx.Err()
		default:
		}

		if r.iterations >= r.maxStep {
			return r.writeFinal()
		}

		compressionUsage, wasCompressed, err := r.prepareContext()
		if err != nil {
			return err
		}

		// Callback: OnThinkStart
		if r.callbacks != nil && r.callbacks.OnThinkStart != nil {
			_ = safeCallback(r.ctx, "OnThinkStart", func() error {
				return r.callbacks.OnThinkStart(r.ctx, &CallbackThinkStartArgs{
					Signature:    r.signature,
					Iteration:    r.iterations,
					MessageCount: len(r.messages),
					WillCompress: wasCompressed,
				})
			})
		}

		thinkResult, err := r.agent.think(r.ctx, &thinkArgs{
			Messages: r.messages,
		}, r.eventStream, r.callOpts...)
		if err != nil {
			return operationError("think", err)
		}
		thinkResult.IsCompressed = wasCompressed
		thinkResult.CompressionUsage = compressionUsage
		addRunUsage(r.usage, thinkResult.ModelUsage)

		raw := thinkResult.RawResponse
		reasoningContent := messageReasoning(raw)
		toolCalls := functionToolCalls(raw)

		// Callback: OnThinkComplete
		if r.callbacks != nil && r.callbacks.OnThinkComplete != nil {
			_ = safeCallback(r.ctx, "OnThinkComplete", func() error {
				return r.callbacks.OnThinkComplete(r.ctx, &CallbackThinkCompleteArgs{
					Signature:        r.signature,
					Iteration:        r.iterations,
					ModelUsage:       thinkResult.ModelUsage,
					HasToolCalls:     len(toolCalls) > 0,
					ToolCallCount:    len(toolCalls),
					HasFinalAnswer:   len(toolCalls) == 0,
					ReasoningContent: reasoningContent,
					WasCompressed:    thinkResult.IsCompressed,
					CompressionUsage: thinkResult.CompressionUsage,
				})
			})
		}

		select {
		case <-r.ctx.Done():
			logging.Infof("Agent.Do: context canceled after LLM call, stopping agent")
			return r.ctx.Err()
		default:
		}

		if len(toolCalls) > 0 {
			if err := r.completeToolTurn(toolCalls, raw); err != nil {
				return err
			}
			if r.iterations >= r.maxStep {
				return r.writeFinal()
			}
			continue
		}

		return r.completeDirectAnswer(raw)
	}
}

func (r *reactRun) finish(err error) {
	var settleErr error
	if !r.settled {
		outcome := contextmgr.RunOutcomeFailed
		switch {
		case errors.Is(err, errAgentLoopInterrupted):
			outcome = contextmgr.RunOutcomeInterrupted
		case r.ctx.Err() != nil:
			outcome = contextmgr.RunOutcomeCanceled
		}
		settleCtx, cancelSettle := context.WithTimeout(context.WithoutCancel(r.parentCtx), settleRunTimeout)
		settleErr = r.agent.contextManager.SettleRun(settleCtx, &contextmgr.SettleRunArgs{
			Signature: r.signature,
			Outcome:   outcome,
		})
		cancelSettle()
	}
	if settleErr != nil {
		if err != nil {
			logging.Errorf(
				"Agent.Do: run stopped before it settled for %s/%s: %v",
				r.signature.ContextUID,
				r.signature.RunUID,
				err,
			)
		}
		err = operationError("settle run", settleErr)
	}
	usage := snapshotRunUsage(r.usage)

	var terminal common.AgentEvent
	switch {
	case err == nil:
		terminal = common.RunCompletedEvent{
			Usage:          usage,
			IterationsUsed: r.iterations,
			ToolCalls:      r.toolCalls,
		}
		// Callback: OnRunComplete
		if r.callbacks != nil && r.callbacks.OnRunComplete != nil {
			_ = safeCallback(r.ctx, "OnRunComplete", func() error {
				return r.callbacks.OnRunComplete(r.ctx, &CallbackRunCompleteArgs{
					Signature:      r.signature,
					Usage:          usage,
					IterationsUsed: r.iterations,
					ToolCallsUsed:  r.toolCalls,
					FinalAnswer:    "", // Final answer already sent via OnFinalAnswer
				})
			})
		}
	case errors.Is(err, errAgentLoopInterrupted):
		terminal = common.RunInterruptedEvent{
			Usage:          usage,
			IterationsUsed: r.iterations,
			Reason:         "tool requested loop interruption",
		}
		// Callback: OnRunInterrupted
		if r.callbacks != nil && r.callbacks.OnRunInterrupted != nil {
			_ = safeCallback(r.ctx, "OnRunInterrupted", func() error {
				return r.callbacks.OnRunInterrupted(r.ctx, &CallbackRunInterruptedArgs{
					Signature:      r.signature,
					Usage:          usage,
					IterationsUsed: r.iterations,
					Reason:         "tool requested loop interruption",
				})
			})
		}
	case r.ctx.Err() != nil && settleErr == nil:
		terminal = common.RunCanceledEvent{
			Usage:          usage,
			IterationsUsed: r.iterations,
			Reason:         r.ctx.Err().Error(),
		}
		// Callback: OnRunCanceled
		if r.callbacks != nil && r.callbacks.OnRunCanceled != nil {
			_ = safeCallback(r.ctx, "OnRunCanceled", func() error {
				return r.callbacks.OnRunCanceled(r.ctx, &CallbackRunCanceledArgs{
					Signature:      r.signature,
					Usage:          usage,
					IterationsUsed: r.iterations,
					Reason:         r.ctx.Err().Error(),
				})
			})
		}
	default:
		operation := "agent run"
		var operationErr *runOperationError
		if errors.As(err, &operationErr) {
			operation = operationErr.operation
		}
		terminal = common.RunFailedEvent{
			Usage:          usage,
			IterationsUsed: r.iterations,
			Operation:      operation,
			Error:          err.Error(),
		}
		// Callback: OnRunFailed
		if r.callbacks != nil && r.callbacks.OnRunFailed != nil {
			_ = safeCallback(r.ctx, "OnRunFailed", func() error {
				return r.callbacks.OnRunFailed(r.ctx, &CallbackRunFailedArgs{
					Signature:      r.signature,
					Usage:          usage,
					IterationsUsed: r.iterations,
					Operation:      operation,
					Error:          err,
				})
			})
		}
		logging.Errorf("Agent.Do: background run error for conversation %s: %v", r.contextUID, err)
	}

	if writeErr := r.eventStream.WriteWithTimeout(terminal, time.Second); writeErr != nil {
		logging.Errorf("Agent.Do: failed to write terminal event for conversation %s: %v", r.contextUID, writeErr)
	}
}

func (r *reactRun) execute() {
	defer r.eventStream.Close()
	r.finish(r.runLoop())
}
