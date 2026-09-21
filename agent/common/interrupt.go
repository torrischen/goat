package common

type interruptLoopTool struct {
	Tool
}

// InterruptLoopAfter wraps a normal tool so executing it requests the agent
// loop to stop after the current tool batch is committed.
func InterruptLoopAfter(tool Tool) Tool {
	if tool == nil {
		return nil
	}
	return &interruptLoopTool{Tool: tool}
}

func (t *interruptLoopTool) Execute(ctx *AgentContext, inputs map[string]any) ToolResult {
	result := t.Tool.Execute(ctx, inputs)
	ctx.signalInterrupt()
	return result
}
