# Agent Callbacks 使用示例

本示例演示了如何在 goat agent 生命周期中使用 callbacks。

## 功能概览

Agent callbacks 允许你在 agent 执行的关键节点插入自定义逻辑，用于：
- 日志记录和监控
- 性能分析和追踪
- 成本计算和预算控制
- 自定义指标收集
- 调试和故障排查

## Callback 生命周期

```
OnRunStart
    ↓
  Loop:
    OnThinkStart
        ↓
    OnThinkComplete
        ↓
    [如果有压缩] → OnCompressionComplete
        ↓
    [如果有 tool calls]:
        OnToolCallRequested (每个 tool)
            ↓
        OnToolCallStarted
            ↓
        OnToolCallCompleted 或 OnToolCallFailed
        ↓
    OnIterationComplete
    ↓
    [如果应用了 steering] → OnSteeringApplied
  End Loop
    ↓
OnFinalAnswer
    ↓
OnRunComplete / OnRunFailed / OnRunInterrupted / OnRunCanceled
```

## 可用的 Callbacks

### 1. OnRunStart
在 agent run 开始时调用，可以记录初始状态和配置。

```go
OnRunStart: func(ctx context.Context, args *react.CallbackRunStartArgs) error {
    log.Printf("Run started: %s", args.Signature.RunUID)
    log.Printf("User input: %s", args.UserInput)
    return nil
}
```

### 2. OnThinkStart
在每次 LLM 推理前调用。

```go
OnThinkStart: func(ctx context.Context, args *react.CallbackThinkStartArgs) error {
    log.Printf("Starting iteration %d", args.Iteration)
    return nil
}
```

### 3. OnThinkComplete
在每次 LLM 推理完成后调用，可以访问 token 使用情况。

```go
OnThinkComplete: func(ctx context.Context, args *react.CallbackThinkCompleteArgs) error {
    if args.ModelUsage != nil {
        log.Printf("Tokens used: %d prompt + %d completion", 
            args.ModelUsage.PromptTokens, 
            args.ModelUsage.CompletionTokens)
    }
    return nil
}
```

### 4. OnToolCallRequested
当 LLM 请求调用某个 tool 时触发。

```go
OnToolCallRequested: func(ctx context.Context, args *react.CallbackToolCallRequestedArgs) error {
    log.Printf("Tool requested: %s with args: %+v", args.Name, args.Arguments)
    return nil
}
```

### 5. OnToolCallStarted
Tool 开始执行时触发。

```go
OnToolCallStarted: func(ctx context.Context, args *react.CallbackToolCallStartedArgs) error {
    log.Printf("Executing tool: %s", args.Name)
    return nil
}
```

### 6. OnToolCallCompleted
Tool 成功执行完成后触发。

```go
OnToolCallCompleted: func(ctx context.Context, args *react.CallbackToolCallCompletedArgs) error {
    log.Printf("Tool %s completed in %v", args.Name, args.Duration)
    log.Printf("Result: %s", args.Result)
    return nil
}
```

### 7. OnToolCallFailed
Tool 执行失败时触发。

```go
OnToolCallFailed: func(ctx context.Context, args *react.CallbackToolCallFailedArgs) error {
    log.Printf("Tool %s failed at stage %v: %s", args.Name, args.Stage, args.Error)
    return nil
}
```

### 8. OnIterationComplete
每次迭代（think + tool execution）完成后触发。

```go
OnIterationComplete: func(ctx context.Context, args *react.CallbackIterationCompleteArgs) error {
    log.Printf("Iteration %d complete with %d tool calls", 
        args.Iteration, 
        args.ToolCallsCount)
    return nil
}
```

### 9. OnFinalAnswer
Agent 生成最终答案时触发。

```go
OnFinalAnswer: func(ctx context.Context, args *react.CallbackFinalAnswerArgs) error {
    log.Printf("Final answer: %s", args.Answer)
    return nil
}
```

### 10. OnCompressionComplete
Context 压缩完成后触发。

```go
OnCompressionComplete: func(ctx context.Context, args *react.CallbackCompressionCompleteArgs) error {
    log.Printf("Context compressed: %d → %d messages", 
        args.OriginalMessageCount, 
        args.CompressedMessageCount)
    return nil
}
```

### 11. OnSteeringApplied
Steering messages 被应用时触发。

```go
OnSteeringApplied: func(ctx context.Context, args *react.CallbackSteeringAppliedArgs) error {
    if args.BeforeRun {
        log.Printf("Applied %d steering messages before run", args.Count)
    } else {
        log.Printf("Applied %d steering messages after iteration", args.Count)
    }
    return nil
}
```

