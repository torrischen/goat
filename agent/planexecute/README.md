# Plan-and-execute agent

`planexecute.Agent` creates a dependency-aware plan, executes each ready step
with an existing `react.Agent`, and produces one final answer in a parent
conversation.

```go
executor := react.NewAgent(llm, 128, ram.NewRAMContextManager())
executor.AddTools(ctx, searchTool, workspaceTool)

agent := planexecute.NewAgent(llm, executor, ram.NewRAMContextManager(), nil)
signature, events, err := agent.Do(ctx, &common.AgentDoArgs{
    UserInput: common.AgentUserInput{Text: "Investigate the incident and propose a fix."},
})
```

The parent agent owns conversation history, steering, forking, and the final
answer. The React executor uses a separate child conversation for its step
work. Steps run sequentially, while the React executor may still run tool calls
in parallel when `ToolExecutionOptions` enables it.
