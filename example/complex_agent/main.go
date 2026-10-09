// Command complex_agent demonstrates a multi-step OpenAI agent with planning,
// parallel tool execution, run-scoped metadata, context compression, typed
// lifecycle events, final-answer streaming, and token accounting.
//
// The tools use deterministic in-memory incident data so the example only
// needs an OpenAI API key and never touches a production system.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/react"
	goatllm "github.com/torrischen/goat/llm"
	openaiprovider "github.com/torrischen/goat/llm/provider/openai"
	"github.com/torrischen/goat/streaming"
)

const (
	defaultQuestion = `Investigate the checkout conversion drop in the EU region during the last 30 minutes.
Correlate service metrics, dependency health, recent deployments, and known incidents.
Identify the most likely root cause, cite the evidence, and propose immediate mitigation plus follow-up actions.`
	regionMetaKey common.AgentDoMetaKey = "region"
)

var outputMu sync.Mutex

func main() {
	question := flag.String("question", defaultQuestion, "incident question for the agent")
	timeout := flag.Duration("timeout", 3*time.Minute, "maximum runtime")
	flag.Parse()

	if os.Getenv("OPENAI_API_KEY") == "" {
		log.Fatal("OPENAI_API_KEY is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	llm, err := newOpenAIModel(ctx)
	if err != nil {
		log.Fatalf("create OpenAI model: %v", err)
	}

	// Create RAM context manager
	manager := ram.NewRAMContextManager()

	agent := react.NewAgent(llm, 128, manager)
	agent.AddTools(ctx,
		serviceMetricsTool(),
		dependencyHealthTool(),
		deploymentHistoryTool(),
		incidentSearchTool(),
	)

	fmt.Printf("Model: %s\nQuestion: %s\n\n", envOr("OPENAI_MODEL", "gpt-5.2"), *question)

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: *question},
		ContextMeta: map[common.AgentDoMetaKey]any{
			regionMetaKey: "eu-west",
		},
		MaxStep:        16,
		EnablePlanning: true,
		PlanUsageInstruction: "Use a concise incident-investigation plan. Gather independent evidence in parallel, " +
			"update every plan item, and distinguish facts from hypotheses.",
		ToolExecutionOptions: &common.ToolExecutionOptions{
			EnableParallel: true,
			MaxConcurrency: 4,
		},
		Compress: true,
		CompressionOptions: common.CompressionOptions{
			Strategy:       common.CompressionStrategyPrecise,
			RecentMessages: 12,
		},
		SpecialRequirements: []string{
			"Treat tool output as the only source of operational facts.",
			"Do not claim that a mitigation was executed; this agent has read-only tools.",
			"Present evidence, confidence, immediate mitigation, and follow-up actions.",
		},
	}, goatllm.WithReasoningEffort("high"), goatllm.WithReasoningSummary("auto"))
	if err != nil {
		log.Fatalf("start agent: %v", err)
	}

	var usage *common.AgentUsage
	toolCalls := 0
	finalStarted := false
	out := &renderer{}
	for {
		event, readErr := eventStream.ReadWithContext(ctx)
		if errors.Is(readErr, streaming.ErrStreamClosed) {
			break
		}
		if readErr != nil {
			log.Fatalf("read agent stream: %v", readErr)
		}
		switch typed := event.(type) {
		case common.ContentDeltaEvent:
			switch typed.ContentType {
			case common.ContentTypeReasoning:
				out.write("reasoning", typed.Delta)
			case common.ContentTypeText:
				switch typed.Phase {
				case common.ContentPhaseCommentary:
					out.write("commentary", typed.Delta)
				case common.ContentPhaseFinalAnswer:
					out.write("answer", typed.Delta)
				default:
					// The provider did not label this text; wait for the turn to end
					// and learn whether it was a preamble or the answer.
					out.buffering = true
				}
			}
		case common.ContentCompletedEvent:
			if out.buffering && typed.ContentType == common.ContentTypeText {
				out.write(sectionFor(typed.Phase), typed.Content)
			}
			if typed.ContentType == common.ContentTypeText && typed.Phase == common.ContentPhaseFinalAnswer {
				out.endTurn()
				finalStarted = true
			}
		case common.ToolCallStartedEvent:
			out.endTurn()
			printLocked("[start] %-24s input=%v\n", typed.Name, typed.Arguments)
		case common.ToolCallCompletedEvent:
			printLocked("[done ] %-24s %s\n", typed.Name, abbreviate(typed.Result, 180))
		case common.ToolCallFailedEvent:
			printLocked("[fail ] %-24s %s\n", typed.Name, typed.Error)
		case common.TurnCompletedEvent:
			usage = typed.Usage
			toolCalls = typed.ToolCalls
		case common.TurnFailedEvent:
			log.Fatalf("agent run failed during %s: %s", typed.Operation, typed.Error)
		case common.TurnCanceledEvent:
			log.Fatalf("agent run canceled: %s", typed.Reason)
		}
	}
	if usage == nil {
		usage = &common.AgentUsage{}
	}

	if finalStarted {
		fmt.Println()
	}
	fmt.Printf("\nConversation: %s\n", signature.ContextUID)
	fmt.Printf("Run: %s\n", signature.RunUID)
	fmt.Printf("Tool calls: %d\n", toolCalls)
	fmt.Printf("Token usage: prompt=%d cached=%d completion=%d\n", usage.PromptTokens, usage.CachedTokens, usage.CompletionTokens)
}

