// Command tool_search demonstrates dynamic tool discovery using a searchable tool registry.
//
// The agent starts with basic tools, then uses search_tools to discover specialized
// capabilities from a registry. Tool information appears in the LLM context, and tools
// are retrieved on-demand when called.
//
// This example only needs an OpenAI API key and uses in-memory mock data.
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

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/react"
	goatllm "github.com/torrischen/goat/llm"
	openaiprovider "github.com/torrischen/goat/llm/provider/openai"
	"github.com/torrischen/goat/streaming"
)

const defaultQuestion = `I need to process a product image: resize it to 800x600 pixels and then compress it to 80% quality. The image path is /images/product-123.jpg`

var outputMu sync.Mutex

func main() {
	question := flag.String("question", defaultQuestion, "task for the agent")
	timeout := flag.Duration("timeout", 2*time.Minute, "maximum runtime")
	flag.Parse()

	if os.Getenv("OPENAI_API_KEY") == "" {
		log.Fatal("OPENAI_API_KEY is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := newOpenAIModel(ctx)
	if err != nil {
		log.Fatalf("create OpenAI model: %v", err)
	}

	manager := ram.NewRAMContextManager()
	agent := react.NewAgent(c, 128, manager)

	// Create tool registry with specialized tools
	registry := NewMockToolRegistry()
	if err := registry.Add(ctx,
		imageResizeTool(),
		imageCompressTool(),
		imageRotateTool(),
		databaseQueryTool(),
		fileReadTool(),
		fileWriteTool(),
	); err != nil {
		log.Fatalf("add tools to registry: %v", err)
	}

	// Enable tool search
	if err := agent.EnableToolSearch(registry); err != nil {
		log.Fatalf("enable tool search: %v", err)
	}

	fmt.Printf("Model: %s\nQuestion: %s\n\n", envOr("OPENAI_MODEL", "gpt-5.2"), *question)
	fmt.Println("The agent will use search_tools to discover relevant tools from the registry.\n")

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput:             common.AgentUserInput{Text: *question},
		MaxStep:               10,
		Compress:              false,
		EnableToolSearch:      true,
		ToolSearchInstruction: "When you need capabilities not in your current tool set, use search_tools to discover relevant tools. Be specific in your search queries.",
	})
	if err != nil {
		log.Fatalf("start agent: %v", err)
	}

	var usage *common.AgentUsage
	toolCalls := 0
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
				if typed.Phase == common.ContentPhaseFinalAnswer {
					out.write("answer", typed.Delta)
				}
			}
		case common.ContentCompletedEvent:
			if typed.ContentType == common.ContentTypeText && typed.Phase == common.ContentPhaseFinalAnswer {
				out.endTurn()
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

	fmt.Printf("\n\nConversation: %s\n", signature.ContextUID)
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
	opts = append(opts, goatllm.WithModel(envOr("OPENAI_MODEL", "gpt-5.6-terra")), goatllm.WithReasoningEffort("max"), goatllm.WithReasoningSummary("auto"))

	return openaiprovider.New(opts...), nil
}

// MockToolRegistry implements a simple in-memory tool registry
type MockToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]common.Tool
}

func NewMockToolRegistry() *MockToolRegistry {
	return &MockToolRegistry{
		tools: make(map[string]common.Tool),
	}
}

func (r *MockToolRegistry) Add(ctx context.Context, tools ...common.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tool := range tools {
		r.tools[tool.Name()] = tool
	}
	return nil
}

func (r *MockToolRegistry) Search(ctx context.Context, query string, limit int) ([]common.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	query = strings.ToLower(query)
	var results []common.Tool
	for _, tool := range r.tools {
		name := strings.ToLower(tool.Name())
		desc := strings.ToLower(tool.Description())
		if strings.Contains(name, query) || strings.Contains(desc, query) {
			results = append(results, tool)
			if len(results) >= limit {
				break
			}
		}
	}
	return results, nil
}

func (r *MockToolRegistry) Get(ctx context.Context, name string) (common.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if tool, ok := r.tools[name]; ok {
		return tool, nil
	}
	return nil, fmt.Errorf("tool not found: %s", name)
}

func (r *MockToolRegistry) List(ctx context.Context) ([]common.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []common.Tool
	for _, tool := range r.tools {
		result = append(result, tool)
	}
	return result, nil
}

func (r *MockToolRegistry) Count(ctx context.Context) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools), nil
}

