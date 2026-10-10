package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/react"
	"github.com/torrischen/goat/llm"
	openaiprovider "github.com/torrischen/goat/llm/provider/openai"
)

func main() {
	ctx := context.Background()

	// 创建 LLM 客户端（OpenAI Responses API）
	model := openaiprovider.New(llm.WithAPIKey("your-api-key"), llm.WithModel("gpt-4o"))

	// 创建 agent
	agent := react.NewAgent(model, 128, nil)

	// 设置 callbacks
	callbacks := &react.AgentCallbacks{
		OnTurnStart: func(ctx context.Context, args *react.CallbackTurnStartArgs) error {
			fmt.Printf("🚀 Turn Started: %s\n", args.Signature.RunUID)
			fmt.Printf("   Context: %s\n", args.ContextUID)
			fmt.Printf("   Max Steps: %d\n", args.MaxStep)
			fmt.Printf("   User Input: %s\n", args.UserInput)
			return nil
		},

		OnToolCallStarted: func(ctx context.Context, args *react.CallbackToolCallStartedArgs) error {
			fmt.Printf("▶️  Tool Started: %s\n", args.Name)
			return nil
		},

		OnToolCallCompleted: func(ctx context.Context, args *react.CallbackToolCallCompletedArgs) error {
			fmt.Printf("✔️  Tool Completed: %s (Duration: %v)\n", args.Name, args.Duration)
			fmt.Printf("   Result: %s\n", args.Result)
			if args.Usage != nil {
				fmt.Printf("   Tool Usage: prompt=%d, completion=%d\n",
					args.Usage.PromptTokens,
					args.Usage.CompletionTokens,
				)
			}
			return nil
		},

		OnToolCallFailed: func(ctx context.Context, args *react.CallbackToolCallFailedArgs) error {
			fmt.Printf("❌ Tool Failed: %s (Stage: %v)\n", args.Name, args.Stage)
			fmt.Printf("   Error: %s\n", args.Error)
			return nil
		},

		OnTurnComplete: func(ctx context.Context, args *react.CallbackTurnCompleteArgs) error {
			fmt.Printf("🏁 Turn Completed Successfully\n")
			fmt.Printf("   Iterations: %d\n", args.IterationsUsed)
			fmt.Printf("   Tool Calls: %d\n", args.ToolCallsUsed)
			if args.Usage != nil {
				fmt.Printf("   Total Tokens: %d (prompt=%d, completion=%d, cached=%d)\n",
					args.Usage.PromptTokens+args.Usage.CompletionTokens,
					args.Usage.PromptTokens,
					args.Usage.CompletionTokens,
					args.Usage.CachedTokens,
				)
			}
			return nil
		},

		OnTurnFailed: func(ctx context.Context, args *react.CallbackTurnFailedArgs) error {
			fmt.Printf("💥 Turn Failed\n")
			fmt.Printf("   Operation: %s\n", args.Operation)
			fmt.Printf("   Error: %v\n", args.Error)
			fmt.Printf("   Iterations: %d\n", args.IterationsUsed)
			return nil
		},

		OnTurnInterrupted: func(ctx context.Context, args *react.CallbackTurnInterruptedArgs) error {
			fmt.Printf("⏸️  Turn Interrupted\n")
			fmt.Printf("   Reason: %s\n", args.Reason)
			fmt.Printf("   Iterations: %d\n", args.IterationsUsed)
			return nil
		},

		OnTurnCanceled: func(ctx context.Context, args *react.CallbackTurnCanceledArgs) error {
			fmt.Printf("🛑 Turn Canceled\n")
			fmt.Printf("   Reason: %s\n", args.Reason)
			fmt.Printf("   Iterations: %d\n", args.IterationsUsed)
			return nil
		},
	}

	// 设置回调
	agent.SetCallbacks(callbacks)

	// 添加一个简单的工具
	agent.AddTool(ctx, common.NewDefaultTool(
		"get_time",
		"Get the current time",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"timezone": map[string]any{
					"type":        "string",
					"description": "Timezone name (e.g., 'UTC', 'America/New_York')",
				},
			},
		},
		func(actx *common.AgentContext, args map[string]any) common.ToolResult {
			timezone := "UTC"
			if tz, ok := args["timezone"].(string); ok {
				timezone = tz
			}
			currentTime := time.Now().Format(time.RFC3339)
			return common.NewDefaultToolResult(fmt.Sprintf("Current time in %s: %s", timezone, currentTime))
		},
	))

	// Execute agent
	signature, stream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{
			Text: "What time is it now?",
		},
		MaxStep: 5,
	})
	if err != nil {
		log.Fatalf("Failed to execute agent: %v", err)
	}

	fmt.Printf("\n📋 Run Signature: %s/%s\n\n", signature.ContextUID, signature.RunUID)

	// Read event stream
	for {
		event, err := stream.Read()
		if err != nil {
			break
		}
		// Events are already handled by callbacks
		_ = event
	}

	fmt.Println("\n✨ Demo completed")
}
