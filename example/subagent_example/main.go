package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/react"
	"github.com/torrischen/goat/agent/tools"
	"github.com/torrischen/goat/streaming"
)

func main() {
	ctx := context.Background()

	// Initialize the LLM
	llm, err := agenticopenai.NewResponsesModel(ctx, &agenticopenai.ResponsesConfig{
		APIKey: os.Getenv("OPENAI_API_KEY"),
		Model:  "gpt-4o",
	})
	if err != nil {
		log.Fatal(err)
	}

	// Create the agent with RAM context manager
	agent := react.NewAgent(llm, 128, ram.NewRAMContextManager())

	// Register subagent tools - this allows the agent to spawn and query subagents
	agent.AddTool(ctx, tools.SpawnSubAgent(agent))
	agent.AddTool(ctx, tools.GetSubAgentStatus())

	// You can also add other tools
	agent.AddTool(ctx, tools.Terminal())

	// Example 1: Ask the agent to delegate tasks to subagents
	fmt.Println("=== Example: Agent delegating tasks to subagents ===")

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{
			Text: `I need you to research three topics in parallel:
1. The history of Go programming language
2. Current trends in AI agents
3. Best practices for concurrent programming

Use subagents to handle these tasks concurrently, then check their status and summarize the results.`,
		},
		MaxStep: 20,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Process the event stream
	var usage *common.AgentUsage
	for {
		event, err := eventStream.ReadWithContext(ctx)
		if errors.Is(err, streaming.ErrStreamClosed) {
			break
		}
		if err != nil {
			log.Fatal(err)
		}

		switch event := event.(type) {
		case common.AssistantTextDeltaEvent:
			fmt.Print(event.Delta)
		case common.ToolCallStartedEvent:
			fmt.Printf("\n[Tool Called: %s]\n", event.Name)
		case common.ToolCallCompletedEvent:
			if event.Name == tools.InternalToolSpawnSubAgent || event.Name == tools.InternalToolGetSubAgentStatus {
				fmt.Printf("[Tool Result: %s]\n%s\n\n", event.Name, event.Result)
			}
		case common.RunCompletedEvent:
			usage = event.Usage
			fmt.Printf("\n\n[Run Completed]\n")
			fmt.Printf("Iterations: %d\n", event.IterationsUsed)
			fmt.Printf("Tool Calls: %d\n", event.ToolCalls)
		case common.RunFailedEvent:
			log.Fatalf("Agent failed during %s: %s", event.Operation, event.Error)
		case common.RunCanceledEvent:
			log.Fatalf("Agent canceled: %s", event.Reason)
		}
	}

	if usage != nil {
		fmt.Printf("\n=== Token Usage ===\n")
		fmt.Printf("Prompt: %d tokens (cached: %d)\n", usage.PromptTokens, usage.CachedTokens)
		fmt.Printf("Completion: %d tokens\n", usage.CompletionTokens)
		fmt.Printf("Total: %d tokens\n", usage.PromptTokens+usage.CompletionTokens)
	}

	fmt.Printf("\n=== Conversation Info ===\n")
	fmt.Printf("ContextUID: %s\n", signature.ContextUID)
	fmt.Printf("RunUID: %s\n", signature.RunUID)
}
