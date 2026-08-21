package react

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	filectx "github.com/torrischen/goat/agent/contextmgr/file"
	"github.com/torrischen/goat/agent/toolplugin"
	"github.com/torrischen/goat/agent/tools"
	"github.com/torrischen/goat/streaming"
	"github.com/torrischen/goat/util/logging"

	"github.com/alitto/pond/v2"
	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/mark3labs/mcp-go/client"
)

var _ common.Agent = (*Agent)(nil)

const settleRunTimeout = 5 * time.Second

type Agent struct {
	mu              *sync.RWMutex
	contextManager  *contextmgr.Manager
	skillsEnabled   bool
	llmClient       model.AgenticModel
	tools           []common.Tool
	toolsMap        map[string]common.Tool
	modelMaxTokensK int
	callbacks       *AgentCallbacks
}

// NewAgent creates a tool-calling agent backed by Eino's model.AgenticModel.
//
// The agent intentionally trusts the AgenticModel contract and does not branch on
// OpenAI/Claude/Gemini-specific message quirks. Provider differences should be
// handled by the Eino agentic model implementations and provider-specific
// model.Option values passed by callers.
//
// Typical provider construction:
//
// OpenAI Responses API:
//
//	import (
//	    "github.com/cloudwego/eino-ext/components/model/agenticopenai"
//	)
//
//	llm, err := agenticopenai.NewResponsesModel(ctx, &agenticopenai.ResponsesConfig{
//	    APIKey: "sk-...",
//	    Model:  "gpt-5.2",
//	    // BaseURL is optional for OpenAI-compatible gateways.
//	    // ByAzure can be set when using Azure OpenAI.
//	})
//	agent := react.NewAgent(llm, 128, nil)
//
// Claude:
//
//	import (
//	    "github.com/cloudwego/eino-ext/components/model/agenticclaude"
//	)
//
//	llm, err := agenticclaude.New(ctx, &agenticclaude.Config{
//	    APIKey:    "sk-ant-...",
//	    Model:     "claude-sonnet-4-5",
//	    MaxTokens: 4096,
//	    // ByBedrock or ByGoogleVertexAI can be set for hosted Claude.
//	})
//	agent := react.NewAgent(llm, 128, nil)
//
// Gemini on Vertex AI:
//
//	import (
//	    "cloud.google.com/go/auth/credentials"
//	    "github.com/cloudwego/eino-ext/components/model/agenticgemini"
//	    "google.golang.org/genai"
//	    "os"
//	)
//
//	client, err := genai.NewClient(ctx, &genai.ClientConfig{
//	    Backend:  genai.BackendVertexAI,
//	    Project:  "your-gcp-project",
//	    Location: "global", // or a region such as "us-central1"
//	    // Credentials may be omitted when Application Default Credentials are available.
//	})
//
//	// Or initialize Vertex AI with service account credentials JSON.
//	credentialsJSON, err := os.ReadFile("/path/to/service-account.json")
//	if err != nil {
//	    return err
//	}
//	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
//	    Scopes:          []string{"https://www.googleapis.com/auth/cloud-platform"},
//	    CredentialsJSON: credentialsJSON,
//	})
//	if err != nil {
//	    return err
//	}
//	client, err = genai.NewClient(ctx, &genai.ClientConfig{
//	    Backend:     genai.BackendVertexAI,
//	    Project:     "your-gcp-project",
//	    Location:    "global",
//	    Credentials: creds,
//	})
//
//	llm, err := agenticgemini.New(ctx, &agenticgemini.Config{
//	    Client: client,
//	    Model:  "gemini-2.5-flash",
//	})
//	agent := react.NewAgent(llm, 128, nil)
//
// Gemini Developer API uses the same agenticgemini model with a genai client
// configured by API key instead of BackendVertexAI.
func NewAgent(
	llm model.AgenticModel,
	modelMaxTokensK int,
	manager *contextmgr.Manager,
) *Agent {
	a := &Agent{
		mu:              &sync.RWMutex{},
		contextManager:  manager,
		llmClient:       llm,
		modelMaxTokensK: modelMaxTokensK,
		toolsMap:        make(map[string]common.Tool),
		callbacks:       nil,
	}

	if a.contextManager == nil {
		a.contextManager = filectx.NewFileContextManager("")
	}

	a.AddTools(
		context.TODO(),
		tools.GeneratePlan(),
		tools.UpdatePlan(),
	)

	return a
}

// SetCallbacks sets the agent's callback functions
func (a *Agent) SetCallbacks(callbacks *AgentCallbacks) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.callbacks = cloneCallbacks(callbacks)
}

// GetCallbacks gets a copy of the agent's callback functions
func (a *Agent) GetCallbacks() *AgentCallbacks {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneCallbacks(a.callbacks)
}