### 12. OnRunComplete
Run 成功完成时触发。

```go
OnRunComplete: func(ctx context.Context, args *react.CallbackRunCompleteArgs) error {
    log.Printf("Run completed: %d iterations, %d tool calls", 
        args.IterationsUsed, 
        args.ToolCallsUsed)
    return nil
}
```

### 13. OnRunFailed
Run 失败时触发。

```go
OnRunFailed: func(ctx context.Context, args *react.CallbackRunFailedArgs) error {
    log.Printf("Run failed: %v", args.Error)
    return nil
}
```

### 14. OnRunInterrupted
Run 被中断时触发。

```go
OnRunInterrupted: func(ctx context.Context, args *react.CallbackRunInterruptedArgs) error {
    log.Printf("Run interrupted: %s", args.Reason)
    return nil
}
```

### 15. OnRunCanceled
Run 被取消时触发。

```go
OnRunCanceled: func(ctx context.Context, args *react.CallbackRunCanceledArgs) error {
    log.Printf("Run canceled: %s", args.Reason)
    return nil
}
```

## 实际应用场景

### 1. 成本追踪

```go
type CostTracker struct {
    totalCost float64
    mu        sync.Mutex
}

func (ct *CostTracker) OnThinkComplete(ctx context.Context, args *react.CallbackThinkCompleteArgs) error {
    if args.ModelUsage != nil {
        cost := calculateCost(args.ModelUsage)
        ct.mu.Lock()
        ct.totalCost += cost
        ct.mu.Unlock()
    }
    return nil
}
```

### 2. 性能监控

```go
type PerformanceMonitor struct {
    toolDurations map[string][]time.Duration
    mu            sync.Mutex
}

func (pm *PerformanceMonitor) OnToolCallCompleted(ctx context.Context, args *react.CallbackToolCallCompletedArgs) error {
    pm.mu.Lock()
    pm.toolDurations[args.Name] = append(pm.toolDurations[args.Name], args.Duration)
    pm.mu.Unlock()
    return nil
}
```

### 3. 结构化日志记录

```go
type StructuredLogger struct {
    logger *slog.Logger
}

func (sl *StructuredLogger) OnRunStart(ctx context.Context, args *react.CallbackRunStartArgs) error {
    sl.logger.InfoContext(ctx, "agent run started",
        "run_id", args.Signature.RunUID,
        "context_id", args.Signature.ContextUID,
        "max_steps", args.MaxStep,
        "user_input", args.UserInput,
    )
    return nil
}
```

### 4. 分布式追踪集成

```go
func (t *Tracer) OnToolCallStarted(ctx context.Context, args *react.CallbackToolCallStartedArgs) error {
    span, _ := opentracing.StartSpanFromContext(ctx, "tool_call")
    span.SetTag("tool_name", args.Name)
    span.SetTag("call_id", args.CallID)
    // Store span for later finish in OnToolCallCompleted
    return nil
}
```

## 运行示例

```bash
cd example/callback_example
go run main.go
```

## 注意事项

1. **错误处理**: Callback 返回的错误会被记录但不会中断 agent 执行
2. **并发安全**: Tool callbacks 可能并发调用，需要使用锁保护共享状态
3. **性能影响**: Callback 中的重型操作应该异步执行，避免阻塞 agent
4. **Context 传递**: 使用 context 传递追踪信息和取消信号
5. **数据快照**: Usage 和 Arguments 已经是拷贝，可以安全保存

## 高级用法

### 链式 Callbacks

可以组合多个 callback 处理器：

```go
type CallbackChain struct {
    handlers []react.AgentCallbacks
}

func (cc *CallbackChain) OnRunStart(ctx context.Context, args *react.CallbackRunStartArgs) error {
    for _, h := range cc.handlers {
        if h.OnRunStart != nil {
            if err := h.OnRunStart(ctx, args); err != nil {
                return err
            }
        }
    }
    return nil
}
```

### 条件性 Callbacks

根据条件启用不同的 callbacks：

```go
func createCallbacks(enableDebug bool, enableCostTracking bool) *react.AgentCallbacks {
    cbs := &react.AgentCallbacks{}
    
    if enableDebug {
        cbs.OnThinkComplete = debugThinkComplete
        cbs.OnToolCallCompleted = debugToolCompleted
    }
    
    if enableCostTracking {
        cbs.OnThinkComplete = trackCost
    }
    
    return cbs
}
```