func newOpenAIModel(ctx context.Context) (goatllm.Client, error) {
	opts := []goatllm.Option{
		goatllm.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
	}
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		opts = append(opts, goatllm.WithBaseURL(baseURL))
	}
	opts = append(opts, goatllm.WithModel(envOr("OPENAI_MODEL", "gpt-5.6-terra")))

	return openaiprovider.New(opts...), nil
}

func serviceMetricsTool() common.Tool {
	return common.NewDefaultTool(
		"get_service_metrics",
		"Return read-only golden signals and business metrics for one service in the active region.",
		common.NewToolParameters(
			common.ToolProperty{Name: "service", Type: "string", Required: true, Description: "Service name: checkout-api, payment-api, or inventory-api."},
			common.ToolProperty{Name: "window_minutes", Type: "integer", Required: true, Description: "Lookback window in minutes."},
		),
		func(ctx *common.AgentContext, input map[string]any) common.ToolResult {
			service, ok := requiredString(input, "service")
			if !ok {
				return toolError("service must be a non-empty string")
			}
			data := map[string]any{
				"checkout-api":  map[string]any{"request_rate_rps": 842, "error_rate_pct": 8.7, "p95_latency_ms": 1840, "conversion_pct": 41.2, "baseline_conversion_pct": 63.8, "error_peak_started_at": "2026-07-27T14:42:00Z"},
				"payment-api":   map[string]any{"request_rate_rps": 355, "error_rate_pct": 0.4, "p95_latency_ms": 240, "authorization_success_pct": 97.9},
				"inventory-api": map[string]any{"request_rate_rps": 517, "error_rate_pct": 0.2, "p95_latency_ms": 128, "availability_pct": 99.98},
			}
			metrics, exists := data[service]
			if !exists {
				return toolError("unknown service: " + service)
			}
			return jsonResult(map[string]any{
				"source": "metrics-snapshot", "region": regionFrom(ctx), "service": service,
				"window_minutes": input["window_minutes"], "metrics": metrics,
			})
		},
	)
}

func dependencyHealthTool() common.Tool {
	return common.NewDefaultTool(
		"get_dependency_health",
		"Return health checks for the direct dependencies of a service.",
		common.NewToolParameters(common.ToolProperty{
			Name: "service", Type: "string", Required: true, Description: "Service whose dependencies should be checked.",
		}),
		func(ctx *common.AgentContext, input map[string]any) common.ToolResult {
			service, ok := requiredString(input, "service")
			if !ok {
				return toolError("service must be a non-empty string")
			}
			if service != "checkout-api" {
				return jsonResult(map[string]any{"source": "dependency-monitor", "region": regionFrom(ctx), "service": service, "dependencies": []any{}})
			}
			return jsonResult(map[string]any{
				"source": "dependency-monitor", "region": regionFrom(ctx), "service": service,
				"dependencies": []map[string]any{
					{"name": "payment-api", "status": "healthy", "p95_latency_ms": 240},
					{"name": "inventory-api", "status": "healthy", "p95_latency_ms": 128},
					{"name": "feature-flag-gateway", "status": "degraded", "error_rate_pct": 12.6, "affected_route": "/v1/evaluate/batch", "started_at": "2026-07-27T14:43:00Z"},
				},
			})
		},
	)
}