func (a *Agent) AddTools(ctx context.Context, tool ...common.Tool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, t := range tool {
		if t == nil {
			continue
		}

		originalName := t.Name()
		sanitized := common.SanitizeToolName(originalName)
		if sanitized == "" {
			sanitized = "tool"
		}

		finalName := sanitized
		if _, exists := a.toolsMap[finalName]; exists {
			for i := 2; ; i++ {
				candidate := fmt.Sprintf("%s_%d", sanitized, i)
				if _, exists := a.toolsMap[candidate]; !exists {
					finalName = candidate
					break
				}
			}
		}

		if finalName != originalName {
			logging.Warnf("Tool name %q sanitized to %q for LLM compatibility", originalName, finalName)
		}

		wrapped := common.WrapToolName(t, finalName)
		a.tools = append(a.tools, wrapped)
		a.toolsMap[finalName] = wrapped
	}
}

func (a *Agent) AddTool(ctx context.Context, tool common.Tool) {
	a.AddTools(ctx, tool)
}

// EnableSkills enables skill discovery for subsequent runs. Skill headers are
// loaded dynamically by the agent through the list_available_skills tool.
func (a *Agent) EnableSkills() {
	a.mu.Lock()
	alreadyEnabled := a.skillsEnabled
	a.skillsEnabled = true
	a.mu.Unlock()

	if alreadyEnabled {
		return
	}
	a.AddTools(
		context.TODO(),
		tools.ListAvailableSkills(),
		tools.LoadSkills(),
		tools.ReadSpecifiedFileInSkill(),
	)
}

func (a *Agent) RegisterMCPTools(ctx context.Context, cli client.MCPClient) error {
	if ctx == nil {
		ctx = context.Background()
	}

	tools, err := common.ListMCPTools(ctx, cli)
	if err != nil {
		return err
	}

	for _, t := range tools {
		a.AddTool(ctx, t)
	}

	return nil
}

func (a *Agent) LoadSharedLibPluginTools(ctx context.Context, pluginDir ...string) error {
	plugins := make([]toolplugin.ToolPlugin, 0)
	for _, dir := range pluginDir {
		ps, err := toolplugin.LoadPluginsFromSharedLib(dir)
		if err != nil {
			logging.Errorf("LoadSharedLibPluginTools error from dir %s: %v", dir, err)
			return err
		}
		plugins = append(plugins, ps...)
	}

	for _, p := range plugins {
		a.AddTools(ctx, common.NewDefaultTool(
			p.Name(),
			p.Description(),
			p.Parameters(),
			p.Execute,
		))
	}

	return nil
}

func (a *Agent) LoadRPCPluginTools(ctx context.Context, address ...string) ([]io.Closer, error) {
	plugins := make([]toolplugin.ToolPlugin, 0, len(address))
	resources := make([]io.Closer, 0, len(address))
	rollback := func() {
		for i := len(resources) - 1; i >= 0; i-- {
			if err := resources[i].Close(); err != nil {
				logging.Errorf("LoadRPCPluginTools close error: %v", err)
			}
		}
	}

	for _, addr := range address {
		ps, closer, err := toolplugin.LoadPluginsFromRPC(addr)
		if err != nil {
			rollback()
			logging.Errorf("LoadRPCPluginTools error from address %s: %v", addr, err)
			return nil, err
		}
		resources = append(resources, closer)

		if err := ps.Ping(); err != nil {
			rollback()
			logging.Errorf("LoadRPCPluginTools ping error for plugin %s: %v", ps.Name(), err)
			return nil, err
		}

		plugins = append(plugins, ps)
	}

	for _, p := range plugins {
		a.AddTools(ctx, common.NewDefaultTool(
			p.Name(),
			p.Description(),
			p.Parameters(),
			p.Execute,
		))
	}

	return resources, nil
}

func (a *Agent) buildSystemPrompt(
	planMode bool,
	specialRequirements []string,
	skillUsageInstruction string,
	planUsageInstruction string,
	actx *common.AgentContext,
) string {
	a.mu.RLock()
	skillsEnabled := a.skillsEnabled
	a.mu.RUnlock()

	return renderReactSystemPrompt(
		planMode,
		skillsEnabled,
		specialRequirements,
		skillUsageInstruction,
		planUsageInstruction,
	)
}

func appendConversationMessage(
	ctx context.Context,
	manager *contextmgr.Manager,
	contextUID common.ContextUID,
	messages *[]*schema.AgenticMessage,
	message *schema.AgenticMessage,
) error {
	*messages = append(*messages, message)
	return manager.Append(ctx, contextUID, message)
}

func commitConversationTurn(
	ctx context.Context,
	manager *contextmgr.Manager,
	contextUID common.ContextUID,
	messages *[]*schema.AgenticMessage,
	turnMessages ...*schema.AgenticMessage,
) ([]*schema.AgenticMessage, error) {
	result, err := manager.CommitTurn(ctx, contextUID, turnMessages)
	if err != nil {
		return nil, err
	}

	*messages = append(*messages, turnMessages...)
	*messages = append(*messages, result.AppliedPendingMessages...)
	return result.AppliedPendingMessages, nil
}

func settleConversationFinal(
	ctx context.Context,
	manager *contextmgr.Manager,
	signature common.RunSignature,
	messages *[]*schema.AgenticMessage,
	message *schema.AgenticMessage,
) error {
	if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
		Signature:    signature,
		Outcome:      contextmgr.RunOutcomeCompleted,
		FinalMessage: message,
	}); err != nil {
		return err
	}
	*messages = append(*messages, message)
	return nil
}

