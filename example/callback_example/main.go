package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/react"
	openaiprovider "github.com/torrischen/goat/llm/provider/openai"
)

func main() {
	ctx := context.Background()

	// 创建 LLM 客户端（OpenAI Responses API）
	model := openaiprovider.New(openaiprovider.WithAPIKey("your-api-key"), openaiprovider.WithModel("gpt-4o"))

	// 创建 agent
	agent := react.NewAgent(model, 128, nil)

	// 设置 callbacks
	callbacks := &react.AgentCallbacks{
		OnRunStart: func(ctx context.Context, args *react.CallbackRunStartArgs) error {
			fmt.Printf("🚀 Run Started: %s\n", args.Signature.RunUID)
			fmt.Printf("   Context: %s\n", args.ContextUID)
			fmt.Printf("   Max Steps: %d\n", args.MaxStep)
			fmt.Printf("   User Input: %s\n", args.UserInput)
			return nil
		},

		OnThinkStart: func(ctx context.Context, args *react.CallbackThinkStartArgs) error {
			fmt.Printf("🤔 Think Start: Iteration %d\n", args.Iteration)
			return nil
		},

		OnThinkComplete: func(ctx context.Context, args *react.CallbackThinkCompleteArgs) error {
			fmt.Printf("✅ Think Complete: Iteration %d\n", args.Iteration)
			if args.ModelUsage != nil {
				fmt.Printf("   Tokens: prompt=%d, completion=%d, cached=%d\n",
					args.ModelUsage.PromptTokens,
					args.ModelUsage.CompletionTokens,
					args.ModelUsage.CachedTokens,
				)
			}
			if args.HasToolCalls {
				fmt.Printf("   Tool Calls: %d\n", args.ToolCallCount)
			}
			if args.HasFinalAnswer {
				fmt.Printf("   Has Final Answer: Yes\n")
			}
			if args.WasCompressed {
				fmt.Printf("   Context was compressed\n")
			}
			return nil
		},

		OnToolCallRequested: func(ctx context.Context, args *react.CallbackToolCallRequestedArgs) error {
			fmt.Printf("🔧 Tool Requested: %s (ID: %s)\n", args.Name, args.CallID)
			fmt.Printf("   Arguments: %+v\n", args.Arguments)
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

		OnIterationComplete: func(ctx context.Context, args *react.CallbackIterationCompleteArgs) error {
			fmt.Printf("🔄 Iteration %d Complete: %d tool calls\n", args.Iteration, args.ToolCallsCount)
			if args.UsageSoFar != nil {
				fmt.Printf("   Total Usage So Far: prompt=%d, completion=%d, cached=%d\n",
					args.UsageSoFar.PromptTokens,
					args.UsageSoFar.CompletionTokens,
					args.UsageSoFar.CachedTokens,
				)
			}
			return nil
		},

		OnFinalAnswer: func(ctx context.Context, args *react.CallbackFinalAnswerArgs) error {
			fmt.Printf("🎯 Final Answer Generated\n")
			fmt.Printf("   Answer: %s\n", args.Answer)
			if args.Usage != nil {
				fmt.Printf("   Total Usage: prompt=%d, completion=%d, cached=%d\n",
					args.Usage.PromptTokens,
					args.Usage.CompletionTokens,
					args.Usage.CachedTokens,
				)
			}
			return nil
		},

		OnSteeringApplied: func(ctx context.Context, args *react.CallbackSteeringAppliedArgs) error {
			if args.BeforeRun {
				fmt.Printf("🎮 Steering Applied (Before Run): %d messages\n", args.Count)
			} else {
				fmt.Printf("🎮 Steering Applied (After Iteration): %d messages\n", args.Count)
			}
			return nil
		},

		OnCompressionComplete: func(ctx context.Context, args *react.CallbackCompressionCompleteArgs) error {
			fmt.Printf("🗜️  Context Compressed: %d -> %d messages\n",
				args.OriginalMessageCount,
				args.CompressedMessageCount,
			)
			return nil
		},

		OnRunComplete: func(ctx context.Context, args *react.CallbackRunCompleteArgs) error {
			fmt.Printf("🏁 Run Completed Successfully\n")
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

		OnRunFailed: func(ctx context.Context, args *react.CallbackRunFailedArgs) error {
			fmt.Printf("💥 Run Failed\n")
			fmt.Printf("   Operation: %s\n", args.Operation)
			fmt.Printf("   Error: %v\n", args.Error)
			fmt.Printf("   Iterations: %d\n", args.IterationsUsed)
			return nil
		},

		OnRunInterrupted: func(ctx context.Context, args *react.CallbackRunInterruptedArgs) error {
			fmt.Printf("⏸️  Run Interrupted\n")
			fmt.Printf("   Reason: %s\n", args.Reason)
			fmt.Printf("   Iterations: %d\n", args.IterationsUsed)
			return nil
		},

		OnRunCanceled: func(ctx context.Context, args *react.CallbackRunCanceledArgs) error {
			fmt.Printf("🛑 Run Canceled\n")
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