func deploymentHistoryTool() common.Tool {
	return common.NewDefaultTool(
		"get_recent_deployments",
		"Return recent application and configuration deployments for a service. This tool is read-only.",
		common.NewToolParameters(
			common.ToolProperty{Name: "service", Type: "string", Required: true, Description: "Service name."},
			common.ToolProperty{Name: "limit", Type: "integer", Required: false, Description: "Maximum records to return."},
		),
		func(ctx *common.AgentContext, input map[string]any) common.ToolResult {
			service, ok := requiredString(input, "service")
			if !ok {
				return toolError("service must be a non-empty string")
			}
			deployments := []map[string]any{}
			if service == "checkout-api" {
				deployments = append(deployments,
					map[string]any{"id": "cfg-8841", "type": "configuration", "version": "checkout-flags-2026.07.27.3", "completed_at": "2026-07-27T14:40:31Z", "change": "Enabled batched feature evaluation for 100% of EU traffic", "actor": "release-automation"},
					map[string]any{"id": "app-5519", "type": "application", "version": "checkout-api-4.18.2", "completed_at": "2026-07-26T09:15:00Z", "change": "Logging-only release", "actor": "release-automation"},
				)
			}
			return jsonResult(map[string]any{"source": "deployment-registry", "region": regionFrom(ctx), "service": service, "deployments": deployments})
		},
	)
}

func incidentSearchTool() common.Tool {
	return common.NewDefaultTool(
		"search_known_incidents",
		"Search the read-only incident catalog by query and return potentially related active incidents.",
		common.NewToolParameters(
			common.ToolProperty{Name: "query", Type: "string", Required: true, Description: "Keywords describing symptoms, services, or dependencies."},
			common.ToolProperty{Name: "limit", Type: "integer", Required: false, Description: "Maximum incidents to return."},
		),
		func(ctx *common.AgentContext, input map[string]any) common.ToolResult {
			query, ok := requiredString(input, "query")
			if !ok {
				return toolError("query must be a non-empty string")
			}
			return jsonResult(map[string]any{
				"source": "incident-catalog", "region": regionFrom(ctx), "query": query,
				"incidents": []map[string]any{{
					"id": "INC-2471", "status": "investigating", "title": "Elevated batch-evaluation errors in feature-flag gateway",
					"started_at": "2026-07-27T14:43:00Z", "regions": []string{"eu-west"},
					"workaround": "Disable batched feature evaluation and use the single-evaluation endpoint.",
				}},
			})
		},
	)
}

func requiredString(input map[string]any, key string) (string, bool) {
	value, ok := input[key].(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func regionFrom(ctx *common.AgentContext) string {
	region, _ := ctx.GetMeta(regionMetaKey).(string)
	if region == "" {
		return "unknown"
	}
	return region
}

func jsonResult(value any) common.ToolResult {
	data, err := sonic.Marshal(value)
	if err != nil {
		return toolError(err.Error())
	}
	return common.NewDefaultToolResult(string(data))
}

func toolError(message string) common.ToolResult {
	return jsonResultWithoutError(map[string]any{"error": message})
}

func jsonResultWithoutError(value any) common.ToolResult {
	data, _ := sonic.Marshal(value)
	return common.NewDefaultToolResult(string(data))
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func abbreviate(value string, max int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}

// renderer prints streamed text under a header that changes only when the kind
// of text changes (reasoning, commentary, answer), instead of per delta.
type renderer struct {
	section string
	// buffering is set when the current turn streamed text without a phase;
	// that text is printed from AssistantTextCompletedEvent instead.
	buffering bool
}

func (r *renderer) write(section, text string) {
	if text == "" {
		return
	}
	if r.section != section {
		printLocked("\n[%s]\n", section)
		r.section = section
	}
	printLocked("%s", text)
}

// endTurn closes the current text section before tool or final output.
func (r *renderer) endTurn() {
	if r.section != "" {
		printLocked("\n")
	}
	r.section = ""
	r.buffering = false
}

func sectionFor(phase common.ContentPhase) string {
	if phase == common.ContentPhaseCommentary {
		return "commentary"
	}
	return "answer"
}

func printLocked(format string, args ...any) {
	outputMu.Lock()
	defer outputMu.Unlock()
	fmt.Printf(format, args...)
}