func responseMetaFromUsage(usage *common.AgentUsage) *schema.AgenticResponseMeta {
	if usage == nil {
		return nil
	}
	return &schema.AgenticResponseMeta{
		TokenUsage: &schema.TokenUsage{
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			TotalTokens:      usage.PromptTokens + usage.CompletionTokens,
			PromptTokenDetails: schema.PromptTokenDetails{
				CachedTokens: usage.CachedTokens,
			},
		},
	}
}

func addRunUsage(total *common.AgentUsage, usage *common.AgentUsage) {
	if total != nil {
		total.Add(usage)
	}
}

func snapshotRunUsage(total *common.AgentUsage) *common.AgentUsage {
	if total == nil {
		return nil
	}
	return common.NewAgentUsage(total.PromptTokens, total.CachedTokens, total.CompletionTokens)
}

func cloneToolArguments(arguments map[string]any) map[string]any {
	if arguments == nil {
		return map[string]any{}
	}
	data, err := sonic.Marshal(arguments)
	if err == nil {
		var clone map[string]any
		if err := sonic.Unmarshal(data, &clone); err == nil && clone != nil {
			return clone
		}
	}
	return maps.Clone(arguments)
}

var errAgentLoopInterrupted = errors.New("agent loop interrupted")

type runOperationError struct {
	operation string
	err       error
}

func (e *runOperationError) Error() string {
	return fmt.Sprintf("%s: %v", e.operation, e.err)
}

func (e *runOperationError) Unwrap() error {
	return e.err
}

func operationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &runOperationError{operation: operation, err: err}
}

func cloneAgentDoArgs(args *common.AgentDoArgs) *common.AgentDoArgs {
	if args == nil {
		return nil
	}

	clone := *args
	clone.UserInput.Images = append([]*schema.ContentBlock(nil), args.UserInput.Images...)
	clone.SpecialRequirements = append([]string(nil), args.SpecialRequirements...)

	if args.ContextMeta != nil {
		clone.ContextMeta = make(map[common.AgentDoMetaKey]any, len(args.ContextMeta))
		for k, v := range args.ContextMeta {
			clone.ContextMeta[k] = v
		}
	}
	if args.ToolExecutionOptions != nil {
		options := *args.ToolExecutionOptions
		clone.ToolExecutionOptions = &options
	}
	if args.FinalAnswerWebhook != nil {
		webhook := *args.FinalAnswerWebhook
		if args.FinalAnswerWebhook.Headers != nil {
			webhook.Headers = make(map[string]string, len(args.FinalAnswerWebhook.Headers))
			maps.Copy(webhook.Headers, args.FinalAnswerWebhook.Headers)
		}
		clone.FinalAnswerWebhook = &webhook
	}

	return &clone
}

// Fork creates a new conversation from a settled run without starting the
// agent loop. The context manager owns the immutable snapshot and atomic copy.
func (a *Agent) Fork(
	ctx context.Context,
	args *common.AgentForkArgs,
) (common.ContextUID, error) {
	if args == nil {
		return "", fmt.Errorf("agent fork args is nil")
	}
	if args.From.ContextUID == "" {
		return "", fmt.Errorf("agent fork context UID is empty: %w", contextmgr.ErrInvalidRunSignature)
	}
	if args.From.RunUID == "" {
		return "", fmt.Errorf("agent fork run UID is empty: %w", contextmgr.ErrInvalidRunSignature)
	}

	contextUID, err := a.contextManager.Fork(ctx, args.From)
	if err != nil {
		return "", fmt.Errorf("fork context: %w", err)
	}
	return contextUID, nil
}

// Steer queues user messages in the conversation's context-manager-backed
// pending inbox. They are applied after the next complete non-final turn. A
// final answer discards pending messages and closes the inbox until the next Do
// appends a new user input.
func (a *Agent) Steer(ctx context.Context, args *common.AgentSteerArgs) error {
	if args == nil {
		return fmt.Errorf("agent steer args is nil")
	}
	if args.ContextUID == "" {
		return fmt.Errorf("agent steer context UID is empty")
	}
	if len(args.UserInputs) == 0 {
		return fmt.Errorf("agent steer user inputs are empty")
	}

	messages := make([]*schema.AgenticMessage, 0, len(args.UserInputs))
	for _, input := range args.UserInputs {
		input.Images = append([]*schema.ContentBlock(nil), input.Images...)
		messages = append(messages, userInputMessage(input))
	}

	if err := a.contextManager.Enqueue(ctx, args.ContextUID, messages); err != nil {
		return fmt.Errorf("enqueue steering messages: %w", err)
	}
	return nil
}

