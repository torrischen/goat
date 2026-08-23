package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr/file"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/react"
	"github.com/torrischen/goat/embedder/openai"
	"github.com/torrischen/goat/retriever/milvus"
	"github.com/torrischen/goat/retriever/milvus/hybrid"
	"github.com/torrischen/goat/streaming"

	"github.com/cloudwego/eino-ext/components/model/agenticclaude"
	"github.com/cloudwego/eino-ext/components/model/agenticgemini"
	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
	"google.golang.org/genai"
)

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvBool(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func openAIModelName() string {
	if value := os.Getenv("OPENAI_MODEL"); value != "" {
		return value
	}
	return getenv("OPENAI_MODEL_ID", "gpt-5.2")
}

func newOpenAIModel(ctx context.Context) (model.AgenticModel, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is required")
	}

	config := &agenticopenai.ResponsesConfig{
		APIKey:  apiKey,
		Model:   openAIModelName(),
		ByAzure: getenvBool("OPENAI_BY_AZURE"),
	}
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		config.BaseURL = baseURL
	}

	return agenticopenai.NewResponsesModel(ctx, config)
}

func newClaudeModel(ctx context.Context) (model.AgenticModel, error) {
	return agenticclaude.New(ctx, &agenticclaude.Config{
		APIKey:    os.Getenv("ANTHROPIC_API_KEY"),
		Model:     getenv("CLAUDE_MODEL", "claude-sonnet-4-5"),
		MaxTokens: 4096,
	})
}

func newGeminiVertexModel(ctx context.Context) (model.AgenticModel, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  os.Getenv("GOOGLE_CLOUD_PROJECT"),
		Location: getenv("GOOGLE_CLOUD_LOCATION", "global"),
	})
	if err != nil {
		return nil, err
	}

	return agenticgemini.New(ctx, &agenticgemini.Config{
		Client: client,
		Model:  getenv("GEMINI_MODEL", "gemini-2.5-flash"),
	})
}

func newMilvusClient(ctx context.Context) (*milvusclient.Client, error) {
	return milvusclient.New(ctx, &milvusclient.ClientConfig{
		Address:  "localhost:19530",
		Username: "",
		Password: "",
	})
}

func AzureOpenAITest() {
	ctx := context.Background()

	llm, err := newOpenAIModel(ctx)
	if err != nil {
		panic(err)
	}

	manager, err := file.NewFileContextManager("data/conversations")
	if err != nil {
		panic(err)
	}
	agent := react.NewAgent(llm, 128, manager)
	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "Say hello in one sentence."},
		MaxStep:   4,
	})
	if err != nil {
		panic(err)
	}

	var usage *common.AgentUsage
	for {
		event, err := eventStream.ReadWithContext(ctx)
		if errors.Is(err, streaming.ErrStreamClosed) {
			break
		}
		if err != nil {
			panic(err)
		}
		if completed, ok := event.(common.RunCompletedEvent); ok {
			usage = completed.Usage
		}
	}
	if usage == nil {
		usage = &common.AgentUsage{}
	}

	fmt.Println("conversation:", signature.ContextUID)
	fmt.Println("run:", signature.RunUID)
	fmt.Printf("usage: prompt=%d cached=%d completion=%d\n", usage.PromptTokens, usage.CachedTokens, usage.CompletionTokens)
}