func (r *MockToolRegistry) Remove(ctx context.Context, names ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range names {
		delete(r.tools, name)
	}
	return nil
}

// Tool definitions
func imageResizeTool() common.Tool {
	return common.NewDefaultTool(
		"resize_image",
		"Resize an image to specified dimensions in pixels",
		common.NewToolParameters(
			common.ToolProperty{Name: "path", Type: "string", Required: true, Description: "Path to the image file"},
			common.ToolProperty{Name: "width", Type: "integer", Required: true, Description: "Target width in pixels"},
			common.ToolProperty{Name: "height", Type: "integer", Required: true, Description: "Target height in pixels"},
		),
		func(ctx *common.AgentContext, args map[string]any) common.ToolResult {
			path := args["path"].(string)
			width := int(args["width"].(float64))
			height := int(args["height"].(float64))
			return common.NewDefaultToolResult(fmt.Sprintf("✓ Resized %s to %dx%d pixels", path, width, height))
		},
	)
}

func imageCompressTool() common.Tool {
	return common.NewDefaultTool(
		"compress_image",
		"Compress an image to reduce file size with specified quality percentage",
		common.NewToolParameters(
			common.ToolProperty{Name: "path", Type: "string", Required: true, Description: "Path to the image file"},
			common.ToolProperty{Name: "quality", Type: "integer", Required: true, Description: "Quality percentage (1-100)"},
		),
		func(ctx *common.AgentContext, args map[string]any) common.ToolResult {
			path := args["path"].(string)
			quality := int(args["quality"].(float64))
			return common.NewDefaultToolResult(fmt.Sprintf("✓ Compressed %s to %d%% quality (saved 1.2MB)", path, quality))
		},
	)
}

func imageRotateTool() common.Tool {
	return common.NewDefaultTool(
		"rotate_image",
		"Rotate an image by specified degrees clockwise",
		common.NewToolParameters(
			common.ToolProperty{Name: "path", Type: "string", Required: true, Description: "Path to the image file"},
			common.ToolProperty{Name: "degrees", Type: "integer", Required: true, Description: "Rotation degrees (0-360)"},
		),
		func(ctx *common.AgentContext, args map[string]any) common.ToolResult {
			path := args["path"].(string)
			degrees := int(args["degrees"].(float64))
			return common.NewDefaultToolResult(fmt.Sprintf("✓ Rotated %s by %d degrees", path, degrees))
		},
	)
}

func databaseQueryTool() common.Tool {
	return common.NewDefaultTool(
		"query_database",
		"Execute a read-only SQL query on the application database",
		common.NewToolParameters(
			common.ToolProperty{Name: "sql", Type: "string", Required: true, Description: "SQL query to execute"},
		),
		func(ctx *common.AgentContext, args map[string]any) common.ToolResult {
			sql := args["sql"].(string)
			return common.NewDefaultToolResult(fmt.Sprintf("✓ Query executed: %s\nReturned 42 rows", abbreviate(sql, 100)))
		},
	)
}

func fileReadTool() common.Tool {
	return common.NewDefaultTool(
		"read_file",
		"Read contents of a text file from the filesystem",
		common.NewToolParameters(
			common.ToolProperty{Name: "path", Type: "string", Required: true, Description: "Path to the file"},
		),
		func(ctx *common.AgentContext, args map[string]any) common.ToolResult {
			path := args["path"].(string)
			return common.NewDefaultToolResult(fmt.Sprintf("✓ Read file: %s\nContent: [mock file contents]", path))
		},
	)
}

func fileWriteTool() common.Tool {
	return common.NewDefaultTool(
		"write_file",
		"Write content to a text file on the filesystem",
		common.NewToolParameters(
			common.ToolProperty{Name: "path", Type: "string", Required: true, Description: "Path to the file"},
			common.ToolProperty{Name: "content", Type: "string", Required: true, Description: "Content to write"},
		),
		func(ctx *common.AgentContext, args map[string]any) common.ToolResult {
			path := args["path"].(string)
			content := args["content"].(string)
			return common.NewDefaultToolResult(fmt.Sprintf("✓ Wrote %d bytes to %s", len(content), path))
		},
	)
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

type renderer struct {
	section string
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

func (r *renderer) endTurn() {
	if r.section != "" {
		printLocked("\n")
	}
	r.section = ""
}

func printLocked(format string, args ...any) {
	outputMu.Lock()
	defer outputMu.Unlock()
	fmt.Printf(format, args...)
}