func (a *Agent) Do(
	ctx context.Context,
	args *common.AgentDoArgs,
	opts ...model.Option,
) (common.RunSignature, streaming.Stream[common.AgentEvent], error) {
	args = cloneAgentDoArgs(args)
	if args == nil {
		return common.RunSignature{}, nil, fmt.Errorf("agent do args is nil")
	}

	actx := common.NewAgentContext(ctx)
	for k, v := range args.ContextMeta {
		actx.SetMeta(k, v)
	}
	args.SkillsDir = strings.TrimSpace(args.SkillsDir)
	if args.SkillsDir == "" {
		args.SkillsDir = common.SkillDefaultFolder
	}
	actx.SetMeta(common.InternalToolSkillsDirMetaKey, args.SkillsDir)

	if args.MaxStep <= 0 {
		args.MaxStep = 8
	}
	maxStep := args.MaxStep

	var contextUID common.ContextUID
	var messages []*schema.AgenticMessage

	systemPrompt := a.buildSystemPrompt(
		args.EnablePlanning,
		args.SpecialRequirements,
		args.SkillUsageInstruction,
		args.PlanUsageInstruction,
		actx,
	)

	// Initialize or restore conversation
	if args.ContextUID == "" {
		systemMessage := schema.SystemAgenticMessage(systemPrompt)
		messages = []*schema.AgenticMessage{systemMessage}
		var err error
		contextUID, err = a.contextManager.Create(ctx, messages)
		if err != nil {
			return common.RunSignature{}, nil, fmt.Errorf("failed to create conversation: %w", err)
		}
	} else {
		// Continue existing conversation
		contextUID = args.ContextUID

		// Restore the managed conversation history.
		var err error
		messages, err = a.contextManager.Load(ctx, contextUID)
		if errors.Is(err, contextmgr.ErrContextNotFound) {
			systemMessage := schema.SystemAgenticMessage(systemPrompt)
			if err := a.contextManager.CreateWithUID(ctx, contextUID, []*schema.AgenticMessage{systemMessage}); err != nil {
				return common.RunSignature{}, nil, fmt.Errorf("failed to create conversation %s: %w", contextUID, err)
			}
			messages = []*schema.AgenticMessage{systemMessage}
			logging.Infof("Agent.Do: initialized new conversation %s", contextUID)
		} else if err != nil {
			return common.RunSignature{}, nil, fmt.Errorf("failed to load conversation: %w", err)
		}
		if len(messages) == 0 {
			systemMessage := schema.SystemAgenticMessage(systemPrompt)
			messages = []*schema.AgenticMessage{systemMessage}
			if err := a.contextManager.Replace(ctx, contextUID, messages); err != nil {
				return common.RunSignature{}, nil, fmt.Errorf("failed to store system message: %w", err)
			}
			logging.Infof("Agent.Do: initialized empty conversation %s", contextUID)
		} else {
			// Always update system prompt to reflect current mode and requirements
			systemMessage := schema.SystemAgenticMessage(systemPrompt)
			if messages[0].Role == schema.AgenticRoleTypeSystem {
				// Replace existing system message
				messages[0] = systemMessage
				logging.Infof("Agent.Do: updated system message for conversation %s", contextUID)
			} else {
				// Insert system message at the beginning (for legacy conversations)
				messages = append([]*schema.AgenticMessage{systemMessage}, messages...)
				logging.Infof("Agent.Do: inserted system message for conversation %s", contextUID)
			}
			// Update the managed context with the new system prompt.
			if err := a.contextManager.Replace(ctx, contextUID, messages); err != nil {
				return common.RunSignature{}, nil, fmt.Errorf("failed to update system message: %w", err)
			}

			logging.Infof("Agent.Do: Restored %d messages from conversation %s", len(messages), contextUID)
		}
	}
	runSignature := common.RunSignature{
		ContextUID: contextUID,
		RunUID:     common.NewRunUID(),
	}
	actx.SetMeta(common.InternalToolContextUIDMetaKey, runSignature.ContextUID.String())
	actx.SetMeta(common.InternalToolRunUIDMetaKey, runSignature.RunUID.String())

	// Apply steering messages left by an interrupted or canceled run before
	// storing this run's explicit user input.
	appliedBeforeRun, err := commitConversationTurn(
		ctx,
		a.contextManager,
		contextUID,
		&messages,
	)
	if err != nil {
		return common.RunSignature{}, nil, fmt.Errorf("failed to apply pending steering messages: %w", err)
	}
	if len(appliedBeforeRun) > 0 {
		logging.Infof(
			"Agent.Do: applied %d pending steering messages before conversation %s resumed",
			len(appliedBeforeRun),
			contextUID,
		)
	}

	// Store this run's user input after any steering messages left by the
	// previous run.
	userMessage := userInputMessage(args.UserInput)
	common.MarkRunStart(userMessage, runSignature.RunUID)
	if err := appendConversationMessage(ctx, a.contextManager, contextUID, &messages, userMessage); err != nil {
		return common.RunSignature{}, nil, fmt.Errorf("failed to store user message: %w", err)
	}

	callOpts := append([]model.Option{}, opts...)
	callOpts = append(callOpts, agenticopenai.WithResponsesPromptCacheKey(contextUID.String()))
	agenticTools := a.convertToolsToAgenticFormat(args.EnablePlanning)
	if len(agenticTools) > 0 {
		callOpts = append(callOpts, model.WithTools(agenticTools))
	}

	eventStream := streaming.NewStream[common.AgentEvent](64)
	if err := eventStream.WriteWithContext(ctx, common.RunStartedEvent{
		Signature: runSignature,
		MaxStep:   maxStep,
	}); err != nil {
		_ = eventStream.Close()
		return common.RunSignature{}, nil, fmt.Errorf("write run started event: %w", err)
	}

	// Callback: OnRunStart
	a.mu.RLock()
	cbs := a.callbacks
	a.mu.RUnlock()
	if cbs != nil && cbs.OnRunStart != nil {
		_ = safeCallback(actx, "OnRunStart", func() error {
			return cbs.OnRunStart(actx, &CallbackRunStartArgs{
				Signature:  runSignature,
				ContextUID: contextUID,
				MaxStep:    maxStep,
				UserInput:  args.UserInput.Text,
			})
		})
	}
	if len(appliedBeforeRun) > 0 {
		// Callback: OnSteeringApplied
		if cbs != nil && cbs.OnSteeringApplied != nil {
			_ = safeCallback(actx, "OnSteeringApplied", func() error {
				return cbs.OnSteeringApplied(actx, &CallbackSteeringAppliedArgs{
					Signature: runSignature,
					Count:     len(appliedBeforeRun),
					BeforeRun: true,
				})
			})
		}
	}

	iterationsUsed := 0
	toolCallsUsed := 0
	runUsage := &common.AgentUsage{}
	runSettled := false

	runLoop := func() error {
		writeFinal := func() error {
			finalAnswer, usage, err := a.generateFinalAnswer(
				actx,
				messages,
				args.SpecialRequirements,
				eventStream,
				callOpts...,
			)
			if err != nil {
				return operationError("generate final answer", err)
			}
			addRunUsage(runUsage, usage)
			finalMessage := common.AssistantTextMessage(finalAnswer)
			finalMessage.ResponseMeta = responseMetaFromUsage(usage)
			if err := settleConversationFinal(
				actx,
				a.contextManager,
				runSignature,
				&messages,
				finalMessage,
			); err != nil {
				return operationError("settle run", err)
			}
			runSettled = true

			iterationsUsed++
			if err := eventStream.WriteWithContext(actx, common.FinalAnswerCompletedEvent{Answer: finalAnswer}); err != nil {
				return operationError("write final answer event", err)
			}

			// Callback: OnFinalAnswer (from writeFinal)
			if cbs != nil && cbs.OnFinalAnswer != nil {
				_ = safeCallback(actx, "OnFinalAnswer", func() error {
					return cbs.OnFinalAnswer(actx, &CallbackFinalAnswerArgs{
						Signature: runSignature,
						Answer:    finalAnswer,
						Usage:     snapshotRunUsage(runUsage),
					})
				})
			}

			a.sendFinalAnswerWebhook(
				actx,
				args.FinalAnswerWebhook,
				a.buildFinalAnswerWebhookPayload(runSignature, args, finalAnswer),
			)

			return nil
		}

		for {
			select {
			case <-actx.Done():
				logging.Infof("Agent.Do: context canceled, stopping agent")
				return actx.Err()
			default:
			}

			if iterationsUsed >= maxStep {
				if err := writeFinal(); err != nil {
					return err
				}
				return nil
			}

			thinkResult, err := a.think(actx, &thinkArgs{
				Compress:           args.Compress,
				CompressionOptions: args.CompressionOptions,
				Messages:           messages,
			}, eventStream, callOpts...)
			if err != nil {
				return operationError("think", err)
			}
			addRunUsage(runUsage, thinkResult.ModelUsage)
			addRunUsage(runUsage, thinkResult.CompressionUsage)

			if thinkResult.IsCompressed {
				if len(thinkResult.CompressedMessages) > 0 {
					messages = thinkResult.CompressedMessages
					if err := a.contextManager.Replace(actx, contextUID, messages); err != nil {
						return operationError("replace compressed context", err)
					}
				}
				// Callback: OnCompressionComplete
				if cbs != nil && cbs.OnCompressionComplete != nil {
					_ = safeCallback(actx, "OnCompressionComplete", func() error {
						return cbs.OnCompressionComplete(actx, &CallbackCompressionCompleteArgs{
							Signature:              runSignature,
							Iteration:              iterationsUsed,
							OriginalMessageCount:   len(messages),
							CompressedMessageCount: len(thinkResult.CompressedMessages),
							Usage:                  thinkResult.CompressionUsage,
						})
					})
				}
			}

			raw := thinkResult.RawResponse
			reasoningContent := messageReasoning(raw)
			toolCalls := functionToolCalls(raw)

			// Callback: OnThinkComplete
			if cbs != nil && cbs.OnThinkComplete != nil {
				_ = safeCallback(actx, "OnThinkComplete", func() error {
					return cbs.OnThinkComplete(actx, &CallbackThinkCompleteArgs{
						Signature:        runSignature,
						Iteration:        iterationsUsed,
						ModelUsage:       thinkResult.ModelUsage,
						HasToolCalls:     len(toolCalls) > 0,
						ToolCallCount:    len(toolCalls),
						HasFinalAnswer:   len(toolCalls) == 0,
						ReasoningContent: reasoningContent,
						WasCompressed:    thinkResult.IsCompressed,
						CompressionUsage: thinkResult.CompressionUsage,
					})
				})
			}

			select {
			case <-actx.Done():
				logging.Infof("Agent.Do: context canceled after LLM call, stopping agent")
				return actx.Err()
			default:
			}

			if len(toolCalls) > 0 {
				assistantMessage := assistantMessageFromResponse(raw)

				toolResults := make([]*schema.FunctionToolResult, len(toolCalls))
				toolUsages := make([]*common.AgentUsage, len(toolCalls))
				type preparedToolCall struct {
					call      *schema.FunctionToolCall
					tool      common.Tool
					arguments map[string]any
					execute   bool
				}
				prepared := make([]preparedToolCall, len(toolCalls))

				for i, toolCall := range toolCalls {
					if toolCall == nil {
						continue
					}
					toolCallsUsed++
					item := preparedToolCall{
						call:      toolCall,
						tool:      a.toolsMap[toolCall.Name],
						arguments: map[string]any{},
					}
					var failureStage common.ToolCallFailureStage
					var failureMessage string
					if item.tool == nil {
						failureStage = common.ToolCallFailureStageLookup
						failureMessage = "Tool not found: " + toolCall.Name
					} else if err := sonic.UnmarshalString(toolCall.Arguments, &item.arguments); err != nil {
						failureStage = common.ToolCallFailureStageArguments
						failureMessage = "Failed to parse arguments: " + err.Error()
						item.arguments = map[string]any{}
					} else {
						if item.arguments == nil {
							item.arguments = map[string]any{}
						}
						item.execute = true
					}
					prepared[i] = item

					// Callback: OnToolCallRequested
					if cbs != nil && cbs.OnToolCallRequested != nil {
						_ = safeCallback(actx, "OnToolCallRequested", func() error {
							return cbs.OnToolCallRequested(actx, &CallbackToolCallRequestedArgs{
								Signature: runSignature,
								Iteration: iterationsUsed,
								CallID:    toolCall.CallID,
								Name:      toolCall.Name,
								Arguments: cloneToolArguments(item.arguments),
							})
						})
					}

					if !item.execute {
						observation := "Error: " + failureMessage
						toolResults[i] = &schema.FunctionToolResult{
							CallID:  toolCall.CallID,
							Name:    toolCall.Name,
							Content: toolResultContentBlocks(observation, nil),
						}
						if err := eventStream.WriteWithContext(actx, common.ToolCallFailedEvent{
							CallID: toolCall.CallID,
							Name:   toolCall.Name,
							Stage:  failureStage,
							Error:  failureMessage,
						}); err != nil {
							return operationError("write tool failure event", err)
						}

						// Callback: OnToolCallFailed
						if cbs != nil && cbs.OnToolCallFailed != nil {
							_ = safeCallback(actx, "OnToolCallFailed", func() error {
								return cbs.OnToolCallFailed(actx, &CallbackToolCallFailedArgs{
									Signature: runSignature,
									Iteration: iterationsUsed,
									CallID:    toolCall.CallID,
									Name:      toolCall.Name,
									Stage:     failureStage,
									Error:     failureMessage,
								})
							})
						}
					}
				}

				concurr := 1
				if args.ToolExecutionOptions != nil &&
					args.ToolExecutionOptions.EnableParallel {
					if args.ToolExecutionOptions.MaxConcurrency > 0 {
						concurr = args.ToolExecutionOptions.MaxConcurrency
					} else {
						concurr = 3
					}
				}
				p := pond.NewPool(concurr, pond.WithQueueSize(len(toolCalls)))
				var batchErr error
				var batchErrMu sync.Mutex
				recordBatchError := func(err error) {
					if err == nil {
						return
					}
					batchErrMu.Lock()
					if batchErr == nil {
						batchErr = err
					}
					batchErrMu.Unlock()
				}

				for i := range prepared {
					if !prepared[i].execute {
						continue
					}
					index := i
					f := func() {
						item := prepared[index]
						if err := eventStream.WriteWithContext(actx, common.ToolCallStartedEvent{
							CallID:    item.call.CallID,
							Name:      item.call.Name,
							Arguments: cloneToolArguments(item.arguments),
						}); err != nil {
							recordBatchError(operationError("write tool started event", err))
							return
						}

						// Callback: OnToolCallStarted
						if cbs != nil && cbs.OnToolCallStarted != nil {
							_ = safeCallback(actx, "OnToolCallStarted", func() error {
								return cbs.OnToolCallStarted(actx, &CallbackToolCallStartedArgs{
									Signature: runSignature,
									Iteration: iterationsUsed,
									CallID:    item.call.CallID,
									Name:      item.call.Name,
									Arguments: cloneToolArguments(item.arguments),
								})
							})
						}

						startedAt := time.Now()
						result := item.tool.Execute(actx, item.arguments)
						if result == nil {
							message := "tool returned a nil result"
							toolResults[index] = &schema.FunctionToolResult{
								CallID:  item.call.CallID,
								Name:    item.call.Name,
								Content: toolResultContentBlocks("Error: "+message, nil),
							}
							if err := eventStream.WriteWithContext(actx, common.ToolCallFailedEvent{
								CallID: item.call.CallID,
								Name:   item.call.Name,
								Stage:  common.ToolCallFailureStageExecution,
								Error:  message,
							}); err != nil {
								recordBatchError(operationError("write tool failure event", err))
							}

							// Callback: OnToolCallFailed (nil result)
							if cbs != nil && cbs.OnToolCallFailed != nil {
								_ = safeCallback(actx, "OnToolCallFailed", func() error {
									return cbs.OnToolCallFailed(actx, &CallbackToolCallFailedArgs{
										Signature: runSignature,
										Iteration: iterationsUsed,
										CallID:    item.call.CallID,
										Name:      item.call.Name,
										Stage:     common.ToolCallFailureStageExecution,
										Error:     message,
									})
								})
							}
							return
						}

						observation := result.String()
						images := append([]*schema.ContentBlock(nil), result.ImageParts()...)
						toolUsages[index] = result.Usage()
						toolResults[index] = &schema.FunctionToolResult{
							CallID:  item.call.CallID,
							Name:    item.call.Name,
							Content: toolResultContentBlocks(observation, images),
						}
						if err := eventStream.WriteWithContext(actx, common.ToolCallCompletedEvent{
							CallID:   item.call.CallID,
							Name:     item.call.Name,
							Result:   observation,
							Images:   append([]*schema.ContentBlock(nil), images...),
							Duration: time.Since(startedAt),
						}); err != nil {
							recordBatchError(operationError("write tool completed event", err))
						}

						// Callback: OnToolCallCompleted
						if cbs != nil && cbs.OnToolCallCompleted != nil {
							_ = safeCallback(actx, "OnToolCallCompleted", func() error {
								return cbs.OnToolCallCompleted(actx, &CallbackToolCallCompletedArgs{
									Signature: runSignature,
									Iteration: iterationsUsed,
									CallID:    item.call.CallID,
									Name:      item.call.Name,
									Result:    observation,
									Images:    append([]*schema.ContentBlock(nil), images...),
									Duration:  time.Since(startedAt),
									Usage:     result.Usage(),
								})
							})
						}
					}

					p.Submit(f)
				}

				p.StopAndWait()
				for _, usage := range toolUsages {
					addRunUsage(runUsage, usage)
				}
				batchErrMu.Lock()
				err = batchErr
				batchErrMu.Unlock()
				if err != nil {
					return err
				}

				pendingMessages := []*schema.AgenticMessage{assistantMessage}
				for _, tr := range toolResults {
					if tr == nil {
						continue
					}
					pendingMessages = append(pendingMessages, common.FunctionToolResultMessage(tr))
				}

				appliedSteering, err := commitConversationTurn(
					actx,
					a.contextManager,
					contextUID,
					&messages,
					pendingMessages...,
				)
				if err != nil {
					return operationError("commit tool turn", err)
				}
				iterationsUsed++

				// Callback: OnIterationComplete
				if cbs != nil && cbs.OnIterationComplete != nil {
					_ = safeCallback(actx, "OnIterationComplete", func() error {
						return cbs.OnIterationComplete(actx, &CallbackIterationCompleteArgs{
							Signature:      runSignature,
							Iteration:      iterationsUsed,
							ToolCallsCount: len(toolCalls),
							UsageSoFar:     snapshotRunUsage(runUsage),
						})
					})
				}

				if len(appliedSteering) > 0 {
					logging.Infof(
						"Agent.Do: applied %d steering messages after tool turn in conversation %s",
						len(appliedSteering),
						contextUID,
					)

					// Callback: OnSteeringApplied (after iteration)
					if cbs != nil && cbs.OnSteeringApplied != nil {
						_ = safeCallback(actx, "OnSteeringApplied", func() error {
							return cbs.OnSteeringApplied(actx, &CallbackSteeringAppliedArgs{
								Signature: runSignature,
								Count:     len(appliedSteering),
								BeforeRun: false,
							})
						})
					}
				}

				if common.ConsumeInterruptSignal(actx) {
					logging.Infof("Agent.Do: interrupt signal received, stopping agent loop for conversation %s", contextUID)
					return errAgentLoopInterrupted
				}

				if iterationsUsed >= maxStep {
					if err := writeFinal(); err != nil {
						return err
					}
					return nil
				}

				continue
			}

			finalAnswer := assistantText(raw)
			finalMessage := common.AssistantTextMessage(finalAnswer)
			finalMessage.ResponseMeta = raw.ResponseMeta
			if reasoningContent != "" {
				finalMessage.ContentBlocks = append([]*schema.ContentBlock{common.ReasoningBlock(reasoningContent)}, finalMessage.ContentBlocks...)
			}
			if err := settleConversationFinal(
				actx,
				a.contextManager,
				runSignature,
				&messages,
				finalMessage,
			); err != nil {
				return operationError("settle run", err)
			}
			runSettled = true

			iterationsUsed++
			if err := eventStream.WriteWithContext(actx, common.FinalAnswerCompletedEvent{Answer: finalAnswer}); err != nil {
				return operationError("write final answer event", err)
			}

			// Callback: OnFinalAnswer (from direct response)
			if cbs != nil && cbs.OnFinalAnswer != nil {
				_ = safeCallback(actx, "OnFinalAnswer", func() error {
					return cbs.OnFinalAnswer(actx, &CallbackFinalAnswerArgs{
						Signature: runSignature,
						Answer:    finalAnswer,
						Usage:     snapshotRunUsage(runUsage),
					})
				})
			}

			a.sendFinalAnswerWebhook(
				actx,
				args.FinalAnswerWebhook,
				a.buildFinalAnswerWebhookPayload(runSignature, args, finalAnswer),
			)

			return nil
		}
	}

	go func() {
		defer eventStream.Close()
		err := runLoop()

		var settleErr error
		if !runSettled {
			outcome := contextmgr.RunOutcomeFailed
			switch {
			case errors.Is(err, errAgentLoopInterrupted):
				outcome = contextmgr.RunOutcomeInterrupted
			case actx.Err() != nil:
				outcome = contextmgr.RunOutcomeCanceled
			}
			settleCtx, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), settleRunTimeout)
			settleErr = a.contextManager.SettleRun(settleCtx, &contextmgr.SettleRunArgs{
				Signature: runSignature,
				Outcome:   outcome,
			})
			cancelSettle()
		}
		if settleErr != nil {
			if err != nil {
				logging.Errorf(
					"Agent.Do: run stopped before it settled for %s/%s: %v",
					runSignature.ContextUID,
					runSignature.RunUID,
					err,
				)
			}
			err = operationError("settle run", settleErr)
		}
		usage := snapshotRunUsage(runUsage)

		var terminal common.AgentEvent
		switch {
		case err == nil:
			terminal = common.RunCompletedEvent{
				Usage:          usage,
				IterationsUsed: iterationsUsed,
				ToolCalls:      toolCallsUsed,
			}
			// Callback: OnRunComplete
			if cbs != nil && cbs.OnRunComplete != nil {
				_ = safeCallback(actx, "OnRunComplete", func() error {
					return cbs.OnRunComplete(actx, &CallbackRunCompleteArgs{
						Signature:      runSignature,
						Usage:          usage,
						IterationsUsed: iterationsUsed,
						ToolCallsUsed:  toolCallsUsed,
						FinalAnswer:    "", // Final answer already sent via OnFinalAnswer
					})
				})
			}
		case errors.Is(err, errAgentLoopInterrupted):
			terminal = common.RunInterruptedEvent{
				Usage:          usage,
				IterationsUsed: iterationsUsed,
				Reason:         "tool requested loop interruption",
			}
			// Callback: OnRunInterrupted
			if cbs != nil && cbs.OnRunInterrupted != nil {
				_ = safeCallback(actx, "OnRunInterrupted", func() error {
					return cbs.OnRunInterrupted(actx, &CallbackRunInterruptedArgs{
						Signature:      runSignature,
						Usage:          usage,
						IterationsUsed: iterationsUsed,
						Reason:         "tool requested loop interruption",
					})
				})
			}
		case actx.Err() != nil && settleErr == nil:
			terminal = common.RunCanceledEvent{
				Usage:          usage,
				IterationsUsed: iterationsUsed,
				Reason:         actx.Err().Error(),
			}
			// Callback: OnRunCanceled
			if cbs != nil && cbs.OnRunCanceled != nil {
				_ = safeCallback(actx, "OnRunCanceled", func() error {
					return cbs.OnRunCanceled(actx, &CallbackRunCanceledArgs{
						Signature:      runSignature,
						Usage:          usage,
						IterationsUsed: iterationsUsed,
						Reason:         actx.Err().Error(),
					})
				})
			}
		default:
			operation := "agent run"
			var operationErr *runOperationError
			if errors.As(err, &operationErr) {
				operation = operationErr.operation
			}
			terminal = common.RunFailedEvent{
				Usage:          usage,
				IterationsUsed: iterationsUsed,
				Operation:      operation,
				Error:          err.Error(),
			}
			// Callback: OnRunFailed
			if cbs != nil && cbs.OnRunFailed != nil {
				_ = safeCallback(actx, "OnRunFailed", func() error {
					return cbs.OnRunFailed(actx, &CallbackRunFailedArgs{
						Signature:      runSignature,
						Usage:          usage,
						IterationsUsed: iterationsUsed,
						Operation:      operation,
						Error:          err,
					})
				})
			}
			logging.Errorf("Agent.Do: background run error for conversation %s: %v", contextUID, err)
		}

		if writeErr := eventStream.WriteWithTimeout(terminal, time.Second); writeErr != nil {
			logging.Errorf("Agent.Do: failed to write terminal event for conversation %s: %v", contextUID, writeErr)
		}
	}()

	return runSignature, eventStream, nil
}