func OpenAIAgentInterruptTest() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	llm, err := newOpenAIModel(ctx)
	if err != nil {
		panic(err)
	}

	manager := ram.NewRAMContextManager()
	agent := react.NewAgent(llm, 128, manager)

	const approvalToolName = "request_human_approval"
	approvalTool := common.NewDefaultTool(
		approvalToolName,
		"Request human approval for an operation, then pause the agent loop until the caller resumes it.",
		common.NewToolParameters(common.ToolProperty{
			Name:        "request",
			Type:        "string",
			Required:    true,
			Description: "The operation that needs human approval.",
		}),
		func(_ *common.AgentContext, inputs map[string]any) common.ToolResult {
			request, _ := inputs["request"].(string)
			if request == "" {
				request = fmt.Sprintf("%v", inputs)
			}
			return common.NewDefaultToolResult("Human approval required before continuing: " + request)
		},
	)
	agent.AddTool(ctx, common.InterruptLoopAfter(approvalTool))

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{
			Text: "Before doing anything else, request human approval for deploying to production. Use the request_human_approval tool and then wait.",
		},
		MaxStep: 4,
	}, model.WithAgenticToolChoice(&schema.AgenticToolChoice{
		Type: schema.ToolChoiceForced,
		Forced: &schema.AgenticForcedToolChoice{
			Tools: []*schema.AllowedTool{
				{FunctionName: approvalToolName},
			},
		},
	}))
	if err != nil {
		panic(err)
	}

	var toolStarted *common.ToolCallStartedEvent
	var toolCompleted *common.ToolCallCompletedEvent
	var usage *common.AgentUsage
	interrupted := false
	for {
		event, err := eventStream.ReadWithContext(ctx)
		if errors.Is(err, streaming.ErrStreamClosed) {
			break
		}
		if err != nil {
			panic(err)
		}
		switch typed := event.(type) {
		case common.FinalAnswerCompletedEvent:
			panic(fmt.Sprintf("unexpected final answer before interrupt: %s", typed.Answer))
		case common.ToolCallStartedEvent:
			if typed.Name == approvalToolName {
				copy := typed
				toolStarted = &copy
			}
		case common.ToolCallCompletedEvent:
			if typed.Name == approvalToolName {
				copy := typed
				toolCompleted = &copy
			}
		case common.RunInterruptedEvent:
			interrupted = true
			usage = typed.Usage
		}
	}
	if toolStarted == nil || toolCompleted == nil || !interrupted {
		panic("approval tool events or interrupted terminal event were not streamed")
	}
	if usage == nil {
		usage = &common.AgentUsage{}
	}

	fmt.Println("conversation:", signature.ContextUID)
	fmt.Println("run:", signature.RunUID)
	fmt.Printf("interrupt tool: %s\n", toolStarted.Name)
	fmt.Printf("tool input: %+v\n", toolStarted.Arguments)
	fmt.Printf("tool observation: %s\n", toolCompleted.Result)
	messages, err := manager.Load(ctx, signature.ContextUID)
	if err != nil {
		panic(err)
	}
	fmt.Printf("context messages after interrupt: %d\n", len(messages))
	fmt.Printf("usage: prompt=%d cached=%d completion=%d\n", usage.PromptTokens, usage.CachedTokens, usage.CompletionTokens)
}

func RetrieverTest() {
	ctx := context.Background()
	mc, err := newMilvusClient(ctx)
	if err != nil {
		panic(err)
	}
	defer mc.Close(ctx)

	em := openai.NewOpenAIEmbedder(ctx, &openai.OpenAIConfig{
		BaseURL: "https://xx.openai.azure.com/openai/v1",
		ApiKey:  "xx",
		Model:   "text-embedding-3-large",
		Dim:     3072,
	})

	r, err := hybrid.NewMilvusHybridRetrieverWithMilvus(
		ctx,
		mc,
		em,
		hybrid.NewHybridRetrieverConfig(
			hybrid.WithRetrieverName("testretriever"),
			hybrid.WithOverwrite(true),
			hybrid.WithDimension(3072),
			hybrid.WithLanguage(hybrid.BM25LanguageJapanese),
			hybrid.WithOnGPU(false),
			hybrid.WithFieldsIndexes(
				milvus.NewFieldsIndex("field", milvus.JSONFieldCastVarchar),
			),
		),
	)
	if err != nil {
		panic(err)
	}

	if err := r.AddPartitions(ctx, "test"); err != nil {
		panic(err)
	}

	if err := r.LoadPartitions(ctx, "test"); err != nil {
		panic(err)
	}

	if _, err := r.AddElement(ctx, "test", milvus.NewElement(
		1,
		"test",
		[]string{},
		milvus.NewFieldsFromObject(struct {
			Field string `json:"field"`
		}{"test"}),
	)); err != nil {
		panic(err)
	}

	if _, err := r.AddElement(ctx, "test", milvus.NewElement(
		2,
		"test",
		[]string{},
		milvus.NewFieldsFromJSONString(`{"field": "test"}`),
	)); err != nil {
		panic(err)
	}

	time.Sleep(time.Second)

	result, err := r.Search(ctx, []string{"test"}, &milvus.SearchArgs{
		Text:          "test",
		OutputFields:  []string{"field"},
		Filter:        milvus.StringEquals(milvus.FieldsPath("field"), "test"),
		SearchMode:    milvus.SearchModeHybrid,
		RerankWeights: []float64{0.8, 0.2},
	})
	if err != nil {
		panic(err)
	}

	for _, res := range result {
		fmt.Printf("search result: %+v\n", res)
	}
}

func main() {
	// RetrieverTest()
	OpenAIAgentInterruptTest()
}
