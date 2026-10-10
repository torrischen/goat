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
OnTurnStart
    ↓
  Loop:
    [如果有 tool calls]:
        OnToolCallStarted
            ↓
        OnToolCallCompleted 或 OnToolCallFailed
  End Loop
    ↓
OnTurnComplete / OnTurnFailed / OnTurnInterrupted / OnTurnCanceled
```

## 可用的 Callbacks

### 1. OnTurnStart
在 agent run 开始时调用，可以记录初始状态和配置。

```go
OnTurnStart: func(ctx context.Context, args *react.CallbackTurnStartArgs) error {
    log.Printf("Run started: %s", args.Signature.RunUID)
    log.Printf("User input: %s", args.UserInput)
    return nil
}
```

### 2. OnToolCallStarted
Tool 开始执行时触发。

```go
OnToolCallStarted: func(ctx context.Context, args *react.CallbackToolCallStartedArgs) error {
    log.Printf("Executing tool: %s", args.Name)
    return nil
}
```

### 3. OnToolCallCompleted
Tool 成功执行完成后触发。

```go
OnToolCallCompleted: func(ctx context.Context, args *react.CallbackToolCallCompletedArgs) error {
    log.Printf("Tool %s completed in %v", args.Name, args.Duration)
    log.Printf("Result: %s", args.Result)
    return nil
}
```

### 4. OnToolCallFailed
Tool 执行失败时触发。

```go
OnToolCallFailed: func(ctx context.Context, args *react.CallbackToolCallFailedArgs) error {
    log.Printf("Tool %s failed at stage %v: %s", args.Name, args.Stage, args.Error)
    return nil
}
```

### 5. OnTurnComplete
Run 成功完成时触发。

```go
OnTurnComplete: func(ctx context.Context, args *react.CallbackTurnCompleteArgs) error {
    log.Printf("Run completed: %d iterations, %d tool calls", 
        args.IterationsUsed, 
        args.ToolCallsUsed)
    return nil
}
```

### 6. OnTurnFailed
Run 失败时触发。

```go
OnTurnFailed: func(ctx context.Context, args *react.CallbackTurnFailedArgs) error {
    log.Printf("Run failed: %v", args.Error)
    return nil
}
```

### 7. OnTurnInterrupted
Run 被中断时触发。

```go
OnTurnInterrupted: func(ctx context.Context, args *react.CallbackTurnInterruptedArgs) error {
    log.Printf("Run interrupted: %s", args.Reason)
    return nil
}
```

### 8. OnTurnCanceled
Run 被取消时触发。

```go
OnTurnCanceled: func(ctx context.Context, args *react.CallbackTurnCanceledArgs) error {
    log.Printf("Run canceled: %s", args.Reason)
    return nil
}
```

## 实际应用场景

### 1. 性能监控

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

### 2. 结构化日志记录

```go
type StructuredLogger struct {
    logger *slog.Logger
}

func (sl *StructuredLogger) OnTurnStart(ctx context.Context, args *react.CallbackTurnStartArgs) error {
    sl.logger.InfoContext(ctx, "agent run started",
        "run_id", args.Signature.RunUID,
        "context_id", args.Signature.ContextUID,
        "max_steps", args.MaxStep,
        "user_input", args.UserInput,
    )
    return nil
}
```

### 3. 分布式追踪集成

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

func (cc *CallbackChain) OnTurnStart(ctx context.Context, args *react.CallbackTurnStartArgs) error {
    for _, h := range cc.handlers {
        if h.OnTurnStart != nil {
            if err := h.OnTurnStart(ctx, args); err != nil {
                return err
            }
        }
    }
    return nil
}
```
