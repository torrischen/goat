package react

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
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
	callOpts    []llm.Option
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

	return prepared.usage, true, nil
}

func (r *reactRun) writeFinal() error {
	if _, _, err := r.prepareContext(); err != nil {
		return err
	}

	finalMessage, usage, err := r.agent.generateFinalAnswer(
		r.ctx,
		r.messages,
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

	// Query tools dynamically: first from stable tools, then from dynamic registry
	for i, toolCall := range toolCalls {
		if toolCall == nil {
			continue
		}
		r.toolCalls++

		var tool common.Tool

		// First, check stable tools
		if t, ok := r.agent.stableTools.Get(toolCall.Name); ok {
			tool = t
		} else if r.agent.dynamicRegistry != nil {
			// Then, check dynamic registry
			if t, err := r.agent.dynamicRegistry.Get(r.ctx, toolCall.Name); err == nil {
				tool = t
			}
		}

		item := preparedToolCall{
			call:      toolCall,
			tool:      tool,
			arguments: map[string]any{},
		}

		var failureStage common.ToolCallFailureStage
		var failureMessage string
		if item.tool == nil {
			failureStage = common.ToolCallFailureStageLookup
			failureMessage = "Tool not found or no longer available: " + toolCall.Name
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

	if len(appliedSteering) > 0 {
		logging.Infof(
			"Agent.Do: applied %d steering messages after tool turn in conversation %s",
			len(appliedSteering),
			r.contextUID,
		)
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
		toolCalls := functionToolCalls(raw)

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
		terminal = common.TurnCompletedEvent{
			Usage:          usage,
			IterationsUsed: r.iterations,
			ToolCalls:      r.toolCalls,
		}
		// Callback: OnTurnComplete
		if r.callbacks != nil && r.callbacks.OnTurnComplete != nil {
			_ = safeCallback(r.ctx, "OnTurnComplete", func() error {
				return r.callbacks.OnTurnComplete(r.ctx, &CallbackTurnCompleteArgs{
					Signature:      r.signature,
					Usage:          usage,
					IterationsUsed: r.iterations,
					ToolCallsUsed:  r.toolCalls,
					FinalAnswer:    "", // Final answer already sent via ContentCompleted event
				})
			})
		}
	case errors.Is(err, errAgentLoopInterrupted):
		terminal = common.TurnInterruptedEvent{
			Usage:          usage,
			IterationsUsed: r.iterations,
			Reason:         "tool requested loop interruption",
		}
		// Callback: OnTurnInterrupted
		if r.callbacks != nil && r.callbacks.OnTurnInterrupted != nil {
			_ = safeCallback(r.ctx, "OnTurnInterrupted", func() error {
				return r.callbacks.OnTurnInterrupted(r.ctx, &CallbackTurnInterruptedArgs{
					Signature:      r.signature,
					Usage:          usage,
					IterationsUsed: r.iterations,
					Reason:         "tool requested loop interruption",
				})
			})
		}
	case r.ctx.Err() != nil && settleErr == nil:
		terminal = common.TurnCanceledEvent{
			Usage:          usage,
			IterationsUsed: r.iterations,
			Reason:         r.ctx.Err().Error(),
		}
		// Callback: OnTurnCanceled
		if r.callbacks != nil && r.callbacks.OnTurnCanceled != nil {
			_ = safeCallback(r.ctx, "OnTurnCanceled", func() error {
				return r.callbacks.OnTurnCanceled(r.ctx, &CallbackTurnCanceledArgs{
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
		terminal = common.TurnFailedEvent{
			Usage:          usage,
			IterationsUsed: r.iterations,
			Operation:      operation,
			Error:          err.Error(),
		}
		// Callback: OnTurnFailed
		if r.callbacks != nil && r.callbacks.OnTurnFailed != nil {
			_ = safeCallback(r.ctx, "OnTurnFailed", func() error {
				return r.callbacks.OnTurnFailed(r.ctx, &CallbackTurnFailedArgs{
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
